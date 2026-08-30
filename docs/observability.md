# Statement observation

[日本語](observability_ja.md)

The `orm` package can observe executed statements without wrapping or replacing
the caller-owned `database/sql` executor. Observation is opt-in and scoped to a
`context.Context`:

```go
ctx = orm.WithStatementObserver(ctx, orm.NewStatementLogger(os.Stderr))

users, err := orm.Query[User]().Where(orm.Equal("Active", true)).All(ctx, db)
```

The built-in logger writes one completed statement per line:

```text
[tidbgo] 12:47:35.066 SELECT   9.419ms args=1 SELECT `id`, `email` FROM `users` WHERE `active` = ?
[tidbgo] 12:47:35.077 UPDATE   10.893ms args=2 affected=1 UPDATE `users` SET `email` = ? WHERE `id` = ?
```

Operation names are colored when the writer is a character-device `*os.File`,
such as an interactive terminal. Errors are red. Redirected files, buffers,
and other writers receive plain text without ANSI escape sequences. SQL and
error control characters are escaped so one event remains one physical line.

The logger includes:

- Local start time
- Logical operation
- Duration
- Bind argument count
- Database-reported affected rows when available
- SQL template
- Error when the operation fails

By default it does not receive or log bind argument values. The SQL template
can still contain application literals when raw SQL is constructed that way,
and a database error can include data values. Treat the logger as an explicit
debug facility and review those sources before enabling it for production
output.

When values are required to reproduce a query, enable them explicitly:

```go
ctx = orm.WithStatementObserver(
    ctx,
    orm.NewStatementLogger(os.Stderr),
    orm.IncludeStatementArguments(),
)
```

Values remain separate from SQL in a `values=[...]` field. `go-tidb` does not
produce an interpolated statement because that would imply driver escaping and
conversion that the observer cannot verify. These are snapshots of the original
Go values before driver conversion. This mode can expose secrets, personal data,
and large payloads, so keep it off unless the output destination and retention
policy are appropriate.

## Custom observers

Use a custom `StatementObserver` when events should go to an application logger,
trace, metric collector, or test assertion:

```go
ctx = orm.WithStatementObserver(ctx, func(event orm.StatementEvent) {
    metrics.Observe(
        string(event.Operation),
        event.Duration,
        event.Error,
    )
})
```

`StatementEvent` retains the SQL template and argument count. `Arguments` is nil
by default and contains a shallow slice snapshot only when
`IncludeStatementArguments` is enabled. A mutation sets `RowsAffectedKnown`
only after `sql.Result.RowsAffected` succeeds. A SELECT or EXPLAIN duration
covers `QueryContext`, row scanning, iteration, and row closing. Terminal errors
such as `sql.ErrNoRows` and `orm.ErrMultipleRows` are included in the event.

Observers run synchronously after the duration is captured. Custom observers
should return quickly, be concurrency-safe when contexts are shared, and not
panic. `NewStatementLogger` serializes its own writes and ignores writer errors
so logging cannot replace a database result. Passing nil to
`WithStatementObserver` disables an inherited observer.

## Operation debug reports

Use `Debug` to group every statement completed by one application operation:

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

The callback must execute the operation with `debugContext`. `Statements` is a
non-nil slice in observer delivery order and includes root queries, collection
preloads, automatically split bulk mutations, raw statements, and transaction
lifecycle events when those paths use that context. Each entry is the same
`StatementEvent` shape used by custom observers.

`Duration` measures the complete callback including observer work.
`StatementDuration` is the sum of captured event durations and can exceed the
callback duration when statements execute concurrently. The callback must wait
for any goroutines that use `debugContext`; events completed after it returns
are outside the report. A callback error is returned unchanged with the report
of statements that already completed.

`Debug` only collects existing events. It adds no database calls, `EXPLAIN`,
ServerRU reads, or implicit transaction. An observer already present on `ctx`
continues to receive the events. Bind arguments are excluded from the report by
default and can be enabled independently with `IncludeStatementArguments`:

```go
report, err := orm.Debug(ctx, operation, orm.IncludeStatementArguments())
```

Argument values can contain secrets, personal data, or large payloads. The
report stores SQL templates and errors even in the default mode, so apply the
same output and retention controls as statement logging.

## SELECT EXPLAIN

Call `Explain` on a typed `SelectQuery` to inspect the plan chosen by TiDB:

```go
plan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    Explain(ctx, db)
```

The result is a non-nil `[]orm.ExplainRow` in TiDB's default row format. Each
row contains `ID`, `EstRows`, `Task`, `AccessObject`, and `OperatorInfo`, which
map directly to the five columns documented by TiDB. `EstRows` is an optimizer
estimate, not an observed row count. Unexpected estimates or access paths can
indicate stale table statistics.

