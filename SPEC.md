# go-tidb Public Product Specification

- Version: 0.1.0 draft
- Last updated: 2026-09-21
- Supported profile: TiDB Cloud Starter

This document defines the public product boundary for `go-tidb`. It describes
what users may rely on in the current implementation. A feature listed as
planned is not available until the README marks it as implemented.

## 1. Product definition

`go-tidb` is a Go toolkit for TiDB Cloud Starter with two independent paths:

- A struct-first application runtime for CRUD, explicit relations, deterministic SQL,
  transactions, historical reads, and opt-in query observations
- Offline development diagnostics and standalone deployment tools for
  migration planning, migration application, and connected schema verification

The implemented runtime uses application-model metadata directly. Future
tooling may share that metadata where it provides a concrete benefit, but
application runtime packages will not depend on migration or code-generation
implementations.

### 1.1 Design priorities

The following product requirements have equal, highest priority:

- Distinct value for TiDB Cloud Starter workloads and operations
- A small, unsurprising API with minimal required declarations
- Very high runtime performance
- Very low CPU, memory, allocation, goroutine, and database-round-trip costs

The default path omits metadata only when it can be inferred deterministically
and unambiguously. The product chooses one clear representation instead of
exposing multiple equivalent ways to express the same behavior. A user choice
is added only when alternatives have materially different consequences that
the library cannot select safely.

New features and abstractions must justify both their API surface and their
runtime and resource costs. Performance and resource claims require
reproducible benchmarks or profiles; they are not inferred from implementation
technique alone.

## 2. Compatibility boundary

Before v1, public APIs, CLI behavior, configuration, and output formats do not
provide backward-compatibility guarantees. Public documentation describes the
currently supported behavior rather than a migration path from prior drafts.

The v0.1 compatibility contract covers TiDB Cloud Starter only. It does not
cover:

- MySQL or MariaDB
- TiDB Cloud Essential, Premium, or Dedicated
- TiDB Self-Managed

Accidental compatibility with an unsupported database or service plan does
not create a compatibility guarantee.

Scalar execution uses an explicitly supplied `database/sql` executor. The
standalone migration CLI uses `go-sql-driver/mysql`; the `orm` and `migrate`
packages do not select a protocol driver or create connections. The project
does not implement a database wire protocol.

## 3. Fixed behavior

The following decisions apply throughout v0.1:

1. Application models are ordinary user-owned Go structs. Model inspection
   requires neither code generation nor a database connection.
2. The current product does not ship a schema DSL or code generator. Any
   future generator must remain optional and provide an explicit benefit after
   the current runtime, diagnostics, and migration work is complete.
3. Relation preloads use a deterministic strategy by relation kind and parent
   query shape. `BelongsTo` and `HasOne` use inline `LEFT JOIN`s. An
   unrestricted `All` loads each root `HasMany` or pure `ManyToMany` source in
   one argument-free secondary SELECT; constrained and nested collections use
   bounded parameter batches. Nested to-one relations are joined into the
   statement that loads their parent. Runtime statistics and result
   cardinality do not switch strategies. The physical schema must make the
   target side of a to-one mapping unique.
4. Lazy loading is not provided.
5. `Update(&model)` writes every writable mapped non-primary-key field, while
   optional Go field names select a partial update. Relation loaded state is
   never used to infer writes.
6. Generated preload SQL never uses `SELECT *`.
7. SQL values use bind parameters. Identifiers come from validated model
   metadata.
8. Application runtime APIs do not create, alter, or drop schema objects.
9. Migrations use explicitly authored, versioned SQL files.
10. Migration SQL runs only through an explicit up/down operation. Missing down
    SQL declares an irreversible version; failed SQL is never reversed automatically.
11. Statement RU reported by TiDB is named `ServerRU`. It is not represented
    as billed RU.
12. `EXPLAIN ANALYZE` is allowed only for `SELECT` and remains opt-in. Plan
    diagnostics inspect only its returned rows and add no database statement.
    Compiler-owned access aliases resolve from query-occurrence metadata, not
    model tags or SQL parsing.
13. Raw SQL is an explicit escape hatch without typed relation hydration or
    static query-AST diagnostics. Returned columns may still use model-aware
    scanning.
14. Offline model and schema checks are selected explicitly by application
    code. They perform no source scan, generated registration, configuration
    discovery, or database access.
15. Go-source lint is an independent opt-in command. It parses production
    source without loading or executing application packages and reports
    uncertainty instead of guessing across an unresolved data-flow boundary.
