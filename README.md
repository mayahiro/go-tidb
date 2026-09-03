# go-tidb

`go-tidb` is an offline-first, struct-first Go ORM for TiDB Cloud Starter

The Go module path is `github.com/mayahiro/go-tidb` and the command name is
`tidbgo`

[日本語](README_ja.md) | [Struct models](docs/models.md) |
[Queries](docs/queries.md) | [Mutations and raw SQL](docs/mutations.md) |
[Analysis](docs/checks.md) | [Statement observation](docs/observability.md) |
[Development](docs/development.md)

## Available features

- Application-owned Go structs without generated models
- Offline model validation, model-intent diagnostics, and SQL construction
- Reasoned suppression and deterministic text or JSON reports for runtime and
  source analysis
- Offline Go-source query-pattern and projection analysis with explicit
  coverage statistics
- Explicit execution through caller-owned `*sql.DB`, `*sql.Conn`, or `*sql.Tx`
- Scalar predicates, ordering, offset pagination, and keyset pagination
- Deterministic direct and many-to-many relation predicates and preloads
- Single and automatically split bulk insert and upsert
- Primary-key and predicate-bounded update and delete
- Soft deletion, restore, pure-junction mutations, and transaction helpers
- Typed scanning for raw joins, CTEs, aggregates, and partial results
- Context-scoped statement observation with automatic terminal colors
- Observer-only structured runtime capture of actual root, preload, and
  split-bulk statements, with offline N+1 analysis
- SELECT-only TiDB execution-plan inspection through the typed query builder
- Explicit SELECT execution with actual TiDB runtime-plan inspection and
  diagnostics over the returned rows
- Explicit or observer-scoped same-session ServerRU diagnostics

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

Install the `tidbgo` command separately when analysis commands are needed:

```sh
go install github.com/mayahiro/go-tidb/cmd/tidbgo@latest
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

Run intent-oriented checks separately when an application wants diagnostics
for valid but potentially unintended declarations:

```go
diagnostics := check.Model[User]()
```

The check performs no database access and reports stable `MOD001` through
`MOD007` codes for invalid metadata, ignored tags, likely tag-position
mistakes, missing primary-key capability, and one-way custom scalar types.
Applications explicitly list the model types they own; no generated registry
or source scan is required.

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

Declare a candidate unique key independently from the primary key by repeating
`tidbgo:",unique=<group>"` on its scalar fields. The logical group name is not
a physical index name. `model.Descriptor.UniqueKeys()` exposes the declaration,
and `check.Schema` requires the SQL snapshot to prove it with an unconditional
primary or unique key before applications rely on the contract.

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

## Offline schema compatibility

Parse a self-contained TiDB `CREATE TABLE` snapshot once and check each
application model against it:

```go
catalog, err := schema.Parse(schemaSQL)
if err != nil {
	return err
}

