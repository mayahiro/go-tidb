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
only after `sql.Result.RowsAffected` succeeds. A SELECT duration covers
`QueryContext`, row scanning, iteration, and row closing. Terminal errors such
as `sql.ErrNoRows` and `orm.ErrMultipleRows` are included in the event.

Observers run synchronously after the duration is captured. Custom observers
should return quickly, be concurrency-safe when contexts are shared, and not
panic. `NewStatementLogger` serializes its own writes and ignores writer errors
so logging cannot replace a database result. Passing nil to
`WithStatementObserver` disables an inherited observer.

## Covered operations

Typed and raw SELECTs, preloads, typed mutations, automatically split bulk
mutations, relation mutations, and `RawExec` are observed. Typed upserts use the
logical `UPSERT` operation. `RawExec` recognizes a leading `INSERT`, `UPDATE`,
or `DELETE`; other raw mutations use `EXEC`.

`Transaction` emits separate `BEGIN`, `COMMIT`, and `ROLLBACK` events. Statements
executed through `go-tidb` inside its callback use the observer from the context
passed to each ORM call. Calls made directly on `*sql.DB`, `*sql.Conn`, or
`*sql.Tx` are outside this boundary because `go-tidb` does not install a
`database/sql` driver interceptor.

No observer is installed by default. Offline `Build` and model inspection never
emit events or perform I/O.