16. Automatic ServerRU collection is an explicit high-cost observer option.
    Target statements and diagnostic statements, and their durations, remain
    separate. A diagnostic failure never replaces the target result.

### 3.1 Implemented surface

The currently implemented surface provides:

- Cached offline scalar metadata for named Go structs and pointer forms
- Positional `tidbgo` scalar tags, `tidbgo:"-"` ignored fields, snake_case
  defaults, and embedded structs
- Deterministic table names, explicit table overrides, ordered primary keys,
  and primary-key-independent candidate unique-key groups
- TiDB `AUTO_RANDOM` integer primary keys and raw-result-only computed fields
- Direct and many-to-many relation metadata with deterministic key mappings
- Ordinary pointer and slice relation fields without lazy loading
- Detection of native scalar types, `sql.Scanner`, and `driver.Valuer`
- Deterministic validation of invalid or duplicate field mappings
- Offline scalar SELECT construction with explicit projections, predicates,
  ordering, offset pagination, and keyset pagination
- Public `Build` compilation without database access and public `All`, `ScanAll`,
  `First`, `Only`, `Exists`, and `Count` execution through an explicitly supplied
  `*sql.DB`, `*sql.Conn`, or `*sql.Tx`
- `ScanAll` writes one selected column into a scalar slice or selected source
  Go fields into a separate struct slice, with exact Go-name mapping and no
  destination metadata. It preserves source SQL and diagnostics, rejects
  `Preload`, and replaces the destination only after successful row completion
- Nested `BelongsTo` and `HasOne` preloading through inline `LEFT JOIN`s, and
  `HasMany` and pure `ManyToMany` preloading through deterministic full-source
  or bounded keyed secondary SELECTs, with automatic key projection, target
  projection, collection ordering, and no loaded-state fields
- Logical direct and pure `ManyToMany` relation predicates compiled offline as
  `EXISTS`, with TiDB semi-join hints for filtered positive collections and a
  metadata-proven relation-first TopN rewrite for eligible direct `HasMany`
  and pure `ManyToMany` pages, using a complete target primary or declared
  candidate unique key and an outer derived-key-first `LEADING` hint without
  implicit preloading or a forced join algorithm, plus relation-only Count for
  eligible unpaginated collection filters under the same integrity contract
- TiDB-default NULL ordering and primary-key-backed deterministic keyset
  validation
- Typed slice `IN` and `NOT IN` predicates
- Single insert and upsert, automatically placeholder-bounded multi-row insert
  and upsert from value or pointer slices, `AUTO_RANDOM` ID backfill for
  single-row `Insert`, full or selected-field primary-key update,
  predicate-bounded assignment and same-column increment, primary-key or
  predicate delete, and affected-row results
- Pure `ManyToMany` multi-row add, explicit duplicate-preserving add, selected
  remove, and source clear operations with scalar or composite relation keys
- Typed raw partial and computed-result scanning plus explicit raw mutation SQL
- To-one related aggregate fields with declared unique target keys and conditional
  `Has` metrics, plus [windows over grouped outputs](docs/windows.md)
- [Vector values/search](docs/vector-search.md), explicit exact/approximate modes,
  offline index SQL/schema diagnostics, and observed ANN plan evidence
- [Explicit replica preparation and capability probes](docs/tiflash.md), including
  initial-readiness waiting and operator/cardinality summaries
- Conservative aggregate projection/grouping source lint (`AGG001`) with separate
  resolved/uncertain coverage; complete validation remains in `Build`
- Source-model `Aggregate[T]` queries with scalar and relation-existence filters,
  validated fields and aggregate
  expressions including `CountIf` and `SumIf`, `Date` and `YearMonth` calendar
  keys, output-name `GROUP BY`,
  `HAVING` and ordering, soft-delete scope, paging, and scalar/struct `ScanAll` results
- Explicit SELECT, aggregate, and vector `ReadFrom(TiKV/TiFlash)` and `MPP(MPPAuto/MPPEnforce)` hints,
  covering the source and related target/junction table occurrences, with
  statement-scoped settings and no `SET SESSION` or replica provisioning
- Aggregate plan reports separate requested hints, planned operators, explicit
  EXPLAIN ANALYZE execution, and same-session warnings; see
  [aggregate contracts](docs/aggregates.md)
- Explicit aggregate auto/TiKV/TiFlash MPP comparison with frozen arguments,
  full result checks, rotated latency/ServerRU samples, separate runtime plans
  and warnings, and complete measurement-only RuntimeCapture export; see
  [comparison contracts](docs/aggregate-comparison.md)
