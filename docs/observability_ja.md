# Statement observation

[English](observability.md)

`orm` packageはcaller-owned `database/sql` executorをwrapまたは置換せず、実行statementを観測できます

観測はopt-inで `context.Context` 単位です

```go
ctx = orm.WithStatementObserver(ctx, orm.NewStatementLogger(os.Stderr))

users, err := orm.Query[User]().Where(orm.Equal("Active", true)).All(ctx, db)
```

built-in loggerは完了したstatementを1行ずつ出力します

```text
[tidbgo] 12:47:35.066 SELECT   9.419ms args=1 SELECT `id`, `email` FROM `users` WHERE `active` = ?
[tidbgo] 12:47:35.077 UPDATE   10.893ms args=2 affected=1 UPDATE `users` SET `email` = ? WHERE `id` = ?
```

writerがinteractive terminalなどのcharacter-device `*os.File` の場合はoperation名へ自動的に色を付け、errorを赤色にします

redirect先のfile、buffer、その他のwriterにはANSI escape sequenceを含まないplain textを出力します

SQLとerrorのcontrol characterをescapeし、1 eventを1 physical lineに保ちます

loggerは次を出力します

- local start time
- logical operation
- duration
- bind argument count
- 取得できた場合のdatabase-reported affected rows
- SQL template
- operation失敗時のerror

defaultではbind argument valueを受け取らず、logにも出力しません

raw SQLをそのように組み立てた場合はSQL template自体にapplication literalが含まれる可能性があり、database errorにdata valueが含まれる可能性もあります

明示的なdebug機能として扱い、production出力へ有効化する前にこれらの入力元を確認してください

queryの再現にvalueが必要な場合だけ明示的に有効化します

```go
ctx = orm.WithStatementObserver(
    ctx,
    orm.NewStatementLogger(os.Stderr),
    orm.IncludeStatementArguments(),
)
```

valueはSQLへ補間せず、独立した `values=[...]` fieldへ出力します

observerからdriverのescapeとconversion結果を検証できないため、interpolated statementは生成しません

出力するのはdriver conversion前のoriginal Go valueのsnapshotです

secret、personal data、大きなpayloadを公開する可能性があるため、出力先とretention policyが適切な場合だけ有効化してください

## Custom observer

application logger、trace、metric collector、test assertionへeventを渡す場合はcustom `StatementObserver` を使います

```go
ctx = orm.WithStatementObserver(ctx, func(event orm.StatementEvent) {
    metrics.Observe(
        string(event.Operation),
        event.Duration,
        event.Error,
    )
})
```

`StatementEvent` はSQL templateとargument countを保持します

`Arguments` はdefaultでnilとなり、`IncludeStatementArguments` を有効化した場合だけshallowなslice snapshotを保持します

mutationは `sql.Result.RowsAffected` が成功した場合だけ `RowsAffectedKnown` を設定します

SELECTまたはEXPLAINのdurationには `QueryContext`、row scan、iteration、row closeを含めます

`sql.ErrNoRows` と `orm.ErrMultipleRows` のようなterminal errorもeventへ含めます

durationを確定した後にobserverを同期実行します

custom observerは短時間でreturnし、contextを共有する場合はconcurrency-safeにし、panicしないようにしてください

`NewStatementLogger` はwriteを直列化し、writer errorがdatabase resultを置き換えないよう無視します

`WithStatementObserver` へnilを渡すと継承したobserverを無効化します

## Operation debug report

`Debug` で1 application operation内に完了した全statementをまとめます

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

callback内のoperationには `debugContext` を使う必要があります

`Statements` はobserver delivery順のnon-nil sliceであり、そのcontextを使ったroot query、collection preload、自動分割bulk mutation、raw statement、transaction lifecycle eventを含みます

各entryはcustom observerと同じ `StatementEvent` です

`Duration` はobserver処理を含むcallback全体、`StatementDuration` はcaptured event durationの合計です

statementを並行実行した場合は `StatementDuration` がcallback durationを超えることがあります

callbackは `debugContext` を使うgoroutineの完了を待つ必要があり、return後に完了したeventはreportへ含みません

callback errorは変更せず、完了済みstatementのreportとともに返します

`Debug` は既存eventだけを収集し、database call、`EXPLAIN`、ServerRU read、implicit transactionを追加しません

`ctx` に既存observerがある場合は同じeventを引き続き受け取ります

reportのbind argumentはdefaultで除外し、必要な場合だけ独立して有効化します

```go
report, err := orm.Debug(ctx, operation, orm.IncludeStatementArguments())
```

argument valueにはsecret、personal data、大きなpayloadが含まれ得ます

default modeでもSQL templateとerrorを保持するため、statement logと同じ出力先とretention controlを適用してください

## SELECT EXPLAIN

typed `SelectQuery` の `Explain` でTiDBが選択したplanを確認できます

```go
plan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    Explain(ctx, db)
```

resultはTiDBのdefault row formatに対応するnon-nilな `[]orm.ExplainRow` です

各rowの `ID`、`EstRows`、`Task`、`AccessObject`、`OperatorInfo` はTiDBが文書化する5 columnへ直接対応します

`EstRows` はoptimizer estimateでありactual row countではありません

想定外のestimateまたはaccess pathはtable statisticsが古い可能性を示します

`Explain` は `SelectQuery` だけで利用できます

mutationまたはcaller-supplied raw SQLを受け取らず、typed SELECTのbind argumentを維持します

