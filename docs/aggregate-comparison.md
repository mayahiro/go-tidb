# Comparing aggregate execution policies

[日本語](aggregate-comparison_ja.md)

`AggregateQuery.Compare` executes the same [aggregate query](aggregates.md)
with `auto`, `tikv`, and `tiflash_mpp` requests. It checks every result value and
its order, measures ordinary SELECT latency and ServerRU, then obtains runtime
plans and warnings in separate executions.

```go
q := orm.Aggregate[Order]().
    Select(orm.Field("ShopID"), orm.CountAll().As("OrderCount"), orm.Sum("Amount").As("Total")).
    GroupBy("ShopID").OrderBy(orm.Asc("ShopID"))

report, err := q.Compare(ctx, db, orm.AggregateCompareOptions{
    Case: "shop-totals-fixture-v1",
})
if err != nil {
    return err // report retains successful samples and any collected plans
}
for _, variant := range report.Variants {
    fmt.Printf("%s rows=%d median_ms=%.3f mean_ServerRU=%.6f plan=%s\n",
        variant.Name, variant.Rows, variant.LatencyMS.Median,
        variant.ServerRU.Mean, variant.PlanStatus)
}
```

The source query is unchanged. Existing `ReadFrom` and `MPP` requests are
replaced in the comparison's copies. `auto` adds no policy hint and inherits
session settings; it does not reset those settings. TiKV requests row storage;
TiFlash requests columnar storage and enforces MPP cost selection. Hints cannot
make an unsupported operation executable or create a missing replica. See
[TiDB MPP selection](https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/).

## Inputs and result checks

Supply a fixed dataset or suitable read snapshot, equivalent driver/session
settings, and stable inputs. `Case` names these conditions, including data
version and input selectivity. Equal output does not prove that the underlying
data stayed unchanged. The caller owns that requirement and any snapshot setup.

| Option | Contract |
| --- | --- |
| `Case` | Required, 1-128 ASCII letters, digits, `.`, `_`, or `-`, starting with a letter or digit |
| `Samples` | Measured SELECTs per variant, 5-1000; zero selects 5 |
| `MaxRows` | Maximum result rows per execution; zero selects 100,000 |
| `FloatAbsoluteTolerance` | Nonnegative finite absolute tolerance; zero requires exact equality |
| `FloatRelativeTolerance` | Finite relative tolerance from 0 to 1; default zero |

