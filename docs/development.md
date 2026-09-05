# Development

[日本語](development_ja.md)

This guide contains contributor-facing commands, repository structure,
integration-test setup, and benchmark procedures for `go-tidb`

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
- `orm`: offline query and mutation building, explicit `database/sql`
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
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkNewServerRUBaseline$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkCompareServerRU$' -benchmem -count=5
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
iteration. It covers single inserts, upserts with known RU, isolated scopes,
and excluded bulk splits. Record construction, JSON decoding, report encoding,
and runtime capture are outside the timed region. Compare allocations as well
as analysis time; aggregation retains one counter set per distinct write group,
not every attempt or RU sample.

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

It creates 18 fixed `tidbgo_it_*` tables and drops only tables created by the
current run. A pre-existing fixture table causes a failure and is not removed.
Do not run multiple suites concurrently against the same database

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
