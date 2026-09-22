# Development

[日本語](development_ja.md)

This guide contains contributor-facing commands, repository structure,
integration-test setup, and benchmark procedures for `go-tidb`

## Migration verification

The [migration runner](migrations.md) has offline tests for baseline adoption,
up/down/reapplication, checksum and drift rejection, independent-client
locking, interrupted DDL and journal writes, explicit repair, and snapshot
output failure:

```sh
go test ./migrate
go -C cmd/tidbgo test ./...
go test ./migrate -race
go -C cmd/tidbgo test ./... -race
go test ./migrate -run '^$' -fuzz '^FuzzSQLBoundaries$' -fuzztime=10s
go test ./migrate -run '^$' -bench '^(BenchmarkSnapshotHash|BenchmarkLoad)$' -benchmem -benchtime=200ms -count=3
```

`BenchmarkLoad` measures file reading and section validation for one version,
100 versions, and a 1 MiB quoted literal. Fixture creation is outside the timer;
database execution and RU are not measured.

`BenchmarkSnapshotHash` compares joining and streaming the same sorted canonical SQL
into SHA-256. It covers one table, 100 tables, many columns, and a 1 MiB quoted
literal. It includes SQL splitting and canonicalization, but excludes file
I/O, the protocol driver, database latency, and RU. Profile both alternatives:

```sh
go test ./migrate -run '^$' -bench '^BenchmarkSnapshotHash$/^hundred_tables$/^join$' -benchtime=2s -cpuprofile=migration-join.cpu.out -memprofile=migration-join.mem.out
go test ./migrate -run '^$' -bench '^BenchmarkSnapshotHash$/^hundred_tables$/^stream$' -benchtime=2s -cpuprofile=migration-stream.cpu.out -memprofile=migration-stream.mem.out
go -C tools tool pprof -top ../migration-join.cpu.out
go -C tools tool pprof -top -alloc_space ../migration-stream.mem.out
```

Connected verification requires `TIDBGO_TEST_DSN` to select an **empty,
dedicated** database whose name starts with `tidbgo_test_`, using verified TLS.
Run the read-only target check first, then explicitly enable the migration test:

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterMigrationTarget$' -count=1 -v
TIDBGO_TEST_MIGRATE=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterMigrations$' -count=1 -v
```

Do not run other suites in the same database concurrently. The migration test
creates and removes only its own `tidbgo_it_migration_accounts` and
`_tidbgo_migrations` tables after verifying initial emptiness. It verifies
retained decimal data, snapshot stability after inserts, refresh after
down/reapplication, partial DDL failure and repair, adoption without recreating
application tables, and replay of captured initial SQL into an empty database. These
tests use the supplied environment; they do not load `.env` automatically.

## Aggregate and TiFlash verification

The [aggregate API](aggregates.md) has offline contract tests for grouping,
aliases, NULL/conversion errors, destination ownership, hint conflicts,
unknown plan tasks, warning failures, and connection/callback ordering:

```sh
go test ./orm -run '^TestAggregate|^TestPlanTask|^TestScanAll'
go test ./orm -run '^$' -bench '^BenchmarkAggregate$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregatePeriod$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateConditional$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateRelation$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateComparison(Manual)?$' -benchmem -benchtime=100ms -count=3
```

The benchmark compares identical SQL and result values through the same local
test driver: aggregate `ScanAll`, typed `Raw`, and a direct `database/sql`
collector. It covers 0, 1, 100, and 10,000 output groups. It excludes TiDB,
network, driver argument conversion, and RU; building SQL and validating the
aggregate's output mapping adds per-call work relative to a fixed raw query.

`BenchmarkAggregatePeriod` uses the same group counts to compare `Date` and
`YearMonth`, including HAVING on the calendar key, against equivalent typed
raw and direct collectors. All paths use identical SQL and destination types;
the local driver does not evaluate date expressions. Profile its daily path
with `-bench '^BenchmarkAggregatePeriod$/^date$/^rows_100$/^aggregate$'` and use
`raw` for the alternative.

`BenchmarkAggregateConditional` uses the same group counts and result types as
`BenchmarkAggregate`, with conditional count/sum expressions and a repeated
conditional output in HAVING. The alternatives use the same SQL and bind
values. The driver does not evaluate SQL conditions. Profile it with
`-bench '^BenchmarkAggregateConditional$/^rows_100$/^aggregate$'` and compare
with `raw`.

`BenchmarkAggregateRelation` compares nested `Has` filters using the same
0/1/100/10,000 group counts, SQL, bindings, and destination types through
aggregate, raw, and direct collectors. The driver does not execute relation
lookups. Profile `-bench '^BenchmarkAggregateRelation$/^rows_100$/^aggregate$'`
and the corresponding `raw` path with the commands below.

The comparison benchmarks use the same 0/1/100/10,000-row driver data, 21
SELECT/RU pairs, and three plan/warning pairs. The manual alternative uses
typed `ScanAll` and result equality; `Compare` freezes inputs, compiles before
the loop, reuses raw result buffers, and returns sample statistics. These
measure complete diagnostic orchestration, not a change in ordinary ORM
query performance or the cost of equivalent destination mapping.

Profile the representative path and compare it with `raw` or `database_sql`:

```sh
aggregate_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkAggregate$/^rows_100$/^aggregate$' -benchtime=2s -cpuprofile "$aggregate_profile_dir/cpu" -memprofile "$aggregate_profile_dir/mem" -o "$aggregate_profile_dir/orm.test"
go -C tools tool pprof -top "$aggregate_profile_dir/orm.test" "$aggregate_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$aggregate_profile_dir/orm.test" "$aggregate_profile_dir/mem"
```

For comparison profiles, use
`-bench '^BenchmarkAggregateComparison$/^rows_100$'` and then
`-bench '^BenchmarkAggregateComparisonManual$/^rows_100$'` with separate output
files and the same profile commands. Remove your temporary profile directory
after inspection.

With `TIDBGO_TEST_DSN` configured for the dedicated database described below:

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterTiFlash$' -count=1 -v
```

