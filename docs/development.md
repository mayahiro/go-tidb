# Development

[日本語](development_ja.md)

This guide contains contributor-facing commands, repository structure,
integration-test setup, and benchmark procedures for `go-tidb`

## Aggregate and TiFlash verification

The [aggregate API](aggregates.md) has offline contract tests for grouping,
aliases, NULL/conversion errors, destination ownership, hint conflicts,
unknown plan tasks, warning failures, and connection/callback ordering:

```sh
go test ./orm -run '^TestAggregate|^TestPlanTask|^TestScanAll'
go test ./orm -run '^$' -bench '^BenchmarkAggregate$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateComparison(Manual)?$' -benchmem -benchtime=100ms -count=3
```

The benchmark compares identical SQL and result values through the same local
test driver: aggregate `ScanAll`, typed `Raw`, and a direct `database/sql`
collector. It covers 0, 1, 100, and 10,000 output groups. It excludes TiDB,
network, driver argument conversion, and RU; building SQL and validating the
aggregate's output mapping adds per-call work relative to a fixed raw query.

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

## Local checks

Run the complete offline verification from the repository root:

```sh
go -C tools tool goimports -w ..
go test ./...
go -C integration test ./...
go vet ./...
go -C integration vet ./...
go build ./...
go -C integration build ./...
```

The root test command does not enter the nested `integration` module

## CLI development

Run the current command directly from the checkout:

```sh
go run ./cmd/tidbgo version
go run ./cmd/tidbgo lint ./examples/starter-app
```

Set a release version through the Go linker when building a release artifact:

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/tidbgo
```

## Package boundaries

- `model`: cached offline metadata for application-owned Go structs
- `orm`: offline query, aggregate, and mutation building, explicit `database/sql`
  execution, relation loading, and typed raw-result scanning
- `schema`: immutable offline catalog parsed from TiDB CREATE TABLE snapshots
- `check`: shared diagnostic data types and offline model and physical schema
  checks
- `migrate`: reserved boundary for standalone migration tooling
- `cmd/tidbgo`: CLI entry point
- `internal`: non-public compiler, analysis, logging, and redaction support
- `examples`: runnable public API examples
- `integration`: independent module for actual TiDB Cloud Starter verification

The `integration` module owns the
[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql) dependency and
uses the current root checkout through a local module replacement. The root
module and its users do not inherit that test dependency

## Source analysis benchmark

Measure recursive collection, Go parsing, model indexing, query-flow analysis,
and diagnostic construction for files containing 100 local queries:

```sh
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredLocalQueries$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedIndexPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedRelationTopNPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedManyToManyRelationTopNPatterns$' -benchmem -count=5
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
