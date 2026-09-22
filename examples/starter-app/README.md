# Struct-first starter app example

This example defines `User`, `Order`, `Role`, `UserRole`, `Clip`, `ClipGenre`, `Genre`,
`Video`, and `WatchLater` as ordinary, application-owned Go structs.

It demonstrates the current struct-first foundation:

- No schema DSL
- No generated model files
- No project configuration file
- No database connection for model inspection
- Offline model-intent diagnostics through explicit model registration
- Offline TiDB CREATE TABLE parsing and directional model compatibility checks
- Inferred columns and an explicit default-equal column name in the first
  `tidbgo` tag position
- Explicit physical table names through the zero-size `model.Meta` marker
- Ordered single-column and composite primary keys through `tidbgo:",pk"`
- Candidate unique keys independent from the primary key by repeating
  `tidbgo:",unique=<group>"` on their fields
- TiDB `AUTO_RANDOM` primary keys through `tidbgo:",pk,auto_random"`
- Aggregate result fields through `tidbgo:"column,computed"`
- Value-form soft deletion through `tidbgo:",soft_delete"` without a separate
  null-zero option
- Ordinary pointers and slices for direct and many-to-many relations
- Read-only `via=ClipGenres.Genre` target preloads ordered by edge `Priority`,
  without removing payload or changing the edge's primary key
- An application-selected decimal type using `sql.Scanner` and `driver.Valuer`
- Offline scalar SQL construction with predicates and keyset pagination
- Executed query-shape and query-to-index diagnostics through RuntimeCapture
  and `tidbgo analyze`, without bind values
- Offline source query-pattern, projection, and optional schema-aware root
  index analysis through `tidbgo lint`
- Explicit scalar execution through caller-owned database/sql executors
- Root `ForceIndex` selection for offset pages, with a separate unhinted total
  count; index choice requires plan and RU measurements for the caller's data
- Nested relation preloading through deterministic inline `LEFT JOIN`s for
  to-one relations and secondary queries for collections, including target
  projection, collection ordering, and relation-scoped deleted-row inclusion
- Logical direct and many-to-many relation predicates, including TiDB
  semi-join hints and relation-first TopN for eligible direct, pure
  many-to-many, and payload-bearing `via` collections, plus relation-only Count
  for eligible unpaginated collection filters, without hydrating relations
- Single insert, automatically batched bulk insert and upsert from model
  pointer slices, full and partial update, physical delete, soft delete, and
  explicit restore operations
- Row-specific `UpdateMany` for edge priorities, without replacing edge rows
  or assigning their primary and relation keys
- Pure many-to-many add, duplicate-ignore add, remove, and clear operations
  through one junction statement
- Typed raw aggregate scanning into a computed field
- Single-table aggregate SQL in `Example_aggregate`, with output aliases,
  HAVING, ordering, and paging through public APIs
- Daily/monthly registration counts in `Example_calendarAggregation`, using
  a reporting source for the existing database-managed timestamp and grouping
  by selected output names
- Per-user order counts and conditional counts/sums for large orders in
  `Example_conditionalAggregation`, retaining groups with no order reaching
  the value threshold
- Order totals restricted by a user's role in `Example_relationAggregation`,
  using nested `Has` without multiplying orders by matching related rows
- Explicit `CompareOrderTotals` for auto/TiKV/TiFlash MPP result checks,
  latency/ServerRU measurements, and separate plan/warning inspection over a
  caller-provided fixed fixture with TiFlash replicas
- Shared-executor statement logging with automatic terminal colors and no bind
  argument values
- Structured runtime capture of actual root, relation, and split-bulk
  statements without per-query wrappers
- SELECT-only TiDB execution-plan inspection through the typed query builder
- Explicit SELECT execution with TiDB runtime-plan inspection and diagnostics
  over the returned rows without another database call
- Explicit same-session ServerRU reading for one completed DML statement
- Optional observer-scoped ServerRU collection without query-specific wrappers

`User` intentionally omits a database-managed `created_at` column because an
application model does not need to mirror every physical column.

See [app.go](app.go) for the models and queries and [app_test.go](app_test.go)
for offline inspection through `model.Describe`, model-intent diagnostics
through `check.Model`, physical compatibility through `schema.Parse` and
`check.Schema`, and SQL construction through `orm.Query`.

Run the example test from the repository root:

```sh
go test ./examples/starter-app
```

Scan the example's production Go source without a database connection:

```sh
go -C cmd/tidbgo run . lint ../../examples/starter-app
go -C cmd/tidbgo run . lint ../../examples/starter-app --schema ../../examples/starter-app/schema.sql
```

The second command also compares statically resolved ordered positive-LIMIT
root and relation-first association accesses with the example's TiDB schema
snapshot. Both reports include recognized query, relation compiler, index,
and uncertainty counts even when no diagnostic is emitted

Preview logical reference checks without connecting to a database:

```sh
go -C cmd/tidbgo run . audit ../../examples/starter-app --schema ../../examples/starter-app/schema.sql --dry-run
```

For an explicitly connected orphan audit, supply `TIDBGO_DSN` for the database
matching the snapshot and omit `--dry-run`. Use `--relation Order.User` to
select one reference and `--timeout 10s` to set the total deadline. An audit
checks physical references, including soft-deleted rows, and never repairs data.
See [reference audits](../../docs/reference-audits.md) for coverage and limits.

`BuildRecentOrdersQuery` compiles SQL and bind arguments without opening a
connection. `BuildRecentClipsInGenreQuery` demonstrates natural
`Clip`-rooted `Has("Genres", Equal("ID", ...))` syntax through its `via` relation
while the compiler uses the `ClipGenre` candidate key to prove one matching edge per
clip, then filters and limits `clip_genres` before loading root rows. Its outer
binary `STRAIGHT_JOIN` keeps that limited key set as the root
lookup's driving input. The edge keeps its surrogate primary key and required
`Priority` payload. `CountClipsInGenre` starts from the same natural
`Clip`-rooted relation predicate while the Count compiler reads only
`clip_genres` when the candidate key proves one edge per Clip.
The compiler excludes NULL edge keys before Limit or Count and preserves
edge soft-delete scopes. Without a declared key proving pair uniqueness,
the via relation remains valid but retains the EXISTS query.
`BuildRecentUsersWithRoleQuery` demonstrates the corresponding pure
many-to-many shape: fixing the complete Role primary key lets the compiler
filter the junction directly and limit `(role_id, user_id)` access before
loading User rows. Both
`tidbgo lint --schema` and captured `tidbgo analyze --schema` can report an
`EXISTS` fallback or a missing association index prefix without another
application wrapper.
`ListVideoIDs` and `ListVideoSummaries` use `ScanAll` to read a scalar slice
or a smaller struct directly. `VideoSummary` matches the source Go field names
without duplicating tags; Video's soft-delete scope still controls SQL.
`FirstRecentOrder`, `FindUserByEmail`,
`HasUserWithEmail`, `CountOrdersForUser`, and `CountClipsInGenre` demonstrate
connected `First`, `Only`, `Exists`, and scalar or relation-only `Count`
terminals. `ListUsersWithOrders` demonstrates
projected and ordered `Preload("Orders.User")`, loading Orders in one secondary
SELECT and joining each User into that statement.
`ListUsersWithRoles` demonstrates a pure
many-to-many `Preload("Roles")`, both without generated relation code.
`ListClipsWithGenres` loads `Clip.Genres` directly through `ClipGenres.Genre`,
ordered by `ClipGenres.Priority` and target `ID` in one secondary SELECT.
`ClipGenres` stays unloaded unless requested separately. Read or write the edge
model directly when the application needs its payload or identity.
`UpdateClipGenrePriorities` writes each edge's own `Priority` through its
existing primary key using `UpdateMany`, accepting pointer slices and inheriting
the supplied executor's transaction and observer settings. The input must
identify distinct database rows; missing edges are not inserted.
`ListUsersInRole` filters through `Has("Roles", ...)` without preloading
the matching roles. `ListVideos` uses the default active-row scope,
`ListVideosWithDeleted` includes deleted root rows, and
`ListWatchLaterVideos` uses `PreloadWithDeleted` for one relation path.
`InsertUser`, `InsertOrders`, `UpsertUser`, `UpsertUsers`,
`SaveUser`, `UpdateUserEmail`, `DeleteUser`, and `DeleteOrdersForUser` show the
ordinary mutation surface without parallel diagnostic wrappers.
`DeleteVideo` and `RestoreVideo` demonstrate a
server-timestamped soft delete and explicit restore. `ClaimJobLease` and
`FailJobLease` demonstrate a
predicate-bounded update, NULL assignments, and atomic increment without raw
SQL. `SaveUserAndInsertOrders` uses `orm.Transaction` to make an update and
every automatically split insert batch atomic. `AddUserRoles`,
`AddUserRolesIfMissing`, `RemoveUserRoles`, and `ClearUserRoles` show
pure-junction relation mutations.
`LoadUserWithOrderCount` scans an aliased aggregate through
`orm.Raw[User]`.
`WithQueryLog` uses `orm.Observe` to configure the shared executor once.
Pass the returned executor to the example functions; preloads and
`SaveUserAndInsertOrders` inherit its logger without per-call context setup.
The application retains ownership of the underlying pool. Use
`orm.WithStatementObserver` only for temporary context overrides.
Structured runtime capture is configured directly at a request or job boundary
instead of adding a companion function for every repository operation:

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture)
```

When the extra same-session round trip per recognized DML statement is
intentional, enable ServerRU collection at the same boundary:

```go
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
```

`*sql.DB` statements are temporarily pinned with their diagnostic query. The
artifact keeps target and diagnostic cost separate, and a collection failure
does not replace the application result. Offline analysis groups attempted
ServerRU collection by bind-free fingerprint and reports count, samples,
errors, total, mean, minimum, and maximum without retaining every sample.
After a clean measurement run, write the deterministic versioned reference
with `tidbgo baseline runtime.jsonl > server-ru-baseline.json`. Baseline
creation is offline and requires complete error-free coverage with at least
five samples per measured fingerprint. Compare another capture with
`tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json`.
The fixed policy reports `RU001` only when the current mean exceeds both 130%
of the baseline mean and the observed baseline maximum; missing or unusable
measurements report `RU002`.

To compare the cost of a whole operation, capture one invocation per scope
and repeat the same scenario at least five times with equivalent fixtures.
For example, `UpdateUserEmail` calls belonging to one job share the job's
capture context; they do not each create a scope. No changes to the repository
functions are needed. Declare the same workload name on both CLI invocations:

```sh
tidbgo baseline reference.jsonl --workload update-users-10 > baseline.json
tidbgo analyze current.jsonl --workload update-users-10 --baseline baseline.json
```

This compares per-scope RU sums and DML statement counts in addition to
per-fingerprint means. More calls within one scope can produce `RU003` even
with unchanged per-statement RU. Incomplete or mismatched workload inputs
produce `RU004`. The flag declares a uniform input, not a filter; keep setup,
plan probes, and different scenarios outside the captured operation.
BEGIN/COMMIT RU is not included. See [operation-level baselines](../../docs/workload-baselines.md)
for coverage and incomplete-capture limits.

The existing functions in this example continue to receive the derived
context unchanged. Analyze the resulting JSON Lines file with
`tidbgo analyze`. Captured typed query shapes are checked automatically; pass
`--schema schema.sql` to add offline physical index-prefix checks without a
database connection or an application-side query registry.

The same capture includes the predicates of `ClaimJobLease`, `FailJobLease`,
`RestoreVideo`, and `DeleteOrdersForUser`. `analyze --schema` checks their
index-prefix candidates without changing these functions. The job primary
key bounds `ClaimJobLease` even though it also has an OR lease condition;
no index containing every WHERE column is required. `DeleteOrdersForUser`
can produce `QRY008` when the snapshot lacks an index starting with `user_id`.
Unsupported index or predicate shapes produce `QRY009` and count as uncertain,
not successfully checked. Matching a prefix does not guarantee the chosen
plan or RU. Use SQL `EXPLAIN` for initial inspection: `EXPLAIN ANALYZE` on a
write executes it and can change data.

Repeated `InsertUser` or `UpsertUser` calls with the same SQL fingerprint in
one captured scope produce the advisory `RUN004` warning. No changes to these
repository functions are needed. The report includes attempt counts, target
duration, and any collected ServerRU with sample coverage; uncollected RU is
`unavailable`, not zero. `InsertOrders` and `UpsertUsers` remain excluded even
when they split into several statements or receive only one row.
Generated-ID dependencies, execution order, transaction boundaries, and
intentional retries must be reviewed before switching to bulk calls; the
analyzer does not combine writes automatically. For example, `InsertUser`
writes back a generated ID while bulk insert does not. Intentional single-row
writes can be acknowledged with a reason:

```sh
tidbgo analyze runtime.jsonl --suppress 'RUN004=single inserts are required for generated IDs'
```

Repeated `UpdateUserEmail`, `ClaimJobLease`, `FailJobLease`, or `RestoreVideo`
calls with the same fingerprint and terminal in one scope produce the advisory
`RUN005` warning with the same attempt, duration, and RU coverage evidence.
No changes to these functions, schema snapshot, or `--workload` flag are needed.
This is a review candidate, not a recommendation to combine leases or atomic
increments: preserve per-row values, concurrency conditions, execution order,
and transaction boundaries, and check retries before measuring a rewrite.
`DeleteVideo` is excluded even though its soft delete executes an UPDATE.
Intentional repetitions can be acknowledged without disabling operation-level
budget regression checks:

```sh
tidbgo analyze runtime.jsonl --suppress 'RUN005=intentional per-row lease boundary'
```

`ExplainUserByEmail` asks TiDB for the default row-format plan of a typed
SELECT without executing that root SELECT.
`ExplainAnalyzeUserByEmail` explicitly executes the same typed SELECT and
returns actual rows, execution information, memory, and disk usage for each
operator. Each row also resolves an unambiguous compiler-owned access alias to
its physical table, Go model, and root-relative relation path. Its returned
`orm.ExplainAnalyzePlan` can be inspected directly:

```go
runtimePlan, err := ExplainAnalyzeUserByEmail(ctx, db, email)
if err != nil {
    return err
}
diagnostics := runtimePlan.Diagnostics()
```

`Diagnostics` does not execute another database statement.
`FindUserByEmailWithServerRU` uses a pinned `*sql.Conn` for one query and reads
its TiDB-reported ServerRU immediately afterward when a single manual sample is
more appropriate.
The example tests call `check.Model` for each application-owned model and call
`check.Schema` with a self-contained TiDB `CREATE TABLE` snapshot. These checks
cover mapped tables, columns, primary keys, `AUTO_RANDOM`, nullability, required
database-only columns, relation targets, the pure junction, and collection
lookup index prefixes entirely offline. The omitted database-managed
`created_at` column is accepted because it has a default.

Execution is available only when the caller explicitly passes an existing
`*sql.DB`, `*sql.Conn`, or `*sql.Tx`. The ORM does not create connections or run
migrations. Use the [standalone migration tooling](../../docs/migrations.md)
for deployment and current-database SQL snapshots.

`RankedOrderTotals` demonstrates a to-one related output and ranking over groups.
`SearchDocuments` demonstrates exact vector search within a tenant using a narrow
projection. Its `search_documents` schema uses three-dimensional non-NULL vectors.
The offline preparation example generates replica and vector-index DDL without
executing it. See [windows](../../docs/windows.md),
[vector search](../../docs/vector-search.md), and [TiFlash preparation](../../docs/tiflash.md).

The [struct model guide](../../docs/models.md) documents the complete current
mapping boundary. The [scalar query guide](../../docs/queries.md) documents the
public query API, and the [mutation guide](../../docs/mutations.md) documents
writes and raw SQL. The [statement observation guide](../../docs/observability.md)
documents query logging and custom observers.
The [aggregate guide](../../docs/aggregates.md) covers typed grouping, result
structs, optional TiFlash/MPP policy, and explicit plan/warning inspection.
The [comparison guide](../../docs/aggregate-comparison.md) explains the
`CompareOrderTotals` report and `WriteCapture` baseline export. This diagnostic
is an explicit call and is not part of application startup.
The [schema compatibility guide](../../docs/schema-checks.md) documents the
offline physical-schema boundary.
The [analysis guide](../../docs/checks.md) documents each evidence boundary,
CLI exit statuses, and reason-carrying suppression for `analyze` and `lint`.

## Server warnings

Configure `orm.Observe(db, orm.NewStatementLogger(os.Stderr), orm.CollectWarnings())`
to collect ordinary DML warnings. This adds one round trip per statement.
Explicit aggregate/vector plans also publish their existing warnings.
`Example_warningDiagnostics` shows value-free MPP warning classification.
Some MPP warnings are only exposed by EXPLAIN; collect ServerRU separately.
See [server warning diagnostics](../../docs/warnings.md).
