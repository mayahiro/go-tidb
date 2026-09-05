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

## Structured runtime capture

Use one reusable `RuntimeCapture` when executed statements should be analyzed
without registering each query in application code:

```go
capture := orm.NewRuntimeCapture(captureWriter)

// Install once at each request, job, or test-operation boundary.
ctx = orm.WithRuntimeCapture(ctx, capture)
```

Continue passing the derived context to existing ORM terminals. No query,
repository, statement-count, or artifact-conversion wrapper is required. Reuse
the capture across concurrent
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
compiler rewrite or fallback decision. LIMIT and OFFSET bind values remain
excluded; the shape records only whether each bound is present and positive so
offline rules can distinguish a zero LIMIT. `Count` and `Exists` retain a stable
bind-free statement fingerprint without claiming the model-row projection
shape. Raw SQL is marked as opaque. Collection preloads and automatically split
bulk mutations are recorded from the actual execution path, so
application-side statement count wrappers are unnecessary.

`UpdateWhere` and `DeleteWhere` records also carry a scalar-only `mutation`
shape: model, physical table, predicate operators and columns, empty-list
classification, and any implicit active-row soft-delete column. Assignments
and bind values are excluded. Soft-delete `DeleteWhere` retains its actual
`UPDATE` operation and `delete_where` terminal. These records use the existing
SQL statement fingerprint, including for ServerRU baselines. Artifact version
is `1`.

Capture is opt-in. When it is disabled, query/mutation-shape construction and artifact
encoding do not run. Ordinary statement observers do not enable this extra
metadata path. Capture performs no additional database I/O by default and
never runs `EXPLAIN`. It records only statements executed through go-tidb with
the derived context; direct `database/sql` and other ORM calls remain outside
its coverage.

Runtime artifacts never contain bind values. `IncludeStatementArguments` is a
statement-observer option and cannot be passed to `WithRuntimeCapture`.

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

For each fingerprint with at least one collection attempt, text output adds a
`server_ru_fingerprint` line and JSON output adds an entry to
`server_ru_by_fingerprint`. `count` is the number of all captured target
statements with that fingerprint, including unsampled statements. `samples`
counts usable values and `errors` counts collection or connection-release
errors; one statement can contribute to both. `total`, `mean`, `min`, and `max`
use only successful samples and are zero when no sample succeeded. Entries are
sorted by fingerprint. Analysis retains one constant-size accumulator per
distinct fingerprint and never retains the individual RU samples. These values
are descriptive statistics, not a regression threshold or a billing-RU value.

Create a reusable reference artifact from a completed capture:

```sh
tidbgo baseline runtime.jsonl > server-ru-baseline.json
```

The baseline is exactly one JSON object with format version `1` and a
fingerprint-sorted `server_ru_by_fingerprint` array. Each entry stores
`fingerprint`, `count`, `samples`, `total`, `mean`, `min`, and `max`. Collection
errors reject creation and are not stored as an always-zero field. The artifact
has no creation timestamp and therefore produces deterministic output for the
same analysis. The CLI streams the runtime input and retains no individual
samples. It writes only to standard output and performs no database access.

Baseline creation requires every measured fingerprint to have complete
coverage (`count == samples`), at least five successful samples, and no
collection or connection-release error. It also fails when the runtime
artifact is invalid. Creating the artifact itself does not compare a current
measurement.

Compare a current runtime capture with the saved reference:

```sh
tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json
```

The baseline option requires a file path so the runtime artifact can still use
standard input. Comparison is offline and deterministic. The baseline and
current capture must contain the same measured fingerprint set. Current
coverage must also be complete and error-free, with at least five successful
samples per fingerprint. Different statement counts are allowed when both
sides have complete coverage because the metric is the per-statement mean.
Fingerprints with no collection attempt are outside this set, so enable
`CollectServerRU` consistently across the complete measurement workload.

For each comparable fingerprint, the effective limit is:

```text
max(baseline mean * 1.30, baseline maximum)
```

The current mean is an `RU001` regression only when it is strictly greater
than that limit. Equality passes. The observed baseline maximum acts as an
empirical noise floor; there is no fixed absolute RU allowance that would hide
a material relative increase in a low-RU query. A new or missing fingerprint,
collection error, incomplete coverage, or fewer than five current samples
produces `RU002` instead of claiming a regression result.

