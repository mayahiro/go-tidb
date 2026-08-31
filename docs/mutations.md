# Mutations and raw SQL

[日本語](mutations_ja.md)

The `orm` package builds CRUD statements from application-owned models and
executes them only through an explicitly supplied `database/sql` executor.
Query construction does not open a connection or require code generation.

## Insert

```go
user := User{Email: "ada@example.test"}
affected, err := orm.Insert(&user).Exec(ctx, db)
```

`Insert` writes every mapped scalar field except `computed` fields and the
single field tagged `auto_random`. After a successful single-row insert, it
assigns `sql.Result.LastInsertId` to the signed or unsigned integer
`auto_random` field. The caller-owned value is otherwise unchanged.

For a non-pointer `soft_delete` field, a zero `time.Time` is sent as SQL
`NULL`. A pointer soft-delete field follows ordinary nullable semantics: nil
is `NULL`, and a non-nil pointer is an explicit value. The same conversion is
used by single-row and bulk writes.

Use automatically bounded multi-row statements for either a value slice or a
pointer slice:

```go
orders := []*Order{orderA, orderB}
affected, err := orm.InsertMany(orders).Exec(ctx, db)
```

Both `[]Order` and `[]*Order` are accepted. A nil element in a pointer slice is
reported with its zero-based row index before execution. An empty slice is a
no-op. `InsertMany` does not populate individual generated IDs because one
`sql.Result` does not expose every generated value.

Dedicated tests and offline capacity tooling can inspect the exact planned
split count without building SQL or opening a connection:

```go
statementCount, err := orm.InsertMany(orders).StatementCount()
```

`StatementCount` applies model-metadata validation, selected-field validation
for `UpsertMany`, and the same placeholder calculation as execution. It does
not inspect element values or call custom `driver.Valuer` methods, so its cost
does not grow with a pointer slice merely to find nil elements. `Build` and
`Exec` retain value validation. An empty slice returns zero. The count
describes the successful execution path; an invalid value, executor, driver
conversion, or database error can stop `Exec` before every planned statement
is attempted.

Production code does not need a matching statement-count wrapper. Runtime
capture records each attempted batch and its group, position, row count, and
total planned count automatically after one observer is installed at the
request or job boundary.

`Exec` deterministically splits values only when one statement would exceed
TiDB's 65,535-placeholder limit. The maximum rows in each statement is
`floor(65535 / max(1, insertedFieldCount))`; the virtual field count of one
also bounds models that insert only a generated column and therefore have no
bind arguments. It validates every element of a pointer slice before the first
statement and returns the sum of the database-reported affected rows. `Build`
represents exactly one executable statement, so it reports an error with the
required statement count when splitting is needed.

`go-tidb` does not start an implicit transaction around batches. A later failure
returns the affected count from completed statements and an error containing
the failed batch and half-open source row range. Pass a caller-owned `*sql.Tx`
when all batches must commit or roll back together.

## Upsert

Use `Upsert` for TiDB's `INSERT ... ON DUPLICATE KEY UPDATE` behavior:

```go
affected, err := orm.Upsert(&rating).Exec(ctx, db)
affected, err = orm.Upsert(&rating, "Score", "RatedAt").Exec(ctx, db)
```

With no field names, a conflict updates every writable mapped non-primary-key
field. Field names select only those Go fields, following the same validation
as `Update`. Inserted values are referenced through TiDB's
`VALUES(column)` syntax, so update values do not add bind parameters.

Use the matching bulk operation for value or pointer slices:

```go
affected, err := orm.UpsertMany(ratings).Exec(ctx, db)
```

`UpsertMany` has the same automatic placeholder-bound batching, affected-count
aggregation, empty-slice behavior, and transaction boundary as `InsertMany`.
Its `StatementCount` method returns the exact planned UPSERT count and validates
the selected update fields offline for dedicated tests and tooling. It is not a
required companion to an application upsert operation.

Neither `Upsert` nor `UpsertMany` changes generated `AUTO_RANDOM` fields. A
single `sql.Result` cannot reliably distinguish an insert from a conflict on a
different unique key. Use `Insert` when the generated ID must be assigned to
the model.

Because the soft-delete field is writable by default, an `Upsert` or
`UpsertMany` model with its active zero value writes `NULL` through
`VALUES(deleted_at)` and restores a conflicting logically deleted row. Select
explicit update fields when an upsert must preserve deletion state.

