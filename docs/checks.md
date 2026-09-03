# Analysis and diagnostics

go-tidb separates checks by the evidence they require instead of asking an
application to register every query in a diagnostic program

| Evidence | API or command | Database access |
| --- | --- | --- |
| Go model metadata | `check.Model[T]` | No |
| SQL snapshot compatibility | `check.Schema[T]` | No |
| Go source query patterns and projection use | `tidbgo lint` | No |
| Executed query shapes and statement behavior | RuntimeCapture and `tidbgo analyze` | Capture only executes application statements unless ServerRU collection is enabled |
| TiDB optimizer estimate | `SelectQuery.Explain` | Yes |
| TiDB runtime plan | `SelectQuery.ExplainAnalyze` and `ExplainAnalyzePlan.Diagnostics` | Yes, and the SELECT is executed |

## Model and schema checks

Use ordinary Go tests for application-owned model types and schema snapshots

```go
func TestUserMapping(t *testing.T) {
    if diagnostics := check.Model[User](); len(diagnostics) != 0 {
        t.Fatalf("model diagnostics: %#v", diagnostics)
    }

    sqlText, err := os.ReadFile("testdata/schema.sql")
    if err != nil {
        t.Fatal(err)
    }
    catalog, err := schema.Parse(string(sqlText))
    if err != nil {
        t.Fatal(err)
    }
    if diagnostics := check.Schema[User](catalog); len(diagnostics) != 0 {
        t.Fatalf("schema diagnostics: %#v", diagnostics)
    }
}
```

Both checks are offline and return `[]check.Diagnostic`

`check.Model` validates model intent and tags through `MOD001` to `MOD007`
`check.Schema` applies directional compatibility rules through `CMP001` to
`CMP015`, including candidate unique-key declarations used by query rewrites

## Executed query diagnostics

Install one RuntimeCapture at a request, job, or analysis-test boundary and
pass the derived context through existing ORM calls

```go
capture := orm.NewRuntimeCapture(writer)
ctx = orm.WithRuntimeCapture(ctx, capture)

if err := runOperation(ctx); err != nil {
    return err
}
if err := capture.Err(); err != nil {
    return err
}
```

Analyze the resulting JSON Lines artifact offline

```sh
tidbgo analyze runtime.jsonl
tidbgo analyze runtime.jsonl --schema schema.sql
```

The analyzer applies these query rules automatically when a captured statement
contains a typed QueryShape

| Code | Meaning |
| --- | --- |
| `QRY002` | Positive OFFSET skips rows before returning a page |
| `QRY003` | Positive LIMIT has no deterministic order |
| `QRY004` | A LIKE predicate starts with a wildcard |
| `QRY005` | Relation-filtered TopN used the EXISTS fallback |
| `QRY006` | The supplied schema cannot describe an analyzed index access |
| `QRY007` | An ordered limited access has no matching index prefix |

`QRY006` and `QRY007` require `--schema`
Fingerprints and QueryShapes exclude bind values, but SQL templates and errors
can still contain application data

Runtime analysis also reports incomplete metadata, possible runtime N+1
SELECTs, ServerRU collection failures, and ServerRU baseline regressions
See [Statement observation](observability.md) for capture scope, artifact
security, ServerRU cost, and baseline comparison

## Go source analysis

Run source analysis without compiling or executing the application

```sh
tidbgo lint .
tidbgo lint . --json
tidbgo lint . --schema schema.sql
```

Source analysis applies `QRY002` through `QRY005` to resolved `Build`, `All`,
`First`, `Only`, `Explain`, and `ExplainAnalyze` query terminals
It resolves fluent chains, a single local builder definition, local query
helpers, integer and string literals, and simple same-file constants

`analyzed_patterns` counts terminals whose relevant pagination, ordering, and
leading-wildcard status were all resolved
Dynamic `Limit` or `Offset` values, variadic ordering, unresolved predicate
helpers, separately mutated builders, and captured builders are counted as
`uncertain_patterns` and are not guessed

