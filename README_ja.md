# go-tidb

`go-tidb` はTiDB Cloud Starter向けのoffline-first、struct-first Go ORMです

Go module pathは `github.com/mayahiro/go-tidb`、command名は `tidbgo` です

[English](README.md) | [Struct model](docs/models_ja.md) |
[Query](docs/queries_ja.md) | [Mutationとraw SQL](docs/mutations_ja.md) |
[Offline check](docs/checks_ja.md) | [Statement observation](docs/observability_ja.md) |
[Development](docs/development_ja.md)

## 利用できる機能

- generated modelを必要としないapplication-owned Go struct
- offline model validation、model intent diagnostic、SQL構築
- reason付きdiagnostic suppressionと決定的なtextまたはJSON CLI report
- 明示的なcoverage statisticsを持つoffline Go source projection解析
- caller-owned `*sql.DB`、`*sql.Conn`、`*sql.Tx` による明示的な実行
- scalar predicate、order、offset pagination、keyset pagination
- 決定的なdirectとmany-to-many Relation predicateおよびpreload
- single insert、upsert、自動分割するbulk insertとbulk upsert
- bulk writeと `All` preloadのoffline statement数予測
- primary keyまたはpredicateで範囲を限定したupdateとdelete
- soft delete、restore、pure junction mutation、transaction helper
- raw JOIN、CTE、aggregate、partial resultのtyped scan
- terminalの自動色付きcontext-scoped statement observation
- observer設定だけで使うstructured runtime captureとoffline N+1解析
- multi-statement ORM callをまとめるoperation-scoped debug report
- typed query builderによるSELECT限定のTiDB execution plan取得
- 明示的なSELECT実行によるTiDB actual runtime plan取得と返されたrowのdiagnostic
- 完了した1 DML statementに対する明示的なsame-session ServerRU取得

## サポート範囲

- TiDB Cloud Starterだけをsupported database profileとする
- MySQL、MariaDB、他のTiDB Cloud plan、TiDB Self-Managedは対象外
- application modelにはユーザーが所有する通常のGo structを使う
- model解析とSQL構築にはcode generationとDB接続が不要
- Relation loadingは明示的かつ決定的でlazy loadingを行わない
- v1まではpublic APIとformatの後方互換性を保証しない

## Requirements

- Go 1.26以上
- 接続機能にはTiDB Cloud Starterが必要

現在のmodel解析とquery構築にはdatabase接続が不要です

## Installation

```sh
go get github.com/mayahiro/go-tidb
```

CLI checkを使用する場合は `tidbgo` commandを別にinstallします

```sh
go install github.com/mayahiro/go-tidb/cmd/tidbgo@latest
```

`go-tidb` はdatabase driverを同梱または選択しません

applicationが使用するdriverを登録し、既存の `database/sql` executorをORMへ渡します

例えば `go-sql-driver/mysql` を使用するapplicationではconnectionを次のように作成します

```go
import (
	"database/sql"

	_ "github.com/go-sql-driver/mysql"
)

db, err := sql.Open("mysql", dsn)
```