This opt-in test creates only its owned `tidbgo_it_tiflash_aggregates` table,
seeds 20,000 rows with nullable DECIMAL values, analyzes statistics, requests two
TiFlash replicas, and waits for initial `AVAILABLE=1`. An existing table is
never removed. The test cleans up its own table, including on failure.

The fixed `aggregate_v1` dataset covers a broad scan, a 20-row primary-key
range, a selective secondary index, and 20,000 output groups. Auto, TiKV, and
TiFlash MPP are compared using hand-written SQL and the aggregate builder.
Both implementations warm up twice; three rounds rotate engine and method
order. Full result values, NULLs, and ordering must match. Latency ends after
row closure; the immediate same-session RU probe is outside that interval.
Warnings and runtime plans come from separate explicit plan executions. There
is no universal latency/RU threshold. Free-plan limits, caches, statistics,
network conditions, and shared service load can affect measurements.

Each workload also runs public `Compare` with two warmups and five measured
rounds. It checks complete result coverage and exports all three variants into
the existing baseline analyzer. The missing-replica case checks incomplete
comparison status and retained warnings. Offline comparison tests additionally
cover changed values/order, frozen Valuers, float tolerances, driver numeric
types, row limits, cancellation, partial reports, and capture writer errors.

The test also checks missing-replica warnings, empty inputs, nullable outputs,
all seven aggregate functions, HAVING/paging, source soft deletion, physical
table resolution, and restoration of MPP session settings. Ordinary and
EXPLAIN ANALYZE executions are distinct observations. These samples are not
billed RU or a guarantee that TiFlash is faster or cheaper. Replica setup,
storage, seeding, warm-up, probes, and cleanup add costs outside reported
SELECT measurements. Do not run connected suites concurrently on this database.

The calendar integration test uses the same dedicated-database guards:

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterPeriodAggregates$' -count=1 -v
```

It creates and cleans up its owned `tidbgo_it_period_aggregates` table with
20,000 rows and two TiFlash replicas. It checks daily/monthly results against
independently computed Go keys across UTC/JST session and driver locations,
both interpolation settings, NULLs, year/month/leap-day boundaries, alias
collisions, HAVING/paging, `parseTime=false`, and empty results. TIMESTAMP and
DATETIME are checked separately.

The four workloads cover a one-week timestamp range, all non-NULL days, all
months, and date/store groups. Original-column range predicates keep the
input filter separate from calendar extraction. `DATE` and
`EXTRACT(YEAR_MONTH ...)` are compared with equivalent `DATE_FORMAT` plus casts
using two warmups and five rounds with rotating method order. Both alternatives
use typed raw scanning, compare every result, and read immediate same-session
ServerRU. Each workload also runs public `Compare`; its plans and storage
aggregation operators are separate observations from ordinary execution.
These workloads do not establish a universal latency or RU advantage.

The conditional aggregate integration test also uses the dedicated-database
guards and requires explicit TiFlash opt-in:

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterConditionalAggregates$' -count=1 -v
```

It creates and cleans up its owned `tidbgo_it_conditional_aggregates` table,
seeds 20,000 rows, analyzes statistics, and requests two TiFlash replicas. It
checks TRUE/FALSE/NULL conditions, empty and unmatched groups, exact decimals,
soft deletion, escaped LIKE, alias collisions, HAVING/ordering, daily/monthly
paging, and both interpolation settings against explicit expected values.

Four workloads cover combined metrics, daily groups, monthly groups, and rare
matches with a status index. Each compares independent hand-written CASE, IF,
and filtered SQL through the same typed raw collector. Filtered SQL uses one
query for all input counts and one for matching counts/sums, merging missing
groups as zero counts and NULL sums. The rare-match workload requests only
filtered metrics, so its alternative is a single WHERE query on the indexed
status column.

Each method warms up twice and measures five rounds with rotating method order
for auto, TiKV, and TiFlash MPP requests. Latency sums SELECT/row-close time and
any result merging; it excludes the immediate same-session RU probes. ServerRU
is summed across the statements needed for one result. Full result values and
ordering must match. Public `Compare` runs separately for every workload and
records its own plans and storage aggregation operators. These results do not
guarantee that combining metrics or using TiFlash reduces latency or RU.