`Explain` is available only on `SelectQuery`. It cannot receive a mutation or
caller-supplied raw SQL, and it preserves the typed SELECT's bind arguments.
The plan covers the root SQL returned by `Build`, including inline to-one
joins. Collection preload statements depend on parent keys returned at runtime
and are not included.

The call adds one database round trip. TiDB normally returns the plan without
executing the root SELECT, although TiDB documents that certain subqueries can
be evaluated during optimization. This is not `EXPLAIN ANALYZE` and contains
no actual row counts, execution timing, memory, or disk measurements.

An observed call emits one `StatementEvent` with `Operation` set to
`StatementExplain` after all plan rows are scanned and closed. The built-in
logger renders `EXPLAIN` in bright cyan on a supported interactive terminal.
Bind values remain excluded unless `IncludeStatementArguments` is enabled.
See TiDB's [EXPLAIN statement
reference](https://docs.pingcap.com/tidb/stable/sql-statement-explain/) and
[execution-plan overview](https://docs.pingcap.com/tidb/stable/explain-overview/).

### EXPLAIN ANALYZE

Call `ExplainAnalyze` only when the typed SELECT should actually run:

```go
runtimePlan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    ExplainAnalyze(ctx, db)
```

The explicit method call is the opt-in. It executes the complete root SELECT
without adding a protective limit, then returns a non-nil
`[]orm.ExplainAnalyzeRow`. The result maps TiDB's nine default columns to
`ID`, `EstRows`, `ActRows`, `Task`, `AccessObject`, `ExecutionInfo`,
`OperatorInfo`, `Memory`, and `Disk`. It returns the runtime plan rather than
hydrating application models.

`ExplainAnalyze` has the same typed boundary as `Explain`: mutation builders
and caller-supplied raw SQL cannot enter it, inline to-one joins are part of the
root SELECT, and collection preload statements are excluded. It executes the
SELECT and consumes its database resources; collecting the runtime plan can
add overhead. Use the caller context for cancellation or a deadline, and do
not run it automatically on production traffic. Adding `Limit` is an
application decision because it changes the measured query and plan.

TiDB includes the RU consumed by this execution in the top-level
`ExecutionInfo`. `go-tidb` preserves that server text without parsing its
format. RU and timing can vary between runs because of caches and service
conditions.

An observed call emits `StatementExplainAnalyze` after the SELECT executes and
all plan rows are scanned and closed. The built-in logger renders
`EXPLAIN ANALYZE` in bright yellow on a supported interactive terminal. Bind
values remain opt-in. See TiDB's [EXPLAIN ANALYZE statement
reference](https://docs.pingcap.com/tidb/stable/sql-statement-explain-analyze/).

## ServerRU

`LastServerRU` reads the `ru_consumption` reported by TiDB for the last DML
statement recorded on the same session:

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

The `ServerRUSession` constraint accepts only a pinned `*sql.Conn` or an active
`*sql.Tx`. A pooled `*sql.DB` is excluded at compile time because the metric is
a session variable and a follow-up query can use another connection. ORM query
terminals consume and close their rows before returning. When the measured SQL
is executed outside an ORM terminal, close all rows before reading the metric.

Each read executes `SELECT @@tidb_last_query_info` and therefore adds one
database round trip. It is a diagnostic read and does not emit a
`StatementEvent`. Call it immediately after the target DML statement. For an
operation with preloads, automatically split bulk writes, or any other
multi-statement path, it reports only the last DML statement and does not
aggregate the operation.

ServerRU is the statement value reported by TiDB. It is not billed RU and can
vary between executions because of caches and service conditions. A missing,
null, malformed, negative, or otherwise invalid `ru_consumption` is returned as
an error. See TiDB's [`tidb_last_query_info` system variable
reference](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info)
and the [TiDB Cloud Starter RU
FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/?plan=starter#how-can-i-estimate-the-number-of-rus-required-by-my-workloads-and-plan-my-monthly-budget).

## Covered operations

Typed and raw SELECTs, typed SELECT `EXPLAIN` and `EXPLAIN ANALYZE`, preloads,
typed mutations, automatically split bulk mutations, relation mutations, and
`RawExec` are observed. Typed upserts use the logical `UPSERT` operation.
`RawExec` recognizes a leading `INSERT`, `UPDATE`, or `DELETE`; other raw
mutations use `EXEC`.

`Transaction` emits separate `BEGIN`, `COMMIT`, and `ROLLBACK` events. Statements
executed through `go-tidb` inside its callback use the observer from the context
passed to each ORM call. Calls made directly on `*sql.DB`, `*sql.Conn`, or
`*sql.Tx` are outside this boundary because `go-tidb` does not install a
`database/sql` driver interceptor.

No observer is installed by default. Offline `Build` and model inspection never
emit events or perform I/O.
