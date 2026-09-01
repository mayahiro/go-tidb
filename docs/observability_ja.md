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
- opt-inのServerRU value、diagnostic duration、auxiliary statement count、collection error

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

`ServerRU` は `CollectServerRU` がrecognized DML operationのdiagnosticを要求した場合だけnon-nilになります

target resultとServerRU value、diagnostic duration、auxiliary statement count、collection errorを分離して保持します

durationを確定した後にobserverを同期実行します

custom observerは短時間でreturnし、contextを共有する場合はconcurrency-safeにし、panicしないようにしてください

`NewStatementLogger` はwriteを直列化し、writer errorがdatabase resultを置き換えないよう無視します

`WithStatementObserver` へnilを渡すと継承した通常observerを無効化しますが、`RuntimeCapture` は無効化しません

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

`Debug` はdefaultでは既存eventだけを収集し、database call、`EXPLAIN`、implicit transactionを追加しません

`ctx` に既存observerがある場合は同じeventを引き続き受け取ります

reportのbind argumentはdefaultで除外し、必要な場合だけ独立して有効化します

```go
report, err := orm.Debug(ctx, operation, orm.IncludeStatementArguments())
```

recognized DML statementごとにsame-session diagnostic queryを追加する場合だけ `CollectServerRU` を渡します

```go
report, err := orm.Debug(ctx, operation, orm.CollectServerRU())
```

`report.StatementDuration` はtarget statementの合計のままです

collectionを要求した場合は `report.ServerRU` がnon-nilとなり、diagnostic duration、auxiliary statement count、成功sample数、collection error数、ServerRU合計を分離して返します

argument valueにはsecret、personal data、大きなpayloadが含まれ得ます

default modeでもSQL templateとerrorを保持するため、statement logと同じ出力先とretention controlを適用してください

## Structured runtime capture

application codeでqueryごとの登録を行わず実行statementを解析する場合は1個の `RuntimeCapture` を再利用します

```go
capture := orm.NewRuntimeCapture(captureWriter)

// request、job、test operationの境界ごとに1回だけ設定します
ctx = orm.WithRuntimeCapture(ctx, capture)
```

既存ORM terminalにはderived contextをそのまま渡します

query、repository、`Debug` callback、`StatementCount`、`EstimateAllStatements`、artifact変換のwrapperは不要です

captureはconcurrentなscope間で再利用でき、`WithRuntimeCapture` の呼び出しごとにoffline N+1解析用の異なるscopeを割り当てます

継承した通常の `StatementObserver` も維持します

通常observerとcaptureはどちらの順序でも設定できます

derived contextへ別のcaptureを設定した場合は継承したcaptureを置き換えます

captureは完了statementごとに1個のJSON objectを書き込みます

recordにはformat version、captureとscopeのidentity、bind valueを含まないfingerprint、SQL template、operation、terminal、判明しているmodelまたはRelation identity、start time、対象statement duration、returnedまたはaffected row count、error、自動bulkまたはpreload batch位置を含めます

model rowを返す `All`、`First`、`Only` とtyped plan recordにはbind valueを含まないquery shapeとcompiler rewriteまたはfallback decisionも記録します

LIMITとOFFSETのbind valueは除外したまま、offline ruleがzero LIMITを区別できるよう指定の有無と正数かどうかだけをshapeへ記録します

`Count` と `Exists` はmodel row projection shapeを表明せず、bind valueを含まない安定したstatement fingerprintを記録します

Raw SQLはopaqueとして記録します

collection preloadと自動分割bulk mutationは実際のexecution pathから記録するため、application側のstatement count wrapperは不要です

captureはopt-inです

無効時はquery shape生成とartifact encodeを実行せず、通常のstatement observerもこの追加metadata pathを有効化しません

captureはdefaultで追加database I/Oを行わず、`EXPLAIN` を実行しません

runtime artifactはbind valueを保持しません

`WithRuntimeCapture` へ `IncludeStatementArguments` を渡しても効果はなく、このsensitive dataが明示的に必要な場合だけ `WithStatementObserver` または `Debug` で使います

recognized DML statementごとの追加round tripを意図して受け入れる場合は同じscope boundaryで高costなServerRU収集を有効にします

