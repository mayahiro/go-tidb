# go-tidb Public Product Specification

- Version: 0.1.0 draft
- Last updated: 2026-09-01
- Supported profile: TiDB Cloud Starter

This document defines the public product boundary for `go-tidb`. It describes
what users may rely on in the current implementation. A feature listed as
planned is not available until the README marks it as implemented.

## 1. Product definition

`go-tidb` is a Go toolkit for TiDB Cloud Starter with two independent paths:

- A struct-first application runtime for CRUD, explicit relations, deterministic SQL,
  transactions, historical reads, and opt-in query observations
- Offline development diagnostics and planned standalone deployment tools for
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

Scalar execution uses an explicitly supplied `database/sql` executor. A later
connection constructor will use the established MySQL protocol driver. The
project will not implement a database wire protocol.

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
10. Destructive migrations require an in-file acknowledgement and an explicit
   apply flag.
11. Statement RU reported by TiDB is named `ServerRU`. It is not represented
    as billed RU.
12. `EXPLAIN ANALYZE` is allowed only for `SELECT` and remains opt-in. Plan
    diagnostics inspect only its returned rows and add no database statement.
    Compiler-owned access aliases resolve from query-occurrence metadata, not
    model tags or SQL parsing.
13. Raw SQL is an explicit escape hatch without typed relation hydration or
    static query-AST diagnostics. Returned columns may still use model-aware
    scanning.
14. Offline checks are selected explicitly by application code. Diagnostic
    reporting performs no source scan, generated registration, configuration
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
- Deterministic table names, explicit table overrides, and ordered primary keys
- TiDB `AUTO_RANDOM` integer primary keys and raw-result-only computed fields
- Direct and many-to-many relation metadata with deterministic key mappings
- Ordinary pointer and slice relation fields without lazy loading
- Detection of native scalar types, `sql.Scanner`, and `driver.Valuer`
- Deterministic validation of invalid or duplicate field mappings
- Offline scalar SELECT construction with explicit projections, predicates,
  ordering, offset pagination, and keyset pagination
- Public `Build` compilation without database access and public `All`, `First`,
  `Only`, `Exists`, and `Count` execution through an explicitly supplied
  `*sql.DB`, `*sql.Conn`, or `*sql.Tx`
- Nested `BelongsTo` and `HasOne` preloading through inline `LEFT JOIN`s, and
  `HasMany` and pure `ManyToMany` preloading through deterministic full-source
  or bounded keyed secondary SELECTs, with automatic key projection, target
  projection, collection ordering, and no loaded-state fields
- Logical direct and pure `ManyToMany` relation predicates compiled offline as
  `EXISTS`, with TiDB semi-join hints for filtered positive collections and a
  metadata-proven relation-first TopN rewrite for eligible direct `HasMany`
  pages, without implicit preloading
- TiDB-default NULL ordering and primary-key-backed deterministic keyset
  validation
- Typed slice `IN` and `NOT IN` predicates
- Single insert and upsert, automatically placeholder-bounded multi-row insert
  and upsert from value or pointer slices, `AUTO_RANDOM` ID backfill for
  single-row `Insert`, full or selected-field primary-key update,
  predicate-bounded assignment and same-column increment, primary-key or
  predicate delete, and affected-row results
- Exact offline statement counts for automatically split bulk insert and
  upsert, plus static minimum and provable-maximum statement bounds for `All`
  with collection preloads
- Pure `ManyToMany` multi-row add, explicit duplicate-preserving add, selected
  remove, and source clear operations with scalar or composite relation keys
- Typed raw partial and computed-result scanning plus explicit raw mutation SQL
- Caller-owned `*sql.Tx` execution for queries, preloads, and mutations
- Context-scoped statement observation and an automatic-color logger with
  argument values excluded by default, explicit bind-value capture, and
  explicit same-session ServerRU collection
- Operation-scoped debug reports that aggregate completed root, relation,
  split-bulk, raw, and transaction statement events, with optional ServerRU
  diagnostic cost kept separate from target cost
- SELECT-only execution-plan inspection using TiDB's default row-format
  `EXPLAIN` output
- Explicit SELECT execution with TiDB's default row-format `EXPLAIN ANALYZE`
  runtime output and conservative diagnostics over the returned plan
- Same-session ServerRU reading for one completed DML statement through a pinned
  `*sql.Conn` or active `*sql.Tx`
- Observer-scoped ServerRU capture that temporarily pins `*sql.DB` per
  recognized DML statement and records one auxiliary query without replacing
  target results on diagnostic failure
- Immutable offline catalogs parsed from self-contained TiDB CREATE TABLE
  snapshots, including SHOW CREATE TABLE executable comments