`report.Options` retains the effective defaults and tolerances. Grouped queries
require `OrderBy`; provide enough tie-breakers to make it deterministic.
Selected [`Date` and `YearMonth` keys](aggregates.md#calendar-grouping) work
with the same result checks. Keep session time zone, driver `loc`, and
`parseTime` consistent within a case.
[`CountIf` and `SumIf`](aggregates.md#conditional-aggregation) use the same
result and NULL checks. Conditional-expression parameters, including repeated
references in HAVING/ordering, are frozen once per SQL parameter before warmup.
`Limit`, `Offset`, predicates, grouping, and soft-delete scope stay as built.
Exceeding `MaxRows` fails the comparison; it does not truncate a successful
result or rewrite LIMIT. Memory holds a reference result and a reusable current
result, plus sample metadata. The bound limits output rows, not bytes per value,
scanned input rows, or database cost.

Before I/O, standard `database/sql` bind values are frozen once per position:
pointers are resolved, `driver.Valuer` is evaluated once, and byte slices are
copied. Unsigned 64-bit integers are retained for drivers that support them.
Driver-specific argument types unsupported by the standard converter are
rejected. Do not mutate the query or its inputs during comparison.

Every warmup and measured result is checked against the first auto warmup.
Rows, columns, driver value types, NULLs, integers, and DECIMAL representations
must match exactly; byte slices are compared by content and times by instant.
DECIMAL bytes are not converted to floating point. For `float32` and `float64`,
the same driver type is required, and either absolute or relative tolerance
may accept a difference. Relative tolerance divides the absolute difference
by the larger absolute value. NaN never matches; infinities match only the
same infinity. Keep tolerance settings consistent within a case.

## Measurement and session lifetime

Each variant runs two warmups and then `Samples` measurements. Each round
rotates the first variant. With defaults, the comparison executes:

- 21 ordinary SELECTs, each immediately followed by a same-session RU probe
- Three separate `EXPLAIN ANALYZE` executions, each followed by `SHOW WARNINGS`

The 48 statements execute on one pinned connection. Supported executors are
`*sql.DB`, `*sql.Conn`, `*sql.Tx`, and their `Observe` wrappers. A pooled
connection is released after the comparison; caller-owned connections and
transactions stay open. Do not concurrently use a borrowed session.

`Duration` measures query execution, raw `database/sql` scanning, and row
closure. Compilation, connection acquisition, argument freezing, result
comparison, RU probes, plans, and callbacks are outside this duration. It does
not measure `ScanAll` destination mapping or application-owned Scanners.
`DiagnosticDuration` records the RU probe separately. Each variant has count,
minimum, median, mean, and maximum summaries for latency in milliseconds and RU.

Comparison explicitly collects RU even without `CollectServerRU`. Existing
observers and RuntimeCapture receive warmups, measurements, and plan events;
callbacks run after comparison and release of an internally pinned connection.
No callback or warning query intervenes between a successful SELECT and its
RU probe. Enclosing capture includes warmups and plans; use `WriteCapture`
below for measurement-only baseline input.

## Plans, failures, and interpretation

`variant.Plan.Executed` and `Warnings` describe a separate `EXPLAIN ANALYZE`.
`Plan.Planned` is empty. `PlanStatus` describes only that separate execution:

| Status | Meaning |
| --- | --- |
| `unrequested` | Auto has no explicit storage/MPP request to verify |
| `matched` | Source storage access is observed, all recognized source/related table accesses match the engine, and an MPP task exists when enforced |
| `mismatch` | Recognized access uses another engine, or enforced MPP is absent |
| `unknown` | Plan collection failed, tasks or table bindings are unknown, or source storage access is absent |

[`Where(Has(...))`](aggregates.md#filtering-by-related-rows) uses the same policy
for the source, targets, and junctions. Plans resolve physical tables and relation
paths, and a related table using another engine makes the policy a mismatch.
The check covers observed accesses; a table removed by TiDB optimization has no
access to verify. An ambiguous physical-table name can leave its relation path
unknown, especially for self relations.

A storage-free plan such as `TableDual` cannot confirm an engine and yields
`unknown` for a forced policy. A separate plan never proves which engine served
the ordinary SELECT samples. Warnings retain unredacted server text and may
contain values; inspect them before sharing a report.

`Complete` requires all result checks, measured samples, plan queries, warning
probes, and policy checks to succeed. Any failure returns an error and an
incomplete report, retaining successful samples and collected plans. A failed
SELECT, RU probe, or value check is not a successful sample. Partial statistics
are diagnostic evidence, and zero samples do not mean zero cost. A policy
mismatch or unknown policy prevents complete export; successful warning
collection can still contain warnings for the caller to review.

The API does not select a winner, change application hints, provision replicas,
start a transaction, or change session settings. ServerRU is TiDB's reported
statement consumption, not billed RU. Evaluate latency, columnar storage,
execution frequency, and costs such as egress separately. See the
[Starter FAQ](https://docs.pingcap.com/tidbcloud/serverless-faqs/).

## Exporting samples for baseline checks

Write one variant of a complete comparison to a caller-owned `io.Writer`:

```go
err = report.WriteCapture(writer, "tiflash_mpp")
```

The existing RuntimeCapture JSON Lines format contains only measured SELECTs,
one scope per sample. Warmups, plans, warnings, bind values, and result values
are excluded. A writer failure can leave partial output; discard it on error.

Keep separate files for each case and variant. Export one reference run to
`reference-tiflash.jsonl` and a later comparable run to `current-tiflash.jsonl`:

```sh
tidbgo baseline reference-tiflash.jsonl --workload shop-totals-fixture-v1 > baseline.json
tidbgo analyze current-tiflash.jsonl --workload shop-totals-fixture-v1 --baseline baseline.json
```

Pass `report.Options.Case` as `--workload` in both commands. The caller supplies
case identity; it is not embedded in the exported records or SQL fingerprint.
Hints remain part of the fingerprint, so existing regression checks require
the same variant and SQL shape. Cross-engine comparisons use the report's
measurements; a TiKV baseline cannot silently accept a TiFlash fingerprint.
See [baseline coverage](workload-baselines.md) and
[reproducible verification](development.md#aggregate-and-tiflash-verification).

## Warning observation

Separate plan executions publish their existing warnings to observers and
captures, with value-free summaries for the built-in logger and CLI.
`CollectWarnings` on ordinary comparison samples reports
`ErrWarningsWithServerRU`: comparison preserves the immediate RU probe.
`WriteCapture` remains a measurement-only export. See [warning diagnostics](warnings.md).
