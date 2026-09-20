# 集計クエリとTiFlash

[English](aggregates.md)

`Aggregate[T]` はsource modelの行を集計し、関連行の存在条件で絞り込めます
`Build` はofflineです。`ScanAll`、`Explain`、`ExplainAnalyze`、`Compare` は明示的なexecutorを使います
sourceに主キーは不要で、結果structにmodel tagは不要です

```go
type Order struct {
    model.Meta `tidbgo:"table=orders"`
    ShopID     int64
    Amount     int64
    CreatedAt  *time.Time
    Status     string
    UserID     int64
    User       *User `tidbgo:"belongs_to,join=UserID:ID"`
    DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type User struct {
    model.Meta `tidbgo:"table=users"`
    ID         int64 `tidbgo:",pk"`
    Plan       string
    DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type ShopStats struct {
    ShopID     int64
    OrderCount int64
    Total      sql.NullString
}

q := orm.Aggregate[Order]().
    Where(orm.Equal("Status", "paid")).
    GroupBy("ShopID").
    Select(
        orm.Field("ShopID"),
        orm.CountAll().As("OrderCount"),
        orm.Sum("Amount").As("Total"),
    ).
    Having(orm.GreaterThan("OrderCount", int64(1))).
    OrderBy(orm.Desc("Total"), orm.Asc("ShopID"))

var stats []ShopStats
err := q.ScanAll(ctx, db, &stats)
```

## 式と結果

| 式 | 意味 |
| --- | --- |
| `Field("ShopID")` | sourceのGo field、選択した出力名でgroupingする |
| `Date("CreatedAt")` | SQL DATE型の日付key、groupingが必要 |
| `YearMonth("CreatedAt")` | 整数 `YYYYMM` の年月key、groupingが必要 |
| `CountAll()` | NULLを含む行も数える `COUNT(*)` |
| `CountIf(condition)` | source modelの条件が真の行を数える |
| `SumIf("Amount", condition)` | 条件が真の行でNULL以外の金額を合計する |
| `Count("Amount")` | NULL以外の値の件数 |
| `CountDistinct("ShopID")` | 1 fieldのNULL以外の異なる値の件数 |
| `Sum`、`Avg`、`Min`、`Max` | sourceの1 fieldのNULL以外の値を集計 |

期間keyと集計関数にはscalar sliceへ読む場合も `As` が必要です
`Field` はsourceのGo名を既定の出力名とし、`As` も使えます
出力名は64 byte以内のexported Go識別子で、大文字小文字を区別せず一意である必要があります
sourceの参照には正確なGo field名を使い、SQL column名やraw式は使いません

`Where` はsource modelに対する既存のscalar predicateと `Has` を使います
`WithDeleted` を呼ばない限りsoft-delete済みの行を除外します
`GroupBy`、`Having`、`OrderBy` は選択済み出力の正確なGo名を参照します
compilerは出力名とsource column名が衝突しても曖昧にならない式の参照を生成します
例えば `Field("ShopID").As("Store")` は `GroupBy("Store")` でgroupingします
`Having` は比較、`In`、`NotIn`、`Between`、NULL判定、`And`／`Or`／`Not` を扱います

選択するすべての非集計式を `GroupBy` に指定します
group keyには選択済みの `Field`、`Date`、`YearMonth` の出力を使います
同じ式を別名で選択した場合は1つのkeyを共有でき、重複したgroup式は拒否します
異なる式の間の関数従属性は推定しません
groupingは結果順を保証しません。安定した結果には `OrderBy` と同順位を解消できる条件を使います
`Limit` と `Offset` はgroupingと `Having` の後の結果に適用し、`Offset` には `Limit` が必要です
builder methodはqueryを変更します。構築後の並行読み取りは可能ですが、並行変更は扱いません

`ScanAll` は出力名をdestinationのexported Go fieldへ対応付け、曖昧さのないpromoted fieldも扱います
destination tagは対応付けへ影響しません。1出力はscalar sliceへ読めます
pointerと `sql.Scanner` を扱い、未選択fieldはzero valueのままです
scanとrowsのcloseが成功してからdestinationを置き換え、errorでは以前の領域と値を維持します
結果が空ならnon-nilの空sliceへ置き換えます。`sql.RawBytes` は拒否します