Both diagnostics are non-suppressible errors and therefore produce check exit
status `1`. Text output includes one `server_ru_comparison` line per
fingerprint and a comparison summary. JSON output adds
`server_ru_comparison`, including the fixed policy, summary, sorted entries,
status, measurement coverage, compared means, observed baseline maximum, and
effective limit. Entry status is one of `pass`, `regression`,
`missing_baseline`, `missing_current`, `collection_error`,
`incomplete_coverage`, or `insufficient_samples`. This value is TiDB statement
ServerRU, not billing RU.

Add `--workload` with the same scenario name when saving and comparing a
uniform operation workload. Each scope then contributes its RU sum and DML
statement count as one sample. `RU003` reports per-operation cost regression;
`RU004` rejects unavailable comparisons. This requires no runtime API or
per-query registration and does not disable `RU001` or `RU002`. See
[operation-level baselines](workload-baselines.md) for the explicit input
contract, five-scope minimum, incomplete-capture limits, and JSON fields.

Analyze a completed artifact without a database connection:

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --json
tidbgo analyze runtime.jsonl --schema schema.sql
tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json
tidbgo analyze runtime.jsonl --suppress 'RUN002=intentional polling'
tidbgo analyze runtime.jsonl --suppress 'RUN004=single inserts are required for generated IDs'
tidbgo analyze runtime.jsonl --suppress 'RUN005=intentional per-row lease boundary'
```

The CLI streams statement records instead of retaining the complete artifact
in memory. Exact aggregate statistics still retain the distinct capture,
scope, fingerprint, and batch identities needed by the report. Memory therefore
grows with distinct identities rather than statement or RU sample count.

The analyzer applies `QRY002` through `QRY005` once per distinct captured
query diagnostic and reports repeated non-preload SELECT fingerprints within
one scope as possible N+1 queries. `--schema` parses an offline TiDB `CREATE
TABLE` snapshot and applies `QRY006` and `QRY007` to each captured query shape.
It never connects to a database. Statistics distinguish all captured
statements, statements carrying a query shape, and statements checked against
the snapshot. `Count`, `Exists`, raw SQL, collection preload statements, and
mutations do not claim a complete model-row query shape and therefore remain
outside these query-shape rules.

Repetition is evidence rather than proof; retries and intentionally repeated
lookups require application review. Failed SELECT attempts are included
because they still consume statements. Every suppression names an exact code
and records a non-empty reason. Preload batch splits are excluded from the
N+1 rule.

`RUN004` reports two or more typed single-row `Insert` or `Upsert` attempts
with the same capture, scope, SQL fingerprint, operation, and terminal. It is
a suppressible warning and does not fail `tidbgo analyze`. It needs neither a
query registry nor a schema snapshot. All `InsertMany` and `UpsertMany` calls
are excluded, including unsplit and one-row calls, as are automatic batch
splits, relation mutations, raw SQL, UPDATE, and DELETE.

`RUN005` reports two or more typed `Update` or `UpdateWhere` attempts with
the same capture, scope, SQL fingerprint, operation, and terminal. It is also
a suppressible warning that does not fail `analyze`, and needs no schema,
baseline, `--workload`, or query registration. Raw SQL, relation mutations,
batches, and soft-delete `Delete`/`DeleteWhere` are excluded even when their
SQL is an UPDATE. An explicit restore through `UpdateWhere` is included.

Both rules include the captured attempt count, reported error count, summed
target duration, and already-collected statement ServerRU with its sample and
collection-error counts. Missing RU is shown as `unavailable`, not zero;
partial totals cover only measured attempts. Known samples may also carry a
collection error, for example after a connection-release failure. Failed
attempts are included, so the count proves neither distinct input rows nor
committed changes. The group-local RU total excludes BEGIN/COMMIT and is not
billed RU or an estimate of bulk savings.

For `RUN004`, review generated-ID use, execution order, transaction boundaries, and
intentional retries before replacing the calls with `InsertMany` or
`UpsertMany`, then measure latency and RU. The diagnostic never batches writes,
changes transaction boundaries, or collects additional RU. RU collection
remains opt-in through `CollectServerRU` at capture time.

For `RUN005`, review whether assignments and predicates allow fewer
statements without changing row-specific values, lease conditions, atomic
increments, execution order, transaction boundaries, or intentional retries.
Bind values are absent, so a shared fingerprint does not prove equal
assignments, distinct targets, or combinable writes. `UpdateWhere` can already
affect multiple rows, and zero affected rows do not exclude an attempt.
The diagnostic does not identify the call site or prove a loop, higher RU, or
potential savings. Measure latency, RU, and results before making a change.

These advisory warnings and [operation-level baselines](workload-baselines.md)
serve different purposes: `RUN005` suggests review of repetitions even without
a baseline; `RU003` checks measured per-operation regression. Suppressing an
intentional repetition does not suppress a budget regression.

Bind values are never written to the runtime artifact. SQL templates can still
contain literals supplied through raw SQL, and database errors can contain
values. Treat the artifact as sensitive development data and choose its file
permissions, destination, and retention accordingly. The caller owns and
closes the writer. Encoding and writer failures never replace database results;
inspect `capture.Err()` when artifact completeness matters.

### Conditional write index checks

`tidbgo analyze runtime.jsonl --schema schema.sql` checks captured
`UpdateWhere` and `DeleteWhere` predicates without application-side query
registration or a database connection. It does not check primary-key `Update`
or `Delete`, relation mutations, Many calls, or raw SQL through this rule.

- `QRY008` is a suppressible warning when no supported predicate bound matches
  the leading column of a default-usable direct-column index
- `QRY009` is suppressible information when index coverage remains uncertain
- `QRY006` is a non-suppressible error for a missing schema table or referenced
  predicate column; assignment compatibility is outside this check

Supported bounds are `Equal`, non-empty `In`, `IsNull`, the four ordered
comparisons, and `Between`. One leading-column bound suffices; an index need
not include every filter column. AND can retain a usable bound alongside
residual predicates. OR is covered when every branch is bounded by the same
leading column, including branches made empty by an empty `In`. Empty-list
constants and their logical combinations can prove a no-row write; other
value-dependent contradictions are not inferred.

Without such a bound, OR/NOT, negative predicates, LIKE predicates, and
expression, prefix-length, specialized, partial, or invisible indexes remain
uncertain. In particular, this check does not model IndexMerge or inspect
bind values to classify LIKE prefixes. Implicit soft-delete `IS NULL` is part
of the predicate; `UpdateWhere.WithDeleted` removes it.

`mutation_shape_statements` counts captured shapes.
`schema_checked_statements` counts snapshot-check attempts for both SELECT
and conditional writes. `mutation_index_checked_statements` counts resolved
write checks, including warnings and proven no-row predicates;
`mutation_index_uncertain_statements` counts unresolved write checks. Missing
schema inputs count in neither resolved nor uncertain write checks and produce
`QRY006`. Diagnostics are deduplicated by statement fingerprint while coverage
counts every captured attempt, including failed statements. Without `--schema`,
shapes are counted but index checks are not attempted.

A matching prefix is only a structural candidate, not proof of index use,
selectivity, narrow locks, or low RU. TiDB uses statistics and cost estimates
to choose access paths. Review the actual plan and index write cost before
adding an index. See TiDB's [index selection](https://docs.pingcap.com/tidb/stable/choose-index/)
and [indexing guidance](https://docs.pingcap.com/developer/dev-guide-index-best-practice/).
Use plain SQL `EXPLAIN` for initial inspection; **`EXPLAIN ANALYZE` executes
the write and can change data**, as documented in the
[TiDB statement reference](https://docs.pingcap.com/tidb/stable/sql-statement-explain-analyze/).
Offline analysis never executes either command or changes SQL or transaction
boundaries. `tidbgo lint` does not currently apply these conditional-write rules.

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

Each row also has compiler-derived `PhysicalTable`, `Model`, and
`RelationPath` fields. These are not additional TiDB columns. go-tidb reads the
exact `table:` token from `AccessObject` and resolves it against metadata for
the compiled root SELECT, inline preload joins, relation predicates,
many-to-many junctions, and relation-first TopN associations. Generated aliases
identify occurrences in one query and do not come from model tags. Relation
occurrences use distinct aliases, so a target and its root-relative relation
path remain identifiable when multiple relations appear at the same nesting
depth.

`PhysicalTable` and `Model` identify the root or related model. `RelationPath`
is empty for the root. A junction table has a physical table and relation path
but no Go model. A derived table has no physical-table mapping. If TiDB returns
an unknown token or only a physical name shared by multiple relation paths,
go-tidb preserves `AccessObject` and leaves the ambiguous derived fields empty
instead of parsing the SQL or guessing.

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

The diagnostic evidence includes operator identifiers, access objects,
resolved physical tables, models and relation paths when available, row counts,
and recognized disk values. It does not copy full `OperatorInfo`, which can
contain predicate ranges derived from bind values. Treat access-object, model,
relation, and schema identifiers as development metadata. The returned
diagnostics are application-owned values that tests can inspect directly.

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
or `WithRuntimeCapture`. It does not sample `EXPLAIN`, transaction
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
