# Aggregate queries and TiFlash

[日本語](aggregates_ja.md)

`Aggregate[T]` aggregates source-model rows, with optional relation-existence filters.
`Build` is offline. `ScanAll`, `Explain`, `ExplainAnalyze`, and `Compare` use an explicit
executor. The source needs no primary key and the result needs no model tags.

```go
type Order struct {
    model.Meta `tidbgo:"table=orders"`
    ShopID     int64
    Amount     int64
    CreatedAt  *time.Time
    Status     string
    UserID     int64
    User       *User `tidbgo:"belongs_to,join=UserID:ID"`
    DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type User struct {
    model.Meta `tidbgo:"table=users"`
    ID         int64 `tidbgo:",pk"`
    Plan       string
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
| `Field("ShopID")` | Source Go field; group by its selected output name |
| `Date("CreatedAt")` | Calendar date as SQL DATE; requires grouping |
| `YearMonth("CreatedAt")` | Calendar year and month as integer `YYYYMM`; requires grouping |
| `CountAll()` | `COUNT(*)`, including rows with NULL fields |
| `CountIf(condition)` | Count rows where a source-model condition is true |
| `SumIf("Amount", condition)` | Sum non-NULL amounts where the condition is true |
| `Count("Amount")` | Count non-NULL values |
| `CountDistinct("ShopID")` | Count distinct non-NULL values of one field |
| `Sum`, `Avg`, `Min`, `Max` | Aggregate non-NULL values of one source field |

Calendar keys and aggregate functions require `As`, even for scalar-slice
results. `Field` defaults to its source Go name and also accepts `As`. Output names must be
exported Go identifiers of at most 64 bytes and unique ignoring case. Source
references use exact Go field names, never SQL column names or raw expressions.

`Where` uses existing scalar predicates and `Has` against the source model. Soft-deleted
rows are excluded unless `WithDeleted` is called. `GroupBy`, `Having`, and
`OrderBy` refer to exact selected output Go names. The compiler renders
unambiguous expression references when an output name shadows a source column.
For example, `Field("ShopID").As("Store")` is grouped with `GroupBy("Store")`.
`Having` accepts comparisons, `In`, `NotIn`, `Between`, null checks, and
`And`/`Or`/`Not`.

Every selected non-aggregate expression must occur in `GroupBy`. Group keys
must be selected `Field`, `Date`, or `YearMonth` outputs. Equivalent expressions
selected under different names can share one key; duplicate group expressions
are rejected. No functional dependency between different expressions is inferred.
Grouping does not imply ordering; use `OrderBy` and sufficient tie-breakers for
stable results. `Limit` and `Offset`
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

## Filtering by related rows

Use `Has` in `Where` to restrict source rows by related data. For example,
aggregate orders placed by users on a paid plan:

```go
q := orm.Aggregate[Order]().
    Where(orm.Has("User", orm.Equal("Plan", "paid"))).
    Select(orm.YearMonth("CreatedAt").As("Month"),
        orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).
    GroupBy("Month").OrderBy(orm.Asc("Month"))
