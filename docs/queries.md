# Scalar queries

[日本語](queries_ja.md)

The `orm` package builds scalar SELECT statements from application-owned Go
structs. Code generation and a database connection are not required to build
or validate a query.

## Build SQL offline

Use the exported Go field names from the model:

```go
query := orm.Query[Order]().
    Select("ID", "UserID", "Total").
    Where(orm.Equal("UserID", userID)).
    OrderBy(orm.Desc("ID")).
    SeekAfter(lastID).
    Limit(100)

sqlText, arguments, err := query.Build()
```

`Build` derives physical table and column names from cached model metadata. It
does not open a connection, execute SQL, read environment variables, or call a
custom `driver.Valuer`. Identifiers are validated metadata and values remain
separate bind arguments.

The model type passed to `Query[T]` must be a non-pointer named struct. Query
methods mutate and return the same builder, so a builder must not be mutated
concurrently.

Without `Select`, the projection contains every mapped non-`computed` scalar
field in struct declaration order. It never uses `SELECT *`. `Select` accepts
Go field names, not physical column names, and preserves the requested scan
order. Computed fields are available only through aliased `Raw[T]` results.

## Check a query shape offline

Call `Diagnostics` on the same builder to apply static query-shape checks:

```go
diagnostics := query.Diagnostics()
```

`Diagnostics` applies the same validation as `Build`, performs no database
I/O, and does not execute custom `driver.Valuer` implementations. Diagnostics
never include predicate or cursor values.

| Code | Severity | Meaning |
| --- | --- | --- |
| `QRY001` | error | Model metadata or the SELECT builder is invalid |
| `QRY002` | warning | A positive OFFSET skips rows and its cost grows as the offset grows |
| `QRY003` | warning | An explicit positive LIMIT has no ORDER BY |
| `QRY004` | warning | `Contains` or `HasSuffix` builds a LIKE pattern with a leading wildcard |