The relation aggregate test also requires the dedicated test database and opt-in:

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterAggregateRelations$' -count=1 -v
```

It owns and cleans up `tidbgo_it_aggregate_relation_nodes` (10,000 source rows
and 20,000 related rows) and `tidbgo_it_aggregate_relation_edges` (20,000 edges).
It analyzes statistics and requests two TiFlash replicas per table. Explicit
contracts cover repeated matches, NULL/missing keys, source/target/edge soft
deletion, nested and negated conditions, `Or`, empty and all-NULL aggregates,
calendar keys, conditional metrics, HAVING, paging, and both interpolation modes.

Broad monthly, selective, many-group, and `via` workloads compare independently
written hinted EXISTS, plain EXISTS, and JOIN against distinct matching keys.
All use typed Raw, identical result types, and exact result/order checks. Each
method warms up twice and measures five samples in rotating order for auto,
TiKV, and TiFlash MPP. Latency includes Raw construction, SELECT, scanning, and
row closure; immediate same-session RU probes are excluded. Public `Compare`
runs separately to verify results, related-table plan bindings, engine requests,
and warnings. Neither a hint nor a rewritten JOIN guarantees a faster plan.

## Local checks

Run the complete offline verification from the repository root:

```sh
go -C tools tool goimports -w ..
go test ./...
go -C cmd/tidbgo test ./...
go -C integration test ./...
go vet ./...
go -C cmd/tidbgo vet ./...
go -C integration vet ./...
go build ./...
go -C cmd/tidbgo build .
go -C integration build ./...
```

The root test command does not enter the nested `cmd/tidbgo` or `integration` modules

## CLI development

Run the current command directly from the checkout:

```sh
go -C cmd/tidbgo run . version
go -C cmd/tidbgo run . lint ../../examples/starter-app
```

Set a release version through the Go linker when building a release artifact:

```sh
go -C cmd/tidbgo build -ldflags "-X main.version=v0.1.0" .
```

## Package boundaries

- `model`: cached offline metadata for application-owned Go structs
- `orm`: offline query, aggregate, and mutation building, explicit `database/sql`
  execution, relation loading, and typed raw-result scanning
- `schema`: immutable offline catalog parsed from TiDB CREATE TABLE snapshots
- `check`: shared diagnostic data types and offline model and physical schema
  checks
- `migrate`: standalone deployment runner, offline SQL file validation, and
  current-database SQL snapshots using caller-owned connections
- `cmd/tidbgo`: independent CLI module containing the MySQL driver and CLI framework
- `internal`: non-public compiler, analysis, logging, and redaction support
- `examples`: runnable public API examples
- `integration`: independent module for actual TiDB Cloud Starter verification

The `cmd/tidbgo` and `integration` modules use the current root checkout through
local module replacements. Both depend on
[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql); the CLI framework
also belongs to the CLI module. The root library module has no third-party
dependencies, and `orm` and `migrate` do not select a driver for applications.

Build or install the current CLI from a checkout.
[Versioned Go installation](https://go.dev/ref/mod#go-install) does not permit
local replacements. Before publishing a versioned CLI module, publish a root
module with the required APIs, require that version, remove the CLI local
replacement, and use a `cmd/tidbgo/vX.Y.Z` tag.

## Source analysis benchmark

Measure recursive collection, Go parsing, model indexing, query-flow analysis,
and diagnostic construction for files containing 100 local queries:

```sh
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredLocalQueries$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedIndexPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedRelationTopNPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedManyToManyRelationTopNPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathSchemaModels$' -benchmem -count=5
```

The benchmark is offline and does not load packages, run application code,
open a database connection, or consume RU. Temporary fixture creation occurs
before timing

The second workload exercises constant pagination, ordering, nested predicate
inspection, source locations, deduplication, and query-pattern diagnostics
The third adds pre-parsed schema metadata, physical model-name resolution, and
the shared index-prefix checker for 100 ordered-limit queries
The fourth resolves direct relation metadata, applies the shared relation-first
TopN compiler decision, and checks 100 association index accesses
The fifth resolves pure many-to-many relation and junction metadata, applies
the same compiler decision, and checks 100 junction index accesses

The schema-model workload checks 100 explicit model declarations with matching
snapshots, removed columns, or unsupported embedded fields. Snapshot parsing
occurs before timing. Compare it with the shared-model query workloads and
the schema-free local-query workload when changing source compatibility checks.

Reference lint also traverses reachable models and verifies logical key mappings.
Use the relation workloads above to measure that added work. CPU/allocation
profiles can be captured with `-cpuprofile`/`-memprofile` and inspected with
`go -C tools tool pprof`.

The connected reference audit test requires `TIDBGO_TEST_DSN` pointing to the
dedicated test database described above. It checks composite and nullable keys,
self references, both junction endpoints, and physical soft-delete semantics
using isolated fixture tables that it removes afterward:

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterReferenceAudit$' -count=1
```

This test skips when the DSN is absent. Source/CLI tests remain offline, and a
passing offline run does not validate TiDB execution plans or audit RU.

## Via relation compiler verification

Measure warmed offline SQL compilation for a payload-bearing edge with a
declared source-target candidate key. The workloads cover relation-first
List, association-only Count, and a root-predicate fallback:

```sh
go test ./orm -run '^$' -bench '^BenchmarkViaRelationCompiler$' -benchmem -count=5
via_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkViaRelationCompiler$' -benchtime=2s -cpuprofile "$via_profile_dir/cpu" -memprofile "$via_profile_dir/mem" -o "$via_profile_dir/orm.test"
go -C tools tool pprof -top "$via_profile_dir/orm.test" "$via_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$via_profile_dir/orm.test" "$via_profile_dir/mem"
```

These measurements exclude database execution and RU. The rewritten List
has a larger SQL shape than EXISTS, so measure compiler allocation separately
from database savings.

After configuring the dedicated test database as described below, run:

```sh
go -C integration test -run '^TestTiDBCloudStarterVia(Compiler|Preload)$' -count=1 -v ./tidbcloud
```

The compiler fixture has 200 parents, nullable edge keys, a surrogate edge
primary key, a source-target unique key, required payload, and soft-delete
scopes. It compares results, order, and counts with reference EXISTS queries,
checks `SHOW WARNINGS`, and logs `ExplainAnalyze` plus a small alternating
hinted-EXISTS/rewrite RU sample. Latency excludes the immediate same-connection
RU probe. The test creates and removes only its own fixed-name tables and
refuses pre-existing tables. It consumes RU; its small data set and optimizer
statistics do not establish production performance or an RU regression gate.
Keep application measurements and baseline review separate.

## Schema compatibility client benchmarks

Measure CREATE TABLE parsing and one pre-parsed model compatibility check:

```sh
go test ./schema -run '^$' -bench '^BenchmarkParse$' -benchmem -count=5
go test ./check -run '^$' -bench '^BenchmarkSchema$' -benchmem -count=5
```

Both benchmarks are offline. They execute no SQL, open no connection, and
consume no actual RU. `BenchmarkParse` includes lexical and catalog
construction work. `BenchmarkSchema` reuses a parsed catalog and cached model
metadata.

## Query analysis client benchmarks