```

`Has` accepts direct, many-to-many, and `via` relations, including nested
`Has` and `And`/`Or`/`Not`. Each `Has` names one relation field; use nested
calls for longer paths. Its conditions use the target model's Go fields.
The compiler emits correlated `EXISTS`, so several matching targets or
junction rows do not multiply the source row's contribution. SQL equality
on every key component excludes NULL keys and missing targets.

Targets and `via` edges retain their active soft-delete scopes. `WithDeleted`
only includes deleted source rows. To-one related fields can be selected and
grouped through dotted paths with declared unique target keys (see below).
`Has` is supported inside `CountIf` and `SumIf`, but not directly inside `Having`.
Scalar conditional metrics, calendar keys, paging, `ScanAll`, and `Compare`
work over the source rows that survive `Where`.

Filtered positive collections use the existing `SEMI_JOIN_REWRITE()` hint;
conditions under `Or` or `Not` keep plain `EXISTS`. The aggregate compiler
retains the source table and does not apply scalar-query TopN or Count
rewrites. See [relation predicates](queries.md#relation-predicates) and
[optimizer hints](https://docs.pingcap.com/tidb/stable/optimizer-hints/#semi_join_rewrite).
Benchmark representative and selective inputs; a related filter does not
guarantee lower latency or RU.

## To-one related fields

Use `Field("User.Plan").As("Plan")` with `GroupBy("Plan")`, or an aggregate such
as `Min("User.ID").As("FirstUser")`. Dotted paths can traverse nested to-one
relations. Every target key must match a declared primary or candidate unique
key; maintain those constraints in the physical schema. A has-one declaration
alone does not prove uniqueness. Collection paths are rejected.

The compiler emits shared LEFT JOINs, preserving one input per source row.
Missing or soft-deleted targets yield NULL. Target deletion conditions stay in
ON; `WithDeleted` affects only source rows. Related expressions require `As`.
Storage hints include the related physical aliases. Use nullable outputs for
missing targets. `CountIf(Has(...))` and `SumIf("Amount", Has(...))` select
metric-specific source subsets without multiplying rows for multiple matches.

## Conditional aggregation

Use `CountIf` and `SumIf` when different metrics need different input conditions.
For example, return all orders and paid orders together for each day:

```go
paid := orm.Equal("Status", "paid")
q := orm.Aggregate[Order]().
    Select(orm.Date("CreatedAt").As("Day"), orm.CountAll().As("AllCount"),
        orm.CountIf(paid).As("PaidCount"), orm.SumIf("Amount", paid).As("PaidTotal")).
    GroupBy("Day").OrderBy(orm.Asc("Day"))

var days []struct {
    Day       sql.NullTime
    AllCount  int64
    PaidCount int64
    PaidTotal sql.NullString
}
err := q.ScanAll(ctx, db, &days)
```

Each function takes one existing scalar or relation-existence `Predicate`; combine conditions with
`And`, `Or`, and `Not`. Conditions reference source Go fields, using the same
validation, parameter binding, and escaped string matching as `Where`.
Conditional `Has` uses plain EXISTS without a WHERE-only semi-join rewrite hint.
Both require `As` and work with
output-name `Having`, ordering, calendar grouping, and [`Compare`](aggregate-comparison.md).

`CountIf(p)` generates `COUNT(CASE WHEN p THEN 1 END)`, and `SumIf("Amount", p)`
generates `SUM(CASE WHEN p THEN amount END)`. Only SQL TRUE matches; FALSE and
NULL do not. Count includes matching rows whose amount is NULL. Sum ignores
NULL amounts and returns NULL when no matching non-NULL amount exists; a
matching amount of zero returns a non-NULL zero. SQL numeric types and exact
decimal scanning follow `Sum`.

`Where` and the soft-delete scope filter the input to every metric. Conditions
inside a metric leave other metrics and groups intact. A group with no matching
rows therefore has count zero and sum NULL. Empty input with no `GroupBy` returns
one row with those values; empty grouped input returns no rows.

Reusing a conditional output in `Having` or `OrderBy` repeats its expression and
parameters in SQL order, avoiding alias/source-column ambiguity. `Build` never
calls `driver.Valuer`; ordinary execution delegates parameter conversion to the
driver. `Compare` evaluates each SQL parameter once before its measurement loop
and keeps that value for all variants and separate plans.

Use query-level `Where` when every metric shares the same input filter. A
selective indexed filter can cost less than scanning all inputs for conditional
metrics. Combining several metrics into one statement does not guarantee lower
latency or RU; compare representative inputs and separately filtered queries.
See [TiDB control flow](https://docs.pingcap.com/tidb/stable/control-flow-functions/)
and [TiFlash pushdown support](https://docs.pingcap.com/tidb/stable/tiflash-supported-pushdown-calculations/).

## Calendar grouping

Use `Date` for daily totals and `YearMonth` for monthly totals. Month keys include
the year, so January in different years forms different groups.

```go
q := orm.Aggregate[Order]().
    Select(orm.Date("CreatedAt").As("Day"), orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).
    GroupBy("Day").Having(orm.IsNotNull("Day")).OrderBy(orm.Asc("Day"))

var days []struct {
    Day   sql.NullTime
    Count int64
    Total sql.NullString
}
err := q.ScanAll(ctx, db, &days)

monthly := orm.Aggregate[Order]().
    Select(orm.YearMonth("CreatedAt").As("Month"), orm.CountAll().As("Count")).
    GroupBy("Month").OrderBy(orm.Asc("Month"))