diagnostics := check.Schema[User](catalog)
```

Both operations are offline and execute no SQL. The comparison is directional:
every mapped, non-computed model field must have a compatible physical column,
while database-only columns are accepted when they are nullable, defaulted, or
generated. A required database-only column is a warning because it can make
model inserts fail. The check also covers ordered primary keys, `AUTO_RANDOM`,
native Go and SQL type families, nullability, writable generated columns, and
physical Relation targets. Collection checks validate many-to-many junction
keys and required columns, target identity, and the index prefixes used by
deterministic `has_many` and `many_to_many` lookups through stable `CMP001`
through `CMP015` codes. A model with relations therefore requires those target
and junction tables in the supplied snapshot.

`schema.Parse` accepts ordinary TiDB `CREATE TABLE` SQL and `SHOW CREATE TABLE`
output, including TiDB executable comments. It ignores schema-dump wrapper
statements such as `SET` and `DROP TABLE`, but does not replay `ALTER TABLE`
history, require foreign keys, recommend general query indexes, or inspect a
live database. See the [schema compatibility guide](docs/schema-checks.md).

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

Runtime capture applies `QRY002` through `QRY005` automatically to executed
typed query shapes. Passing an offline schema snapshot to `tidbgo analyze`
adds `QRY006` and `QRY007` index checks. `tidbgo lint` applies `QRY002` through
`QRY005` to statically resolved source query terminals, including `Build`,
without compiling or executing the application. For relation-filtered TopN it
uses the same normalized compiler decision as runtime query compilation. Its
optional `--schema` applies the same `QRY006` and `QRY007` index rules to
high-confidence root and relation-first ordered-limit shapes. Dynamic and
separately mutated builder flows remain explicitly uncertain.
Index presence does not predict the optimizer's selected plan, so verify it
with `Explain` or `ExplainAnalyze`.

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

`Has` is a logical relation-existence predicate. The compiler normally emits
`EXISTS` and adds TiDB's `SEMI_JOIN_REWRITE()` hint to filtered collection
predicates in a positive conjunctive context. For a narrow, metadata-proven
`has_many` or pure `many_to_many` + root-primary-key order + positive-limit
shape, it instead applies the relation filter and Limit before loading root
rows. The one-row proof can use either the target primary key or an explicitly
declared candidate unique key whose complete field set is fixed by the relation
and conjunctive `Equal` predicates. The generated outer query uses
`LEADING(tidbgo_k0, tidbgo_t0)` so the limited derived keys drive root-row
lookups; it does not force a join algorithm. Runtime analysis emits
`QRY005` when an ordered, limited collection filter falls back to `EXISTS`.
This applies both to executed runtime shapes and statically resolved source
terminals. Schema-aware runtime and source analysis emit `QRY007` for a
missing association index prefix. An unpaginated `Count` with one direct
positive collection `Has`, no root predicate or active root soft-delete scope,
and the same one-row proof counts the association table directly. Pure
many-to-many Count uses the junction directly only when every target predicate
maps to its target-key columns. Other Count shapes retain the root `EXISTS`.
The direct Count rewrite relies on the same documented relation-integrity
contract as relation-first TopN. Pass target
predicates to require a matching related row, or omit them for existence only.
Relation and target field names are exported Go field names. `Build` validates
and compiles them entirely offline. See the [scalar query
guide](docs/queries.md#relation-predicates) for the exact rewrite conditions,
relation-integrity contract, and index guidance.

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
limit, while `Build` continues to represent one executable statement. Runtime
capture records the actual split automatically.
Pass a `*sql.Tx`, created directly or supplied to a `Transaction` callback,
when every batch must be atomic. `Transaction` uses default `database/sql`
options and does not retry its callback. Every typed mutation supports offline
`Build`. An empty predicate list cannot produce a typed DELETE. `*sql.DB`,
`*sql.Conn`, and `*sql.Tx` implement the mutation executor boundary.

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

For structured analysis, create one reusable capture and install it only at a
request or job boundary:

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture)
```

Existing ORM calls require no registration, wrapper, or diagnostic call. The
JSON Lines artifact records actual root queries, collection preloads, and bulk
splits with bind-free fingerprints, duration, and row counts. Model-row SELECT
and plan records also include bind-free query shapes and compiler decisions.
Analyze the artifact offline without a database connection or per-query
registration:

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json
```

The analyzer applies `QRY002` through `QRY005` to captured query shapes and
reports possible N+1 SELECTs. Supplying an offline TiDB `CREATE TABLE` snapshot
also applies the `QRY006` and `QRY007` physical index checks. Coverage counters
separate all captured statements from statements that carried a query shape
and those checked against the snapshot.

Runtime capture does not add `EXPLAIN` or other database I/O by default. Add
`orm.CollectServerRU()` to `WithRuntimeCapture` only when one extra
same-session diagnostic round trip per recognized DML statement is acceptable.
The artifact and `tidbgo analyze` keep target and diagnostic durations, go-tidb
and auxiliary statement counts, samples, errors, and summed ServerRU separate.
For each fingerprint that attempted collection, the analyzer also reports the
captured statement count and successful-sample total, mean, minimum, and
maximum without retaining individual samples.
Save those aggregates as a deterministic versioned baseline when every
measured fingerprint has complete measurement coverage, at least five
successful samples, and no collection errors:

```sh
tidbgo baseline runtime.jsonl > server-ru-baseline.json
```

The command is offline, writes exactly one JSON value to standard output, and
does not retain individual samples. Compare a current capture offline with
`tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json`.
Every measured fingerprint must exist on both sides, current collection must
also be complete and error-free, and each side must have at least five samples.
A current per-statement mean is an `RU001` regression only when it exceeds both
130% of the baseline mean and the maximum value observed in the baseline.
Missing fingerprints or unusable measurements produce `RU002`. Both are
non-suppressible errors; equality with the effective limit passes.
Bind values remain excluded, but SQL templates and errors can still contain
application data. See the [statement observation guide](docs/observability.md)
for scope, cost, writer-error, and retention details.

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
if err != nil {
    return err
}
diagnostics := runtimePlan.Diagnostics()
```

