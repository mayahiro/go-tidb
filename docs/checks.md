# Diagnostic reports

[日本語](checks_ja.md)

`go-tidb` keeps diagnostic production separate from reporting

Offline checks let application code explicitly select model types, query
builders, and SQL schema snapshots, while `check.Report` and `tidbgo check`
apply one fixed result policy without a database connection

This offline boundary requires no generated registry, source scan, project
YAML, or live-schema inspection. Explicit connected runtime-plan collection is
separate and remains opt-in

## Produce diagnostics

Collect diagnostics in an application-owned check command:

```go
diagnostics := make([]check.Diagnostic, 0)
diagnostics = append(diagnostics, check.Model[User]()...)
diagnostics = append(diagnostics, recentOrdersQuery.Diagnostics()...)
diagnostics = append(diagnostics, check.Schema[User](catalog)...)
diagnostics = append(diagnostics, recentClipsQuery.DiagnosticsWithSchema(catalog)...)

err := json.NewEncoder(os.Stdout).Encode(diagnostics)
```

The JSON input to `tidbgo check` is exactly one array of `check.Diagnostic`
objects

`DiagnosticsWithSchema` reuses the parsed snapshot to compare
high-confidence ordered query access with physical index prefixes. It remains
offline and includes a bind-free query fingerprint in emitted schema-aware
evidence

See the runnable
[`examples/starter-app/cmd/check`](../examples/starter-app/cmd/check) command
for an explicit model and query registration example

## Optional offline statement-count tooling

Statement counts are factual planning data rather than warnings by default:

```go
bulkCount, err := orm.InsertMany(orders).StatementCount()
allEstimate, err := usersWithOrdersQuery().EstimateAllStatements()
```

Bulk insert and upsert counts are exact for their successful execution path.
An `All` estimate always contains a minimum and marks its maximum known only
when the builder and relation cardinality prove one. These methods are offline
and do not inspect element values, execute custom `driver.Valuer` methods, or
access a database.

Do not mirror every production operation with a statement-count wrapper.
Normal application code should execute the builder directly. Structured
runtime capture records the actual statements, automatic bulk splits, and
preload batches after one observer is installed at the request or job boundary.

The offline values are not appended to `SelectQuery.Diagnostics` and are not
consumed directly by `tidbgo check`. Multiple statements can be an intentional
result of safe bulk splitting or relation loading, so dedicated tests or custom
offline tooling decide whether a project-specific threshold should become a
diagnostic.
See the [query guide](queries.md#preload-relations) and [mutation
guide](mutations.md#insert) for the exact bounds, and the [observation
guide](observability.md#structured-runtime-capture) for automatic runtime
collection.

## Opt-in connected plan diagnostics

An explicitly executed `ExplainAnalyze` plan can contribute diagnostics to the
same report:

```go
runtimePlan, err := recentClipsQuery.ExplainAnalyze(ctx, db)
if err != nil {
    return err
}
diagnostics = append(diagnostics, runtimePlan.Diagnostics()...)
```

`ExplainAnalyze` executes the complete root SELECT and consumes database
resources. `Diagnostics` itself is deterministic, performs no database I/O,
and inspects only those already returned rows. It emits suppressible `PLN001`
through `PLN004` warnings for incomplete statistics, conservative row-estimate
divergence, a large table full scan, and positive recognized disk usage.

This is manual connected analysis, not automatic runtime capture. Installing a
`RuntimeCapture` does not run `EXPLAIN ANALYZE`, collect plan rows, or create
these diagnostics. See the [EXPLAIN ANALYZE
boundary](observability.md#explain-analyze) for thresholds, evidence, cost, and
data-handling details.

## Report with the CLI

Read the array from standard input:

```sh
go run ./cmd/check | tidbgo check
```

Pass one input file when a separate artifact is useful:

```sh
tidbgo check diagnostics.json
```

An omitted input or an input of `-` reads standard input

The default text report preserves diagnostic order, lists suppressed
diagnostics with their reasons, and ends with active error, warning, info, and
suppressed counts

Use `--json` for the stable structured report:

```sh
go run ./cmd/check | tidbgo check --json
```

The fixed exit policy is:

| Status | Meaning |
| ---: | --- |
| `0` | No active error diagnostic remains |
| `1` | One or more active error diagnostics remain |
| `2` | Command arguments, diagnostic JSON, or suppression input is invalid |
| `5` | The command cannot read input, write output, or complete an internal operation |

Warnings and info do not change a successful status

Runtime capture artifacts use a separate command because they contain
execution records rather than a prebuilt diagnostic array:

```sh
tidbgo analyze runtime.jsonl
```

The command performs no database access. See the [observation
guide](observability.md#structured-runtime-capture) for the artifact boundary.

## Suppress an accepted diagnostic

Every suppression names one exact diagnostic code and requires a non-empty
reason:

```sh
go run ./cmd/check | \
  tidbgo check --suppress 'MOD005=read-only model does not use key mutations'
```

`--suppress` may be repeated for different codes

One suppression applies to every suppressible diagnostic with that exact code
in the input array. The complete suppressed diagnostic and reason remain in the
report. The command rejects duplicate suppression codes, unused suppressions,
empty reasons, and attempts to suppress a diagnostic whose `Suppressible`
field is false

Apply the same policy directly in Go when a CLI process is not useful:

```go
report, err := check.NewReport(
    diagnostics,
    check.Allow("MOD005", "read-only model does not use key mutations"),
)
if err != nil {
    return err
}
if report.HasErrors() {
    return errors.New("go-tidb checks failed")
}
```

`Report.Diagnostics()` returns active diagnostics, `Report.Suppressed()` returns
the recorded suppressions, and `Report.Summary()` returns the fixed counts

## Security and data boundary

Current model and query checks do not include bind values, and schema-aware
checks operate only on the supplied snapshot. Query fingerprints likewise
exclude bind and pagination values. Diagnostic messages can still contain
model names, schema identifiers, source paths, and parser errors

Treat diagnostic JSON and reports as development artifacts and control their
destination and retention. The text renderer escapes control characters before
writing untrusted input to a terminal. JSON output remains structured and does
not interpolate any value into SQL
