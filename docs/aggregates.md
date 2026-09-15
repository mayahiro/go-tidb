# Aggregate queries and TiFlash

[日本語](aggregates_ja.md)

`Aggregate[T]` builds a single-table aggregate SELECT from a source model.
`Build` is offline. `ScanAll`, `Explain`, `ExplainAnalyze`, and `Compare` use an explicit
executor. The source needs no primary key and the result needs no model tags.

```go
type Order struct {
    model.Meta `tidbgo:"table=orders"`
    ShopID     int64
    Amount     int64
    Status     string
    DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type ShopStats struct {
    ShopID     int64
    OrderCount int64
    Total      sql.NullString
}

q := orm.Aggregate[Order]().
    Where(orm.Equal("Status", "paid")).
    GroupBy("ShopID").
    Select(
        orm.Field("ShopID"),
        orm.CountAll().As("OrderCount"),
        orm.Sum("Amount").As("Total"),
    ).
    Having(orm.GreaterThan("OrderCount", int64(1))).
    OrderBy(orm.Desc("Total"), orm.Asc("ShopID"))

var stats []ShopStats
err := q.ScanAll(ctx, db, &stats)
```

## Expressions and results

| Expression | Meaning |
| --- | --- |
| `Field("ShopID")` | Source Go field, also required in `GroupBy` |
| `CountAll()` | `COUNT(*)`, including rows with NULL fields |
| `Count("Amount")` | Count non-NULL values |
| `CountDistinct("ShopID")` | Count distinct non-NULL values of one field |
| `Sum`, `Avg`, `Min`, `Max` | Aggregate non-NULL values of one source field |

Aggregate functions require `As`, even for scalar-slice results. `Field`
defaults to its source Go name and also accepts `As`. Output names must be
exported Go identifiers of at most 64 bytes and unique ignoring case. Source
references use exact Go field names, never SQL column names or raw expressions.

`Where` uses existing scalar predicates against the source model. Soft-deleted
rows are excluded unless `WithDeleted` is called. `Having` and `OrderBy` refer
to exact selected output Go names. The compiler renders their expressions to
avoid ambiguity when an output name shadows a source column. `Having` accepts
comparisons, `In`, `NotIn`, `Between`, null checks, and `And`/`Or`/`Not`.

Every selected `Field` must occur in `GroupBy`. Grouping does not imply ordering;
use `OrderBy` and sufficient tie-breakers for stable results. `Limit` and `Offset`
apply after grouping and `Having`; `Offset` requires `Limit`. Builder methods
mutate the query. Completed builders support concurrent reads, not concurrent
mutation.

`ScanAll` maps outputs to exact exported destination Go fields, including
unambiguous promoted fields; destination tags do not affect mapping. One output
can scan into a scalar slice. Pointers and `sql.Scanner` types are supported.
Unselected fields stay zero. The destination is replaced only after successful
scanning and row closure; errors preserve its previous storage and values.
An empty result replaces it with a non-nil empty slice. `sql.RawBytes` is rejected.

Without `GroupBy`, an empty input produces one aggregate row: counts are zero,
and `SUM`/`AVG`/`MIN`/`MAX` are NULL. `Having` can remove that row. Grouped empty
inputs produce no rows. TiDB determines numeric result types and SQL coercion;
the ORM does not infer physical DECIMAL precision from a Go type. Use nullable
destinations and an application-owned decimal Scanner when exact arithmetic
matters. `sql.NullString` can retain a nullable decimal representation.
`database/sql` reports conversion and overflow errors while scanning. Unlike a
normal source-field `ScanAll`, aggregate outputs never convert a soft-delete
NULL to a zero `time.Time`.