[TiDB Cloud Starter connection requirements](https://docs.pingcap.com/tidbcloud/connect-to-tidb-cluster-serverless/?plan=starter)に従い、TLSを使用します

`go-sql-driver/mysql` で `DATE` または `DATETIME` を `time.Time` へscanする場合は `parseTime=true` を使用します

`interpolateParams=true` は短命なparameterized queryのround tripを削減できますが、BIG5、CP932、GB2312、GBK、SJISとは併用できません

詳細はdriverの[`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams)を参照してください

## Struct-first model metadata

Applicationで使用するfieldを直接定義します

```go
type User struct {
	model.Meta `tidbgo:"table=users"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Email      string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
	OrderCount int64 `tidbgo:"order_count,computed"`
	Orders     []Order `tidbgo:"has_many"`
}
```

toolingまたはtestでmetadataが必要な場合はofflineで解析できます

```go
metadata, err := model.Describe[User]()
```

有効ではあるものの意図と異なる可能性がある宣言は、独立したcheckで診断できます

```go
diagnostics := check.Model[User]()
```

checkはDBへ接続せず、不正なmetadata、無視されるtag、tag位置の間違い候補、primary key capabilityの不足、片方向だけのcustom scalar typeをstableな `MOD001` から `MOD007` で報告します

applicationが所有するmodel typeは明示的に列挙し、generated registryとsource scanを必要としません

unexported fieldと `tidbgo:"-"` を指定したfieldは無視します

scalarの `tidbgo` tagは省略可能なcolumn名を第1要素、optionを第2要素以降に指定します

column名を省略したfieldには決定的なsnake_caseを使用します

primary keyは推定columnなら `tidbgo:",pk"`、明示columnなら `tidbgo:"column_name,pk"` で指定し、複数指定した場合は宣言順のcomposite keyになります

`db` を含む `tidbgo` 以外のstruct tag namespaceは無視し、column名の変更やfieldの除外には使用しません

default table名はGo type名のsnake_caseです

物理table名を上書きする場合だけzero-sizeの `model.Meta` markerを埋め込みます

custom scalar typeは `sql.Scanner` と `driver.Valuer` を実装できます

`go-tidb` はapplicationで使うDecimal libraryを選択しません

1個のinteger primary keyへ `auto_random` を指定するとinsert statementから省略します

single-rowの `Insert` だけがgenerated IDを反映し、Upsertとbulk operationは反映しません

alias付きraw query resultには `computed` を使い、base-table readとwriteから除外します

削除時刻を表す1個の `time.Time` または `*time.Time` fieldには `soft_delete` を使います

value fieldはzero timeをSQL `NULL` へmappingし、pointer fieldはnilを `NULL` として扱います

通常のnullable columnには引き続きpointerまたは `sql.Scanner` typeを使います

to-one Relationにはpointer、to-many Relationにはvalueまたはpointerのsliceを使います

direct Relationは一意に解決できる一般的なsingle primary key mappingを推定し、それ以外ではordered `join=Source:Target` optionを明示します

many-to-manyではjunction tableと両側のjunction key mappingを明示します

Relation fieldはlazy loadを行わず、独立したloaded-state metadataも保持しません

詳細は[Struct model guide](docs/models_ja.md)と実行可能な[starter app example](examples/starter-app/README.md)を参照してください

## Offline schema compatibility

self-containedなTiDB `CREATE TABLE` snapshotを1回parseし、application modelごとにcheckします

```go
catalog, err := schema.Parse(schemaSQL)
if err != nil {
	return err
}

diagnostics := check.Schema[User](catalog)
```

どちらのoperationもofflineで動作し、SQLを実行しません

比較は方向付きで、modelがmappingするcomputed以外のfieldには互換性のある物理columnを必要とし、databaseだけに存在するcolumnはnullable、default付き、generatedのいずれかなら許容します

databaseだけに存在する必須columnはmodel insertが失敗する可能性があるためwarningになります

ordered primary key、`AUTO_RANDOM`、native GoとSQLのtype family、nullability、writableなgenerated column、物理Relation targetもcheckします

collection checkはmany-to-many junction keyと必須column、target identity、決定的な `has_many` と `many_to_many` lookupが使うindex prefixをstableな `CMP001` から `CMP014` で検査します

Relationを持つmodelでは、渡すsnapshotにtarget tableとjunction tableも必要です

`schema.Parse` は通常のTiDB `CREATE TABLE` SQLとTiDB executable commentを含む `SHOW CREATE TABLE` outputを受け付けます

schema dumpの `SET` や `DROP TABLE` などのwrapper statementは無視しますが、`ALTER TABLE` historyのreplay、foreign keyの要求、一般queryのindex推奨、live databaseのinspectは行いません

詳細は[Schema compatibility guide](docs/schema-checks_ja.md)を参照してください

## Struct-first scalar query

exported Go field名を使い、validated SQLとbind argumentをofflineで構築します

```go
query := orm.Query[Order]().
    Select("ID", "UserID", "Total").
    Where(orm.Equal("UserID", userID)).
    OrderBy(orm.Desc("ID")).
    SeekAfter(lastID).
    Limit(100)

sqlText, arguments, err := query.Build()
diagnostics := query.Diagnostics()
schemaDiagnostics := query.DiagnosticsWithSchema(catalog)
```

`Build`はDBへ接続せず、custom `driver.Valuer`も実行しません

valueはbind argumentとしてSQLから分離し、物理identifierにはvalidated model metadataだけを使います

`Diagnostics` もofflineで動作します

build failureを `QRY001` へ変換し、有効なOFFSET pagination、明示的なpaginationのorder不足、leading wildcard predicate、relation-first compilerを使用できないRelation filter付きTopNをbind valueなしの `QRY002` から `QRY005` で報告します

`DiagnosticsWithSchema` は渡したparse済みsnapshotが解析対象accessを表せない場合に `QRY006`、positive Limit付きordered accessに一致するdefaultで利用可能なdirect-column index prefixがない場合に `QRY007` を追加します

このmethodもofflineで動作し、schema-aware diagnosticを出力する場合はbind valueを含まないstable query fingerprintをevidenceへ含めます

indexの存在はoptimizerが選ぶplanを予測しないため、`Explain` または `ExplainAnalyze` で確認します

既存executorを明示的に渡した場合だけ同じqueryを実行します

```go
orders, err := query.All(ctx, db)
order, err := query.First(ctx, db)
exists, err := query.Exists(ctx, db)
count, err := query.Count(ctx, db)
```

`*sql.DB`、`*sql.Conn`、`*sql.Tx` は `orm.QueryExecutor` を実装します

現在の `go-tidb` はconnectionのopenと設定を行わず、MySQL protocol driverも含みません

`Only`は0件、1件、複数件を区別します

`Exists`はmodelをscanせず `SELECT 1 ... LIMIT 1` を使います

`Count`はmodelをscanせず、builderのpredicateとpaginationを反映したcount専用SQLを使います

terminal error、predicate、pagination、NULL ordering、現在の実行境界は[Scalar query guide](docs/queries_ja.md)を参照してください

Relationをloadせず、存在条件だけでfilterできます

```go
admins, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Has("Roles", orm.Equal("Name", "admin"))).
    All(ctx, db)
```

`Has` はRelationの存在を表すlogical predicateです

通常は `EXISTS` を生成し、positive conjunctive contextのfiltered collection predicateにはTiDBの `SEMI_JOIN_REWRITE()` hintを追加します

metadataから証明できる限定的な `has_many`、root primary key order、positive Limitのshapeでは、target filterとLimitをroot rowのloadより先へ適用します

orderedかつlimitedなcollection filterが `EXISTS` fallbackになる理由は `QRY005` で確認できます

schema snapshotを渡した場合はassociation index prefixの不足を `QRY007` で確認できます

target predicateを渡せば一致するrelated rowを条件とし、省略すれば存在だけを条件とします

Relation名とtarget field名にはexported Go field名を使い、`Build` はDB接続なしでvalidationとcompileを行います

正確な変換条件、Relation data integrity contract、index guidanceは[Scalar query guide](docs/queries_ja.md#relation-predicate)を参照してください

exported Go field名でdirectまたはpure many-to-many Relationをpreloadします

```go
users, err := orm.Query[User]().
    Select("Email").
    Preload("Orders").
    Preload("Roles").
    All(ctx, db)
```

`Preload` はmetadataをofflineで検証し、lazy loadを使わず通常のpointerまたはslice fieldをhydrateします

`belongs_to` と `has_one` は決定的なinline `LEFT JOIN` を使います

`has_many` とpure `many_to_many` はpreceding rowsをcloseした後、決定的なsecondary SELECTで処理します

predicate、seek、limit、offset、activeなroot soft-delete scopeがない無制限の `All` はroot collection sourceを `IN` なしの1 statementで読みます

default scopeのsoft-delete root、その他の制約付きroot query、全nested collectionは5,000 bind parameterのbudgetでkey batchを作り、composite key幅に応じてbatch sizeを縮小し、TiDBの[65,535 placeholder上限](https://docs.pingcap.com/tidb/stable/sql-statement-prepare/)を超えません

この選択はparent query shapeだけで決まり、runtime statisticsとresult cardinalityでは切り替えません

many-to-many secondary SELECTは固定のjunction-to-target JOINを1回使います

collection配下のto-oneはcollection statementへinline joinするため、`Preload("Orders.User")` は通常parent SELECTとUserをinline joinしたOrders SELECTの2 statementを実行します

`PreloadFields` は任意のRelation projectionを限定し、`PreloadOrderBy` はcollection orderを指定し、必要なRelation keyは自動追加します

複数statementで同じtransaction snapshotが必要な場合はcallerが直接作成したrepeatable-read `*sql.Tx` または `Transaction` callbackから受け取った `*sql.Tx` を渡します

`PreloadWithDeleted` は指定したRelation pathだけでlogical deleted targetを含めます

任意のRelation固有predicateには現在未対応です

`EstimateAllStatements` は同じbuilderをofflineで検証し、statement数の最小値とquery shapeから証明できる場合の最大値を返します

root SELECTとcollection preloadを対象とし、diagnostic queryとServerRU queryは含めません

inline joinはstatementを追加せず、上限のないkeyed collectionまたはnested collectionのcardinalityを証明できない場合は最大値をunknownとします

## Mutationとraw SQL

通常のwrite pathはmodel valueとprimary key metadataを直接使います

```go
affected, err := orm.Insert(&user).Exec(ctx, db)
affected, err = orm.Upsert(&user).Exec(ctx, db)
affected, err = orm.UpsertMany(users).Exec(ctx, db)
affected, err = orm.Update(&user).Exec(ctx, db)
affected, err = orm.Update(&user, "Email").Exec(ctx, db)
affected, err = orm.UpdateWhere[JobLease](
    orm.Set("LockOwner", owner),
    orm.Set("LockUntil", lockUntil),
).Where(
    orm.Equal("JobID", jobID),
    orm.Or(orm.IsNull("LockUntil"), orm.LessThanOrEqual("LockUntil", now)),
).Exec(ctx, db)
affected, err = orm.Delete(&user).Exec(ctx, db)
affected, err = orm.DeleteWhere[Order](
    orm.Equal("UserID", user.ID),
).Exec(ctx, db)
affected, err = orm.AddRelation[User]("Roles", user.ID, roleIDs...).Exec(ctx, db)
affected, err = orm.RemoveRelation[User]("Roles", user.ID, roleIDs...).Exec(ctx, db)
affected, err = orm.ClearRelation[User]("Roles", user.ID).Exec(ctx, db)
```

applicationが決めたoperationを明示的なtransaction helperでまとめられます

```go
err = orm.Transaction(ctx, db, func(tx *sql.Tx) error {
    if _, err := orm.Update(&user).Exec(ctx, tx); err != nil {
        return err
    }
    _, err := orm.InsertMany(orders).Exec(ctx, tx)
    return err
})
```

`InsertMany(values)` と `UpsertMany(values)` は `[]Model` と `[]*Model` のどちらも受け取ります

`Exec` はTiDBの65535 placeholder上限で自動分割し、`Build` は1個の実行可能statementを表す契約を維持します

`StatementCount` はtestとoffline capacity tooling向けに残しますが、通常のapplication operationへ並行するdiagnostic wrapperは不要です

runtime captureが実際の分割を自動的に記録します

全batchをatomicにする場合は直接作成した `*sql.Tx` または `Transaction` callbackから受け取った `*sql.Tx` を使います

`Transaction` はdefaultの `database/sql` optionを使い、callbackをretryしません

全typed mutationはoffline `Build` に対応します

empty predicate listからtyped DELETEを生成できません

`*sql.DB`、`*sql.Conn`、`*sql.Tx` はmutation executor boundaryを実装します

pure many-to-many Relation mutationはcode generationなしでexported Relation field名とkey valueを使います

`AddRelation` は1個のmulti-row junction INSERTを生成し、defaultではduplicateをerrorにします

既存junction rowを維持する場合だけ `IgnoreExisting` を明示します

`RemoveRelation` と `ClearRelation` はそれぞれ1個のbounded DELETEを生成し、composite mappingでは宣言順に `CompositeKey` を使います

`UpdateWhere` はassignmentとpredicateの明示を要求し、`nil` によるSQL NULL代入と `Increment` による同じcolumnへのatomic additionに対応します

無条件のtyped updateはありません

soft-delete modelのSELECTとUPDATEにはactive-row guardを追加します

`WithDeleted` はdeleted root rowの取得または明示的なrestoreを有効にし、`PreloadWithDeleted` は1個のRelation pathへscopeを限定します

対象modelの `Delete` と `DeleteWhere` はserver timestampを使う1個のUPDATEになり、tagがないmodelはphysical DELETEを維持します

`Upsert` と `UpsertMany` のzero-valued soft-delete fieldはNULLを書き込むため、conflictしたrowをrestoreします

scalar builderの範囲外となるJOIN、CTE、aggregateなどには `Raw[T]` を使います

result column名を `computed` fieldを含むmodel columnへmappingします

typed APIで表現できないmutation式だけ `RawExec` を使います

詳細は[Mutation and raw SQL guide](docs/mutations_ja.md)を参照してください

`Raw[T]` と `RawExec` はcaller-supplied SQLをparse、sanitizeせず、typed mutation safeguardも適用せずexecutorへ渡します

SQL statementはtrusted application codeとして扱い、未信頼valueはすべて `?` placeholderと別argumentで渡します

```go
users, err := orm.Raw[User](
    "SELECT id, email FROM users WHERE email = ?",
    requestedEmail,
).All(ctx, db)
```

request parameter、user input、外部dataをraw statementへ文字列連結してはいけません

placeholderが表現できるのはvalueでありidentifierやSQL keywordではありません

table、column、directionなどのSQL構造を動的に選ぶ場合はapplication内のclosed allowlistへmappingします

`RawExec` のpredicate範囲、transaction boundary、destructive operationの安全性もcallerの責任です

Go公式の[SQL injection guidance](https://go.dev/doc/database/sql-injection)も参照してください

## Statement observation

caller-owned executorを置換せず、context単位の実行logを有効化できます

```go
ctx = orm.WithStatementObserver(ctx, orm.NewStatementLogger(os.Stderr))
```

defaultのloggerはargument valueを受け取らず、operation、duration、bind count、affected rows、SQL template、errorを記録します

interactive terminalでは自動的に色を付け、redirect先にはplain textを出力します

lifecycleの対象、custom observer、logの安全境界は[Statement observation guide](docs/observability_ja.md)を参照してください

bind argument valueが必要な場合だけ `IncludeStatementArguments` で明示的に有効化できます

structured analysisには1個のcaptureを再利用し、requestまたはjob境界だけで設定します

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture)
```

既存ORM callには登録、wrapper、diagnostic callを追加しません

JSON Lines artifactは実際に通過したroot query、collection preload、bulk splitをbind valueなしのfingerprint、duration、row countとともに記録します

model rowを返すSELECTとplan recordにはcompiler decisionも含めます

DB接続なしでoffline解析できます

```sh
tidbgo analyze runtime.jsonl
```

runtime captureは `EXPLAIN`、ServerRU read、その他のdatabase I/Oを追加しません

bind valueは除外しますが、SQL templateとerrorにはapplication dataが含まれる場合があります

scope、writer error、retention、任意の1 operation向け `Debug` の詳細は[Statement observation guide](docs/observability_ja.md)を参照してください

## TiDB diagnostics

root queryを実行せずtyped SELECTのplanを確認できます

```go
plan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    Explain(ctx, db)
```

`Explain` はTiDBのdefault 5 column row formatを `[]orm.ExplainRow` として返します

mutationとraw SQLを受け付けず、1 database round tripを追加し、inline to-one joinを含むroot SELECTだけを対象にします

collection preload statementは含みません

TiDBは `EXPLAIN` のoptimization中に一部のsubqueryを評価する場合があります

完全なboundaryは[Statement observation guide](docs/observability_ja.md#select-explain)を参照してください

明示した場合だけtyped SELECTを実行してactual operator dataを取得できます

```go
runtimePlan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    ExplainAnalyze(ctx, db)
if err != nil {
    return err
}
diagnostics := runtimePlan.Diagnostics()
```

`ExplainAnalyze` はactual row、execution information、memory、disk usageを `orm.ExplainAnalyzePlan` として返します

`Diagnostics` は追加のDB callを行わず、返されたrowから不完全なstatistics、保守的なestimateとactual rowの差、大規模table full scan、disk usageを検査します

limitを追加せずcompleteなroot SELECTを実行し、database resourceとRUを消費します

mutation、raw SQL、collection preload statementは対象外です

applicationで有効化する前に[runtime plan boundary](docs/observability_ja.md#explain-analyze)を確認してください

完了した1 DML statementについて、同じpinned sessionからTiDBのServerRUを取得できます

```go
connection, err := db.Conn(ctx)
if err != nil {
    return err
}
defer connection.Close()

users, err := orm.Query[User]().Where(orm.Equal("Active", true)).All(ctx, connection)
if err != nil {
    return err
}
serverRU, err := orm.LastServerRU(ctx, connection)
```

`LastServerRU` が受け付けるのは `*sql.Conn` またはactiveな `*sql.Tx` だけです

2回目のcallが別のpooled connectionを使い得るため `*sql.DB` は対象外です

1 round tripを追加し、sessionに記録された最後の1 DML statementだけを返すため、preloadや分割bulk operationのRUは合計しません

ServerRUはTiDBが報告するdiagnostic valueであり請求RUではありません

詳細は[Statement observation guide](docs/observability_ja.md#serverru)を参照してください

## CLI

applicationが所有するJSON diagnostic arrayをstandard inputまたは1個の明示的なfileからreportします

```sh
go run ./examples/starter-app/cmd/check | tidbgo check
tidbgo check diagnostics.json --json
```

active errorがある場合はstatus `1`、warningとinfoだけの場合はsuccessになります

suppressionにはexact codeとreasonが必須でreportへ残り、non-suppressibleまたは未使用の指定はerrorになります

```sh
go run ./examples/starter-app/cmd/check | \
  tidbgo check --suppress 'MOD005=read-only model does not use key mutations'
```

application側のcheck commandがmodel type、query builder、schema snapshotを明示的に選択します

`tidbgo check` はsource scan、code generation、configuration discovery、DB accessを行いません

詳細は[Offline diagnostic report guide](docs/checks_ja.md)を参照してください

application queryの登録とDB接続なしでstructured runtime artifactを解析できます

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --json
```

このcommandはcaptured statementを集約し、observer scopeごとのcompiler fallbackとN+1 SELECT候補をreportします

詳細は[Statement observation guide](docs/observability_ja.md#structured-runtime-capture)を参照してください

default projectionが同じfunction内のresult利用より広いことを証明できるproduction Go sourceを解析できます

```sh
tidbgo lint
tidbgo lint ./internal/repository --json
```

pathを省略するとcurrent directoryを使用します

application codeの実行、package load、DB接続、source変更は行いません

`All`、`First`、`Only` resultの全利用を同じfunction内で理解できる場合だけ `SRC001` を出力します

return、別functionへの引き渡し、alias、preloadなど不確実なflowは推測せず `uncertain` として数え、全reportへcoverage statisticsを含めます

詳細は[Offline diagnostic report guide](docs/checks_ja.md#go-source-projection解析)を参照してください

version情報は次のcommandで出力します

```sh
tidbgo version
```

development buildは `tidbgo dev`、release buildはbuild時に設定したversionを出力します

`tidbgo --version` と `tidbgo -V` でも同じ結果を得られます

command helpは `tidbgo --help` で表示できます

## Security

- typed builderはvalueをbind argumentとしてSQL textから分離する
- model由来identifierはSQLへ書き込む前にvalidationする
- built-in statement loggerはdefaultでargument valueを除外する
- debug reportはdefaultでargument valueを除外するがSQL templateとerrorは保持する
- runtime captureはbind valueを除外するがSQL templateとerrorを保持するためartifactの出力先とretentionを保護する
- `IncludeStatementArguments` はcredential、token、personal dataを公開し得るため管理されたdebug時だけ有効化する
- Raw SQLはtrusted application codeでありtyped builderの構造validationとmutation safety validationを受けない

[Mutationとraw SQL](docs/mutations_ja.md)と[Statement observation](docs/observability_ja.md)も参照してください

## 現在の制限

- scalar runtimeは `Build`、`All`、`First`、`Only`、`Exists`、`Count`、`Explain`、`ExplainAnalyze` に対応し、`IDs` は未実装
- directとpure `many_to_many` Relation predicateとpreloadはnested指定にも対応
- filtered positive collection predicateはTiDBのsemi-join rewrite hintを使い、条件を満たすordered direct `has_many` pageはrelation-first TopN SQLを使う
- preload projection、collection order、logical deleted targetをRelation path単位で含める指定に対応し、任意のtarget predicateは未実装
- typed mutationはbind value代入と同じcolumnへのadditionだけを公開し、任意のSQL expression、無条件UPDATE、無条件DELETEには `RawExec` を明示的なescape hatchとする
- database connection constructor、bundled protocol driver、Migration application API、live schema introspection APIはまだ存在しない

## License

MIT Licenseです

[LICENSE](LICENSE) を参照してください