var months []struct {
    Month sql.NullInt64
    Count int64
}
err = monthly.ScanAll(ctx, db, &months)
```

`Date` compiles to `DATE(column)` and `YearMonth` to
`EXTRACT(YEAR_MONTH FROM column)`, such as integer `202402` for February 2024.
Both preserve NULL; NULL inputs form one group unless filtered. Empty inputs
produce no groups, and dates absent from the input are not filled with zeroes.
Use fields mapped to DATE, DATETIME, or TIMESTAMP. Offline validation checks
field references; physical SQL types, coercion, and invalid/zero date behavior
remain TiDB's responsibility.

With `go-sql-driver/mysql`, `parseTime=true` allows SQL DATE to scan into
`time.Time`, `*time.Time`, or `sql.NullTime`. The driver attaches its `loc` to
the date at midnight; the result is a calendar key, not a UTC-normalized event
instant. Without `parseTime`, DATE arrives as bytes and can scan into `string`
or `sql.NullString`. Year-month keys scan into `int64`, `*int64`, or
`sql.NullInt64`. Destination conversion errors retain the previous slice.

TIMESTAMP calendar boundaries follow the session time zone. DATETIME and DATE
retain their stored calendar fields. Driver `loc` controls conversion of Go
values and does not set the server's time zone. Configure consistent connection
settings and date conventions explicitly; calendar expressions add no session
SET or automatic UTC conversion. See [TiDB time zone support](https://docs.pingcap.com/tidb/stable/configure-time-zone/)
and [date/time functions](https://docs.pingcap.com/tidb/stable/date-and-time-functions/).

For an input interval, compare the original field with explicit start-inclusive
and end-exclusive bounds, using driver/session settings consistent with those
values:

```go
q.Where(orm.GreaterThanOrEqual("CreatedAt", start), orm.LessThan("CreatedAt", end))
```

Calendar outputs work with `Having`, ordering, pagination, scalar/struct
scanning, explicit plans, and [`Compare`](aggregate-comparison.md). Calendar
HAVING references use `MIN(key expression)`: every row in the group has the
same key, including NULL, so this preserves the value and avoids TiDB's source
column resolution problem for repeated non-column group expressions.
It also avoids ambiguity with a grouped source column named like an output.
Date and integer month keys allow comparisons with explicit bounds; the ORM
does not parse display labels such as `"February 2024"` into a key.

## Explicit storage and MPP policy

```go
q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
```

This adds `READ_FROM_STORAGE(TIFLASH[a])`, `SET_VAR(tidb_allow_mpp=1)`, and
`SET_VAR(tidb_enforce_mpp=1)` to the outer SELECT. `a` is the compiler-owned
source-table alias. The same engine request is also emitted inside each `Has`
query block for its target and junction aliases, including nested and self
relations. Prepare replicas for every referenced table when requesting TiFlash.
`SET_VAR` appears only on the outer statement. `ReadFrom(TiKV)` requests row
storage for all these tables. `MPPAuto` enables MPP
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
server text and can include values. Observers receive the collected warnings;
the built-in logger and capture write only safe summaries. `Diagnostics()` adds
`WRN001` through `WRN003`. See [server warning diagnostics](warnings.md).

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

`Summary()` returns operator locations, estimated/actual output cardinalities,
and immediate child outputs with explicit unknown states. See the
[summary contract](tiflash.md#capabilities-and-plan-evidence).

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
not emit scalar-query metadata. Source lint separately checks statically known
aggregate projection/grouping contracts with `AGG001`; see [coverage](checks.md).
Existing ServerRU summaries and baselines can use these
records. A fingerprint does not identify input selectivity or dataset version:
maintain separate benchmark case IDs and baselines for comparable conditions.
The baseline CLI retains its per-fingerprint policy; use the comparison report
for measurements across engine requests.

ServerRU is not billed RU: server measurements exclude egress, and adoption
cost also depends on columnar storage and execution frequency. See the
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/) and
[reproducible checks](development.md#aggregate-and-tiflash-verification).

This API supports to-one related outputs and [windows over groups](windows.md).
Arbitrary joins, preload, and raw expressions use `Raw[T]` or other builders.
[Vector search](vector-search.md) is a separate builder.
[Replica preparation and capability probes](tiflash.md) are explicit operations.
