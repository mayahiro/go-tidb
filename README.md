# go-tidb

`go-tidb` is an offline-first, struct-first Go ORM for TiDB Cloud Starter

The Go module path is `github.com/mayahiro/go-tidb` and the command name is
`tidbgo`

[日本語](README_ja.md) | [Struct models](docs/models.md) |
[Queries](docs/queries.md) | [Mutations and raw SQL](docs/mutations.md) |
[Statement observation](docs/observability.md) | [Development](docs/development.md)

## Available features

- Application-owned Go structs without generated models
- Offline model validation and SQL construction
- Explicit execution through caller-owned `*sql.DB`, `*sql.Conn`, or `*sql.Tx`
- Scalar predicates, ordering, offset pagination, and keyset pagination
- Deterministic direct and many-to-many relation predicates and preloads
- Single and automatically split bulk insert and upsert
- Primary-key and predicate-bounded update and delete
- Soft deletion, restore, pure-junction mutations, and transaction helpers
- Typed scanning for raw joins, CTEs, aggregates, and partial results
- Context-scoped statement observation with automatic terminal colors
- SELECT-only TiDB execution-plan inspection through the typed query builder
- Explicit SELECT execution with actual TiDB runtime-plan inspection
- Explicit same-session ServerRU reading for one completed DML statement

## Supported scope

- TiDB Cloud Starter is the only supported database profile
- MySQL, MariaDB, other TiDB Cloud plans, and TiDB Self-Managed are not covered
- Application models are ordinary user-owned Go structs
- Model inspection and SQL construction require neither code generation nor a
  database connection
- Relation loading is explicit and deterministic, with no lazy loading
- Before v1, public APIs and formats may change without backward compatibility

## Requirements

- Go 1.26 or newer
- TiDB Cloud Starter for connected execution

Current model inspection and query building do not require a database
connection.

## Installation

```sh
go get github.com/mayahiro/go-tidb
```

`go-tidb` does not include or select a database driver. Register the driver
used by your application and pass an existing `database/sql` executor to the
ORM.

For example, an application using `go-sql-driver/mysql` can open its own
connection:

```go
import (
	"database/sql"

	_ "github.com/go-sql-driver/mysql"
)

db, err := sql.Open("mysql", dsn)
```

Follow the official [TiDB Cloud Starter connection
requirements](https://docs.pingcap.com/tidbcloud/connect-to-tidb-cluster-serverless/?plan=starter),
including TLS. With `go-sql-driver/mysql`, use `parseTime=true` when `DATE` or
`DATETIME` values must scan into `time.Time`.
`interpolateParams=true` can reduce round trips for short-lived parameterized
queries, but it must not be combined with BIG5, CP932, GB2312, GBK, or SJIS.
See the driver's [`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams).

## Struct-first model metadata

Define the fields used by the application directly:

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

Inspect and validate the mapping offline when tooling or tests need metadata:

```go
metadata, err := model.Describe[User]()
```

Unexported fields and fields tagged with `tidbgo:"-"` are ignored. Scalar tag
values put an optional column name first and options after it. Without an
explicit name, fields use deterministic snake_case columns. Primary keys use
`tidbgo:",pk"` with an inferred column or `tidbgo:"column_name,pk"` with an
explicit column. Multiple marked fields form an ordered composite key. The
default table name is the snake_case Go type name. Embed the zero-size
`model.Meta` marker only when a physical table name override is needed. Custom
scalar types can implement `sql.Scanner` and `driver.Valuer`; `go-tidb` does not
select a decimal library for the application. Struct-tag namespaces other than
`tidbgo`, including `db`, are ignored and cannot rename or exclude fields.
Use `auto_random` on one integer primary key to omit it from insert statements.
Single-row `Insert` assigns the generated ID; upserts and bulk operations do
not. Use `computed` for aliased raw-query results that must not participate in
base-table reads or writes.

Use `soft_delete` on one `time.Time` or `*time.Time` deletion field. A value
field maps zero time to SQL `NULL`; a pointer field uses nil as `NULL`.
Ordinary nullable columns continue to use pointers or `sql.Scanner` types.

To-one relations use pointers, and to-many relations use slices of values or
pointers. Direct relations infer the common single-primary-key mapping when it
resolves unambiguously and accept explicit ordered `join=Source:Target`
options otherwise. Many-to-many mappings explicitly name the junction table
and both junction key mappings. Relation fields do not perform lazy loading or
track separate loaded-state metadata.

See the [struct model guide](docs/models.md) and the runnable
[starter app example](examples/starter-app/README.md).

## Struct-first scalar queries

Build validated SQL and bind arguments offline using exported Go field names:

```go
query := orm.Query[Order]().
    Select("ID", "UserID", "Total").
    Where(orm.Equal("UserID", userID)).
    OrderBy(orm.Desc("ID")).
    SeekAfter(lastID).
    Limit(100)

sqlText, arguments, err := query.Build()
```

`Build` does not access a database or execute custom `driver.Valuer`
implementations. Values remain separate bind arguments, and physical
identifiers come only from validated model metadata.

Execute the same query only when an existing executor is passed explicitly:

```go
orders, err := query.All(ctx, db)
order, err := query.First(ctx, db)
exists, err := query.Exists(ctx, db)
count, err := query.Count(ctx, db)
```

`*sql.DB`, `*sql.Conn`, and `*sql.Tx` implement `orm.QueryExecutor`. `go-tidb`
does not currently open or configure connections or include a MySQL protocol
driver. `Only` distinguishes zero, one, and multiple rows. `Exists` uses
`SELECT 1 ... LIMIT 1` without scanning a model. `Count` uses count-specific
SQL and includes the builder's predicates and pagination. See the [scalar
query guide](docs/queries.md) for terminal errors, predicates, pagination,
NULL ordering, and the current execution boundary.

