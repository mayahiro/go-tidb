# Struct-first starter app example

This example defines `User`, `Order`, `Role`, `UserRole`, `Video`, and
`WatchLater` as ordinary, application-owned Go structs.

It demonstrates the current struct-first foundation:

- No schema DSL
- No generated model files
- No project configuration file
- No database connection for model inspection
- Offline model-intent diagnostics through explicit model registration
- Inferred columns and an explicit default-equal column name in the first
  `tidbgo` tag position
- Explicit physical table names through the zero-size `model.Meta` marker
- Ordered single-column and composite primary keys through `tidbgo:",pk"`
- TiDB `AUTO_RANDOM` primary keys through `tidbgo:",pk,auto_random"`
- Aggregate result fields through `tidbgo:"column,computed"`
- Value-form soft deletion through `tidbgo:",soft_delete"` without a separate
  null-zero option
- Ordinary pointers and slices for direct and many-to-many relations
- An application-selected decimal type using `sql.Scanner` and `driver.Valuer`
- Offline scalar SQL construction with predicates and keyset pagination
- Offline query-shape diagnostics without exposing bind values
- Explicit scalar execution through caller-owned database/sql executors
- Nested relation preloading through deterministic inline `LEFT JOIN`s for
  to-one relations and secondary queries for collections, including target
  projection, collection ordering, and relation-scoped deleted-row inclusion
- Direct and pure many-to-many relation predicates compiled to correlated
  `EXISTS` subqueries without hydrating relations
- Single insert, automatically batched bulk insert and upsert from model
  pointer slices, full and partial update, physical delete, soft delete, and
  explicit restore operations
- Pure many-to-many add, duplicate-ignore add, remove, and clear operations
  through one junction statement
- Typed raw aggregate scanning into a computed field
- Context-scoped statement logging with automatic terminal colors and no bind
  argument values
- Operation-scoped debug reports that aggregate root and relation statements
  without additional database calls
- SELECT-only TiDB execution-plan inspection through the typed query builder
- Explicit SELECT execution with TiDB runtime-plan inspection
- Explicit same-session ServerRU reading for one completed DML statement

`User` intentionally omits a database-managed `created_at` column because an
application model does not need to mirror every physical column.

See [app.go](app.go) for the models and queries and [app_test.go](app_test.go)
for offline inspection through `model.Describe`, model-intent diagnostics
through `check.Model`, and SQL construction through `orm.Query`.

Run the example test from the repository root:

```sh
go test ./examples/starter-app
```

`BuildRecentOrdersQuery` compiles SQL and bind arguments without opening a
connection, and `CheckRecentOrdersQuery` applies static query-shape diagnostics
to the same builder. `FirstRecentOrder`, `FindUserByEmail`,
`HasUserWithEmail`, and `CountOrdersForUser` demonstrate connected `First`,
`Only`, `Exists`, and `Count` terminals. `ListUsersWithOrders` demonstrates
projected and ordered `Preload("Orders.User")`, loading Orders in one secondary
SELECT and joining each User into that statement. `ListUsersWithRoles`
demonstrates a pure
many-to-many `Preload("Roles")`, both without generated relation code.
`ListUsersInRole` filters through `Has("Roles", ...)` without preloading
the matching roles. `ListVideos` uses the default active-row scope,
`ListVideosWithDeleted` includes deleted root rows, and
`ListWatchLaterVideos` uses `PreloadWithDeleted` for one relation path.
`InsertUser`, `InsertOrders`, `UpsertUser`, `UpsertUsers`,
`SaveUser`, `UpdateUserEmail`, `DeleteUser`, and `DeleteOrdersForUser` show the
ordinary mutation surface. `DeleteVideo` and `RestoreVideo` demonstrate a
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
`DebugUsersWithOrders` returns the users together with one `orm.DebugReport`
containing the root SELECT and collection preload SELECT. The report omits bind
values and performs no additional database calls.
`ExplainUserByEmail` asks TiDB for the default row-format plan of a typed
SELECT without executing that root SELECT.
`ExplainAnalyzeUserByEmail` explicitly executes the same typed SELECT and
returns actual rows, execution information, memory, and disk usage for each
operator.
`FindUserByEmailWithServerRU` uses a pinned `*sql.Conn` for one query and reads
its TiDB-reported ServerRU immediately afterward.
`CheckModels` explicitly lists the application-owned model types and returns
their diagnostics without source generation, configuration, or a database
connection.

Execution is available only when the caller explicitly passes an existing
`*sql.DB`, `*sql.Conn`, or `*sql.Tx`. Connection creation and SQL schema
comparison are not implemented.

The [struct model guide](../../docs/models.md) documents the complete current
mapping boundary. The [scalar query guide](../../docs/queries.md) documents the
public query API, and the [mutation guide](../../docs/mutations.md) documents
writes and raw SQL. The [statement observation guide](../../docs/observability.md)
documents query logging and custom observers.
