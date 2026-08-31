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
- Opt-in ServerRU value, diagnostic duration, auxiliary statement count, and
  collection error

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
`ServerRU` is nil unless `CollectServerRU` requested a diagnostic for a
recognized DML operation. When present, it keeps the target result separate
from the value, diagnostic duration, auxiliary statement count, and collection
error.

Observers run synchronously after the duration is captured. Custom observers
should return quickly, be concurrency-safe when contexts are shared, and not
panic. `NewStatementLogger` serializes its own writes and ignores writer errors
so logging cannot replace a database result. Passing nil to
`WithStatementObserver` disables an inherited ordinary observer without
disabling `RuntimeCapture`.

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

`Debug` only collects existing events by default. It adds no database calls,
`EXPLAIN`, or implicit transaction. An observer already present on `ctx`
continues to receive the events. Bind arguments are excluded from the report by
default and can be enabled independently with `IncludeStatementArguments`:

```go
report, err := orm.Debug(ctx, operation, orm.IncludeStatementArguments())
```

Pass `CollectServerRU` only when the callback should add a same-session
diagnostic query after each recognized DML statement:

```go
report, err := orm.Debug(ctx, operation, orm.CollectServerRU())
```

`report.StatementDuration` remains the target-statement total.
`report.ServerRU` is non-nil when collection was requested and separately
reports diagnostic duration, auxiliary statement count, successful sample
count, collection-error count, and the summed ServerRU value.

Argument values can contain secrets, personal data, or large payloads. The
report stores SQL templates and errors even in the default mode, so apply the
same output and retention controls as statement logging.

## Structured runtime capture

Use one reusable `RuntimeCapture` when executed statements should be analyzed
without registering each query in application code:

```go
capture := orm.NewRuntimeCapture(captureWriter)

// Install once at each request, job, or test-operation boundary.
ctx = orm.WithRuntimeCapture(ctx, capture)
```

Continue passing the derived context to existing ORM terminals. No query,
repository, `Debug` callback, `StatementCount`, `EstimateAllStatements`, or
artifact-conversion wrapper is required. Reuse the capture across concurrent
scopes; `WithRuntimeCapture` assigns each call a distinct scope used by offline
N+1 analysis. It also preserves an inherited ordinary `StatementObserver`.
The ordinary observer and capture can be installed in either order. Installing
another capture on the derived context replaces the inherited capture.

The capture writes one JSON object per completed statement. Records contain a
format version, capture and scope identities, bind-free fingerprint, SQL
template, operation, terminal, model or Relation identity when known, start
time, target-statement duration, returned or affected row count, error, and
automatic bulk or preload batch position. Model-row `All`, `First`, and `Only`
records and typed plan records also carry the bind-free query shape and
compiler rewrite or fallback decision. `Count` and `Exists` retain a stable
bind-free statement fingerprint without claiming the model-row projection
shape. Raw SQL is marked as opaque. Collection preloads and automatically split
bulk mutations are recorded from the actual execution path, so
application-side statement count wrappers are unnecessary.

Capture is opt-in. When it is disabled, query-shape construction and artifact
encoding do not run. Ordinary statement observers do not enable this extra
metadata path. Capture performs no additional database I/O by default and
never runs `EXPLAIN`. It records only statements executed through go-tidb with
the derived context; direct `database/sql` and other ORM calls remain outside
its coverage.

Runtime artifacts never contain bind values. Passing
`IncludeStatementArguments` to `WithRuntimeCapture` has no effect; use it only
with `WithStatementObserver` or `Debug` when that sensitive data is explicitly
required.

Enable high-cost ServerRU collection at the same scope boundary when the extra
round trip per recognized DML statement is intentional:

```go
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
```

The resulting record keeps target duration separate from ServerRU diagnostic
duration and auxiliary statement count. `tidbgo analyze` reports go-tidb
statement and auxiliary statement counts separately, together with successful
sample count, collection-error count, and summed ServerRU. A collection failure
produces `RUN003` but never replaces the target statement result.

