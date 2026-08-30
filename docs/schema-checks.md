# Offline schema compatibility

[日本語](schema-checks_ja.md)

`go-tidb` treats a SQL schema snapshot and an application-owned Go struct as
authoritative for different responsibilities. The SQL snapshot describes the
expected physical database. The struct describes the columns and relations the
application reads or writes. They do not need to contain the same information.

## Basic use

Parse the snapshot once and reuse the immutable catalog for every model:

```go
catalog, err := schema.Parse(schemaSQL)
if err != nil {
	return err
}

diagnostics := make([]check.Diagnostic, 0)
diagnostics = append(diagnostics, check.Schema[User](catalog)...)
diagnostics = append(diagnostics, check.Schema[Order](catalog)...)
```

`schema.Parse`, `check.Schema`, and `check.SchemaType` perform no database I/O,
read no connection configuration, and execute no SQL or user methods. A caller
that already has a `reflect.Type` can use `check.SchemaType`.

## Accepted SQL snapshots

`schema.Parse` accepts one or more self-contained TiDB `CREATE TABLE`
definitions. It recognizes:

- Ordinary table and column definitions
- Inline and table-level primary and unique keys
- Ordinary indexes and functional index parts
- Nullability, defaults, generated columns, `AUTO_INCREMENT`, and
  `AUTO_RANDOM`
- Schema-qualified table names
- TiDB executable comments emitted by `SHOW CREATE TABLE`, including the
  comment form of `AUTO_RANDOM`

Non-`CREATE TABLE` statements are ignored so wrappers commonly found in a
schema-only dump, such as `SET` and `DROP TABLE`, can remain in the input.
`CREATE TABLE LIKE` and `CREATE TABLE AS SELECT` are rejected because they do
not contain a self-contained column definition. The parser does not execute or
replay `ALTER TABLE` statements.

The current model metadata uses unqualified table names. A snapshot containing
the same unqualified table name in more than one schema is therefore rejected
as ambiguous. Table and column comparison follows TiDB's case-insensitive
identifier lookup.

## Directional comparison

The check reports facts that can affect model reads, writes, or relation
cardinality:

- The mapped table must exist
- Every mapped non-computed field must have a physical column
- Known native Go and SQL type families must be compatible
- A nullable physical column with a non-nullable native Go field is a warning
- A pointer, byte slice, or value-form soft-delete field mapped to `NOT NULL`
  is a warning because it can produce SQL `NULL`
- When a model declares a primary key, its ordered columns must match the
  physical primary key
- A mapped column must declare `AUTO_RANDOM` on both sides or neither side
- A physical generated column cannot be an ordinary writable model field
- A `belongs_to` or `has_one` target must have a primary or unique key that
  proves at-most-one-row semantics

Database-only columns are deliberately not errors merely because the struct
omits them. Nullable, defaulted, and database-generated columns are accepted.
A database-only `NOT NULL` column without a default or database generation is a
warning because inserts through that model can fail. This permits common
database-managed columns such as `created_at` to remain out of the struct.

If a model does not declare a primary key, the compatibility check does not
require it to repeat the physical primary key. `check.Model` separately
reports the unavailable primary-key mutation capability as `MOD005`.

## Diagnostics

| Code | Default severity | Meaning |
| --- | --- | --- |
| `CMP001` | error | A non-nil parsed schema catalog was not supplied |
| `CMP002` | error | A model or to-one relation target physical table is absent |
| `CMP003` | error | A mapped non-computed field or to-one target key has no physical column |
| `CMP004` | error | A known native Go representation and SQL type family are incompatible |
| `CMP005` | warning | A nullable physical column uses a non-nullable native Go field |
| `CMP006` | warning | A nullable Go representation maps to a `NOT NULL` column |
| `CMP007` | error | The model and physical ordered primary keys differ |
| `CMP008` | error | The mapped field and physical column disagree about `AUTO_RANDOM` |
| `CMP009` | error | An ordinary writable model field maps to a generated column |
| `CMP010` | warning | A required database-only column can make model inserts fail |
| `CMP011` | error | A to-one relation target is not proven unique by the snapshot |

A value-form `time.Time` soft-delete field is treated as nullable because the
runtime scans SQL `NULL` to zero time and writes zero time as SQL `NULL`.

Invalid model metadata is returned through the existing non-suppressible
`MOD001` diagnostic before physical compatibility is evaluated.

Warnings are suppressible in the shared diagnostic representation through
`check.NewReport` or `tidbgo check`. Errors represent an executable mapping or
cardinality conflict and are not suppressible. See the [offline diagnostic
report guide](checks.md).

## Type-check boundary

The current type check intentionally uses broad representation families. It
does not yet compare integer widths, signed ranges, decimal precision and
scale, string lengths, character sets, collations, temporal precision, or
default expressions. Unknown future SQL types and application-selected custom
types implementing `sql.Scanner` or `driver.Valuer` are not guessed. Custom
type semantics remain the application's responsibility.

The parser records ordinary indexes, but this slice checks only uniqueness
needed for to-one relation correctness. It does not yet diagnose missing
performance indexes, foreign keys, junction-table constraints, migration
history, or live database drift.

TiDB documents the current [`CREATE TABLE`
grammar](https://docs.pingcap.com/tidb/stable/sql-statement-create-table/),
[`AUTO_RANDOM`](https://docs.pingcap.com/tidbcloud/auto-random/), and
[case-insensitive table-name behavior](https://docs.pingcap.com/tidbcloud/mysql-compatibility/).