TiDB reacts to any primary-key or unique-key conflict; this API does not select
a conflict target. TiDB's official guidance recommends using this statement
only when a conflict can identify one intended row, especially for tables with
multiple unique keys. The returned affected count is the value reported by the
database and is not necessarily the number of input models. See
[TiDB's update guide](https://docs.pingcap.com/developer/dev-guide-update-data/#use-insert-on-duplicate-key-update)
for the database semantics and constraint warning.

## Update

`Update` always identifies the row through every primary-key component on the
model. With no field names it writes every writable, mapped non-primary-key
field:

```go
user.Email = "grace@example.test"
affected, err := orm.Update(&user).Exec(ctx, db)
```

Pass Go field names for a partial update:

```go
affected, err := orm.Update(&user, "Email").Exec(ctx, db)
```

Primary-key, `auto_random`, and `computed` fields cannot be selected for an
update.

For a soft-delete model, `Update` and `UpdateWhere` match active rows only by
default. Use `WithDeleted` to restore a row by clearing the deletion field:

```go
affected, err := orm.UpdateWhere[Video](
    orm.Set("DeletedAt", time.Time{}),
).WithDeleted().Where(
    orm.Equal("ID", videoID),
).Exec(ctx, db)
```

`WithDeleted` is also available on a primary-key `Update`. It requires the
model to declare a soft-delete field. The value-form zero time above becomes
SQL `NULL`; `nil` can be used for a pointer-form field.

Use explicit assignments and predicates when an update can affect any number
of matching rows:

```go
affected, err := orm.UpdateWhere[JobLease](
    orm.Set("LockOwner", owner),
    orm.Set("LockUntil", lockUntil),
).Where(
    orm.Equal("JobID", jobID),
    orm.Or(
        orm.IsNull("LockUntil"),
        orm.LessThanOrEqual("LockUntil", now),
    ),
).Exec(ctx, db)
```

`Set` keeps its value in a bind argument. Pass `nil` to assign SQL `NULL`.
Use `Increment` for same-column atomic addition:

```go
affected, err = orm.UpdateWhere[JobLease](
    orm.Increment("RetryCount", int64(1)),
    orm.Set("LastError", message),
    orm.Set("LockOwner", nil),
    orm.Set("LockUntil", nil),
).Where(
    orm.Equal("JobID", jobID),
    orm.Equal("LockOwner", owner),
).Exec(ctx, db)
```

`Increment` accepts native numeric fields and custom `driver.Valuer`-backed
fields so applications can select their own DECIMAL representation. TiDB
validates that the physical column and delta support addition. `Build` does
not invoke the delta's `driver.Valuer`. See TiDB's
[transaction overview](https://docs.pingcap.com/tidb/stable/dev-guide-transaction-overview/)
for official same-column arithmetic examples in `UPDATE` statements.

`UpdateWhere` requires at least one assignment and one scalar predicate. It
rejects relation predicates, repeated assignments, and changes to primary-key,
`auto_random`, or `computed` fields. There is no unconditional typed update.
The only typed SQL expression is same-column addition through `Increment`;
use `RawExec` for other expressions or joined updates.

## Delete

Delete one model through all of its primary-key components:

```go
affected, err := orm.Delete(&user).Exec(ctx, db)
```

Delete multiple rows only through one or more explicit scalar predicates:

```go
affected, err := orm.DeleteWhere[Order](
    orm.Equal("UserID", user.ID),
).Exec(ctx, db)
```

`DeleteWhere` rejects an empty predicate list and relation predicates. There
is no unconditional typed delete operation.

When the model has a `soft_delete` field, both delete builders emit one
`UPDATE` that assigns TiDB's `CURRENT_TIMESTAMP(6)` and adds
`deleted_at IS NULL`. Repeating the operation therefore affects zero rows.
The server timestamp is not assigned back to the caller-owned Go model, and
statement observation reports the actual operation as `UPDATE`. A model
without the tag continues to use physical `DELETE`. Use explicit `RawExec`
when an application policy requires a hard delete for a soft-delete model.

## Pure many-to-many relations

Add target keys to a pure many-to-many relation with one multi-row junction
statement:

```go
roleIDs := []int64{adminRoleID, readerRoleID}
affected, err := orm.AddRelation[User](
    "Roles",
    user.ID,
    roleIDs...,
).Exec(ctx, db)
```

The relation argument is the exported Go field name on the source model. A
duplicate junction key is an error by default. Opt in to keeping existing
junction rows when repeated delivery is expected:

```go
affected, err := orm.AddRelation[User]("Roles", user.ID, roleIDs...).
    IgnoreExisting().
    Exec(ctx, db)
```

`IgnoreExisting` uses a no-op
[`ON DUPLICATE KEY UPDATE`](https://docs.pingcap.com/developer/dev-guide-update-data/#insert-on-duplicate-key-update)
clause instead of `INSERT IGNORE`, so unrelated insert errors are not
intentionally downgraded to warnings. This relies on the documented
pure-junction invariant that the source-target pair is the junction's only
unique key.

Remove selected targets or every target for one source:

```go
affected, err := orm.RemoveRelation[User]("Roles", user.ID, roleIDs...).
    Exec(ctx, db)
affected, err = orm.ClearRelation[User]("Roles", user.ID).
    Exec(ctx, db)
```

A single-column relation key is passed as its scalar Go value. For a composite
mapping, pass each key in declared relation order through `CompositeKey`:

```go
source := orm.CompositeKey(tenantID, userID)
groups := []orm.RelationKey{
    orm.CompositeKey(tenantID, groupAID),
    orm.CompositeKey(tenantID, groupBID),
}
affected, err := orm.AddRelation[User]("Groups", source, groups...).Exec(ctx, db)
```

All four operations support offline `Build`. An empty add or remove target
slice is a no-op. A statement that would exceed TiDB's 65,535-placeholder
limit is rejected instead of being split into partially successful writes.
Only payload-free pure junctions use this API; model a junction carrying
application data as a normal edge model and use ordinary CRUD operations.

## Build offline

Every typed mutation supports `Build`:

```go
sqlText, arguments, err := orm.Update(&user, "Email").Build()
```

`Build` validates metadata, primary keys, field names, predicate safety, and
the placeholder limit without database access. It returns custom
`driver.Valuer` values as bind arguments without invoking their `Value`
methods. `InsertMany.Build` and `UpsertMany.Build` return one statement and
report when `Exec` would require automatic batching.

## Typed raw results

Use `Raw[T]` for joins, CTEs, aggregates, and other SQL outside the scalar
builder:

```go
type UserSummary struct {
    model.Meta `tidbgo:"table=users"`
    UserID     int64 `tidbgo:"user_id"`
    OrderCount int64 `tidbgo:"order_count,computed"`
}

summary, err := orm.Raw[UserSummary](`
SELECT user_id, COUNT(*) AS order_count
FROM orders
WHERE user_id = ?
GROUP BY user_id`, userID).Only(ctx, db)
```

Returned column names map to `tidbgo` columns. Partial results are valid, and
SQL expressions should use aliases that map to ordinary or `computed` fields.
Unknown and duplicate result columns are rejected. Scan plans are cached by
model type and returned-column signature.

`Raw[T].Build` validates the model and that SQL is non-empty, but it cannot
parse or validate caller-supplied SQL or know its returned columns before
execution. `First` and `Only` do not rewrite raw SQL, so add ordering and limits
when they matter.

Use `RawExec` for an explicit mutation escape hatch:

```go
affected, err := orm.RawExec(
    ctx,
    db,
    "UPDATE counters SET value = value + ? WHERE id = ?",
    delta,
    id,
)
```

`RawExec` validates only the execution boundary and non-empty SQL. It bypasses
model, identifier, predicate, and mutation-safety checks.

### Raw SQL security

`Raw[T]` and `RawExec` send the supplied statement to the executor unchanged.
They do not parse, sanitize, or prove the safety of the SQL text.

- Keep the statement in trusted application code
- Pass request parameters and every other untrusted value through `?`
  placeholders and separate arguments
- Never assemble values with string concatenation, `fmt.Sprintf`, or template
  expansion
- Placeholders cannot represent table names, column names, sort directions, or
  SQL keywords; select dynamic SQL structure through a closed allowlist
- Review predicate coverage, transaction boundaries, and permissions for every
  `RawExec` call

This is safe because the value remains separate from the statement:

```go
result, err := orm.Raw[User](
    "SELECT id, email FROM users WHERE email = ?",
    requestedEmail,
).Only(ctx, db)
```

Do not write `"... WHERE email = '" + requestedEmail + "'"`. The Raw APIs
cannot protect an application after untrusted input has been inserted into the
SQL text. See Go's official [SQL injection guidance](https://go.dev/doc/database/sql-injection).

When the application uses `go-sql-driver/mysql` with
`interpolateParams=true`, also follow the driver's restriction against BIG5,
CP932, GB2312, GBK, and SJIS. The restriction applies even when values are
passed correctly as arguments because interpolation is implemented by the
driver. See the driver's
[`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams).

## Transactions and connections

`*sql.DB`, `*sql.Conn`, and `*sql.Tx` implement the relevant executor
interfaces. Mutation methods never begin, commit, or roll back a transaction
implicitly. Use `Transaction` when multiple operations must share a transaction
with the default `database/sql` options:

```go
err := orm.Transaction(ctx, db, func(tx *sql.Tx) error {
    if _, err := orm.Insert(&user).Exec(ctx, tx); err != nil {
        return err
    }
    if _, err := orm.InsertMany(orders).Exec(ctx, tx); err != nil {
        return err
    }
    return nil
})
```

`*sql.DB` and `*sql.Conn` implement `TransactionBeginner`. `Transaction`
commits after a nil callback result and rolls back after a callback error or
panic. A panic is propagated. A callback error is returned unchanged when
rollback succeeds; a rollback failure is joined to it. The callback receives a
concrete `*sql.Tx`, owns the work inside the transaction, and must not commit or
roll back that value itself. The helper never retries the callback and does not
support nested transactions.

Use `BeginTx` directly when custom `sql.TxOptions` or manual lifecycle control
is required. Connection configuration, ping, close, driver registration, DSN
handling, TLS, retry policy, and transaction options remain application
responsibilities.
