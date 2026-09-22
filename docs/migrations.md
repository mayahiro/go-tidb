# Versioned SQL migrations

[日本語](migrations_ja.md)

`tidbgo migrate` is explicit deployment tooling for TiDB Cloud Starter. It
supports new databases, adoption of existing databases, up/down SQL, and a
`schema.sql` snapshot of the **current target database**. Application startup
and ORM queries never run migrations.

Use the generated snapshot for [offline schema compatibility checks](schema-checks.md)
with `schema.Parse` and `check.Schema`, or pass it to `tidbgo lint --schema`.
Down also updates the snapshot to reflect the current target database.

`tidbgo lint . --schema schema.sql` checks source model tables, mapped columns,
declared primary/unique keys, and unmapped required columns. Use a snapshot of
the intended migration or rollback target before deploying that application
version; see [source schema checks](checks.md#go-source-analysis) for coverage
and unresolved-model reporting.

After a migration, [reference audits](reference-audits.md) can check live
orphan references using the refreshed snapshot and application Relation
declarations, even when the database has no foreign keys. Preview the probes
with `tidbgo audit . --schema schema.sql --dry-run` before connected execution.

## Connection and files

Set `TIDBGO_DSN` through your deployment environment or secret manager. It uses
the [go-sql-driver/mysql DSN format](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#dsn-data-source-name)
and requires a selected database, TCP, and verified TLS (`tls=true`). No `.env`
file is loaded automatically. `--dsn-env NAME` selects another environment
variable; DSN values are not accepted as command-line arguments.

The CLI is a separate Go module and includes the MySQL driver and CLI framework.
The root library module has no third-party dependencies. The `migrate` and `orm`
packages accept caller-owned `database/sql` pools/executors and do not select a
driver. See [installation](../README.md#installation) for the CLI checkout build.
The connection must use autocommit and default quoting semantics. Arbitrary
DSN session-variable parameters, multiple-statement execution, insecure TLS,
and unrestricted local-file access are rejected. `parseTime=true` is optional
for migrations. A successful TiDB version check cannot verify the Cloud plan;
the operator must supply a Starter endpoint.

Default paths are relative to the current directory:

```text
migrations/20260921093000123_create_accounts.sql
migrations/20260921104500456_add_label.sql
schema.sql
```

Use `--dir PATH` and `--schema FILE` to select input and output. The snapshot
must be outside the migration directory. Files use a 17-digit UTC timestamp
version, `YYYYMMDDHHMMSSmmm` (year through milliseconds),
and a lowercase name of at most 128 bytes beginning with a letter, followed by
letters, digits, or underscores. Versions are sorted numerically. Invalid dates,
duplicate versions, symlinks, and empty SQL sections are rejected. Each SQL file and
snapshot is limited to 16 MiB; total migration input is limited to 128 MiB.

Each file starts with `-- tidbgo:up`, optionally after header comments, and may
contain a following `-- tidbgo:down` section. Directives occupy their own lines
outside quoted values and block comments. Terminate the up SQL with a semicolon
before down. Omitting the entire down section explicitly makes the migration
irreversible. Both sections must contain SQL if present. Keep every recorded
migration, including reverted versions, unchanged. Checksums cover all file
bytes, including directives, comments, and whitespace.

`new` and `init` generate versions using the UTC clock truncated to milliseconds.
Creation rejects a version at or before the latest local version, including
same-millisecond collisions; check the clock or retry in a later millisecond.
Concurrent local creators use `.tidbgo-create.lock` in the migration directory.
After an interrupted file creation, inspect any partial file and remove this
lock only after confirming no creator remains active. Existing SQL is never
overwritten.

## A new database

```sh
tidbgo migrate new create_accounts
```

Fill both sections in the generated file, for example:

```sql
-- tidbgo:up
CREATE TABLE accounts (
    id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);

-- tidbgo:down
DROP TABLE accounts;
```

Then validate, review, and execute:

```sh
tidbgo migrate lint
tidbgo migrate plan
tidbgo migrate up
tidbgo migrate status
tidbgo migrate plan --direction down
tidbgo migrate down
```

`new` and `lint` are offline. `plan` reads the live database and history without
changing application or history tables. It includes the SQL to execute and
whether the history table needs creation. `up` applies all pending migrations;
`--steps N` selects an exact positive count. `down` defaults to one version and
accepts `--steps N`. The entire selected reverse path is checked before any
SQL runs. A new version cannot be inserted below previously recorded versions.

## Adopting an existing database

Start with an empty migration directory:

```sh
tidbgo migrate init
# Review migrations/<UTC-timestamp>_initial.sql and schema.sql
tidbgo migrate baseline
```

`init` only reads database metadata and writes files. It captures a portable
initial migration with only an up section. `baseline` re-reads the live structure
and requires it to match that initial SQL before creating the history table
and recording the first version as adopted. It never executes the initial SQL
or rebuilds existing application tables, indexes, or data. Other pending files
may exist, but only the first version can be adopted. On success, `baseline`
also creates or refreshes `schema.sql` from the actual database, even if the
file written by `init` is missing.

The same initial up SQL can initialize an empty database. Subsequent migrations
are shared by both workflows. An adopted database cannot be taken below its
baseline timestamp version with `down`. The initial SQL remains fixed when
`schema.sql` changes. Ordinary `up` refuses a nonempty database with no recorded
history rather than adopting it automatically.

Coordinate schema changes with other tools during adoption and execution.
Application DML does not require a maintenance pause from this tool, but
metadata reads, history-table creation, and DDL still consume database
resources. The tool does not promise zero latency impact.

## Current schema snapshot

After each successfully completed migration, including `down`, the tool reads
the actual database and replaces `schema.sql`. Migration files and execution
history remain available for reapplication. A standalone refresh is:

```sh
tidbgo migrate dump
```

The dump retains column precision, default expressions, indexes, generated
columns, table options, and executable TiDB comments from `SHOW CREATE TABLE`.
It emits TiFlash replica configuration separately; asynchronous availability
and progress are not schema attributes. Allocator counters such as
`AUTO_INCREMENT=n` and `AUTO_RANDOM_BASE=n` are omitted so ordinary inserts do
not change structural fingerprints. This is a schema snapshot, not a data
backup or a guarantee of restoring the next allocated ID.

Tables are ordered by foreign-key dependencies with deterministic ordering
among independent tables. Same-database foreign-key qualifiers are removed so
replay targets the selected database. Self references are supported; cross-database and
cyclic foreign-key graphs are rejected. Views, sequences, and unsupported
object types are reported as errors rather than silently omitted. TiFlash
configuration supports zero or two replicas without location labels. Vector
index definitions are retained as returned by TiDB.

Output is replaced only after a complete write and file sync, preserving an
existing regular file's permission bits. A failed read/write leaves the previous
complete file in place. If database work succeeded but output failed, the result
reports `snapshot_updated=false` and a nonzero exit status; use `dump` to retry
the output without rerunning SQL. A dirty-state dump may describe a partial
schema and does not repair history. Dumping drift does not acknowledge it as an
expected migration result.

Use an output location appropriate to the selected database. Keeping
`schema.sql` in Git is supported; the tool never performs Git operations.
CI comparisons must use the same applied migration version as the snapshot.
SQL snapshots can contain sensitive defaults or comments, and `plan` prints
trusted SQL file contents: protect these artifacts like application source.

## Interrupted operations and repair

TiDB DDL [commits independently of ordinary transaction rollback](https://docs.pingcap.com/tidb/stable/transaction-overview/).
`down` executes explicit inverse SQL; it does not restore deleted values.
A failed `up` never invokes `down` automatically.

Each attempt is recorded in `_tidbgo_migrations` before its SQL runs. Progress
counts confirmed statements. A failure or lost acknowledgement leaves the
attempt `failed` or `running`; subsequent up/down operations stop. Even the
statement following the confirmed count might already have taken effect.
Read the status, inspect server DDL jobs and data, and manually complete or
reverse the operation before acknowledging its result:

```sh
tidbgo migrate status --json
tidbgo migrate dump --schema reviewed.sql
# Independently verify the structure, data, and completion of server DDL jobs
tidbgo migrate repair 20260921113000789 --state applied --expected-schema reviewed.sql --reason 'Verified the completed operation'
```

Use `--state reverted` when that version is confirmed absent. The reviewed snapshot
must match the live schema. Restoring the state before the interrupted attempt
also requires its original structural fingerprint. Schema equality cannot
prove a backfill or other DML succeeded: that remains an operator check.
Repair executes no migration SQL; it atomically marks the interrupted attempt
resolved and appends a separate audit event with the required reason.

After completion, the next mutation compares the database with the last
recorded structural fingerprint and rejects drift. History/file disagreement
and missing or edited recorded files also stop execution. Repair addresses an
interrupted attempt; arbitrary drift and checksum rewriting are not silently
accepted. SQL files are trusted deployment code, and the lightweight validator
does not implement full SQL grammar or sandbox cross-database references.

All connected operations pin one connection and use a database-scoped
[TiDB advisory lock](https://docs.pingcap.com/tidbcloud/locking-functions/).
These locks serialize cooperating runners, not unrelated tools or manual SQL.
The CLI defaults to a 30-minute operation deadline and a 30-second lock wait;
use `--timeout` and `--lock-timeout` to override them. Lock timeout must be a
whole number of seconds from 1 to 3600. SQL is sent one statement at a time,
without automatic retries. Control/session commands and direct references to
the history table or advisory-lock functions are rejected.

All subcommands accept `--json`. Migration execution/validation failures and
dirty/drifting `status` return status 1; invalid CLI usage returns status 2.
JSON versions are integers; use a decoder that preserves 64-bit integers or
number text rather than converting the 17-digit version to a floating-point value.
Library `OperationError` messages exclude underlying server text, while
`errors.Unwrap` remains available for controlled diagnosis.

## Go deployment tooling

```go
runner, err := migrate.New(db, migrate.Config{
    Directory:  "migrations",
    SchemaFile: "schema.sql",
})
if err != nil {
    return err
}
result, err := runner.Apply(ctx, migrate.Up, 0)
// Inspect result even when err != nil, especially Dirty and SnapshotUpdated
```

The caller owns the pool, TLS configuration, cancellation, and deployment
timing. `migrate.Load` and `migrate.Create` require no database. The runner is
not invoked by any ORM API.
