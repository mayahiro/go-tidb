# Offline diagnostic reports

[日本語](checks_ja.md)

`go-tidb` keeps diagnostic production separate from reporting

Application code explicitly selects its model types, query builders, and SQL
schema snapshots, while `check.Report` and `tidbgo check` apply one fixed result
policy without a database connection

This boundary requires no generated registry, source scan, project YAML, or
live-schema inspection

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