```go
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
```

recordはtarget durationとServerRU diagnostic durationおよびauxiliary statement countを分離します

`tidbgo analyze` はgo-tidb statement数、auxiliary statement数、成功sample数、collection error数、ServerRU合計を別々にreportします

collection failureは `RUN003` を生成しますがtarget statement resultを置き換えません

derived contextを使ってgo-tidbから実行したstatementだけを記録し、直接の `database/sql` または他ORMのcallは対象外です

完了したartifactはDB接続なしで解析できます

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --json
tidbgo analyze runtime.jsonl --schema schema.sql
tidbgo analyze runtime.jsonl --suppress 'RUN002=intentional polling'
```

CLIはartifact全体をmemoryへ保持せずstatement recordをstreaming解析します

正確なaggregate statisticsに必要な異なるcapture、scope、fingerprint、batch identityは保持します

analyzerは異なるcaptured query diagnosticごとに `QRY002` から `QRY005` を1回適用し、同一scope内でpreload以外のSELECT fingerprintが繰り返された場合のN+1候補をreportします

`--schema` はofflineのTiDB `CREATE TABLE` snapshotをparseし、各captured query shapeへ `QRY006` と `QRY007` を適用します

DB接続は行いません

statisticsは全captured statement、query shapeを持つstatement、snapshotと照合したstatementを分離します

`Count`、`Exists`、raw SQL、collection preload statement、mutationはcompleteなmodel row query shapeを表明しないため、これらのquery shape ruleの対象外です

反復は証拠であり確定ではなく、retryまたは意図した反復lookupはapplication reviewが必要な場合があります

失敗したSELECT attemptもstatementを消費するため対象に含めます

suppressionはexact codeと空ではないreasonを記録します

preload batch splitはN+1 ruleから除外します

bind valueはruntime artifactへ書き込みません

Raw SQLへ直接記述したliteralはSQL templateに残り、database errorにもvalueが含まれる場合があります

artifactをsensitiveなdevelopment dataとして扱い、file permission、destination、retentionを管理してください

writerの所有とcloseはcallerが担当します

encodeまたはwriter errorはdatabase resultを置き換えず、artifactの完全性が必要な場合は `capture.Err()` を確認します

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

protective limitを追加せずcompleteなroot SELECTを実行し、non-nilな `orm.ExplainAnalyzePlan` を返します

TiDB default formatの9 columnを `ID`、`EstRows`、`ActRows`、`Task`、`AccessObject`、`ExecutionInfo`、`OperatorInfo`、`Memory`、`Disk` へ対応させます

application modelをhydrateせずruntime planを返します

各rowはcompilerから導出した `PhysicalTable`、`Model`、`RelationPath` fieldも持ちます

これらはTiDBが返す追加columnではありません

go-tidbは `AccessObject` の正確な `table:` tokenを読み、compile済みroot SELECT、inline preload join、Relation predicate、many-to-many junction、Relation-first TopN associationのmetadataと照合します

生成aliasはmodel tagではなく1 query内のoccurrenceを識別します

同じnesting depthに複数Relationがある場合もRelation occurrenceごとに異なるaliasを使い、targetとrootからのRelation pathを識別可能にします

`PhysicalTable` と `Model` はrootまたは関連modelを識別し、rootの `RelationPath` は空です

junction tableはphysical tableとRelation pathを持ちますがGo modelを持ちません

derived tableにはphysical table mappingを設定しません

TiDBが未知のtokenまたは複数Relation pathで共有するphysical nameだけを返した場合は `AccessObject` を保持し、SQL parseまたは推測を行わず曖昧なderived fieldを空にします

`Explain` と同じtyped boundaryを持ち、mutation builderとcaller-supplied raw SQLは対象外です

inline to-one joinはroot SELECTの一部として実行し、collection preload statementは除外します

SELECT自体のdatabase resourceを消費し、runtime plan収集のoverheadも追加され得ます

cancellationまたはdeadlineにはcaller contextを使い、production trafficへ自動実行しないでください

`Limit` の追加は測定するqueryとplanを変更するためapplication側の判断とします

TiDBは今回の実行で消費したRUをtop-level `ExecutionInfo` へ含めます

`go-tidb` はserver textのformatをparseせず保持します

cacheとservice conditionによってRUとtimingは実行ごとに変化し得ます

返されたrowに含まれる確度の高い事実をDB I/Oなしで検査できます

```go
diagnostics := runtimePlan.Diagnostics()
```

`Diagnostics` はruleごとに最大1個のsuppressible warningを生成し、該当するoperatorごとにevidenceを1個追加します

| Code | Runtime planの事実 |
| --- | --- |
| `PLN001` | `OperatorInfo` が `stats:pseudo` または `stats:partial` を報告する |
| `PLN002` | estimateとactual rowが100倍以上異なり、いずれかが1,000 row以上である |
| `PLN003` | `TableFullScan` operatorが10,000 row以上を出力する |
| `PLN004` | `Disk` columnがTiDBの認識可能なbyte unitで正数を報告する |

不完全なstatisticsを持つoperatorは原因を直接示す `PLN001` を優先し、同じoperatorを `PLN002` へ含めません

固定thresholdは意図的に保守的です

warningは確認すべきplan上の事実を示すものであり、index追加またはquery rewriteが常に改善になることを保証しません

timing、RU、loop、RPC detail、memory、その他のfree-form execution textはparseしません

diagnostic evidenceにはoperator identifier、access object、解決できたphysical table、model、Relation path、row count、認識したdisk valueを含みます

bind value由来のpredicate rangeを含み得るため、完全な `OperatorInfo` はコピーしません

access object、model、Relation、schema identifierはdevelopment metadataとして扱ってください

結果はapplicationが所有するdiagnostic arrayへ追加し、offline checkと同じreportおよびsuppression policyで `tidbgo check` へ渡せます

TiDBの `estRows`、`actRows`、pseudo statistics、execution info fieldについては[execution-plan overview](https://docs.pingcap.com/tidb/stable/explain-overview/)と[EXPLAIN walkthrough](https://docs.pingcap.com/tidb/stable/explain-walkthrough/)を参照してください

正数のdisk usageはintermediate operatorのdisk spillを示す場合があります

TiDBの[disk-spill documentation](https://docs.pingcap.com/tidb/stable/configure-memory-usage/#disk-spill)を参照してください

observerを設定した場合はSELECT実行と全plan rowのscanおよびclose後に `StatementExplainAnalyze` を生成します

built-in loggerは対応するinteractive terminalで `EXPLAIN ANALYZE` をbright yellowにし、bind valueはopt-inのままです

TiDBの[EXPLAIN ANALYZE statement reference](https://docs.pingcap.com/ja/tidb/stable/sql-statement-explain-analyze/)を参照してください

## ServerRU

`CollectServerRU` は `WithStatementObserver`、`WithRuntimeCapture`、`Debug` へ渡した場合にrecognized SELECT、INSERT、UPSERT、UPDATE、DELETE operationを自動sampleします

`EXPLAIN`、transaction lifecycle event、分類できないraw `EXEC` はsampleしません

`*sql.DB` ではgo-tidbがtarget callの前に1 connectionを一時的にpinし、そのconnection上でtargetとdiagnosticを実行してからpoolへ返します

caller-supplied `*sql.Conn` またはactiveな `*sql.Tx` は直接使います

渡されたconnectionまたはtransactionのownershipはcallerが維持します

その他のexecutor implementationではtargetを通常どおり実行し、auxiliary queryを行わずcollection errorをreportします

測定中のORM callと並行してcaller-supplied connectionまたはtransactionへ別statementを挟まないでください

eligible statementごとにtarget完了後の `SELECT @@tidb_last_query_info` 1 round tripを追加します

SELECT rowは先にscanしてcloseします

auxiliary queryは別の `StatementEvent` またはruntime recordを生成せず、count、duration、value、errorをtarget eventへ保持します

automaticな `*sql.DB` pinningで追加されたconnection pool waitとrelease timeはtarget durationではなくdiagnostic durationへ含めます

collectionが失敗してもoperationのreturn valueとerrorはtargetのものを維持します

ServerRU合計はTiDBがtarget statementについて返したvalueだけを含み、diagnostic query自体のresource useは測定しません

automatic collectionではなく1回だけ明示的に読む場合は `LastServerRU` を使います

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