- Caller-owned `*sql.Tx` execution for queries, preloads, and mutations
- Context-scoped statement observation and an automatic-color logger with
  explicit color overrides for any writer, argument values excluded by default,
  explicit bind-value capture, and explicit same-session ServerRU collection
- Structured runtime capture of completed root, relation, split-bulk, raw, and
  transaction statement events without per-query wrappers, with optional
  ServerRU diagnostic cost kept separate from target cost
- SELECT-only execution-plan inspection using TiDB's default row-format
  `EXPLAIN` output
- Explicit SELECT execution with TiDB's default row-format `EXPLAIN ANALYZE`
  runtime output and conservative diagnostics over the returned plan
- Same-session ServerRU reading for one completed DML statement through a pinned
  `*sql.Conn` or active `*sql.Tx`
- Observer-scoped ServerRU capture that temporarily pins `*sql.DB` per
  recognized DML statement and records one auxiliary query without replacing
  target results on diagnostic failure
- Opt-in ordinary DML warning collection, automatic delivery of already
  collected aggregate/vector plan warnings, and value-free `WRN001` through
  `WRN003` summaries in the logger, RuntimeCapture, and CLI; see
  [warning contracts](docs/warnings.md)
- Immutable offline catalogs parsed from self-contained TiDB CREATE TABLE
  snapshots, including SHOW CREATE TABLE executable comments
- Directional SQL-snapshot and Go-model compatibility checks for mapped tables,
  columns, type families, nullability, primary and candidate unique keys,
  `AUTO_RANDOM`, generated columns, required database-only columns, Relation
  target identity, many-to-many junction pair uniqueness and insert shape, and
  deterministic collection-relation index prefixes
- Shared diagnostic data types and offline model and schema checks
- The `tidbgo version` command, offline `tidbgo analyze` runtime-capture and
  optional SQL-snapshot index command, deterministic versioned
  `tidbgo baseline` ServerRU reference command, and offline `tidbgo lint`
  Go-source command

## 4. Planned runtime surface

The struct-first runtime is planned to provide:

- Read-only `AsOf` and fixed-duration stale snapshot clients

Per-parent preload limits, opaque cursors, lazy loading, connected cost-based
plan probing, and object-graph persistence are outside v0.1.

The `IDs` terminal is deferred until a measured large-ID workload justifies a
dedicated result API and any resulting minimum-Go-version cost.

## 5. Migration surface

Migrations use increasing UTC timestamp versions with millisecond precision
(`YYYYMMDDHHMMSSmmm`), such as:

```text
20260921093000123_create_users.sql
schema.sql
```

Each migration file contains `-- tidbgo:up` and an optional `-- tidbgo:down`
section. Directives occupy their own lines outside SQL quotes and block comments.
Omitting down declares an irreversible change. Both sections must contain SQL
when present. Checksums cover the complete file, including comments and whitespace.
Generated versions must be later than the latest local version; same-millisecond
collisions and clock rollback are rejected without overwriting files.

`tidbgo migrate` provides offline `new` and `lint`, and explicit connected
`init`, `baseline`, `plan`, `status`, `up`, `down`, `dump`, and `repair`.
Application startup and ORM APIs do not run migrations. A caller-owned
`database/sql` pool can also be passed to the deployment-only `migrate.Runner`.
The CLI is an independent module under `cmd/tidbgo` containing the MySQL driver
and CLI framework. The root library module has no third-party dependencies.

`init` reads an existing database into the first migration's up section.
`baseline` compares that SQL with a fresh snapshot, records adoption without
application DDL or DML, and creates or refreshes `schema.sql` from the database.
The adopted version is the lower bound for down. The same initial SQL
can initialize an empty database through up.

Migration application:

1. Hold one dedicated connection
2. Acquire a named advisory lock
3. Validate local SQL, all recorded checksums, and the live structural fingerprint
4. Record a running state
5. Execute statements in source order
6. Record success or interruption and the confirmed statement count
7. Regenerate `schema.sql` from the current database after each completed version
8. Release the advisory lock

Down uses authored reverse SQL and also refreshes `schema.sql`; migration
files and history stay fixed. DDL is not transactionally rolled back. An
interrupted attempt blocks further up/down until an operator inspects the
structure, data, and DDL jobs and explicitly repairs the recorded state using
a matching reviewed snapshot and an audit reason. Database success and
snapshot-output failure are reported separately; dump can retry file output.

