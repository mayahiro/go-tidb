# Versioned SQL migrations

[日本語](migrations_ja.md)

`tidbgo migrate` is explicit deployment tooling for TiDB Cloud Starter. It
supports new databases, adoption of existing databases, authored up/down SQL,
and a `schema.sql` snapshot of the current target database. Application startup
and ORM queries never run migrations.

## Connection and files

Set `TIDBGO_DSN` through your deployment environment or secret manager. It uses
the [go-sql-driver/mysql DSN format](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#dsn-data-source-name)
and requires a selected database, TCP, and verified TLS (`tls=true`). No `.env`
file is loaded automatically. `--dsn-env NAME` selects another environment
variable; DSN values are not accepted as command-line arguments.

The CLI is a separate Go module with the MySQL driver and CLI framework. The
root library has no third-party dependencies and accepts a caller-owned
`database/sql` pool. See [installation](../README.md#installation).
Connections must use autocommit and default quoting semantics. Arbitrary DSN
session variables, multiple-statement execution, insecure TLS, and unrestricted
local-file access are rejected. `parseTime=true` is optional. A TiDB identity
check cannot verify the Cloud plan; supply a Starter endpoint.

Default paths are relative to the current directory:

```text
migrations/20260921093000123_create_accounts.sql
migrations/20260921104500456_add_flags.sql
schema.sql
log/tidbgo/run-<UTC-time>-<unique-suffix>.log
```

`--dir PATH` selects the migration directory. Connected commands use `--schema
FILE` for snapshot output, which must be outside that directory. For `lint`,
`--schema` instead selects an input snapshot.

The **version is the complete filename without `.sql`**, for example
`20260921104500456_add_flags`. It is a case-sensitive string, not a number.
Versions may contain ASCII letters, digits, dots, underscores, and hyphens,
must start with a letter or digit, and must be at most 251 bytes. Files are
sorted lexicographically. Files with the same timestamp prefix and different
names are distinct versions. Renaming an applied file creates a different
version, so keep applied filenames stable.

`new NAME` creates a template named `<UTC-milliseconds>_<name>.sql`. Names use
lowercase letters, digits, and underscores, begin with a letter, and are at most
128 bytes. An exact filename collision fails without overwriting the file.
Each SQL file and snapshot is limited to 16 MiB; total migration input is
limited to 128 MiB and 20,000 files. Symlinks are rejected.

Each file starts with `-- tidbgo:up`, optionally after header comments, and may
contain `-- tidbgo:down`. Directives occupy their own lines outside quoted values
and block comments. Separate statements with semicolons, including before the
down directive. Each present section must contain SQL. Omitting the entire down
section makes the file irreversible. Multiple SQL statements per section are
supported and sent separately in source order.

## Applied records and execution order

`_tidbgo_migrations` contains only these two columns:

```sql
version VARCHAR(251) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
```

TiDB supplies `created_at` when a file is registered. The runner temporarily
sets its pinned session to UTC and restores its previous time zone afterward.
The table carries an ownership comment; an unrelated table with the reserved
name is rejected.

| Operation | Selection | Record update |
| --- | --- | --- |
| `up` | Unregistered filenames, ascending | Insert after every Up SQL succeeds |
| `down` | Registered versions, `created_at DESC, version DESC` | Delete after every Down SQL succeeds |
| `baseline` | One reviewed initial file without Down | Insert without executing its SQL |

An older filename introduced later is still pending and can be applied. Down
follows application time, with descending filename as a deterministic tie
breaker. It relies on the managed database clock. Reapplying a reverted file
creates a new registration time. There are no checksums, failed-attempt rows,
or per-statement database records.

`up` defaults to all pending files; `--steps N` selects an exact positive count.
`down` defaults to one file and accepts `--steps N`. A selected down path with a
missing file or missing Down section fails before any SQL runs or records are
removed. Missing files outside that path do not block execution. `status`
reports local pending files, applied versions, their timestamps, and applied
versions whose files are missing. Its top-level `version` is the latest
registration; it does not imply that every smaller filename is applied.

## A new database

```sh
tidbgo migrate new create_accounts
```

Fill the template, for example:

```sql
-- tidbgo:up
CREATE TABLE IF NOT EXISTS accounts (
    id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS active BOOL NOT NULL DEFAULT TRUE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS reviewed BOOL NOT NULL DEFAULT FALSE;

-- tidbgo:down
DROP TABLE IF EXISTS accounts;
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

`new` and `lint` are offline. `plan` reads the live database and applied records
without changing tables. It includes the SQL to execute and whether the version
table needs creation. SQL files are trusted deployment code. Control/session
commands and direct references to the version table or advisory-lock functions
are rejected; the lightweight validator is not a full SQL parser or sandbox.

## Adopting an existing database

Start with an empty migration directory:

```sh
tidbgo migrate init
# Review migrations/<UTC-milliseconds>_initial.sql and schema.sql
tidbgo migrate baseline
# Add subsequent migration files after baseline succeeds
```

`init` reads metadata and writes one initial file containing all captured SQL,
with no Down section. It does not create a version table or register a version.
`baseline` requires exactly this one file and no existing applied records. Its
Up SQL must match the current database snapshot. Baseline then creates the
version table if necessary, registers the initial filename, and refreshes
`schema.sql`, without executing application SQL or changing existing data.

The same initial file can initialize an empty database through `up`. On both
new and adopted databases, its absent Down section stops down before touching
its record. Since that record remains, subsequent up skips the initial SQL.
Generated initial SQL need not be rerunnable over existing tables. Do not add a
Down section or remove its record to bypass that boundary without independently
planning the consequences.

Ordinary up refuses a nonempty database without the tool's version table.
Coordinate external schema changes during adoption and migration. Application
DML need not pause for this tool, but metadata queries and DDL still consume
resources and can affect latency.

## Offline migration lint

```sh
tidbgo migrate lint
```

The basic check reads all files and both available directions. It checks file
format and existence guards on supported `CREATE TABLE`, `ADD/DROP COLUMN`,
and simple `CREATE/ADD/DROP INDEX` and `DROP TABLE` forms. Missing `IF NOT EXISTS`
or `IF EXISTS` is a warning. Unsupported statements, including DML and complex
ALTER forms, are explicitly reported as unverified.

To check structure, supply the snapshot **before** the selected migrations and
list files in their intended execution order:

```sh
tidbgo migrate lint --schema before.sql --direction up \
  --file 20260921104500456_add_flags.sql \
  --file 20260921113000789_index_flags.sql
```

Use `--direction down` and reverse execution order with a snapshot of the state
before that down operation. The tool does not infer pending files from the
current `schema.sql`. An empty snapshot represents an empty database. Supply a
snapshot produced by `dump`, or equivalent unqualified CREATE TABLE statements.

Schema checks track table/column existence, nullability, unsigned and generated
attributes, and simple index columns and uniqueness. Definite conflicts are
errors. Type normalization, lengths, precision, defaults, expressions, foreign
keys, and full table or index options are not compared. Guarded skips with incomplete definition
comparisons are reported as unverified. Unsupported changes invalidate further
schema simulation, so dependent statements remain unverified.

The report always states whether schema checks were requested, how many
statements remain unverified, and that database execution was not checked.
There is no catalogue of TiDB-specific restrictions. Existence guards alone
prove neither whole-file idempotence nor successful execution: use a dedicated
TiDB database to verify new construction and the intended down/up flow. Lint
diagnostics do not prevent manual recovery using up/down.

## Failure output and manual recovery

TiDB DDL [commits independently of ordinary transaction rollback](https://docs.pingcap.com/tidb/stable/transaction-overview/).
Statements and record updates are separate operations. A failed up does not
invoke down automatically. Down executes authored SQL and cannot recover data
that SQL has deleted.

For every up, down, and baseline invocation, the CLI creates a unique file
under `log/tidbgo` with mode `0600`, retaining it on success and failure. SQL
progress goes to stderr, leaving stdout available for `--json`. The result
includes `log_file`. Log creation or a failed progress write stops execution;
the start entry is written and synced before the corresponding SQL is sent.

Entries include time, filename/version, direction, statement number, and the
SQL text at execution time. States distinguish started, succeeded, server
failure (`failed`), an unknown result such as a lost connection (`unknown`),
and SQL not sent (`unexecuted`). Record creation/deletion uses a separate phase.
A process crash can leave a started entry without a result; treat it as unknown.
Database error number, SQLSTATE, and cause are included by default in both
CLI output and the log. Connection DSNs and passwords are redacted. Authored
SQL and server messages may still contain application values; protect logs
like deployment source and manage their retention outside the tool.

After any failure, stop and inspect the log and the database, including server
DDL jobs where a result is unknown. A successful statement entry confirms its
acknowledgement, not the success of the entire file.

| Failure point | Applied record | Recovery |
| --- | --- | --- |
| Partway through Up SQL | Absent | Manually undo or complete partial changes, or correct guarded SQL before retrying up |
| Partway through Down SQL | Retained | Manually restore or complete partial changes, or correct guarded Down SQL before retrying down |
| Record update | May differ from the completed SQL | Inspect structure, data, and the record; manually reconcile them |
| Snapshot output after record success | Already updated | Run `dump` to refresh the file |

A failed up has no record, so ordinary down does not select it. A failed down
keeps its record, so ordinary up skips it. There is no automatic retry,
compensation, or repair command. If manually completing an operation requires
changing a record, do so only after verifying all its SQL effects. Structure
alone does not establish that data migrations succeeded. Confirmed completed
files remain completed when a later file fails. A reverted file may be edited
and applied again.

All connected operations use a pinned connection and a database-scoped
[TiDB advisory lock](https://docs.pingcap.com/tidbcloud/locking-functions/).
Locks coordinate cooperating runners; coordinate manual SQL and other tools
separately. The CLI defaults to a 30-minute operation deadline and a 30-second
lock wait. Override them with `--timeout` and `--lock-timeout`; lock wait must
be a whole number of seconds from 1 to 3600. SQL is sent one statement at a time.

## Current schema snapshot

After each completed file, including down, the runner reads the actual database
and replaces `schema.sql`. Partial failure leaves the last complete snapshot;
it may no longer describe the database. An explicit refresh is:

```sh
tidbgo migrate dump
```

Dump never modifies applied records. Use the snapshot with `schema.Parse`,
`check.Schema`, or `tidbgo lint . --schema schema.sql` for [model and Relation
compatibility checks](schema-checks.md). These source-model checks are separate
from `tidbgo migrate lint`. Use [reference audits](reference-audits.md) for
orphan checks after migration; preview with `tidbgo audit . --schema schema.sql
--dry-run`.

The snapshot preserves `SHOW CREATE TABLE` definitions, including precision,
defaults, indexes, generated columns, table options, and executable TiDB
comments. TiFlash replica settings are emitted separately; asynchronous
availability is not structure. Allocator counters such as `AUTO_INCREMENT=n`
and `AUTO_RANDOM_BASE=n` are omitted. A snapshot is not a data backup or a
promise to restore the next allocated ID.

Tables follow foreign-key dependency order with deterministic ordering among
independent tables. Same-database qualifiers are removed for replay into the
selected database. Self references work; cross-database references, cyclic
foreign keys, views, sequences, and unsupported object types fail explicitly.
TiFlash supports zero or two replicas without location labels. Vector index
SQL is retained as returned by TiDB.

Output is replaced after a complete write and sync, preserving an existing
regular file's permissions. Failed writes leave the previous complete file.
Database success and snapshot success are reported separately:
`snapshot_updated=false` with completed versions means output needs attention,
not that SQL should be replayed. Use a location appropriate to the target DB;
Git tracking is optional and never performed by the tool. Snapshots and plan
output can contain sensitive defaults or comments.

## Go deployment tooling and output

```go
runner, err := migrate.New(db, migrate.Config{
    Directory:  "migrations",
    SchemaFile: "schema.sql",
    OnEvent:    persistProgress,
})
if err != nil {
    return err
}
result, err := runner.Apply(ctx, migrate.Up, 0)
// Inspect result.Completed, result.Version, and result.SnapshotUpdated even on error.
```

`persistProgress` is a caller-defined `func(migrate.Event) error`. It receives
trusted SQL and raw errors synchronously before and after SQL/record operations.
Persist the start before returning; callback errors stop further execution.
The library's `error` event means a call failed: use your driver's error type to
distinguish server rejection from unknown outcomes. `OnEvent` is optional; only
the CLI automatically writes `log/tidbgo`. The caller owns the pool, TLS,
cancellation, and deployment timing. `OperationError` omits raw server text
from `Error()` and exposes the cause through `Unwrap()`.

All subcommands accept `--json`; versions are strings. Execution and validation
failures return status 1, and invalid CLI usage returns status 2. Lint warnings
and unverified checks alone return status 0, with their scope explicitly shown.
A successful lint exit is not a database-execution guarantee.