Analyze a completed artifact without a database connection:

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --json
tidbgo analyze runtime.jsonl --suppress 'RUN002=intentional polling'
```

The CLI streams statement records instead of retaining the complete artifact
in memory. Exact aggregate statistics still retain the distinct capture,
scope, fingerprint, and batch identities needed by the report.

The analyzer reports captured counts and durations, compiler TopN fallbacks,
and repeated non-preload SELECT fingerprints within one scope as possible N+1
queries. Repetition is evidence rather than proof; retries and intentionally
repeated lookups require application review. Failed SELECT attempts are
included because they still consume statements. Every suppression names an
exact code and records a non-empty reason. Preload batch splits are excluded
from the N+1 rule.

Bind values are never written to the runtime artifact. SQL templates can still
contain literals supplied through raw SQL, and database errors can contain
values. Treat the artifact as sensitive development data and choose its file
permissions, destination, and retention accordingly. The caller owns and
closes the writer. Encoding and writer failures never replace database results;
inspect `capture.Err()` when artifact completeness matters.

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
`orm.ExplainAnalyzePlan`. The result maps TiDB's nine default columns to
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

Inspect high-confidence facts in the returned rows without database I/O:

```go
diagnostics := runtimePlan.Diagnostics()
```

`Diagnostics` emits at most one suppressible warning for each rule and attaches
one evidence item per matching operator:

| Code | Runtime-plan fact |
| --- | --- |
| `PLN001` | `OperatorInfo` reports `stats:pseudo` or `stats:partial` |
| `PLN002` | Estimated and actual rows differ by at least 100 times and either side is at least 1,000 rows |
| `PLN003` | A `TableFullScan` operator outputs at least 10,000 rows |
| `PLN004` | The `Disk` column reports a positive value in a recognized TiDB byte unit |

An operator with incomplete statistics is omitted from `PLN002`, because the
known statistics condition is the more direct evidence. The fixed thresholds
are intentionally conservative. A warning identifies a plan fact to inspect;
it does not prove that an index or query rewrite is better. Timing, RU, loops,
RPC details, memory, and other free-form execution text are not parsed.

The diagnostic evidence includes operator identifiers, access objects, row
counts, and recognized disk values. It does not copy full `OperatorInfo`, which
can contain predicate ranges derived from bind values. Treat access-object and
schema identifiers as development metadata. The result can be appended to an
application-owned diagnostic array and passed to `tidbgo check` for the same
reporting and suppression policy as offline checks.

TiDB documents `estRows`, `actRows`, pseudo statistics, and execution-info
fields in its [execution-plan overview](https://docs.pingcap.com/tidb/stable/explain-overview/)
and [EXPLAIN walkthrough](https://docs.pingcap.com/tidb/stable/explain-walkthrough/).
Positive disk usage can indicate an intermediate operator spilled to disk; see
TiDB's [disk-spill documentation](https://docs.pingcap.com/tidb/stable/configure-memory-usage/#disk-spill).

An observed call emits `StatementExplainAnalyze` after the SELECT executes and
all plan rows are scanned and closed. The built-in logger renders
`EXPLAIN ANALYZE` in bright yellow on a supported interactive terminal. Bind
values remain opt-in. See TiDB's [EXPLAIN ANALYZE statement
reference](https://docs.pingcap.com/tidb/stable/sql-statement-explain-analyze/).

## ServerRU

`CollectServerRU` automatically samples recognized SELECT, INSERT, UPSERT,
UPDATE, and DELETE operations when passed to `WithStatementObserver`,
`WithRuntimeCapture`, or `Debug`. It does not sample `EXPLAIN`, transaction
lifecycle events, or unclassified `EXEC` raw SQL.

For `*sql.DB`, go-tidb temporarily pins one connection before the target call,
executes the target and diagnostic on that connection, then returns it to the
pool. A caller-supplied `*sql.Conn` or active `*sql.Tx` is used directly. Other
executor implementations still execute the target but report a collection
error without an auxiliary query. The caller retains ownership of a supplied
connection or transaction. Do not interleave another statement on it while a
measured ORM call is in progress.

Each eligible statement adds one `SELECT @@tidb_last_query_info` round trip
after target completion. SELECT rows are scanned and closed first. The
auxiliary query does not emit another `StatementEvent` or runtime record;
instead its count, duration, value, and error are attached to the target event.
Connection-pool wait and release time introduced by automatic `*sql.DB`
pinning are included in diagnostic duration, not target duration. The
operation's return value and error remain those of the target even when
collection fails. Summed ServerRU contains only the values TiDB reports for
target statements; it does not measure the diagnostic query's own resource
use.

Use `LastServerRU` for a single explicit read instead of automatic collection.

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