`GroupBy` のない空入力は1行を返し、countは0、`SUM`／`AVG`／`MIN`／`MAX` はNULLです
`Having` によってこの行が除かれる場合があります。groupingした空入力は0行です
数値の結果型とSQL変換はTiDBが決め、ORMはGo型から物理DECIMALの精度を推定しません
正確な計算にはnullableなdestinationとapplication所有のdecimal Scannerを使います
`sql.NullString` はNULLを含むdecimal表現を保持できます
変換と桁あふれのerrorはscan時に `database/sql` が返します
通常のsource fieldの `ScanAll` と異なり、集計結果ではsoft-deleteのNULLをzero `time.Time` へ変換しません

[TiDB集計関数](https://docs.pingcap.com/tidb/stable/aggregate-group-by-functions/)も参照してください

## 関連行による絞り込み

`Where` の `Has` で関連先のデータを条件にsource行を絞り込めます
例えば有料プランのユーザーによる注文を月別に集計します

```go
q := orm.Aggregate[Order]().
    Where(orm.Has("User", orm.Equal("Plan", "paid"))).
    Select(orm.YearMonth("CreatedAt").As("Month"),
        orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).
    GroupBy("Month").OrderBy(orm.Asc("Month"))
```

`Has` はdirect、many-to-many、`via` に対応し、入れ子の `Has` と `And`／`Or`／`Not` を使えます
各 `Has` には1つのRelation field名を指定し、長いpathは入れ子の呼び出しで表します
内部の条件はtarget modelのGo field名を使います
compilerは相関 `EXISTS` を生成するため、複数のtargetや中間行が一致してもsource行の寄与は増えません
keyの全要素をSQLの等価比較で照合し、NULL keyや存在しないtargetは一致しません

targetと `via` edgeはactiveなsoft-delete scopeを維持します
`WithDeleted` が含めるのは削除済みsource行だけです
宣言済みunique target keyを持つto-one関連fieldはdotted pathで出力／group keyに利用できます
`CountIf` と `SumIf` 内の `Has` に対応しますが、`Having` 内に直接書く `Has` は未対応です
scalarの条件付き集計、期間key、paging、`ScanAll`、`Compare` は `Where` を通過したsource行に対して利用できます

条件付きのpositive collectionには既存の `SEMI_JOIN_REWRITE()` hintを使い、`Or` または `Not` 配下は通常の `EXISTS` を維持します
集計compilerはsource tableを保持し、通常queryのTopN／Count書き換えは適用しません
[Relation predicate](queries_ja.md#relation-predicate)と[optimizer hint](https://docs.pingcap.com/tidb/stable/optimizer-hints/#semi_join_rewrite)を参照してください
代表入力と選択性の高い入力を測定してください。関連条件によるlatencyやRUの低下は保証しません

## To-one関連field

`Field("User.Plan").As("Plan")` と `GroupBy("Plan")`、または `Min("User.ID").As("FirstUser")` のような集計を使えます
dotted pathは入れ子のto-one関連を辿れます
各target keyは宣言済みprimary／candidate unique keyと一致する必要があり、物理schemaでもその制約を維持します
has-one宣言だけでは一意性を証明しません。collection pathは拒否します

compilerは同じpathのLEFT JOINを共有し、source行ごとの寄与を1回に保ちます
不在／削除済みtargetはNULLです。targetの削除条件はONに置き、`WithDeleted` はsourceだけに作用します
関連式には `As` が必須です。storage hintは関連先の物理aliasにも適用します
不在targetを扱う出力はnullableにします
`CountIf(Has(...))` と `SumIf("Amount", Has(...))` は複数一致でsource行を増やさず、指標ごとの対象集合を選びます

## 条件付き集計

指標ごとに異なる入力条件が必要な場合は `CountIf` と `SumIf` を使います
例えば、全注文と支払済み注文を日別にまとめて取得できます

```go
paid := orm.Equal("Status", "paid")
q := orm.Aggregate[Order]().
    Select(orm.Date("CreatedAt").As("Day"), orm.CountAll().As("AllCount"),
        orm.CountIf(paid).As("PaidCount"), orm.SumIf("Amount", paid).As("PaidTotal")).
    GroupBy("Day").OrderBy(orm.Asc("Day"))

var days []struct {
    Day       sql.NullTime
    AllCount  int64
    PaidCount int64
    PaidTotal sql.NullString
}
err := q.ScanAll(ctx, db, &days)
```

各関数は既存のscalarまたはRelation存在条件の `Predicate` を1つ受け取り、複数条件は `And`、`Or`、`Not` で組み合わせます
条件はsourceのGo fieldを参照し、検証、parameter binding、escape付き文字列検索は `Where` と同じです
条件付き `Has` は通常のEXISTSを使い、WHERE向けのsemi-join rewrite hintを付けません
両関数とも `As` が必要で、出力名を使う `Having`、順序、期間集計、[`Compare`](aggregate-comparison_ja.md) と組み合わせられます

`CountIf(p)` は `COUNT(CASE WHEN p THEN 1 END)`、`SumIf("Amount", p)` は `SUM(CASE WHEN p THEN amount END)` を生成します
SQLでTRUEになる行だけが一致し、FALSEとNULLは一致しません
件数には金額がNULLの一致行も含みます
合計はNULLの金額を除外し、一致する非NULL金額がなければNULL、金額0の一致行があれば非NULLの0になります
SQLの数値型と正確なDECIMALのscanは `Sum` と同じです

`Where` とsoft-delete scopeは全指標の入力を絞ります
指標内の条件は他の指標やgroupを削除しません
一致行がないgroupは件数0、合計NULLになります
空入力で `GroupBy` がなければその値の1行を返し、groupingした空入力は0行を返します

条件付き出力を `Having` や `OrderBy` で参照すると、aliasとsource columnの曖昧さを避けるため式とparameterをSQL内の順序で繰り返します
`Build` は `driver.Valuer` を呼ばず、通常実行ではparameter変換をdriverへ委ねます
`Compare` は測定loop前に各SQL parameterを1回評価し、全variantと別実行のplanで同じ値を維持します

全指標が同じ入力filterを共有する場合はquery全体の `Where` を使います
選択性の高いindex filterは、条件付き指標のために全入力をscanするよりcostが小さい場合があります
複数指標を1 statementにまとめてもlatencyやRUの削減は保証されず、代表入力と条件別queryで比較します
[TiDBの制御フロー](https://docs.pingcap.com/tidb/stable/control-flow-functions/)と[TiFlash pushdown対応](https://docs.pingcap.com/tidb/stable/tiflash-supported-pushdown-calculations/)も参照してください

## 日別・月別の集計

日別合計には `Date`、月別合計には `YearMonth` を使います
月のkeyには年を含めるため、異なる年の1月は別のgroupになります

```go
q := orm.Aggregate[Order]().
    Select(orm.Date("CreatedAt").As("Day"), orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).
    GroupBy("Day").Having(orm.IsNotNull("Day")).OrderBy(orm.Asc("Day"))

var days []struct {
    Day   sql.NullTime
    Count int64
    Total sql.NullString
}
err := q.ScanAll(ctx, db, &days)

monthly := orm.Aggregate[Order]().
    Select(orm.YearMonth("CreatedAt").As("Month"), orm.CountAll().As("Count")).
    GroupBy("Month").OrderBy(orm.Asc("Month"))
var months []struct {
    Month sql.NullInt64
    Count int64
}
err = monthly.ScanAll(ctx, db, &months)
```

`Date` は `DATE(column)`、`YearMonth` は `EXTRACT(YEAR_MONTH FROM column)` へcompileします
2024年2月の年月keyは整数 `202402` です
両方ともNULLを保持し、filterしなければNULL入力は1つのgroupになります
空入力は0 groupとなり、入力に存在しない日付を0で補完することはありません
DATE、DATETIME、TIMESTAMPへmappingされたfieldを使います
offline検証はfield参照を確認し、SQLの物理型、型変換、無効な日付やzero dateの挙動はTiDBが決めます

`go-sql-driver/mysql` の `parseTime=true` では、SQL DATEを `time.Time`、`*time.Time`、`sql.NullTime` へscanできます
driverはその日の午前0時へ `loc` を付けます。結果は暦上のkeyであり、UTCへ正規化したeventの瞬間ではありません
`parseTime` がない場合、DATEはbyteとして返り、`string` または `sql.NullString` へscanできます
年月keyは `int64`、`*int64`、`sql.NullInt64` へscanできます
結果変換errorでは元のsliceを維持します

TIMESTAMPの日付境界はsession timezoneに従い、DATETIMEとDATEは保存された暦上の値を維持します
driverの `loc` はGo値の変換に使い、serverのtimezoneは設定しません
整合するconnection設定と日付の保存規約を明示してください
期間式はsession SETや自動UTC変換を追加しません
[TiDB timezone](https://docs.pingcap.com/tidb/stable/configure-time-zone/)と[日時関数](https://docs.pingcap.com/tidb/stable/date-and-time-functions/)も参照してください

入力期間の指定には、driver／session設定と整合した値を使い、元fieldへ開始を含み終了を含まない範囲を指定します

```go
q.Where(orm.GreaterThanOrEqual("CreatedAt", start), orm.LessThan("CreatedAt", end))
```

期間の出力は `Having`、順序、pagination、scalar／structへのscan、明示的plan、[`Compare`](aggregate-comparison_ja.md) で使えます
期間keyのHAVING参照には `MIN(key式)` を使います
group内のkeyはNULLを含め同じ値なので結果を維持でき、列以外のgroup式を繰り返した場合のTiDBのsource column名前解決の問題を避けられます
groupingしたsource columnと出力名が同じ場合も曖昧になりません
日付と整数の月keyを明示的な境界値と比較でき、ORMが `"February 2024"` のような表示labelをkeyへparseすることはありません

## 明示的なストレージとMPP方針

```go
q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
```

最外SELECTへ `READ_FROM_STORAGE(TIFLASH[a])`、`SET_VAR(tidb_allow_mpp=1)`、`SET_VAR(tidb_enforce_mpp=1)` を追加します
`a` はcompilerが所有するsource tableのaliasです
同じengine指定を、各 `Has` のquery block内でtargetと中間tableのaliasにも付け、入れ子と自己参照も扱います
TiFlashを指定する場合は参照するすべてのtableにreplicaを準備してください
`SET_VAR` は外側のstatementだけに付けます
`ReadFrom(TiKV)` はこれらすべてのtableに行指向ストレージを要求します
`MPPAuto` はMPPを許可してcostに基づく選択を使い、`MPPEnforce` はMPPのcost比較を上書きします
明示的なTiKVとMPPEnforceの組み合わせはofflineで拒否します
同じ設定項目への後続callは前の値を置き換えます
項目を省略すると対応hintを生成せずsessionの挙動を維持し、ORMは `SET SESSION` を実行しません

これらは方針の指定です。レプリカ不在や非対応の演算によって適用されない場合があります
`SET_VAR` は実行後にsession変数を戻します
Starterにはengine isolation設定の制約があり、これらのAPIはTiFlash実行を保証しません
TiFlashのMPP無効化はこのAPIの対象外です。必要になる場合がある `tidb_allow_tiflash_cop` は `SET_VAR` に対応しません
[optimizer hint](https://docs.pingcap.com/tidb/stable/optimizer-hints/)と[MPP選択](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/)を参照してください

## 指定・計画・実行

```go
planned, err := q.Explain(ctx, db)
// Check err, then planned.WarningsError before trusting warning coverage.
// planned.Requested records hints; planned.Planned contains estimated operators.

executed, err := q.ExplainAnalyze(ctx, db)
// Check err, then executed.WarningsError.
for _, row := range executed.Executed {
    task := row.TaskInfo()
    // task.Known, task.Engine, task.Kind; row.PhysicalTable identifies the table.
    _ = task
}
diagnostics := executed.Diagnostics() // Offline; performs no extra SQL.
_ = diagnostics
```

`ExplainAnalyze` はcompleteなSELECTを実行してresourceを消費し、集計値ではなくplan rowを返します
両terminalは直後に同じconnectionで `SHOW WARNINGS` を取得します
`*sql.DB`、`*sql.Conn`、`*sql.Tx` とそれらの `Observe` wrapperを受け付けます
poolのDBは両statementの完了までconnectionを固定し、caller所有のsessionはcloseしません
observer callbackは警告回収と内部で取得したconnectionの解放後に実行します

`AggregatePlan.Planned` は `Explain` のみ、`Executed` は `ExplainAnalyze` のみが設定します
どちらも別の `ScanAll` が使ったengineの証拠にはしません
警告回収成功時は空の場合もnon-nilのsliceを返します
`WarningsError` は警告queryまたはconnection解放の失敗を示し、取得済みplanは維持します
警告本文は値を含む可能性のある未編集のserver textです
取得済み警告をobserverへ渡し、組み込みloggerとcaptureには安全な要約だけを保存します
`Diagnostics()` は `WRN001` から `WRN003` を追加します。[サーバー警告の診断](warnings_ja.md)を参照してください

`TaskInfo` は通常の `ExplainRow` と `ExplainAnalyzeRow` でも使えます
`root`、`cop[tikv]`、`cop[tiflash]`、`batchCop[tiflash]`、`mpp[tiflash]` を認識します
未知の文字列は `Task` に保持し、`Known=false` になります
root taskにstorage engineはありません。kindが `mpp` の場合にMPP使用を確認できます
非対応のplan column構成はerrorを返します

`AggregatePlan.Diagnostics` は既存の実行planの事実を保持します
集計の `PLN003` は情報レベルで、operatorの出力行数を示し、物理的な読み取りbyte数を示しません
部分集計と最終集計は共存できるため、rootの `HashAgg` からpushdown不在とは判定しません
`PLN005` は認識済みtable accessと指定engineの不一致を示します
`PLN006` はMPPEnforceに対し認識済みstorage taskがMPPを使わない場合に出力し、未知taskがあれば判定を控えます
通常の `ExplainAnalyzePlan.Diagnostics` のseverityは維持します

## 測定と現在の範囲

[`Compare`](aggregate-comparison_ja.md) はauto、TiKV、TiFlash MPPを実行し、結果値の照合、順序を交代するwarmupとsample、通常latency／ServerRU、独立したruntime planと警告、測定だけのcapture出力を提供します
policyを選ぶ前に、固定したcaseで1つのqueryを測定するために使います

各variantに同じ入力と固定datasetまたは適切なread snapshotを使います
結果値と順序を比較し、浮動小数点の結果には許容誤差を明示します
広いscanに加え、選択性が高いqueryと多数groupを返すqueryも含めます
両実装をwarm-upして測定順を交代します

通常SELECTのrowsをcloseした直後に同じconnectionで `LastServerRU` を読むか、`ScanAll` に `CollectServerRU` を使います
Starterでは先行する `SHOW WARNINGS` がlast-query情報を置き換える場合があるため、SELECTのRU計測とplanの警告確認は別実行にします
plan callbackのtarget durationは警告probeとconnection固定の時間を除外します
`CollectServerRU` はEXPLAINをprobeしません。通常のlatencyとEXPLAIN ANALYZEの値は分けます

RuntimeCaptureは集計SELECTを `typed_aggregate` として記録します
`s1:` のSQL template fingerprintはhintを含み、bind値を含みません
scalar query metadataは生成しません
source lintは静的に確定した集計出力／groupingを `AGG001` で別途検証します。[coverage](checks_ja.md)を参照してください
既存のServerRU集計とbaselineではこのrecordを使えます
fingerprintは入力の選択性やdatasetの版を識別しないため、比較可能な条件ごとにbenchmark case IDとbaselineを管理します
baseline CLIはfingerprintごとのpolicyを維持します。engine要求をまたぐ測定比較には比較reportを使います

ServerRUは請求RUではありません。server測定値はegressを含まず、採用costは列指向ストレージと実行頻度にも依存します
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/)と[再現可能な確認](development_ja.md#集計とtiflashの検証)を参照してください

to-one関連出力と[groupに対するwindow](windows_ja.md)に対応します
任意のJOIN、Preload、raw式には `Raw[T]` または別builderを使います
[ベクトル検索](vector-search_ja.md)は独立したbuilderです
[レプリカ準備と機能確認](tiflash_ja.md)は明示的な操作として提供します

`Summary()` はoperatorの処理位置、推定／実測出力行数、直下の子の出力と未確定状態を返します
[summaryの契約](tiflash_ja.md#機能とplanの確認)を参照してください
