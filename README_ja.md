# go-tidb

`go-tidb` はTiDB Cloud Starter向けのoffline-first、struct-first Go ORMです

Go module pathは `github.com/mayahiro/go-tidb`、command名は `tidbgo` です

[English](README.md) | [Struct model](docs/models_ja.md) |
[Query](docs/queries_ja.md) | [Mutationとraw SQL](docs/mutations_ja.md) |
[Statement observation](docs/observability_ja.md) | [Development](docs/development_ja.md)

## 利用できる機能

- generated modelを必要としないapplication-owned Go struct
- offline model validationとSQL構築
- caller-owned `*sql.DB`、`*sql.Conn`、`*sql.Tx` による明示的な実行
- scalar predicate、order、offset pagination、keyset pagination
- 決定的なdirectとmany-to-many Relation predicateおよびpreload
- single insert、upsert、自動分割するbulk insertとbulk upsert
- primary keyまたはpredicateで範囲を限定したupdateとdelete
- soft delete、restore、pure junction mutation、transaction helper
- raw JOIN、CTE、aggregate、partial resultのtyped scan
- terminalの自動色付きcontext-scoped statement observation
- multi-statement ORM callをまとめるoperation-scoped debug report
- typed query builderによるSELECT限定のTiDB execution plan取得
- 明示的なSELECT実行によるTiDB actual runtime plan取得
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
```

`Build`はDBへ接続せず、custom `driver.Valuer`も実行しません

valueはbind argumentとしてSQLから分離し、物理identifierにはvalidated model metadataだけを使います

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

`Has` はdirectまたはpure many-to-many Relation条件をcorrelated `EXISTS` subqueryへcompileします

target predicateを渡せば一致するrelated rowを条件とし、省略すれば存在だけを条件とします

Relation名とtarget field名にはexported Go field名を使い、`Build` はDB接続なしでvalidationとcompileを行います

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

追加database callなしで1 ORM operationのroot statementとRelation statementをまとめて取得できます

```go
var users []User
report, err := orm.Debug(ctx, func(debugContext context.Context) error {
    var queryErr error
    users, queryErr = orm.Query[User]().
        Preload("Orders").
        All(debugContext, db)
    return queryErr
})
```

`report.Statements` は完了event、`report.StatementDuration` はその累積duration、`report.Duration` はcallback全体のdurationを保持します

bind valueは `Debug` へ `IncludeStatementArguments` を渡した場合だけ含めます

wrapper自体は `EXPLAIN`、ServerRU read、その他のdatabase I/Oを行いません

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
```

`ExplainAnalyze` はactual row、execution information、memory、disk usageを `[]orm.ExplainAnalyzeRow` として返します

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

`tidbgo` CLIで現在利用できるのはversion情報だけです

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
- `IncludeStatementArguments` はcredential、token、personal dataを公開し得るため管理されたdebug時だけ有効化する
- Raw SQLはtrusted application codeでありtyped builderの構造validationとmutation safety validationを受けない

[Mutationとraw SQL](docs/mutations_ja.md)と[Statement observation](docs/observability_ja.md)も参照してください

## 現在の制限

- scalar runtimeは `Build`、`All`、`First`、`Only`、`Exists`、`Count`、`Explain`、`ExplainAnalyze` に対応し、`IDs` は未実装
- directとpure `many_to_many` Relation predicateとpreloadはnested指定にも対応
- preload projection、collection order、logical deleted targetをRelation path単位で含める指定に対応し、任意のtarget predicateは未実装
- typed mutationはbind value代入と同じcolumnへのadditionだけを公開し、任意のSQL expression、無条件UPDATE、無条件DELETEには `RawExec` を明示的なescape hatchとする
- database connection constructor、bundled protocol driver、Migration APIはまだ存在しない

## License

MIT Licenseです

[LICENSE](LICENSE) を参照してください