planは `Build` が返すroot SQLを対象とし、inline to-one joinを含みます

collection preload statementはruntimeに返されるparent keyを必要とするため含みません

callごとに1 database round tripを追加します

TiDBは通常root SELECTを実行せずplanを返しますが、一部のsubqueryをoptimization中に評価する場合があると公式文書に記載されています

これは `EXPLAIN ANALYZE` ではなく、actual row count、execution timing、memory、disk measurementを含みません

observerを設定した場合は全plan rowのscanとclose後に `Operation` が `StatementExplain` の `StatementEvent` を1件生成します

built-in loggerは対応するinteractive terminalで `EXPLAIN` をbright cyanにします

bind valueは `IncludeStatementArguments` を有効にしない限り除外します

TiDBの[EXPLAIN statement reference](https://docs.pingcap.com/tidb/stable/sql-statement-explain/)と[execution-plan overview](https://docs.pingcap.com/tidb/stable/explain-overview/)を参照してください

### EXPLAIN ANALYZE

typed SELECTを実際に実行する場合だけ `ExplainAnalyze` を呼び出します

```go
runtimePlan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    ExplainAnalyze(ctx, db)
```

明示的なmethod call自体がopt-inです

protective limitを追加せずcompleteなroot SELECTを実行し、non-nilな `[]orm.ExplainAnalyzeRow` を返します

TiDB default formatの9 columnを `ID`、`EstRows`、`ActRows`、`Task`、`AccessObject`、`ExecutionInfo`、`OperatorInfo`、`Memory`、`Disk` へ対応させます

application modelをhydrateせずruntime planを返します

`Explain` と同じtyped boundaryを持ち、mutation builderとcaller-supplied raw SQLは対象外です

inline to-one joinはroot SELECTの一部として実行し、collection preload statementは除外します

SELECT自体のdatabase resourceを消費し、runtime plan収集のoverheadも追加され得ます

cancellationまたはdeadlineにはcaller contextを使い、production trafficへ自動実行しないでください

`Limit` の追加は測定するqueryとplanを変更するためapplication側の判断とします

TiDBは今回の実行で消費したRUをtop-level `ExecutionInfo` へ含めます

`go-tidb` はserver textのformatをparseせず保持します

cacheとservice conditionによってRUとtimingは実行ごとに変化し得ます

observerを設定した場合はSELECT実行と全plan rowのscanおよびclose後に `StatementExplainAnalyze` を生成します

built-in loggerは対応するinteractive terminalで `EXPLAIN ANALYZE` をbright yellowにし、bind valueはopt-inのままです

TiDBの[EXPLAIN ANALYZE statement reference](https://docs.pingcap.com/ja/tidb/stable/sql-statement-explain-analyze/)を参照してください

## ServerRU

`LastServerRU` は同じsessionに記録された最後のDML statementについて、TiDBが報告する `ru_consumption` を取得します

```go
connection, err := db.Conn(ctx)
if err != nil {
    return err
}
defer connection.Close()

users, err := orm.Query[User]().
    Where(orm.Equal("Active", true)).
    All(ctx, connection)
if err != nil {
    return err
}
serverRU, err := orm.LastServerRU(ctx, connection)
```

`ServerRUSession` constraintが受け付けるのはpinned `*sql.Conn` またはactiveな `*sql.Tx` だけです

metricはsession variableでありfollow-up queryが別connectionを使い得るため、pooled `*sql.DB` はcompile時に対象外となります

ORM query terminalはreturn前にrowを最後まで処理してcloseします

ORM terminal以外で測定対象SQLを実行する場合はmetricを読む前に全rowをcloseしてください

取得ごとに `SELECT @@tidb_last_query_info` を実行するため1 database round tripを追加します

diagnostic readは `StatementEvent` を生成しません

対象DML statementの直後に呼び出してください

preload、自動分割bulk write、その他のmulti-statement pathでは最後の1 DML statementだけを返し、operation全体を合計しません

ServerRUはTiDBが報告するstatement valueであり請求RUではなく、cacheやservice conditionによって実行ごとに変化し得ます

`ru_consumption` が欠落、null、不正JSON、負数、その他の不正値の場合はerrorを返します

TiDBの[`tidb_last_query_info` system variable reference](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info)と[TiDB Cloud Starter RU FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/?plan=starter#how-can-i-estimate-the-number-of-rus-required-by-my-workloads-and-plan-my-monthly-budget)を参照してください

## 対象operation

typedとrawのSELECT、typed SELECTの `EXPLAIN` と `EXPLAIN ANALYZE`、preload、typed mutation、自動分割したbulk mutation、Relation mutation、`RawExec` を観測します

typed upsertはlogical `UPSERT` operationを使います

`RawExec` は先頭の `INSERT`、`UPDATE`、`DELETE` を判定し、その他のraw mutationは `EXEC` を使います

`Transaction` は `BEGIN`、`COMMIT`、`ROLLBACK` を別eventとして出力します

callback内で `go-tidb` を通じて実行したstatementは、各ORM callへ渡したcontextのobserverを使います

`*sql.DB`、`*sql.Conn`、`*sql.Tx` を直接呼び出した場合は対象外です

`go-tidb` は `database/sql` driver interceptorをinstallしません

defaultではobserverを設定しません

offlineの `Build` とmodel inspectionはeventを生成せず、I/Oも行いません