The snapshot retains supported table definitions and TiFlash replica settings,
excluding history metadata and allocator counters. It rejects unsupported
objects and cross-database or cyclic foreign keys. Advisory locks serialize
cooperating migration runners; unrelated schema changes must be coordinated.
See [versioned SQL migrations](docs/migrations.md) for connection settings,
limits, file rules, and recovery. Automatic schema diffs and code generation
are not provided.

## 6. Diagnostics

The implemented diagnostic representation has a code, a severity of `info`,
`warning`, or `error`, a human-readable explanation, evidence, a suggestion,
an optional source location, and an optional reference. Offline model checks
use `MOD001` through `MOD007`, offline physical schema compatibility checks use
`CMP001` through `CMP015`, and executed typed-query analysis uses `QRY002`
through `QRY007` without bind values.

The implemented offline model and schema checks cover executable model
metadata, ignored and likely misplaced tags, primary-key and custom-scalar
capabilities, directional model and SQL-snapshot compatibility, Relation target
and junction correctness, and deterministic collection-relation index-prefix
coverage. Application tests call `check.Model` and `check.Schema` directly and
own their pass/fail policy.

`tidbgo analyze` and `tidbgo lint` apply one internal reporting policy: active
errors fail, while warnings and information remain successful. A suppression
matches every suppressible diagnostic with one exact code, requires a non-empty
reason, and remains in the output with that reason. Duplicate codes, unused
suppressions, and attempts to suppress non-suppressible diagnostics are
rejected.

`tidbgo lint` scans one production Go file or a directory recursively and
defaults to the current directory. It follows the current build context and
excludes tests, generated files, vendor, testdata, and hidden directories. The
current suppressible `SRC001` warning recommends a narrower `Select` only when
all uses of a default-projection `All`, `First`, or `Only` result are proven
within the same function. Escaped, aliased, preloaded, method-dependent, and
otherwise unresolved flows produce no projection warning. Text and JSON output
always include recognized, explicitly projected, analyzed, and uncertain
coverage counts. The command executes no application code and performs no
database access.

`ScanAll` participates in query-pattern and schema-aware index analysis. Its
explicit `Select` counts as an explicit projection; default-projection
destination-pointer flows remain uncertain for `SRC001`.

`RuntimeCapture` is an opt-in reusable observer configured once at a request,
job, or test-operation boundary. It records only go-tidb statements using the
derived context and requires no query-specific registration or diagnostic
wrapper. Its versioned JSON Lines artifact excludes bind values and records
typed query or statement fingerprints, model-row SELECT query shapes and
compiler decisions, actual preload and bulk batch positions, statement
duration, row counts, and errors. Pagination values remain excluded while the
shape preserves presence and positive classification. `tidbgo analyze` streams
that artifact without a database connection, applies `QRY002` through `QRY005`
to captured query shapes, and reports possible runtime N+1 query shapes. Its
optional `--schema` input parses a TiDB CREATE TABLE snapshot offline and adds
`QRY006` and `QRY007` index checks without an application-side query registry.
Coverage counts distinguish all statements, query-shape statements, and
schema-checked statements. `CollectServerRU` optionally adds one same-session
diagnostic query for each recognized DML statement. The artifact and analyzer
separate target duration, diagnostic duration, go-tidb statement count,
auxiliary statement count, successful samples, collection errors, and summed
ServerRU. The analyzer emits deterministic per-fingerprint ServerRU aggregates
with captured statement count, successful sample count, error count, total,
mean, minimum, and maximum. It retains one constant-size accumulator per
observed fingerprint rather than individual samples. `tidbgo baseline` streams
the same artifact and writes one versioned, timestamp-free, fingerprint-sorted
ServerRU aggregate JSON object to standard output. It rejects captures with no
successful sample or any collection error and does not yet apply a regression
threshold.

`CollectWarnings` optionally probes ordinary DML warnings on the same connection.
It distinguishes uncollected, known-empty, and failed observations. Explicit
aggregate/vector plans publish existing warning results without another probe.
The logger and capture store only fixed categories and counts, never warning
messages or auxiliary warning-error text. `tidbgo analyze` aggregates those
counts by fingerprint and reports `WRN001` through `WRN003`. If ServerRU is also
requested, it takes precedence and warnings report `ErrWarningsWithServerRU`:
the two probes overwrite each other's session state on Starter. Some MPP
warnings require EXPLAIN, so empty ordinary warnings do not establish support.

`ExplainAnalyzePlan.Diagnostics` inspects an explicitly collected runtime plan
without another database call. Suppressible `PLN001` through `PLN004` warnings
cover pseudo or partial statistics, conservative estimated-to-actual row
divergence, large `TableFullScan` output, and recognized positive disk usage.
Runtime-plan rows and diagnostic evidence attach unambiguous physical table,
Go model, and root-relative relation-path metadata to compiler-owned aliases.
The checks do not parse timing, RU text, or compiled SQL and do not predict that
a proposed index or rewrite will improve the plan.