See [TiDB aggregate functions](https://docs.pingcap.com/tidb/stable/aggregate-group-by-functions/).

## Explicit storage and MPP policy

```go
q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
```

This adds `READ_FROM_STORAGE(TIFLASH[a])`, `SET_VAR(tidb_allow_mpp=1)`, and
`SET_VAR(tidb_enforce_mpp=1)` to the outer SELECT. `a` is the compiler-owned
source-table alias. `ReadFrom(TiKV)` selects row storage. `MPPAuto` enables MPP
with cost-based selection; `MPPEnforce` bypasses the MPP cost comparison.
Explicit TiKV plus MPPEnforce is rejected offline. Later calls replace the same
policy component. Omitting a component generates no corresponding hint and
retains the session's behavior; the ORM never issues `SET SESSION`.

These are requests. Missing replicas or unsupported operations can prevent
their application. `SET_VAR` restores the session variable after the statement.
Starter restricts engine-isolation settings, so these APIs do not guarantee
TiFlash execution. Disabling TiFlash MPP is outside this API: it can require
`tidb_allow_tiflash_cop`, which does not support `SET_VAR`.
See [optimizer hints](https://docs.pingcap.com/tidb/stable/optimizer-hints/) and
[MPP selection](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/).

## Requested, planned, and executed

```go
planned, err := q.Explain(ctx, db)
// Check err, then planned.WarningsError before trusting warning coverage.
// planned.Requested records hints; planned.Planned contains estimated operators.

executed, err := q.ExplainAnalyze(ctx, db)
// Check err, then executed.WarningsError.
for _, row := range executed.Executed {
    task := row.TaskInfo()
    // task.Known, task.Engine, task.Kind; row.PhysicalTable identifies the table.
    _ = task
}
diagnostics := executed.Diagnostics() // Offline; performs no extra SQL.
_ = diagnostics
```

`ExplainAnalyze` executes the complete SELECT and consumes its resources; it
returns plan rows, not aggregate values. Each terminal collects `SHOW WARNINGS`
immediately afterward on the same connection. It accepts `*sql.DB`, `*sql.Conn`,
`*sql.Tx`, and their `Observe` wrappers. A pooled DB is pinned until both
statements finish. Caller-owned sessions are never closed. Observer callbacks
run after warning collection and release of an internally pinned connection.

`AggregatePlan.Planned` is populated only by `Explain`; `Executed` only by
`ExplainAnalyze`. Neither is evidence of the engine used by a separate
`ScanAll`. Successful warning collection returns a non-nil slice, possibly
empty. `WarningsError` reports warning-query or connection-release failure
without discarding a successfully collected plan. Warnings contain unredacted
server text and can include values; they are not automatically logged or
captured.

`TaskInfo` is also available on ordinary `ExplainRow` and `ExplainAnalyzeRow`.
It recognizes `root`, `cop[tikv]`, `cop[tiflash]`, `batchCop[tiflash]`, and
`mpp[tiflash]`. Unknown strings remain available in `Task` with `Known=false`.
A root task has no storage engine; only kind `mpp` establishes MPP usage.
Unsupported plan column layouts return an error.

`AggregatePlan.Diagnostics` keeps existing executed-plan facts. `PLN003` is
informational for aggregate queries and describes operator output rows, not
physical bytes read. It does not infer missing pushdown from a root `HashAgg`:
partial and final aggregation can coexist. `PLN005` reports recognized table
accesses that differ from the requested engine. `PLN006` reports MPPEnforce with
recognized storage tasks but no MPP; unknown tasks prevent that conclusion.
Ordinary `ExplainAnalyzePlan.Diagnostics` retains its existing severities.

## Measurement and current scope

[`Compare`](aggregate-comparison.md) runs auto, TiKV, and TiFlash MPP with
result-value checks, rotated warmups and samples, ordinary latency/ServerRU,
separate runtime plans and warnings, and measurement-only capture export.
Use it to measure one query against a fixed case before choosing a policy.

Use the same inputs and fixed dataset or appropriate read snapshot for every
variant. Compare result values and ordering, with explicit tolerances for
floating-point results. Include selective queries and many output groups as
well as broad scans. Warm both implementations and rotate measurement order.

Read `LastServerRU` immediately after the ordinary SELECT's rows close on the
same connection, or use `CollectServerRU` with `ScanAll`. On Starter, a preceding
`SHOW WARNINGS` can replace last-query information, so measure SELECT RU and
inspect plan warnings in separate executions. Plan callbacks exclude warning
probe and pinning time from the target duration. `CollectServerRU` does not
probe EXPLAIN statements. Keep ordinary latency separate from EXPLAIN ANALYZE.

RuntimeCapture records aggregate SELECTs as `typed_aggregate`, with an `s1:`
SQL-template fingerprint that includes hints and excludes bind values. It does
not emit scalar-query metadata, so scalar/source query lint does not analyze
aggregate shapes. Existing ServerRU summaries and baselines can use these
records. A fingerprint does not identify input selectivity or dataset version:
maintain separate benchmark case IDs and baselines for comparable conditions.
The baseline CLI retains its per-fingerprint policy; use the comparison report
for measurements across engine requests.

ServerRU is not billed RU: server measurements exclude egress, and adoption
cost also depends on columnar storage and execution frequency. See the
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/) and
[reproducible checks](development.md#aggregate-and-tiflash-verification).

This initial API has no joins, relation predicates, preload, window functions,
raw expressions, vector search, or automatic replica management. Use `Raw[T]`
for SQL beyond the supported aggregate expressions. Provision replicas
explicitly outside application query execution using the
[Starter replica procedure](https://docs.pingcap.com/tidb/stable/create-tiflash-replicas/).