`ExplainAnalyze` returns actual rows, execution information, memory, and disk
usage as `orm.ExplainAnalyzePlan`. `Diagnostics` checks the returned rows for
incomplete statistics, a conservative estimate-to-actual row divergence, a
large table full scan, and positive disk usage without another database call.
When TiDB reports a compiler-owned table alias, each runtime-plan row also
resolves `PhysicalTable`, `Model`, and the root-relative `RelationPath` from the
typed query metadata. Junction tables have no model, and ambiguous access
objects remain unresolved instead of being guessed.
`ExplainAnalyze` executes the complete root SELECT without adding a limit and
consumes database resources and RU. Mutation, raw SQL, and collection preload
statements remain outside this path. See the [runtime-plan
boundary](docs/observability.md#explain-analyze) before enabling it in an
application.

Read TiDB's ServerRU for one completed DML statement from the same pinned
session:

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
```

This observer-scoped form requires no query-specific wrapper. For `*sql.DB`,
go-tidb temporarily pins each recognized DML statement and its diagnostic query
to one connection. It also accepts caller-supplied `*sql.Conn` and active
`*sql.Tx` executors. Collection failures are recorded separately and never
replace target results.

For one manual read, use a caller-pinned session:

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

Analyze a structured runtime artifact without registering application queries
or connecting to a database:

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --json
tidbgo analyze runtime.jsonl --schema schema.sql
tidbgo analyze current-runtime.jsonl --baseline server-ru-baseline.json
```

The command aggregates captured statements, applies `QRY002` through `QRY005`
to recorded query shapes, and reports possible N+1 SELECTs within each observer
scope. `--schema` adds offline `QRY006` and `QRY007` index checks. See the
[statement observation guide](docs/observability.md#structured-runtime-capture).

`tidbgo baseline` emits a versioned, fingerprint-sorted ServerRU reference and
requires complete error-free coverage with at least five samples per measured
fingerprint. `tidbgo analyze --baseline` applies the fixed offline regression
policy described in the [statement observation
guide](docs/observability.md#structured-runtime-capture).

```sh
tidbgo baseline runtime.jsonl > server-ru-baseline.json
```

Analyze production Go source for resolved query patterns and default
projections that can be proven wider than their local result use:

```sh
tidbgo lint
tidbgo lint ./internal/repository --json
tidbgo lint ./internal/repository --schema schema.sql
```

The optional path defaults to the current directory. The command does not
execute application code, load packages, connect to a database, or modify
source. `QRY002` through `QRY005` cover resolved `Offset`, `Limit` plus
ordering, leading-wildcard predicates, and relation-first TopN compiler
fallbacks. `SRC001` is emitted only when every
use of an `All`, `First`, or `Only` result is understood within the same
function. With `--schema`, resolved root queries using a positive explicit
`Limit`, uniform-direction `OrderBy`, and only conjunctive `Equal` filters are
checked for a matching physical index prefix. Eligible direct `has_many` and
pure `many_to_many` relation-first TopN queries check the association access in
the same way.
Dynamic relation names, unresolved relation metadata, range filters, mixed
ordering, and separately mutated builders remain uncertain. Projection
analysis also leaves returned or passed results, aliases, and preloads
uncertain. Every report includes general, relation compiler, and index
coverage statistics. See the [analysis guide](docs/checks.md#go-source-analysis)

Print version information with:

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
- Runtime capture excludes bind values but retains SQL templates and errors;
  protect the artifact destination and retention
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
  Filtered positive collection predicates use TiDB's semi-join rewrite hint,
  and eligible ordered `has_many` and pure `many_to_many` pages use
  relation-first TopN SQL.
  Preload projection, collection ordering, and relation-scoped inclusion of
  logically deleted targets are implemented; arbitrary target predicates are
  not.
- Typed mutations expose only bound value assignment and same-column addition,
  not arbitrary SQL expressions, unconditional UPDATE, or unconditional
  DELETE. `RawExec` is the explicit escape hatch.
- Source lint applies `QRY002` through `QRY005` only when the relevant builder
  flow and relation metadata are statically resolved. `--schema` adds
  `QRY006` and `QRY007` for high-confidence root and relation-first
  ordered-limit shapes. Dynamic relations remain explicit in uncertainty
  counters instead of being guessed.
- No database connection constructor, bundled protocol driver, migration
  application API, or live-schema introspection API is available yet.

## License

MIT. See [LICENSE](LICENSE).