Measure query-shape compilation, neutral query checks, schema-aware
index-prefix checks, runtime artifact analysis, and ServerRU comparison:

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryShapeIndexDiagnostics$' -benchmem -count=5
go test ./internal/querycheck -run '^$' -bench '^BenchmarkDiagnostics$' -benchmem -count=5
go test ./internal/queryshape -run '^$' -bench '^BenchmarkQueryFingerprint$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeCapturedQueryShapes$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeServerRUOneFingerprint$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeRepeatedWrites$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeMutationIndexes$' -benchmem -count=5
go test ./orm -run '^$' -bench '^BenchmarkConditionalMutationObservation$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkNewServerRUBaseline$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkCompareServerRU$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeWorkload$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkWorkloadBaselineAndComparison$' -benchmem -count=5
```

These benchmarks are offline and exclude SQL execution, network calls, TiDB
optimization, and actual RU consumption. The schema-aware benchmark includes
QueryShape construction and physical index-prefix matching. Fingerprinting is
lazy when evidence is not needed and has its own benchmark. The comparison
benchmark uses the same builder for both diagnostic paths.
The neutral query-check benchmark excludes builder compilation. The runtime
benchmark analyzes 100 captured typed-query records without JSON decoding or
database access. The ServerRU benchmark compares one and 10,000 samples for one
fingerprint so retained bytes and allocation count can be checked independently
of sample count. The baseline benchmark compares one and 10,000 persisted
fingerprint aggregates; its memory must scale with fingerprint count because
the output itself contains one entry per fingerprint. The comparison benchmark
uses matching one- and 10,000-fingerprint baseline/current sets and includes
validation plus deterministic merge, but excludes JSON decoding and report
encoding.

The repeated-write benchmark analyzes 1,000 prebuilt statement records per
iteration. It covers single inserts, upserts with known RU, primary-key
updates, conditional updates with known RU, isolated insert/update scopes,
and excluded bulk splits and soft deletes. Record construction, JSON decoding,
report encoding, and runtime capture are outside the timed region. Compare
allocations as well as analysis time; aggregation retains one counter set per
distinct write group, not every attempt or RU sample.

The mutation-index benchmark compares one and 1,000 records of one SQL
fingerprint against a pre-parsed schema. Index checks are cached per
fingerprint while coverage counts each record. The conditional-observation
benchmark compares UpdateWhere and DeleteWhere with no observer, an ordinary
observer, and runtime capture. Capture includes scalar metadata construction,
JSON encoding, and a discard writer, but no driver conversion or database I/O.

The workload benchmark runs identical prebuilt UPDATE records with scoped
budget aggregation disabled and enabled. It covers one statement, 10,000
statements in one scope, and 1,000 scopes of ten statements. Scope counters
scale with distinct scopes, not attempts within a scope. The workload baseline
and comparison benchmark includes validation for five ten-statement scopes.
Neither benchmark measures JSON, runtime observation, or actual database RU.

## TiDB Cloud Starter integration tests

The connected suite is opt-in. Without `TIDBGO_TEST_DSN`, its connected tests
are skipped while the driver and test harness still compile

Use an empty dedicated database whose lowercase name starts with
`tidbgo_test_` and supply a TLS DSN:

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -count=1 ./tidbcloud
```

