# Struct-first starter app example

This example defines `User`, `Order`, `Role`, `UserRole`, `Clip`, `ClipGenre`,
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
- An application-selected decimal type using `sql.Scanner` and `driver.Valuer`
- Offline scalar SQL construction with predicates and keyset pagination
- Executed query-shape and query-to-index diagnostics through RuntimeCapture
  and `tidbgo analyze`, without bind values
- Offline source query-pattern, projection, and optional schema-aware root
  index analysis through `tidbgo lint`
- Explicit scalar execution through caller-owned database/sql executors
- Nested relation preloading through deterministic inline `LEFT JOIN`s for
  to-one relations and secondary queries for collections, including target
  projection, collection ordering, and relation-scoped deleted-row inclusion
- Logical direct and pure many-to-many relation predicates, including TiDB
  semi-join hints and relation-first TopN for eligible direct and pure
  many-to-many collections, plus relation-only Count for eligible unpaginated
  collection filters, without hydrating relations
- Single insert, automatically batched bulk insert and upsert from model
  pointer slices, full and partial update, physical delete, soft delete, and
  explicit restore operations
- Pure many-to-many add, duplicate-ignore add, remove, and clear operations
  through one junction statement
- Typed raw aggregate scanning into a computed field
- Context-scoped statement logging with automatic terminal colors and no bind
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
go run ./cmd/tidbgo lint ./examples/starter-app
go run ./cmd/tidbgo lint ./examples/starter-app --schema ./examples/starter-app/schema.sql
```

The second command also compares statically resolved ordered positive-LIMIT
root and relation-first association accesses with the example's TiDB schema
snapshot. Both reports include recognized query, relation compiler, index,
and uncertainty counts even when no diagnostic is emitted

`BuildRecentOrdersQuery` compiles SQL and bind arguments without opening a
connection. `BuildRecentClipsInGenreQuery` demonstrates natural
`Clip`-rooted `Has("ClipGenres", Equal("GenreID", ...))` syntax while the
compiler uses the `ClipGenre` candidate key to prove one matching edge per
clip, then filters and limits `clip_genres` before loading root rows. Its outer
`LEADING(tidbgo_k0, tidbgo_t0)` hint keeps that limited key set as the root
lookup's driving input. The edge keeps its surrogate primary key and required
`Priority` payload. `CountClipsInGenre` starts from the same natural
`Clip`-rooted relation predicate while the Count compiler reads only
`clip_genres` when the candidate key proves one edge per Clip.
`BuildRecentUsersWithRoleQuery` demonstrates the corresponding pure
many-to-many shape: fixing the complete Role primary key lets the compiler
filter the junction directly and limit `(role_id, user_id)` access before
loading User rows. Both
`tidbgo lint --schema` and captured `tidbgo analyze --schema` can report an
`EXISTS` fallback or a missing association index prefix without another
application wrapper.
`FirstRecentOrder`, `FindUserByEmail`,
`HasUserWithEmail`, `CountOrdersForUser`, and `CountClipsInGenre` demonstrate
connected `First`, `Only`, `Exists`, and scalar or relation-only `Count`
terminals. `ListUsersWithOrders` demonstrates
projected and ordered `Preload("Orders.User")`, loading Orders in one secondary
SELECT and joining each User into that statement.
`ListUsersWithRoles` demonstrates a pure
many-to-many `Preload("Roles")`, both without generated relation code.
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
`WithQueryLog` enables the built-in statement logger for selected operations
without replacing the application-owned executor.
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

The existing functions in this example continue to receive the derived
context unchanged. Analyze the resulting JSON Lines file with
`tidbgo analyze`. Captured typed query shapes are checked automatically; pass
`--schema schema.sql` to add offline physical index-prefix checks without a
database connection or an application-side query registry.

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
`*sql.DB`, `*sql.Conn`, or `*sql.Tx`. Connection creation, live schema
introspection, and migration application are not implemented.

The [struct model guide](../../docs/models.md) documents the complete current
mapping boundary. The [scalar query guide](../../docs/queries.md) documents the
public query API, and the [mutation guide](../../docs/mutations.md) documents
writes and raw SQL. The [statement observation guide](../../docs/observability.md)
documents query logging and custom observers.
The [schema compatibility guide](../../docs/schema-checks.md) documents the
offline physical-schema boundary.
The [analysis guide](../../docs/checks.md) documents each evidence boundary,
CLI exit statuses, and reason-carrying suppression for `analyze` and `lint`.
