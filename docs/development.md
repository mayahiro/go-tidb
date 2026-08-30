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
go run ./examples/starter-app/cmd/check | go run ./cmd/tidbgo check
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
- `check`: shared diagnostic data types, reasoned reports and suppression, and
  offline model, query, and physical schema checks
- `migrate`: reserved boundary for standalone migration tooling
- `cmd/tidbgo`: CLI entry point
- `internal`: non-public logging and redaction support
- `examples`: runnable public API examples
- `integration`: independent module for actual TiDB Cloud Starter verification

The `integration` module owns the
[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql) dependency and
uses the current root checkout through a local module replacement. The root
module and its users do not inherit that test dependency

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
ServerRU reads, plus operation debug reports spanning root and preload SELECTs

It creates 18 fixed `tidbgo_it_*` tables and drops only tables created by the
current run. A pre-existing fixture table causes a failure and is not removed.
Do not run multiple suites concurrently against the same database

## Debug report client benchmark

Measure the client-side cost of grouping two completed statement events:

```sh
go test ./orm -run '^$' -bench '^BenchmarkDebugReportTwoStatements$' -benchmem -count=5
```

This benchmark uses a local mutation executor. It excludes the MySQL driver,
network calls, TiDB execution, and actual RU consumption

## EXPLAIN client benchmark

Measure the client-side cost of compiling one typed SELECT and scanning a
three-operator TiDB row-format plan:

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplain$' -benchmem -count=5
```

This benchmark uses a local `database/sql` test driver. It excludes the MySQL
driver, network round trip, TiDB optimization, and actual RU consumption

## EXPLAIN ANALYZE client benchmark

Measure the client-side cost of compiling one typed SELECT and scanning a
three-operator TiDB runtime plan:

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplainAnalyze$' -benchmem -count=5
```

This benchmark uses a local `database/sql` test driver. It measures neither
the SELECT execution nor TiDB runtime cost and consumes no actual RU

## ServerRU client benchmark

Measure the client-side cost of reading and decoding one ServerRU value:

```sh
go test ./orm -run '^$' -bench '^BenchmarkLastServerRU$' -benchmem -count=5
```

This benchmark uses a local `database/sql` test driver. It includes the
`database/sql` row path and JSON decoding but excludes the MySQL driver,
network round trip, TiDB execution, and actual RU consumption

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
