# Server warning diagnostics

[日本語](warnings_ja.md) | [Statement observation](observability.md)

`CollectWarnings` explicitly collects `SHOW WARNINGS` after ordinary SELECT,
INSERT, UPSERT, UPDATE, and DELETE statements. Configure it once on the executor
or context used by your repositories:

```go
executor := orm.Observe(db, orm.NewStatementLogger(os.Stderr), orm.CollectWarnings())
err := q.ScanAll(ctx, executor, &result)
```

`WithStatementObserver` and `WithRuntimeCapture` accept the same option:

```go
capture := orm.NewRuntimeCapture(captureWriter)
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectWarnings())
```

Collection adds one database round trip per recognized DML statement, including
individual preload and bulk statements. It pins a pooled `*sql.DB` connection
through result closure and the warning probe. `*sql.Conn`, `*sql.Tx`, and their
`Observe` wrappers are also supported. Callbacks run after an internally pinned
connection is released. A borrowed session must not be used concurrently.
There is no implicit EXPLAIN, query replay, session SET, or replica query.

## Results and coverage

`StatementEvent.Warnings` contains a `*WarningObservation`:

| State | Meaning |
| --- | --- |
| `nil` | Collection was not requested or does not apply |
| `Known=true`, empty `Warnings`, `Error=nil` | SHOW WARNINGS succeeded and returned no rows |
| `Known=true`, nonempty `Warnings` | Collected server rows are available |
| `Known=false`, `Error!=nil` | Collection failed or was skipped |

`Error` can also accompany a known result when connection release fails.
`AuxiliaryStatements` counts attempted warning queries; `DiagnosticDuration`
is separate from target-statement duration. Failed target statements or result
scans skip warning collection to avoid attributing stale session warnings.
Unsupported executors and probe failures are reported in the observation.
These errors never replace the target statement's result or error.

Raw `PlanWarning` rows expose `Level`, `Code`, and `Message` to the caller.
Messages and auxiliary errors can contain SQL values. `Diagnostics()` produces
fixed, value-free summaries:

| Code | Meaning |
| --- | --- |
| `WRN001` | TiDB reported that MPP might be blocked |
| `WRN002` | Other server warnings/errors or notes were returned |
| `WRN003` | Warning collection did not complete successfully |

`WRN001` recognizes warning level, code 1105, and TiDB's known MPP message prefix
together. Code 1105 alone is not evidence of an MPP limitation. Unknown messages
remain generic. Notes alone produce informational `WRN002`; the other cases
are warnings. None automatically fail a successful query or CLI analysis.

The built-in logger emits these categories and counts, without raw warning
messages or auxiliary error text. RuntimeCapture saves only category counts,
collection status, duration, and auxiliary statement count. Run
`tidbgo analyze capture.jsonl` to see fingerprint-grouped diagnostics; repeated
occurrences are combined. JSON output includes `warning_collections` and
`warning_collection_errors`. Omitted warning metadata in older captures means
uncollected, not a successful empty result. CLI suppressions accept these codes.

## MPP and explicit plans

Aggregate/vector `Explain`, `ExplainAnalyze`, and aggregate `Compare` already
collect same-session warnings. They publish those observations to configured
observers and captures without `CollectWarnings` or another probe. Their plan
`Diagnostics()` methods also classify warnings. `Compare.WriteCapture` exports
only measurements and continues to exclude plan/warning observations.

Some MPP limitations are exposed by TiDB only during EXPLAIN. An ordinary
SELECT's empty warning list does **not** prove MPP support or execution.
For a window SUM limitation, inspect an explicit plan and its warnings;
`WRN001` can coexist with MPP operators elsewhere in that plan. `PLN006` instead
reports a recognized plan with no MPP despite the request. Neither warning
establishes a performance regression. An explicit plan describes its own
statement, not an earlier `ScanAll` execution.

See TiDB's [MPP documentation](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/),
[warning semantics](https://docs.pingcap.com/tidb/stable/sql-statement-show-warnings/),
and [MPP warning implementation](https://github.com/pingcap/tidb/blob/release-8.5/pkg/sessionctx/variable/session.go).

## Interaction with ServerRU

On TiDB Cloud Starter, `SHOW WARNINGS` changes the last-query RU state, and
`SELECT @@tidb_last_query_info` replaces the warnings of the target statement.
The two probes cannot reliably describe the same execution in either order.

When both options apply, ServerRU takes precedence. Warnings are left unknown,
no warning SQL is issued, and `WarningObservation.Error` matches
`orm.ErrWarningsWithServerRU` via `errors.Is`. The logger and analyzer report
`WRN003`. This also applies to ordinary `Compare` samples, which always measure
RU; their separate plan executions still publish warnings.

Collect warnings and RU in separate, explicitly chosen executions. Warning
collection alone also changes the state read by a subsequent `LastServerRU`.
Do not treat a later RU probe as the target's cost. See
[tidb_last_query_info](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info-new-in-v4014).