The suite verifies that the endpoint identifies itself as TiDB. The environment
owner remains responsible for supplying a Starter endpoint. Follow the
[TiDB Cloud Starter connection requirements](https://docs.pingcap.com/tidbcloud/connect-to-tidb-cluster-serverless/?plan=starter)

The fixture uses `DATETIME(6)` fields scanned into `time.Time`, so
`parseTime=true` is required. The suite also verifies that the same scan fails
with `parseTime=false`. The current short-lived parameterized-query workload
uses `interpolateParams=true`. Without interpolation, the driver prepares,
executes, and closes a statement for each call. An explicitly prepared and
reused statement is a different workload. The driver documents that
interpolation must not be combined with BIG5, CP932, GB2312, GBK, or SJIS
because of SQL injection risk. Keep the connection character set at `utf8mb4`
for this suite. See the driver's
[`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams)

The suite limits the connection pool to one connection. It covers scalar
terminals, slice predicates, an application-selected DECIMAL type, temporal
fields, relation predicates and preloads, CRUD, bulk insert and upsert,
`AUTO_RANDOM`, typed raw SQL, soft deletion, restore, transaction commit and
rollback paths, typed SELECT EXPLAIN and EXPLAIN ANALYZE, and same-session
ServerRU reads, plus statement observation spanning root and preload SELECTs

The connected tests create fixed `tidbgo_it_*` fixture tables and drop only
tables created by the current run. A pre-existing fixture table causes a
failure and is not removed.
Do not run multiple suites concurrently against the same database

The argument tests compare typed mutations, raw SQL, and direct `database/sql`
execution with UTC/JST inputs, UTC/JST driver locations, both interpolation
modes, and UTC/JST session time zones. They check `DATETIME(6)` and
`TIMESTAMP(6)` writes, updates, and range predicates, including adjacent
microseconds, nullable pointers, an application-defined wall-clock Valuer,
and strings containing quotes, backslashes, NUL, and Unicode. They also read
stored time representations with the session set to UTC to detect differences
that a round trip through the same connection can hide.
The test sets `parseTime=true` and disables driver `timeTruncate` for its own
connections. To run only these cases with `TIDBGO_TEST_DSN` configured:

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterArguments$' -count=1 -v
```

## Partial-result scanning

Compare `All` followed by a conversion loop, `ScanAll`, and a benchmark-only
generic collector through the same offline `database/sql` driver and SQL:

```sh
go test ./orm -run '^$' -bench '^BenchmarkScanAll$' -benchmem -benchtime=100ms -count=3
```

The workloads include scalar IDs, small structs, nullable/Scanner fields, and
full-width results at 0, 1, 100, and 10,000 rows. The full-width `all_map` case
uses `All` directly because its result already has the desired type. The generic
alternative shares source compilation and diagnostics but omits destination
pointer validation; it is not a public API. These measurements cover client
time and allocation, not RU or network cost. Capture CPU and allocation
profiles for the `rows_10000/dto/all_map` and `rows_10000/dto/scan_all` subcases
with `-cpuprofile` and `-memprofile`; inspect them using `go -C tools tool pprof`.

With the dedicated test DSN configured, verify actual TiDB results, source
SQL, pagination, relation conditions, soft-delete NULLs, and ServerRU capture:

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterScanAll$' -count=1 -v
```

This creates and removes only its own `tidbgo_it_projection_*` fixture tables
after validating the test database. Pre-existing fixture tables cause failure
and are left untouched.

## Ordered list SQL comparison

After configuring the dedicated test database above, explicitly enable the
comparison:

```sh
TIDBGO_TEST_ORDERED_LIST=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterOrderedListSQLShapes$' -count=1 -v
```

The test creates 12,000 links and 600 targets in its own tables, refuses
pre-existing tables, and removes only the tables it created. It compares the
default SELECT, `FORCE INDEX`, and a derived page of IDs through `Raw[T]`,
plus the `Query` / `ForceIndex` / `Preload` / `All` compiler path. Cases include
first pages of 10, 50, and 100 rows, the second, middle, last, and beyond-end
pages, ascending order, large LIMIT,
few or no matches, and an unindexed filter. The final case drops the ordered
index from the newly created fixture, checks that forcing it returns a database
error, and compares unhinted execution without it.

It checks fixture-derived IDs, ordering, values, and missing or deleted
targets, then compares all variants. After one warmup per variant, three
samples rotate execution order and read ServerRU immediately on the same
pinned connection. Logs include samples, medians, runtime plans, and hint
warning checks. Timings include client scanning and, for the compiler variant,
flattening hydrated results for comparison. They exclude the RU probe;
setup, cleanup, and EXPLAIN are outside the reported SELECT RU.
These timings do not isolate ORM overhead.

This is an opt-in experiment, not an RU regression gate. It does not reproduce
application statistics or guarantee a particular optimizer decision. No
universal RU improvement should be inferred from these results alone.

To isolate explicit index hints from SQL shape, statistics, and index
coverage, run the separate plan experiment:

```sh
TIDBGO_TEST_ORDERED_LIST=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterIndexPlans$' -count=1 -v
```

It requires verified TLS and a dedicated test database. It creates and removes
the same two owned fixture tables, so do not run both experiments concurrently.
It compares identical ORM queries with no hint and an explicit hint,
retaining the raw SQL variants to expose projection or query-shape
differences. An explicit `PRIMARY` hint provides a table-scan alternative.
The phases run after seeding, after `ANALYZE TABLE ... ALL COLUMNS`, and after
adding a covering index and analyzing again. The initial phase does not assume
statistics are absent; their observed state is logged.

Each first-page, last-page, large-limit, and few-matches case has one warmup and
seven measured rounds with rotating execution order. JSON observation logs
retain SQL, synthetic fixture arguments, individual
ServerRU/latency samples, and separate `EXPLAIN ANALYZE` executions with execution
details. Large-limit and last-page cases repeat EXPLAIN seven times to reveal
variations in processed keys, join batches, and the RU reported by the top
operator. This RU is distinct from the measured SELECT's session-local RU.
Session settings and table statistics are also logged when permissions allow;
denied metadata reads are recorded as unavailable. The explicit and unhinted
ORM SQL must differ only by the hint, with identical arguments, and all variants
must return identical fixture results. No plan shape or RU threshold is asserted.
ANALYZE warnings are logged; completing ANALYZE does not prove that every
subsequent plan has loaded complete statistics. The same timing boundaries as
the preceding experiment apply; EXPLAIN is a
separate execution and might differ from an earlier measured execution.

Compare offline compilation of hinted lists across page sizes and offsets,
and scalar queries, separately from database savings:

```sh
go test ./orm -run '^$' -bench '^BenchmarkOrderedListCompiler$' -benchmem -benchtime=200ms -count=3
ordered_list_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkOrderedListCompiler/first_50$' -benchtime=1s -cpuprofile "$ordered_list_profile_dir/cpu" -memprofile "$ordered_list_profile_dir/mem" -o "$ordered_list_profile_dir/orm.test"
go -C tools tool pprof -top "$ordered_list_profile_dir/orm.test" "$ordered_list_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$ordered_list_profile_dir/orm.test" "$ordered_list_profile_dir/mem"
rm -rf "$ordered_list_profile_dir"
```

The benchmark reuses model metadata and measures `Build` without a driver,
network access, or RU. Compare CPU and allocation profiles before and after
compiler changes; a longer SQL template need not mean more allocated bytes.

## Write compiler benchmarks

Measure single-row CRUD, selected-field updates, and value/pointer bulk writes:

```sh
go test ./orm -run '^$' -bench '^BenchmarkMutationWrite$' -benchmem -benchtime=200ms -count=5
go test ./orm -run '^$' -bench '^BenchmarkMutationWrite$/^upsert_values$/^rows_24580$' -benchtime=3s -cpuprofile /tmp/tidbgo-write.cpu -memprofile /tmp/tidbgo-write.mem -o /tmp/tidbgo-write.test
go -C tools tool pprof -top /tmp/tidbgo-write.test /tmp/tidbgo-write.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-write.test /tmp/tidbgo-write.mem
```

The offline workload includes native scalars, nullable pointers, byte slices,
time values, and a pointer-receiver `driver.Valuer`. It covers 100 rows and
24,580 rows: three full eight-column batches followed by a seven-row remainder.
It creates a builder for each operation, uses warmed model metadata, and never
calls `Value` or a database. It measures compiler and argument preparation cost,
not driver conversion, network latency, or RU.

The mutation plan caches field access and Valuer receiver selection, plus one
default single-row upsert SQL per model. Bulk execution reuses equal-sized batch
SQL within that execution; it retains no global cache keyed by batch size or
selected fields. Each batch has its own argument slice.

## Row-specific UPDATE verification and benchmarks

The bulk UPDATE correctness test covers nullable values, JSON, application-selected
DECIMAL values, composite and high-bit unsigned primary keys, soft deletion,
restore, unique-key errors, missing rows, and transaction commit and rollback.
It tests both settings of `interpolateParams` and `clientFoundRows` and removes
only its three newly created `tidbgo_it_update_many*` tables:

```sh
# Set TIDBGO_TEST_DSN to the dedicated database described above.
go -C integration test -run '^TestTiDBCloudStarterUpdateMany$' -count=1 -v ./tidbcloud
```

Compare client-side compiler costs without a database, then profile the same
input and selected fields through individual `Update` calls and `UpdateMany`:

```sh
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$' -benchmem -benchtime=200ms -count=5
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$/^rows_1000$/^selected_true$/^loop$' -benchtime=3s -cpuprofile /tmp/tidbgo-update-loop.cpu -memprofile /tmp/tidbgo-update-loop.mem -o /tmp/tidbgo-update-loop.test
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$/^rows_1000$/^selected_true$/^values$' -benchtime=3s -cpuprofile /tmp/tidbgo-update-many.cpu -memprofile /tmp/tidbgo-update-many.mem -o /tmp/tidbgo-update-many.test
go -C tools tool pprof -top /tmp/tidbgo-update-loop.test /tmp/tidbgo-update-loop.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-update-loop.test /tmp/tidbgo-update-loop.mem
go -C tools tool pprof -top /tmp/tidbgo-update-many.test /tmp/tidbgo-update-many.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-update-many.test /tmp/tidbgo-update-many.mem
```

The offline workload uses warmed metadata, native scalars, pointers, byte
slices, time values, and a custom Valuer that is never executed. It covers
selected and all writable fields, value and pointer slices, and automatic
splits. It measures compilation and argument preparation, not network or RU.
CASE statements repeat key arguments, so fewer statements or allocations do
not guarantee fewer allocated bytes for every projection.

The connected comparison uses a newly created `tidbgo_it_update_shapes` table
with 1,000 rows, refusing any pre-existing table. It compares Update loops,
`UpdateMany`, and precompiled derived-table JOIN alternatives, including a
hinted JOIN. Each sample updates 25, 100, or 500 rows in a transaction and rolls
back afterward. Use fixed iteration counts to bound its cost:

```sh
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterUpdateMany$' -benchmem -benchtime=3x -count=3 ./tidbcloud
# SQL-only shape comparison, one warm-up and three samples per case:
go -C integration test -run '^TestTiDBCloudStarterUpdateManySQLShapes$' -count=1 -v ./tidbcloud
```

`ns/op` times DML only. Setup, BEGIN, ROLLBACK, result verification, and the
three separate same-session RU samples are excluded. `DML-ServerRU/op` is the
sum of captured UPDATE RU, not billed RU or a committed transaction's total
cost. `DML-statements/op` excludes transaction controls and RU probes. The
fixture has no updated secondary indexes; remeasure the actual application's
indexes, values, concurrency, batching, and commit path before generalizing.
No test asserts a universal speed or RU threshold.

## Connected write baseline

Use the dedicated database described above and a fixed iteration count:

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterWrite$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

This opt-in benchmark creates only `tidbgo_it_write_benchmark`, refuses an
existing table, and drops the table it created during cleanup. It uses one
pinned connection and preserves the DSN's `interpolateParams` and
`clientFoundRows` settings. Compare runs with identical driver settings.

The matrix covers single inserts, new/changed/unchanged upserts, 100-row
inserts, and mixed/changed/unchanged bulk upserts, with 32-byte and 2,048-byte
JSON string payloads. The table has an `AUTO_RANDOM` primary key and a separate
unique key. Every trial resets rows and seeds the same conflicts outside the
timer, so repeated upserts do not silently turn into a different workload.
Warm-up and RU samples verify final values, affected rows, existing IDs, and
the generated-ID assignment contract.

Latency and Go allocations exclude setup, validation, and RU queries. Three
separate post-timing samples report `ServerRU/op` and `ServerRU/row`; each uses
automatic collection immediately after the target statement on the pinned
connection. `statements/op` counts target DML only. Setup, seeding, validation,
RU collection, and cleanup consume additional resources outside these metrics.
These are autocommit DML measurements, not explicit-transaction totals or
billing RU. Keep iteration counts bounded because every trial writes real data.

## Write batch-size comparison

Compare 100-, 500-, and 1,000-row batches for the same 1,000 input rows:

```sh
# Set TIDBGO_TEST_DSN to the dedicated database described above.
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterWriteBatchSizes$' -benchmem -benchtime=1x -count=1 ./tidbcloud
# Repeat a narrower comparison after the initial matrix.
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterWriteBatchSizes$/^payload_2048$/^autocommit$/^upsert_changed$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

The 60 cases cover 32-byte and 2,048-byte JSON string payloads, inserts,
new/mixed/changed/unchanged upserts, and two transaction boundaries. In
`autocommit`, each DML statement commits independently. In `transaction`, all
batches use one `orm.Transaction` on the pinned connection; latency and Go
allocations include BEGIN and COMMIT. The benchmark requires session autocommit
to be enabled and records the existing TiDB transaction mode without changing it.
Compare batch sizes within the same mode; the modes have different atomicity.

Each case starts with the same values and conflicts. The mixed case seeds the
first half of the input, and the changed case changes one integer field.
Results and existing IDs are verified after commit. With six writable columns,
all candidate batches fit in one statement, so `batch_1000` has the same DML
shape as the current automatic policy for this workload. The comparison uses
the public mutation API to slice inputs; it does not change the ORM's policy.
The three sizes are measurement candidates, not recommended defaults or public
batch-size options. Automatic `Exec` batching still uses the placeholder budget.

`DML-ServerRU/op` sums the per-statement ServerRU over all batches for one
1,000-row operation, then averages three independently reset samples.
`DML-ServerRU/row` divides that sum by input rows. RU probes run immediately
after each DML statement on the same connection or active transaction, outside
latency and allocation measurement. These metrics exclude BEGIN/COMMIT RU,
setup, seeding, verification, probes, and cleanup; **they cannot compare total
transaction RU against autocommit RU or represent billed RU**.

`statements/op` counts target DML (10, 2, or 1); `tx-controls/op` counts explicit
BEGIN/COMMIT (0 or 2). Neither counts all driver/network round trips.
`max-args/statement` and `max-SQL-bytes/statement` describe the largest bind list
and placeholder SQL template, not interpolated packet size or peak memory.
`B/op` measures total Go allocation, not retained or peak heap. Connection
setup, source data, and verification are excluded from it.

The full `1x` matrix executes 300,000 target input rows across warm-up, timing,
and RU samples, plus reset and seed writes. Use filters and fixed iteration
counts to bound resource use. It uses the same disposable table as the write
baseline, so do not run them concurrently against one database. Results from
this fixed-size, single-client workload do not establish an optimal size for
larger rows, placeholder-limit batches, or concurrent writers.

The same batching loop can be profiled entirely offline:

```sh
go -C integration test -run '^$' -bench '^BenchmarkWriteBatchCompiler$' -benchmem -benchtime=200ms -count=5 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkWriteBatchCompiler$/^payload_2048$/^upsert_true$/^batch_1000$' -benchtime=3s -cpuprofile /tmp/tidbgo-batch.cpu -memprofile /tmp/tidbgo-batch.mem -o /tmp/tidbgo-batch.test ./tidbcloud
go -C tools tool pprof -top /tmp/tidbgo-batch.test /tmp/tidbgo-batch.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-batch.test /tmp/tidbgo-batch.mem
```

Replace `batch_1000` with `batch_100` to profile the smaller-batch candidate.
The offline executor does not connect to TiDB, convert driver arguments, or
retain them. Data and metadata are prepared before timing; payload length does
not measure transmission or JSON processing in this benchmark.

## Relation synchronization comparison

See [Relation synchronization benchmarks](relation-sync-benchmarks.md) for the connected replacement, set-based, and read-diff comparison, identity and locking assumptions, measurement boundaries, and offline profiles

## EXPLAIN client benchmark

Measure the client-side cost of compiling one typed SELECT and scanning a
three-operator TiDB row-format plan:

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplain$' -benchmem -count=5
```

This benchmark uses a local `database/sql` test driver. It excludes the MySQL
driver, network round trip, TiDB optimization, and actual RU consumption

## EXPLAIN ANALYZE client benchmark

Measure the client-side cost of compiling typed SELECTs, resolving plan access
metadata, and scanning TiDB runtime plans:

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplainAnalyze($|RelationAliases$)' -benchmem -count=5
```

The first workload scans three physical-table operators. The relation workload
scans four operators and resolves root, direct relation, many-to-many junction,
and target aliases. Both use a local `database/sql` test driver, measure neither
the SELECT execution nor TiDB runtime cost, and consume no actual RU

Measure the cost of diagnosing already returned plans separately:

```sh
go test ./orm -run '^$' -bench '^BenchmarkExplainAnalyzePlanDiagnostics' -benchmem -count=5
```

The clean case has no diagnostics, the warning case emits incomplete
statistics, large full-scan, and disk-use evidence, and the resolved-access
case includes physical table, model, and relation metadata. No case performs
database I/O or parses timing and RU text

## ServerRU client benchmark

Measure the client-side cost of reading and decoding one ServerRU value:

```sh
go test ./orm -run '^$' -bench '^BenchmarkLastServerRU$' -benchmem -count=5
```

Measure automatic connection pinning, one target `RawExec`, the auxiliary
query, decoding, and either ordinary observer or runtime-capture delivery:

```sh
go test ./orm -run '^$' -bench '^BenchmarkRawExecWith(ServerRUCollection|RuntimeCaptureAndServerRU)$' -benchmem -count=5
```

These benchmarks use a local `database/sql` test driver. They include the
relevant client paths but exclude the MySQL driver, network round trip, TiDB
execution, and actual RU consumption

## Driver transport benchmark

Compare both `interpolateParams` modes against the same Starter point query:

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterInterpolateParams$' \
    -benchmem -benchtime=20x -count=5 ./tidbcloud
```

The benchmark derives both modes without printing the DSN, uses one connection
per mode, and reports latency, Go allocations, and five post-timing samples of
`@@tidb_last_query_info.ru_consumption`

Results include network and Starter variability and are not portable
performance guarantees or billed-RU measurements

## Relation graph benchmark

Measure client-side work separately, without a database:

```sh
go test ./orm -run '^$' -bench '^(BenchmarkSelectQueryBuildPreload.*|BenchmarkSelectQueryPreloadRelationGraphThreeStatements|BenchmarkSelectQueryPreloadHasMany100Parents300Children|BenchmarkSelectQueryPreloadManyToMany100Parents300Targets|BenchmarkSelectQueryPreloadNested100Parents300Children|BenchmarkViaPreload100Parents300Targets)$' -benchmem -count=5
preload_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryBuildPreloadRelationGraph$' -benchtime=2s -cpuprofile "$preload_profile_dir/build.cpu" -memprofile "$preload_profile_dir/build.mem" -o "$preload_profile_dir/build.test"
go test ./orm -run '^$' -bench '^BenchmarkViaPreload100Parents300Targets$/^via$' -benchtime=2s -cpuprofile "$preload_profile_dir/via.cpu" -memprofile "$preload_profile_dir/via.mem" -o "$preload_profile_dir/via.test"
go -C tools tool pprof -top "$preload_profile_dir/build.test" "$preload_profile_dir/build.cpu"
go -C tools tool pprof -top -alloc_space "$preload_profile_dir/build.test" "$preload_profile_dir/build.mem"
go -C tools tool pprof -top "$preload_profile_dir/via.test" "$preload_profile_dir/via.cpu"
go -C tools tool pprof -top -alloc_space "$preload_profile_dir/via.test" "$preload_profile_dir/via.mem"
```

`Build` workloads measure repeated offline plan and SQL construction.
Execution workloads use a local `database/sql` test driver and include result
decoding and relation hydration. They do not measure MySQL-driver work,
network latency, or TiDB RU. Compare equivalent SQL, statement counts, and
results before interpreting allocation or timing differences. Cached default
target scan plans are reused only when projection order also matches; query
aliases, scopes, and result slices remain independent.

### Via preload cost isolation

Compare value-target via loading with explicit edge loading and target extraction:

```sh
go test ./orm -run '^TestViaCostFixtureResults$|^TestManyToManyReusableScan' -count=1
go test ./orm -run '^$' -bench '^(BenchmarkViaPreloadCost|BenchmarkViaPreloadPointerFallback|BenchmarkSelectQueryPreloadNested100Parents300Children)$' -benchmem -benchtime=200ms -count=5
via_cost_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkViaPreloadCost$/^shared$/^via_true$' -benchtime=2s -cpuprofile "$via_cost_profile_dir/cpu" -memprofile "$via_cost_profile_dir/mem" -o "$via_cost_profile_dir/orm.test"
go -C tools tool pprof -top "$via_cost_profile_dir/orm.test" "$via_cost_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$via_cost_profile_dir/orm.test" "$via_cost_profile_dir/mem"
```

The offline matrix includes empty and single-edge inputs, 20 parents with 100
edges sharing 12 targets, a narrow projection, distinct targets, and 2,000 edges.
It includes column decoding and final target extraction, but not MySQL-driver
or network work. The pointer and nested workloads cover paths that cannot reuse
the same scan target. Value collections with direct fields and no inline target
relations bind scan destinations once per batch; pointer collections and embedded
field paths retain per-row binding. Result ownership and generated SQL are unchanged.

For a connected diagnostic, first configure the dedicated test database above:

```sh
TIDBGO_TEST_VIA_COST=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterViaCost$' -count=1 -v
```

This opt-in test creates and removes only its own `tidbgo_it_cost_*` fixtures,
rejecting pre-existing tables. It compares three-statement edge and via queries
with identical final results, and separately drains each variant's generated SQL
through `database/sql`. Raw draining validates row counts but does not build the
ORM result graph, so it is a SQL/driver diagnostic, not an equivalent repository
implementation. After two warm-ups, six measured rounds alternate the order of
four variants. Unobserved total latency, separate observed per-statement timings,
three per-operation DML ServerRU samples, and representative relation plans are
reported separately. RU probes, validation, setup and EXPLAIN ANALYZE are outside
the unobserved latency interval. No latency or RU threshold is asserted.

The neutral fixture is a reproduction aid, not a production-data replica. Keep
raw samples and compare both favorable and unfavorable inputs before adopting a
change; neither an isolated plan nor an allocation reduction proves lower
end-to-end latency. Do not subtract separately measured raw and ORM medians to
claim an exact ORM overhead.

### Connected relation graph

Measure the representative relation graph on the same dedicated database:

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterPreloadRelationGraph$' \
    -benchmem -benchtime=5x -count=5 ./tidbcloud
```

The benchmark verifies exactly three application statements per operation: a
parent SELECT with five inline to-one joins, one many-to-many batch with its
nested to-one join, and one has-many batch with its nested to-one join

It uses one pinned connection and reports elapsed time, Go allocations, and
sampled statement RU summed per operation. Setup, RU-sampling queries, and
cleanup are outside the timed and statement-counted operation

## Window, vector, and preparation checks

```sh
go test ./orm ./schema ./tiflash ./vector ./internal/sourcecheck ./examples/starter-app
go test ./orm -run '^$' -bench '^(BenchmarkAggregateWindow|BenchmarkAggregateRelatedBuild|BenchmarkVectorSearchBuild)$' -benchmem -count=3
go test ./vector -run '^$' -bench '^(BenchmarkVectorRoundTrip|BenchmarkVectorDecoderAlternatives)$' -benchmem -count=3
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterTiFlashExtensions$' -count=1 -v
```

The connected extension test requires the same dedicated DSN safeguards as the
other Starter tests. It owns and removes `tidbgo_it_tiflash_extensions`, seeds
5,000 rows with missing/deleted relations, requests two replicas, and creates an
L2 vector index. It verifies capability probes and replica waiting; ordinary
queries/preloads; related grouping and conditional EXISTS metrics; grouped
ROW_NUMBER/LAG/running SUM against a manual JOIN; prepared/interpolated parameters;
exact vector results against a TiKV reference; ANN plan selection and prefilter
fallback; nullable cosine distance and atomic dimension-error handling; and actual
SHOW CREATE TABLE vector metadata. It retains server warnings instead of claiming
that every window operator supports MPP. Logged single samples are observations,
not repeatable performance guarantees or recall benchmarks.

The fake-driver window benchmark covers 0, 1, 100, and 10,000 result groups using
identical builder/Raw/database/sql results. Vector decoder alternatives cover
3, 768, and 16,383 dimensions and compare bounded typed-array parsing with token
parsing. These isolate client CPU/allocation; they do not measure ANN quality,
TiFlash indexing, network cost, or production throughput. Use the profile workflow
above with these benchmark names. Test large embeddings and representative
filters on application data before choosing approximate search.

## Warning verification

```sh
go test ./orm ./internal/warningcheck ./internal/runtimecapture
go -C cmd/tidbgo test ./...
go test ./orm -run '^$' -bench '^BenchmarkWarningCollection$' -benchmem -benchtime=100ms -count=3
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterWarningState$' -count=1 -v
```

The connected test needs the dedicated `TIDBGO_TEST_DSN` described above. It
creates and removes only its owned `tidbgo_it_warning_state` table. It checks
warning/RU interference, observed SELECT and mutation warnings, safe capture
analysis, and rotated small-query latency with collection off/on. The existing
TiFlash extension test also checks window-plan warning delivery and ordinary
SELECT warning coverage. The offline benchmark compares no collection, opt-in
collection, and manual connection pinning plus SHOW WARNINGS for 1/100 rows and
0/1/1,000 warning rows. It excludes network and TiDB time. Profile its
`warnings_1/rows_100/collect` and `warnings_1000/rows_1/collect` paths with the
profile commands above.