`QRY001` is not suppressible because the query cannot compile. The other
diagnostics describe valid query shapes and set `Suppressible` to true. TiDB's
[pagination guide](https://docs.pingcap.com/developer/dev-guide-paginate-results/)
recommends ordering paginated results and notes the increasing compute cost of
larger offsets. Prefer `SeekAfter` when a stable cursor fits the application.

`Contains` and `HasSuffix` deliberately begin the pattern with `%`, whose
matching behavior is defined by TiDB's
[`LIKE` documentation](https://docs.pingcap.com/tidb/stable/string-functions/#like).
The static check does not claim a specific physical plan because indexes,
statistics, collation, and optimizer behavior are connected concerns. Use
`Explain` or `ExplainAnalyze` to verify the actual access path.

The same builder can be used with `All`, `First`, `Only`, `Exists`, `Count`, or
plan terminals, so this method checks only state explicitly stored on the
builder. It does not report terminal-implied limits or an unbounded `All` call.
Raw SQL is outside the typed query AST and is not inspected.

## Soft-delete scope

A model with one field tagged `tidbgo:",soft_delete"` receives
`deleted_at IS NULL` automatically in `Build`, `All`, `First`, `Only`,
`Exists`, `Count`, `Explain`, and `ExplainAnalyze`. Use `WithDeleted` only when
both active and logically deleted root rows are required:

```go
allVideos, err := orm.Query[Video]().
    WithDeleted().
    OrderBy(orm.Asc("ID")).
    All(ctx, db)
```

`WithDeleted` requires a soft-delete field and affects only the root model.
It does not change independently scoped relation preloads. Typed `Raw[T]`
never adds hidden predicates because the supplied SQL is authoritative.
There is no separate only-deleted method; compose
`WithDeleted().Where(orm.IsNotNull("DeletedAt"))` when only logically deleted
root rows are required.

## Predicates

Multiple predicates passed to `Where`, including predicates added by later
calls, are joined with `AND` in call order.

The current constructors are:

- `Equal`, `NotEqual`
- `GreaterThan`, `GreaterThanOrEqual`
- `LessThan`, `LessThanOrEqual`
- `In`, `NotIn`
- `IsNull`, `IsNotNull`
- `Between`
- `Contains`, `HasPrefix`, `HasSuffix`
- `Has`
- `And`, `Or`, `Not`

`In` and `NotIn` accept a typed slice directly:

```go
videos := orm.Query[Video]().Where(orm.NotIn("ID", excludedVideoIDs))
```

An empty slice passed to `In` compiles to `FALSE`, and an empty slice passed
to `NotIn` compiles to `TRUE`. Comparison predicates reject nil values; use
`IsNull` or `IsNotNull` instead. String patterns escape literal `%`, `_`, and
the fixed escape character before adding compiler-owned wildcards.

### Relation predicates

`Has` requires at least one related row. Optional target-model predicates
require that row to match every supplied condition:

```go
admins, err := orm.Query[User]().
    Select("ID", "Email").
    Where(orm.Has(
        "Roles",
        orm.Equal("Name", "admin"),
    )).
    All(ctx, db)
```

The relation name and every target field name are exported Go field names. Use
`Has("Orders")` without target predicates for an existence-only condition.
Logical predicates and nested `Has` conditions are valid in the target scope.

Direct `belongs_to`, `has_one`, and `has_many` relations compile to a
correlated target-table `EXISTS`. Pure `many_to_many` relations compile to a
correlated junction-to-target `EXISTS`. Every declared component of a
composite direct or junction mapping participates in the correlation.
When the target model has a soft-delete field, `Has` considers active target
rows only. Use `Raw[T]` when an existence condition must explicitly inspect
deleted targets.

Relation predicates are validated and compiled by `Build` without database
access. Building the correlation does not read relation keys into Go, execute
a relation-key `driver.Valuer`, issue a secondary query, or hydrate the
relation. A target predicate argument follows the scalar predicate rules:
`Build` does not execute its `driver.Valuer`, but `database/sql` may do so
during connected execution. Add an explicit `Preload` to the same query when
the full relation must also be hydrated. `Preload` is not constrained by the
target predicates passed to `Has`.

## Ordering and pagination

`OrderBy` accepts `orm.Asc("Field")` and `orm.Desc("Field")`. Repeated fields,
unknown fields, and unknown directions are rejected during compilation.

`Limit` and `Offset` use non-negative `int64` values and bind parameters.
`Offset` requires `Limit`.

`SeekAfter` enables keyset pagination and accepts values in exactly the same
position order as `OrderBy`:

```go
query.OrderBy(
    orm.Desc("CreatedAt"),
    orm.Desc("ID"),
).SeekAfter(lastCreatedAt, lastID)
```

Field names are not repeated in `SeekAfter`. The order must contain every
primary-key component declared by the model so uniqueness can be proven from
offline metadata. `SeekAfter` cannot be combined with `Offset`.

A nil or typed-nil cursor value represents SQL NULL. TiDB's fixed default
ordering is used: NULL values come first for ascending order and last for
descending order. Custom non-pointer values are not executed to discover
whether their `driver.Valuer` result is NULL; use a nullable pointer
representation when NULL must be expressed.

## Execute explicitly

`All`, `First`, `Only`, `Exists`, `Count`, `Explain`, and `ExplainAnalyze`
perform I/O only when an existing executor is passed explicitly:

```go
orders, err := query.All(ctx, db)
order, err := query.First(ctx, db)
user, err := orm.Query[User]().
    Where(orm.Equal("Email", email)).
    Only(ctx, db)
exists, err := orm.Query[User]().
    Where(orm.Equal("Email", email)).
    Exists(ctx, db)
count, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    Count(ctx, db)
plan, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    Explain(ctx, db)
runtimePlan, err := orm.Query[Order]().
    Where(orm.Equal("UserID", userID)).
    ExplainAnalyze(ctx, db)
```

`*sql.DB`, `*sql.Conn`, and `*sql.Tx` implement `orm.QueryExecutor`. Terminals
do not open, configure, ping, or close the executor. They close the returned
`*sql.Rows` and check applicable scan, iteration, and close errors. `All`
returns a non-nil empty slice when no rows match.

`First` applies `LIMIT 1` and returns `sql.ErrNoRows` when no row matches. It
does not add an implicit order; use `OrderBy` when the selected row must be
deterministic.

`Only` applies `LIMIT 2` to distinguish zero, one, and multiple rows. It
returns `sql.ErrNoRows` for zero rows and `orm.ErrMultipleRows` for two or more
rows. Use `errors.Is` to inspect both errors.

Both single-row terminals replace a builder's existing `Limit` for their
execution without mutating the builder. `Offset`, predicates, ordering,
projection, and keyset state remain unchanged.

`Exists` emits `SELECT 1 ... LIMIT 1`, does not scan a model, and returns
`false, nil` when no row matches. Projection and ordinary ordering do not
affect existence and are omitted from its SQL. An active `SeekAfter` still
uses `OrderBy` to define and validate its cursor predicate. Predicates,
`Offset`, and keyset state remain effective. Its temporary `LIMIT 1` does not
mutate the builder.

`Count` returns the number of rows represented by the current builder and does
not scan a model. Predicates, keyset state, `Limit`, and `Offset` remain
effective. Projection and ordinary ordering do not change the count and are
omitted. An active `SeekAfter` still uses `OrderBy` to define and validate its
cursor predicate. Without `Limit` or `Offset`, `Count` uses a direct
`COUNT(*)`. With `Limit` or `Offset`, it counts a derived `SELECT 1` so the
pagination remains part of the result. Omit `Limit` and `Offset` when the total
number of matching rows is required.

`Explain` executes `EXPLAIN` for the root SELECT represented by `Build` and
returns TiDB's default row-format operators as `[]orm.ExplainRow`. It is
available only on `SelectQuery`, so mutations and caller-supplied raw SQL cannot
enter this path. Inline to-one joins are part of the plan. Collection preload
statements require parent keys and are not included. See [Statement
observation](observability.md#select-explain) for the fields, runtime boundary,
and TiDB-specific caveats.

`ExplainAnalyze` is the explicit opt-in terminal that executes the complete
root SELECT and returns TiDB's runtime plan as `[]orm.ExplainAnalyzeRow`. It
does not add a protective `LIMIT`, because changing the query would change the
measured plan. It consumes the executed SELECT's database resources and RU,
and runtime-plan collection can add overhead. Typed mutations and
caller-supplied raw SQL cannot enter this path. Collection preload statements
remain excluded.

The caller remains responsible for driver registration and connection
security. `go-tidb` does not currently include a MySQL protocol driver or a
TiDB Cloud Starter connection constructor.

## Preload relations

`Preload` accepts exported Go relation field names and is explicit on the
query that returns the parent models:

```go
users, err := orm.Query[User]().
    Select("Email").
    Preload("Orders").
    All(ctx, db)
```

The current slice supports `belongs_to`, `has_one`, `has_many`, and pure
`many_to_many` relations. Dot-separated paths request nested relations:

```go
users, err := orm.Query[User]().
    Preload("Orders.User").
    All(ctx, db)
```

`All`, `First`, and `Only` derive one deterministic strategy from the relation
kind and parent query shape:

- `belongs_to` and `has_one` use inline `LEFT JOIN`s
- `has_many` uses a target-table secondary SELECT
- Pure `many_to_many` uses a secondary SELECT with one fixed
  junction-to-target JOIN

A to-one relation nested below a collection is joined into that collection's
statement. An unrestricted `All` with no predicates, seek cursor, limit,
offset, or active root soft-delete scope reads each root collection source once
without a key predicate. Root ordering does not restrict the selected row set
and retains this strategy. A default-scoped soft-delete root, `First`, `Only`,
other constrained `All` queries, and nested collections use keyed batches. A
soft-delete root becomes unrestricted only when `WithDeleted` removes its
active-row scope. Runtime statistics and result size never switch the strategy,
and lazy loading is never used. Rows from the preceding statement are closed
before a collection statement is issued through the same caller-supplied
executor.

Pass options to the same `Preload` method when a relation needs a narrower
projection, explicit collection order, or deleted rows:

```go
users, err := orm.Query[User]().
    Preload(
        "Orders",
        orm.PreloadFields("ID", "Total"),
        orm.PreloadOrderBy(orm.Desc("ID")),
        orm.PreloadWithDeleted(),
    ).
    Preload("Orders.User").
    All(ctx, db)
```

Preload option field names are target-model Go fields. Required target relation
keys and source keys for nested collection loads are appended automatically to
an explicit target projection. Options apply to the relation at the end of the
supplied path. `PreloadFields` applies to both to-one and collection relations.
`PreloadOrderBy` applies only to collections; `Build` rejects it for a to-one
relation. `PreloadWithDeleted` requires a soft-delete field on the target and
applies only to the relation at the end of that path. Without it, inline JOINs
and secondary SELECTs exclude logically deleted targets. Arbitrary preload
target predicates are not currently available.

`Build` validates every requested relation offline and returns the complete
parent SQL, including any inline to-one joins. Keyed collection bind values do
not exist until preceding rows have been scanned, so collection SQL is built
only during execution. If an explicit projection omits a source key required
by a collection, that key is appended and hydrated normally. Inline join keys
are referenced as SQL columns and do not need to be added to the projection
solely for lookup. Calling `Build` does not invoke a custom `driver.Valuer`.

Collection source keys must be readable and usable as database arguments. A
custom collection source-key type must therefore support both `sql.Scanner`
and `driver.Valuer`; its `Value` method is called only during connected preload
execution. A direct collection target key has the same requirement because it
identifies hydrated target rows. A many-to-many target key participates in the
SQL JOIN and must be readable as part of the target model, but is not converted
to a bind argument or an in-memory lookup key. Inline target fields support the
same native representations and `sql.Scanner` types as ordinary result
scanning.

Collection hydration groups keys by their exact Go value or `driver.Valuer`
result. A `has_many` parent source key and target key must round-trip to the
same representation. A many-to-many parent source key and its junction source
column must round-trip through the source field type to the same
representation. This slice does not normalize byte-different string keys that
a SQL collation might consider equal.

Collection source keys are deduplicated in parent order. NULL parent source
keys do not match and do not cause keyed work.

For an unrestricted `All` without an active root soft-delete scope, each root
collection source is selected without an `IN` predicate. Hydration ignores
decoded relation rows whose source key is NULL or does not occur in the parent
result. For every constrained or nested collection, including a default-scoped
soft-delete root, batches target a budget of 5,000 bind parameters.
Composite-key width reduces the number of keys in each batch. Generated
statements never exceed TiDB's
[65,535-placeholder statement limit](https://docs.pingcap.com/tidb/stable/sql-statement-prepare/).
Composite relations use an OR of complete key equalities, so a row is never
matched from only part of a composite key.

Collection relations execute in request order, and each nested collection
subtree executes depth first. Inline relations do not add statements. Without
a keyed batch split, `Preload("Orders").Preload("Roles")` executes three
statements: the parent, the Orders SELECT, and the Roles SELECT.
`Preload("Orders.User")` executes two: the parent and an Orders SELECT with User
joined inline.

A keyed pure many-to-many SELECT reads the junction source key first as
internal bookkeeping, then every mapped scalar target field:

```sql
SELECT `j`.`user_id`, `t`.`id`, `t`.`name`
FROM `user_roles` AS `j`
JOIN `roles` AS `t` ON (`t`.`id` = `j`.`role_id`)
WHERE `j`.`user_id` IN (?, ?)
```

The unrestricted root-collection form uses the same SELECT without the
key `WHERE` clause or bind arguments. Target soft-delete filtering may still
add its own `WHERE` condition.

The junction-to-target JOIN uses every declared target-key component. Each
returned junction row appends one target value. The database schema remains
responsible for enforcing a unique source-target pair. Use an ordinary edge
model with direct relations when junction payload is part of application
behavior.

Generated preload statements select explicit mapped fields and never use
`SELECT *`. To-many fields remain nil when no target row matches. An inline
to-one field remains nil when every joined target-key component is NULL; a
partially NULL composite target key is an error. The physical schema must make
the target side of a to-one mapping unique. This is especially important for a
`has_one` foreign key: duplicate target rows duplicate parent result rows
rather than triggering a separate cardinality query. Relation fields have no
separate loaded state.

Collection order follows the database result. It is defined by
`PreloadOrderBy` when supplied and otherwise remains unspecified. `Exists` and
`Count` ignore preload declarations because they do not return models.

A single `*sql.DB` operation can use different connections for the parent and
collection statements. Pass a `*sql.Tx` using TiDB's repeatable-read snapshot
isolation when every statement must share one snapshot. It can be created
directly or supplied to a `Transaction` callback. Query methods do not begin a
transaction implicitly. A preload containing only inline to-one relations
executes as one statement and does not need a cross-statement snapshot.

## Current boundary

The public query surface includes `Build`, `All`, `First`, `Only`, `Exists`,
`Count`, `Explain`, `ExplainAnalyze`, direct and pure many-to-many relation
predicates, and nested direct or pure many-to-many preloads with target
projection, collection ordering, and per-path soft-delete scope.
`IDs` remains deferred. Use typed `Raw[T]` for joins, CTEs, aggregates, and
other SQL outside the scalar builder surface.