For an ordered positive-limit root `Has`, source analysis resolves the same
collection relation metadata and applies the same normalized relation-first
TopN decision as the runtime compiler. `relation_topn_patterns`,
`analyzed_relation_topn_patterns`, and
`uncertain_relation_topn_patterns` expose that rule's coverage. A resolved
fallback emits `QRY005`; an unresolved relation name, model, key, order, or
builder flow remains uncertain instead of being guessed

The source decision recognizes `unique=<group>` candidate keys in the same way
as runtime model metadata. Source lint does not replace model-to-schema
compatibility tests; use `check.Schema` to verify that every declaration is
backed by an unconditional physical unique constraint

With `--schema`, source analysis also derives physical table and column names
from the same `tidbgo` metadata and default naming rule as the runtime model
descriptor. It sends only resolved root shapes with a positive explicit
`Limit`, uniform-direction `OrderBy`, and conjunctive `Equal` filters to the
same neutral index-prefix checker used by runtime analysis. A resolved
relation-first TopN decision sends its association access to that checker as
well. Direct `has_many` access checks target equality columns followed by the
relation key. Pure `many_to_many` access checks junction target columns
followed by junction source columns. The default active soft-delete column
participates in the equality prefix unless `WithDeleted` is resolved on the
root query; a direct relation target soft-delete column participates in its
association equality prefix

`index_patterns` counts ordered positive-limit candidates while
`analyzed_index_patterns` and `uncertain_index_patterns` separate shapes that
could and could not be checked. Relation fallbacks, non-equality association
filters, mixed directions, unknown fields, embedded model shapes, and
separately mutated builders remain uncertain rather than receiving a
speculative index diagnostic

`SRC001` proposes a narrower projection only when one function proves the
complete result use
Repository returns, aliases, model methods, and unresolved result flows remain
explicitly uncertain in the separate `analyzed` and `uncertain` projection
counters

## Runtime plan diagnostics

`Explain` requests TiDB's estimated plan without executing the root SELECT

`ExplainAnalyze` executes the root SELECT and returns
`orm.ExplainAnalyzePlan`
Calling `Diagnostics` on that plan performs no additional database call and
reports incomplete statistics, large estimate divergence, large full scans,
and positive disk use through `PLN001` to `PLN004`

```go
plan, err := query.ExplainAnalyze(ctx, connection)
if err != nil {
    return err
}
diagnostics := plan.Diagnostics()
```

Plan choice depends on data distribution and statistics, so plan diagnostics
and measured ServerRU baselines complement static rules rather than guarantee a
future plan

## Suppressions and exit status

`tidbgo analyze` and `tidbgo lint` accept repeated reason-carrying
suppressions

```sh
tidbgo analyze runtime.jsonl --suppress 'RUN002=bounded retry loop'
tidbgo lint . --suppress 'SRC001=full row is intentionally returned'
```

The code must exist in the current result and declare itself suppressible
Unused, repeated, reasonless, and non-suppressible entries are rejected
Suppressed diagnostics remain visible in text and JSON output

Active error diagnostics return status `1`
Warnings and information do not fail the command
Invalid input returns status `2`, and I/O or internal failures return status
`5`

## Current coverage boundary

- RuntimeCapture sees only statements executed through go-tidb with the
  derived context
- Source lint applies `QRY002` through `QRY005` only to statically resolved
  builder flows and relation metadata
- Source lint with `--schema` applies `QRY006` and `QRY007` only to resolved
  root or relation-first ordered-limit accesses
- Dynamic relation names and relation shapes that cannot be proven from local
  source metadata remain visible in relation uncertainty counters
- `EXPLAIN ANALYZE` executes the SELECT and consumes RU
- ServerRU collection adds one same-session diagnostic round trip per
  recognized DML statement
- Query plans and RU depend on current statistics, data distribution, and
  workload