Planned catalogs cover:

- General application-query index shape and optional foreign-key policy
- Unsafe mutations, large offsets, unbounded queries, and preload limits
- Cross-run connected plan regressions
- Cross-run query-count and duration regressions
- SELECT server-RU regressions
- Additional migration SQL diagnostics for destructive changes and unsupported
  Starter syntax; migration execution already enforces checksums, schema drift,
  and advisory locking independently of the diagnostic catalog

Future diagnostics continue to choose suppressibility as part of each rule
contract. Safety errors such as unqualified updates and deletes will not be
generally suppressible.

## 7. Configuration

No project configuration-file format is currently public. The application
runtime receives an explicit `database/sql` executor and does not read
connection settings from files or environment variables. Connected integration
tests use `TIDBGO_TEST_DSN` only as a test-harness input. The standalone
migration CLI reads its DSN from `TIDBGO_DSN` or the explicitly selected
`--dsn-env` name, requires verified TLS, and loads no `.env` file.

Native `time.Time` bind arguments are passed to the executor without ORM-level
literal formatting or timezone conversion. Their serialization follows the
database driver and connection settings. The ORM does not change connection
time zones; see [SQL arguments and time zones](docs/models.md#sql-arguments-and-time-zones).

Migration directory, snapshot output, operation deadline, and lock wait are
explicit CLI options or runner configuration. They do not change runtime
connection behavior.

## 8. Security requirements

- DSNs, passwords, tokens, and bind values are excluded from default logs and
  persisted reports. Raw SQL templates and database errors can still contain
  application data and require explicit retention controls.
- Full SQL text is not logged by default.
- TLS is required by default for database connections.
- Identifier values are validated before SQL construction.
- User-provided reasons are not inserted into generated SQL comments.
- Runtime telemetry is opt-in, written only to the caller-owned writer, and is
  not sent by the core packages.
- Migration repair operations require an explicit action and reason.

## 9. Delivery order

- Implemented: repository foundation, safe logging, CI, documentation, the
  `tidbgo version` command, and shared diagnostic data types
- Implemented: offline struct metadata, query construction and execution,
  deterministic relation loading, transactions, CRUD, bulk mutations, soft
  deletion, typed raw SQL, statement observation, and same-session ServerRU
  reading, SELECT-only `EXPLAIN`, and explicit
  SELECT-only `EXPLAIN ANALYZE` with returned-plan diagnostics, observer-only
  structured runtime capture, automatic captured-query diagnostics,
  SQL-snapshot index checks, offline runtime N+1 analysis, and versioned
  ServerRU baseline generation
- Implemented: offline struct-first model intent checks, executed typed-query
  diagnostics, TiDB CREATE TABLE snapshot parsing, directional Go-model
  compatibility checks, Relation target identity, pure-junction correctness,
  deterministic collection-relation index-prefix checks, and conservative
  same-function Go-source projection analysis
- Implemented: current-database SQL snapshot generation and normalization,
  versioned up/down migrations, existing-database adoption, checksums, drift
  checks, advisory locking, and explicit interrupted-operation repair
- Planned next: historical reads and release hardening
- Deferred until the current work is complete: reconsideration of optional
  code generation or a schema DSL based only on demonstrated product value

Root tests and vet checks must pass as work progresses. Starter-connected tests
remain opt-in and use an isolated dedicated test database.

## 10. Authoritative external references

Service capabilities can change under managed upgrades. Implementations must
re-check current official documentation and, where appropriate, probe the
connected service instead of relying only on a version string.

- [TiDB Cloud plans](https://docs.pingcap.com/tidbcloud/select-cluster-tier/)
- [TiDB Cloud feature matrix](https://docs.pingcap.com/tidbcloud/features/)
- [TiDB Cloud Starter FAQs](https://docs.pingcap.com/tidbcloud/serverless-faqs/?plan=starter)
- [Limited SQL features on TiDB X instances](https://docs.pingcap.com/tidbcloud/limited-sql-features-tidb-x/)
- [CREATE TABLE statement](https://docs.pingcap.com/tidb/stable/sql-statement-create-table/)
- [AUTO_RANDOM](https://docs.pingcap.com/tidbcloud/auto-random/)
- [TiDB and MySQL compatibility](https://docs.pingcap.com/tidbcloud/mysql-compatibility/)
