# Diagnostic reports

[日本語](checks_ja.md)

`go-tidb` keeps diagnostic production separate from reporting

Offline checks let application code explicitly select model types, query
builders, and SQL schema snapshots, while `check.Report` and `tidbgo check`
apply one fixed result policy without a database connection

This explicit-check boundary requires no generated registry, project YAML, or
live-schema inspection. A separate opt-in `tidbgo lint` command scans Go source
without loading packages. Explicit connected runtime-plan collection is also
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

## Go-source projection analysis

Scan the current directory recursively without registering each query:

```sh
tidbgo lint
```

Pass one production Go file or another directory when a narrower scope is
useful:

```sh
tidbgo lint ./internal/repository
tidbgo lint ./internal/repository --json
```

`tidbgo lint` parses source without loading or executing application packages,
running code generation, reading schema files, or connecting to a database. A
directory scan follows the current Go build context and excludes test files,
generated files, `vendor`, `testdata`, and hidden directories

The current `SRC001` rule recognizes default projections ending in `All`,
`First`, or `Only`. It recommends `Select` only when the model's mapped scalar
fields and every use of that result can be proven within the same function.
Direct same-package top-level query helper functions returning
`*orm.SelectQuery[T]` and local mutable builders are followed conservatively.
Computed, ignored, and relation fields are not part of the default projection
comparison

A result passed to another function, returned, aliased, used through a model
method or relation, or loaded with `Preload` is uncertain and produces no
projection warning. Unresolved model shapes and conditionally changed builders
are likewise uncertain. This avoids presenting an unsafe projection as a
mechanical fix

Every text or JSON report includes these coverage counts:

- `files`: parsed non-generated production files
- `model_types`: query result model types resolved for analysis
- `result_queries`: recognized `All`, `First`, and `Only` terminals
- `explicit_projections`: recognized result queries already using `Select`
- `analyzed`: default-projection results whose complete local use was proven
- `uncertain`: recognized results that were deliberately not guessed

`SRC001` is a suppressible warning, so it does not change the successful exit
status. `--suppress 'SRC001=reason'` uses the same reason-carrying policy as
`tidbgo check`. Invalid source or an input with no matching production Go files
returns status `2`; unreadable paths and output failures return status `5`

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
Evidence keeps TiDB's access object and adds the compiler-resolved physical
table, Go model, and relation path when the mapping is unambiguous.

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
tidbgo analyze runtime.jsonl --schema schema.sql
```

The command performs no database access. See the [observation
guide](observability.md#structured-runtime-capture) for the artifact boundary.
It automatically applies `QRY002` through `QRY005` to captured typed query
shapes. `--schema` parses the supplied TiDB `CREATE TABLE` snapshot offline and
adds `QRY006` and `QRY007`; no application-side query registry is required.
Statistics report how many captured statements carried query shapes and how
many were checked against the snapshot. Statements without a complete shape
remain outside these query-pattern and schema rules.
When `CollectServerRU` produced samples, runtime statistics keep target and
diagnostic durations, go-tidb and auxiliary statement counts, successful
samples, collection errors, and summed ServerRU separate. `RUN003` reports a
collection failure and is not suppressible because the measured data is
incomplete.

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
exclude bind and pagination values. `SRC001` does not include source literals
or bind values. Diagnostic messages can still contain model names, field
names, schema identifiers, source paths, and Go parser errors; a parser error
can quote invalid token text

Treat diagnostic JSON and reports as development artifacts and control their
destination and retention. The text renderer escapes control characters before
writing untrusted input to a terminal. JSON output remains structured and does
not interpolate any value into SQL
