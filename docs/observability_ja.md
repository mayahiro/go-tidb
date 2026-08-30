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

SELECT durationには `QueryContext`、row scan、iteration、row closeを含めます

`sql.ErrNoRows` と `orm.ErrMultipleRows` のようなterminal errorもeventへ含めます

durationを確定した後にobserverを同期実行します

custom observerは短時間でreturnし、contextを共有する場合はconcurrency-safeにし、panicしないようにしてください

`NewStatementLogger` はwriteを直列化し、writer errorがdatabase resultを置き換えないよう無視します

`WithStatementObserver` へnilを渡すと継承したobserverを無効化します

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

typedとrawのSELECT、preload、typed mutation、自動分割したbulk mutation、Relation mutation、`RawExec` を観測します

typed upsertはlogical `UPSERT` operationを使います

`RawExec` は先頭の `INSERT`、`UPDATE`、`DELETE` を判定し、その他のraw mutationは `EXEC` を使います

`Transaction` は `BEGIN`、`COMMIT`、`ROLLBACK` を別eventとして出力します

callback内で `go-tidb` を通じて実行したstatementは、各ORM callへ渡したcontextのobserverを使います

`*sql.DB`、`*sql.Conn`、`*sql.Tx` を直接呼び出した場合は対象外です

`go-tidb` は `database/sql` driver interceptorをinstallしません

defaultではobserverを設定しません

offlineの `Build` とmodel inspectionはeventを生成せず、I/Oも行いません