- Directional SQL-snapshot and Go-model compatibility checks for mapped tables,
  columns, type families, nullability, primary keys, `AUTO_RANDOM`, generated
  columns, required database-only columns, Relation target identity,
  many-to-many junction pair uniqueness and insert shape, and deterministic
  collection-relation index prefixes
- Shared diagnostic data types, immutable active and suppressed reports, and
  exact-code suppression with a required recorded reason
- The `tidbgo version` command, `tidbgo check` text or JSON report command,
  offline `tidbgo analyze` runtime-capture and optional SQL-snapshot index
  command, and offline `tidbgo lint` Go-source command

## 4. Planned runtime surface

The struct-first runtime is planned to provide:

- Read-only `AsOf` and fixed-duration stale snapshot clients

Per-parent preload limits, opaque cursors, lazy loading, connected cost-based
plan probing, and object-graph persistence are outside v0.1.

The `IDs` terminal is deferred until a measured large-ID workload justifies a
dedicated result API and any resulting minimum-Go-version cost.

## 5. Planned migration surface

Migrations use monotonically increasing versioned files such as:

```text
202608280001_create_users.up.sql
```

The CLI is planned to provide `new`, `lint`, `plan`, `status`, `apply`,
`verify`, and explicitly audited `repair` operations. Application binaries
will not run migrations.

Migration application will:

1. Hold one dedicated connection
2. Acquire a named advisory lock
3. Verify applied and pending checksums
4. Record a running state
5. Execute statements in source order
6. Record applied or failed state, including the failed statement index
7. Release the advisory lock

The implementation will not assume that DDL is atomically rolled back and
will not automatically execute down migrations.

## 6. Diagnostics

The implemented diagnostic representation has a code, a severity of `info`,
`warning`, or `error`, a human-readable explanation, evidence, a suggestion,
an optional source location, and an optional reference. Offline model checks
currently use `MOD001` through `MOD007`. Offline typed SELECT checks currently
use `QRY001` through `QRY007` and never include bind values. Offline physical
schema compatibility checks use `CMP001` through `CMP014`.

The implemented offline checks cover executable model metadata, ignored and
likely misplaced tags, primary-key and custom-scalar capabilities, invalid
typed SELECTs, OFFSET pagination, unordered explicit pagination, leading
wildcard predicates, relation-filtered TopN fallback, directional model and
SQL-snapshot compatibility, schema-aware ordered-LIMIT index-prefix coverage,
and Relation target and junction correctness. Schema-aware query checks attach
a versioned bind-free fingerprint and do not predict optimizer selection. They
also warn when deterministic
`has_many` or `many_to_many` lookups have no structural source-key index
prefix. They do not require source generation, configuration, or a database
connection. Terminal-specific unbounded-query detection and raw SQL are not
inferred from a reusable query builder.

`check.NewReport` applies one fixed policy: active errors fail, while warnings
and info remain successful. A suppression matches every suppressible
diagnostic with one exact code, requires a non-empty reason, and remains in the
report with that reason. Duplicate codes, unused suppressions, and attempts to
suppress non-suppressible diagnostics are rejected.

`tidbgo check` reads one JSON diagnostic array from standard input or one
explicit file. It emits deterministic text by default or the complete report
with `--json`, returns status `1` for active errors, and performs no check
discovery or database access.

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
observed fingerprint rather than individual samples.

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
- Migration checksums, destructive changes, unsupported Starter SQL, schema
  drift, and migration locking

Future diagnostics continue to choose suppressibility as part of each rule
contract. Safety errors such as unqualified updates and deletes will not be
generally suppressible.

## 7. Configuration

No project configuration-file format is currently public. The application
runtime receives an explicit `database/sql` executor and does not read
connection settings from files or environment variables. Connected integration
tests use `TIDBGO_TEST_DSN` only as a test-harness input.

Configuration for future diagnostics and migration commands will be designed
with those commands rather than preserving the removed schema-generator
configuration.

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
  reading, operation debug reports, SELECT-only `EXPLAIN`, and explicit
  SELECT-only `EXPLAIN ANALYZE` with returned-plan diagnostics, observer-only
  structured runtime capture, automatic captured-query diagnostics,
  SQL-snapshot index checks, and offline runtime N+1 analysis
- Implemented: offline struct-first model intent checks, typed SELECT builder
  diagnostics, TiDB CREATE TABLE snapshot parsing, directional Go-model
  compatibility checks, Relation target identity, pure-junction correctness,
  deterministic collection-relation index-prefix checks, and conservative
  same-function Go-source projection analysis
- Planned next: schema snapshot generation and normalization, versioned
  migration tooling, historical reads, and release hardening
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
