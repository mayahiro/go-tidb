# 集計クエリとTiFlash

[English](aggregates.md)

`Aggregate[T]` はsource modelから単一テーブルの集計SELECTを構築します
`Build` はofflineです。`ScanAll`、`Explain`、`ExplainAnalyze`、`Compare` は明示的なexecutorを使います
sourceに主キーは不要で、結果structにmodel tagは不要です

```go
type Order struct {
    model.Meta `tidbgo:"table=orders"`
    ShopID     int64
    Amount     int64
    CreatedAt  *time.Time
    Status     string
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
| `Count("Amount")` | NULL以外の値の件数 |
| `CountDistinct("ShopID")` | 1 fieldのNULL以外の異なる値の件数 |
| `Sum`、`Avg`、`Min`、`Max` | sourceの1 fieldのNULL以外の値を集計 |

期間keyと集計関数にはscalar sliceへ読む場合も `As` が必要です
`Field` はsourceのGo名を既定の出力名とし、`As` も使えます
出力名は64 byte以内のexported Go識別子で、大文字小文字を区別せず一意である必要があります
sourceの参照には正確なGo field名を使い、SQL column名やraw式は使いません

`Where` はsource modelに対する既存のscalar predicateを使います
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
`ReadFrom(TiKV)` は行指向ストレージを指定します
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
警告は値を含む可能性のある未編集のserver textです。自動でログやcaptureへ保存しません

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
scalar query metadataは生成せず、scalar/source query lintは集計shapeを解析しません
既存のServerRU集計とbaselineではこのrecordを使えます
fingerprintは入力の選択性やdatasetの版を識別しないため、比較可能な条件ごとにbenchmark case IDとbaselineを管理します
baseline CLIはfingerprintごとのpolicyを維持します。engine要求をまたぐ測定比較には比較reportを使います

ServerRUは請求RUではありません。server測定値はegressを含まず、採用costは列指向ストレージと実行頻度にも依存します
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/)と[再現可能な確認](development_ja.md#集計とtiflashの検証)を参照してください

初期APIはJOIN、Relation predicate、Preload、window関数、raw式、vector検索、レプリカ自動管理を扱いません
対応する集計式を超えるSQLには `Raw[T]` を使います
レプリカはquery実行とは独立して、[Starterの手順](https://docs.pingcap.com/tidb/stable/create-tiflash-replicas/)で明示的に準備します