Filter by relation existence without loading the relation:

```go
admins, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Has("Roles", orm.Equal("Name", "admin"))).
    All(ctx, db)
```

`Has` compiles direct and pure many-to-many relation conditions to correlated
`EXISTS` subqueries. Pass target predicates to require a matching related row,
or omit them for existence only. Relation and target field names are exported
Go field names. `Build` validates and compiles them entirely offline.

Preload a direct or pure many-to-many relation by its exported Go field name:

```go
users, err := orm.Query[User]().
    Select("Email").
    Preload("Orders").
    Preload("Roles").
    All(ctx, db)
```

`Preload` validates metadata offline and hydrates ordinary pointer or slice
fields without lazy loading. `belongs_to` and `has_one` relations use
deterministic inline `LEFT JOIN`s. `has_many` and pure `many_to_many` relations
use deterministic secondary SELECTs after the preceding rows close. An
unrestricted `All` without an active root soft-delete scope loads each root
collection source once without an `IN` list. A default-scoped soft-delete
root, any other constrained root query, and every nested collection use keyed
batches with a 5,000-bind-parameter budget, reduced by composite-key width and
bounded by TiDB's
[65,535-placeholder limit](https://docs.pingcap.com/tidb/stable/sql-statement-prepare/).
The parent query shape determines this choice; runtime statistics and result
cardinality do not. A many-to-many secondary SELECT contains one fixed
junction-to-target JOIN. A to-one relation
nested below a collection is joined into that collection statement, so
`Preload("Orders.User")` normally executes two statements: the parent SELECT
and the Orders SELECT with User joined inline. `PreloadFields` limits any
relation projection, and `PreloadOrderBy` defines collection order; required
keys are added automatically. Use a caller-owned repeatable-read `*sql.Tx`
when multiple statements must share one transaction snapshot, or use the
`*sql.Tx` supplied to a `Transaction` callback.
`PreloadWithDeleted` includes logically deleted targets for only the requested
relation path. Arbitrary relation-specific predicates remain unavailable.

## Mutations and raw SQL

The common write path uses model values and primary-key metadata directly:

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

Group application-defined operations with the explicit transaction helper:

```go
err = orm.Transaction(ctx, db, func(tx *sql.Tx) error {
    if _, err := orm.Update(&user).Exec(ctx, tx); err != nil {
        return err
    }
    _, err := orm.InsertMany(orders).Exec(ctx, tx)
    return err
})
```

`InsertMany(values)` and `UpsertMany(values)` accept either `[]Model` or
`[]*Model`. `Exec` automatically splits them at TiDB's 65,535-placeholder
limit, while `Build` continues to represent one executable statement. Pass a
`*sql.Tx`, created directly or supplied to a `Transaction` callback, when every
batch must be atomic. `Transaction` uses default `database/sql` options and does
not retry its callback. Every typed mutation supports offline `Build`. An empty
predicate list cannot produce a typed DELETE. `*sql.DB`, `*sql.Conn`, and
`*sql.Tx` implement the mutation executor boundary.

Pure many-to-many relation mutations use the exported relation field name and
key values without generated code. `AddRelation` emits one multi-row junction
INSERT and reports duplicates by default. Call `IgnoreExisting` explicitly to
keep existing junction rows. `RemoveRelation` and `ClearRelation` each emit one
bounded DELETE. Composite mappings use `CompositeKey` in declared key order.

`UpdateWhere` requires explicit assignments and predicates, supports `nil` as
SQL `NULL`, and provides same-column atomic addition through `Increment`.
There is no unconditional typed update.

Soft-delete models add active-row guards to SELECT and UPDATE statements.
`WithDeleted` includes deleted root rows or enables an explicit restore, while
`PreloadWithDeleted` scopes inclusion to one relation path. `Delete` and
`DeleteWhere` use one server-timestamped UPDATE for those models; untagged
models continue to use physical DELETE. Zero-valued soft-delete fields in
`Upsert` and `UpsertMany` write NULL and therefore restore conflicting rows.

Use `Raw[T]` for joins, CTEs, aggregates, and other SQL outside the scalar
builder. Returned column names map to model columns, including fields tagged
`computed`. Use `RawExec` only when a mutation expression cannot be represented
by the typed API. See the [mutation and raw SQL guide](docs/mutations.md).

`Raw[T]` and `RawExec` pass caller-supplied SQL to the executor without parsing,
sanitizing, or applying typed mutation safeguards. Treat the SQL statement as
trusted application code and pass every untrusted value separately through a
`?` placeholder:

```go
users, err := orm.Raw[User](
    "SELECT id, email FROM users WHERE email = ?",
    requestedEmail,
).All(ctx, db)
```

Never concatenate request parameters, user input, or external data into a raw
statement. Placeholders represent values, not identifiers or SQL keywords. If
a table, column, direction, or other SQL structure must be selected dynamically,
map it through a closed application allowlist. `RawExec` also leaves predicate
coverage, transaction boundaries, and destructive-operation safety entirely to
the caller. See Go's official [SQL injection guidance](https://go.dev/doc/database/sql-injection).

## Statement observation

Enable a context-scoped execution log without replacing the caller-owned
executor:

```go
ctx = orm.WithStatementObserver(ctx, orm.NewStatementLogger(os.Stderr))
```

By default, the logger records operation, duration, bind count, affected rows,
SQL template, and errors without receiving argument values. Interactive
terminal output uses colors automatically, while redirected output is plain
text. See the [statement observation guide](docs/observability.md) for lifecycle
coverage, custom observers, the explicit `IncludeStatementArguments` mode, and
logging safety boundaries.

## TiDB diagnostics

Inspect the plan for a typed SELECT without executing its root query:

```go
plan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    Explain(ctx, db)
```

`Explain` returns TiDB's default five-column row format as
`[]orm.ExplainRow`. It accepts neither mutation nor raw SQL, adds one database
round trip, and describes only the root SELECT, including inline to-one joins.
Collection preload statements are not included. TiDB can evaluate certain
subqueries while optimizing an `EXPLAIN`. See the [statement observation
guide](docs/observability.md#select-explain) for the complete boundary.

Run the typed SELECT and collect actual operator data only when explicitly
requested:

```go
runtimePlan, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Equal("Email", email)).
    ExplainAnalyze(ctx, db)
```

`ExplainAnalyze` returns actual rows, execution information, memory, and disk
usage as `[]orm.ExplainAnalyzeRow`. It executes the complete root SELECT without
adding a limit and consumes database resources and RU. Mutation, raw SQL, and
collection preload statements remain outside this path. See the [runtime-plan
boundary](docs/observability.md#explain-analyze) before enabling it in an
application.

Read TiDB's ServerRU for one completed DML statement from the same pinned
session:

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

`LastServerRU` accepts only `*sql.Conn` or an active `*sql.Tx`; `*sql.DB` is
excluded because a second call can use another pooled connection. It adds one
round trip and reports only the last DML statement recorded on the session, so
a preload or split bulk operation is not aggregated. ServerRU is a diagnostic
value reported by TiDB and is not billed RU. See the [statement observation
guide](docs/observability.md#serverru) for the complete boundary.

## CLI

The `tidbgo` CLI currently exposes version information only:

```sh
tidbgo version
```

Development builds print `tidbgo dev`; release builds print the version set at
build time.

`tidbgo --version` and `tidbgo -V` provide the same result. Run
`tidbgo --help` for command help.

## Security

- Typed builders keep values separate from SQL text as bind arguments
- Model-derived identifiers are validated before being written into SQL
- The built-in statement logger excludes argument values by default
- Enabling `IncludeStatementArguments` can expose credentials, tokens, or
  personal data and must be limited to controlled debugging
- Raw SQL is trusted application code and receives none of the typed builder's
  structural or mutation-safety validation

See [Mutations and raw SQL](docs/mutations.md) and [Statement observation](docs/observability.md)

## Known limitations

- The scalar runtime currently provides `Build`, `All`, `First`, `Only`,
  `Exists`, `Count`, `Explain`, and `ExplainAnalyze`; `IDs` is not implemented
  yet.
- Direct and pure `many_to_many` relation predicates and preloads may be nested.
  Preload projection, collection ordering, and relation-scoped inclusion of
  logically deleted targets are implemented; arbitrary target predicates are
  not.
- Typed mutations expose only bound value assignment and same-column addition,
  not arbitrary SQL expressions, unconditional UPDATE, or unconditional
  DELETE. `RawExec` is the explicit escape hatch.
- No database connection constructor, bundled protocol driver, or migration
  API is available yet.

## License

MIT. See [LICENSE](LICENSE).
