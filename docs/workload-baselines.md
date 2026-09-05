# Operation-level ServerRU baselines

[日本語](workload-baselines_ja.md)

Per-fingerprint baselines compare the mean cost of one statement. They do not
detect executing the same statement more often when its mean RU stays the
same. An optional workload baseline also compares the mean **sum per captured
scope** and the number of DML statements per scope.

## Capture contract

Use the existing runtime capture at an operation boundary:

```go
capture := orm.NewRuntimeCapture(captureWriter)

// Once for each request, job, or test operation, not once per query.
ctx = orm.WithRuntimeCapture(ctx, capture, orm.CollectServerRU())
// Pass ctx to the existing repository functions.
```

No workload label, query registration, wrapper, or additional API is required
in the application. Configure and propagate the capture context as described
in [statement observation](observability.md#structured-runtime-capture).

For operation comparisons, prepare separate baseline and current artifacts
under these conditions:

- One scope represents one invocation of the operation, including its SELECTs,
  writes, preloads, and automatically split batches
- Every scope in each input belongs to the same operation and equivalent input
  conditions; keep different operations, row counts, conflict patterns, and
  transaction policies in separate workloads
- Restore equivalent fixtures between repetitions when writes change the
  next operation's input; check results as well as costs
- Enable ServerRU collection consistently throughout the operation and keep
  setup SQL, fixture checks, and plan probes outside its captured scope
- Finish all operation work, close query rows, and check `capture.Err()` before
  saving the artifact; do not compare a still-growing file

`--workload` declares this contract for the **entire input**. It does not
filter records, infer business meaning from SQL, or prove equivalent inputs.
Numeric scope IDs may differ across runs; grouping uses `(capture_id, scope_id)`
within each artifact, then compares distributions across scopes.

## Save and compare

```sh
tidbgo baseline reference.jsonl --workload sync-video-100-edges > baseline.json
tidbgo analyze current.jsonl --workload sync-video-100-edges --baseline baseline.json
tidbgo analyze current.jsonl --workload sync-video-100-edges --baseline baseline.json --json
```

Use the same explicit name on both sides. Names are 1-128 ASCII letters,
digits, dots, underscores, or hyphens, starting with a letter or digit. Use a
stable scenario name, not user data, IDs, secrets, or an absolute path.

Both sides require at least five scopes, each with at least one recognized
DML statement, complete ServerRU measurement coverage, and no statement or
collection errors. Unclassified raw SQL and plan statements make the workload
comparison unavailable. A hundred measured statements in one scope still
count as one operation sample. The existing requirement of five complete
samples per measured fingerprint also applies.

For each metric, the fixed comparison limit is:

```text
max(baseline scope mean * 1.30, baseline observed scope maximum)
```

- `RU003`: the current per-scope mean RU sum or DML statement count strictly
  exceeds its limit; equality passes
- `RU004`: workload comparison is unavailable, including a missing or different
  name, insufficient scopes, measurement gaps, errors, unsupported statements,
  or overflowing RU totals

Both are non-suppressible errors and make `analyze` exit with status `1`.
Invalid inputs or insufficient reference measurements make `baseline` fail
without writing a baseline. Without `--baseline`, workload analysis is
descriptive: inspect coverage, since a successful CLI exit is not a budget check.

Each metric is compared independently. Doubling the number of operations does
not itself regress the mean cost per operation. Increasing UPDATE calls from
10 to 100 within each scope can regress even when per-statement RU is unchanged.
More statements can also regress while total RU falls.

Fingerprint checks remain active. SQL-shape changes still produce `RU002`,
even if workload totals improve. Review the new SQL and measurements before
replacing the baseline; workload mode does not automatically accept rewrites.

## Output and limits

Analysis JSON adds `workload`, containing scope counts, complete-scope and
measurement coverage, DML and transaction-control counts, and `server_ru` and
`statement_count` metrics with `total`, `mean`, `min`, and `max` across scopes.
With incomplete coverage, RU metrics summarize only observed samples and are
not comparable operation costs; zero successful samples do not mean zero RU.
Comparison JSON adds `server_ru_comparison.workload`, with status, reason,
baseline/current metrics, limits, and independent regression flags. Text output
uses `server_ru_workload` and `server_ru_workload_comparison` lines.

`server_ru_comparison.summary` remains fingerprint-only. Use the CLI exit
status or the top-level diagnostic `summary.errors` for a combined gate; a
passing fingerprint summary does not imply a passing workload comparison.

The baseline stays at format version `1` and adds optional `workload` metrics.
It stores no capture/scope IDs, operation records, or redundant success/error
counters. Runtime artifact format and SQL generation are unchanged. The CLI
works offline and retains per-scope counters only when `--workload` is present;
it does not retain every statement or RU sample. Memory grows with distinct
scopes and other existing analysis identities.

The budget includes recognized SELECT, INSERT, UPSERT, UPDATE, and DELETE
statements. BEGIN, COMMIT, and ROLLBACK events are counted separately, but their
RU is not collected. Automatic RU probes are also excluded from the budget.
TiDB's [`tidb_last_query_info`](https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info)
reports the last DML statement, not a whole-operation total. These sums are
neither billed RU nor whole-transaction RU. Collection still adds one probe
round trip per recognized DML; use dedicated measurements rather than treating
instrumented timings as pure application latency.

Scopes have no completion marker. The analyzer cannot detect an omitted
operation or tail, prove application success, or see direct `database/sql`
calls and calls missing the capture context. Operations with no captured
statements are invisible, not zero-cost samples; a recorded transaction-only
scope cannot be compared. Verify complete artifacts and application results
outside this diagnostic. Plans, cache effects, and data distribution can change
RU even when the code is unchanged.
