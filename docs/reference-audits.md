# Logical references without foreign keys

[日本語](reference-audits_ja.md)

Use Go Relation declarations to describe references and `schema.sql` to describe
physical tables, columns, and keys. Physical foreign keys are optional. Source
lint checks the structure offline; `tidbgo audit` explicitly connects to the
database to detect orphan references. These tools support TiDB 8.5.3 and do not
depend on the foreign-key shared-lock setting introduced in 8.5.6.

## Check structure

```sh
tidbgo lint . --schema schema.sql
```

The input includes explicit `model.Meta` models, recognized query source models,
and their reachable Relation targets. `belongs_to` checks source-to-target
references; `has_one` and `has_many` check target-to-source references. Pure
`many_to_many` junctions and `via` edge models have both endpoints checked.
References are declared through the existing `tidbgo` relation tags; column
names alone are not used to guess relationships.

| Code | Severity | Meaning |
| --- | --- | --- |
| `REF001` | error | A reference table or key column is missing, or its mapping is invalid |
| `REF002` | error | The physical key columns differ in SQL base type or signedness |
| `REF003` | error | The referenced identity has no unconditional primary or unique constraint |
| `REF004` | warning | A reference lookup lacks a visible, complete-column index prefix |
| `SRC003` | info | The source relation mapping could not be resolved |
| `CMP011` | error | A `has_one` target lacks a unique key over its relation columns |
| `CMP012` | error | A pure junction lacks an exact unique source-target pair |

SQL type aliases such as `INT`/`INTEGER` are normalized. This comparison does
not establish equality of precision, lengths, collations, or custom Go type
semantics. Index checks recognize equality prefixes in either column order;
they do not predict which index TiDB will choose or how much RU it will use.
Invisible unique indexes prove uniqueness, but not default lookup coverage.
`REF004` can be suppressed in lint with a reason; reference errors cannot.

`schema_relations`, `analyzed_schema_relations`, and
`uncertain_schema_relations` describe declaration coverage separately from
model and query coverage. Analyzed declarations include those with detected
schema errors. `SRC003` is informational in lint. Embedded fields, aliases,
unavailable model sources, and unsupported mappings can leave checks incomplete.
Use `check.Schema` with application types for reflection-based model checks.

## Preview and run an audit

```sh
# Offline: inspect the resolved references and generated SELECT statements
tidbgo audit . --schema schema.sql --dry-run

# Connected: TIDBGO_DSN is supplied by your deployment environment
tidbgo audit . --schema schema.sql --timeout 30s

# Inspect names with --dry-run, then select references to examine
tidbgo audit . --schema schema.sql --relation User.Orders --timeout 10s --json
```

`--schema` is required and must describe the database selected by the DSN.
Audit does not verify live schema drift. Use `--dsn-env NAME` to choose another
environment variable. The connection requires a database and TCP with verified
TLS (`tls=true`); multiple statements, session-variable DSN parameters, and
unrestricted local-file access are rejected. The driver stays in the independent
CLI module. Neither lint nor audit executes application Go code.

`--relation` is repeatable and uses the names printed by the plan, such as
`User.Orders` or `models/User.Orders`. A many-to-many name selects both endpoints;
append `#source` or `#target` to select one. Identical generated probes are
executed once, with other declarations listed in `also_declared_by`. Selection
limits database probes; the whole source input still must pass schema preflight.

An audit refuses database execution if schema errors or uncertain model/relation
mappings remain, or if no reference was selected. Its `--dry-run` output can
show the resolved portion, but returns failure when coverage is incomplete.
Unrecognized code and unregistered models remain outside source discovery;
include the intended model sources and use explicit `model.Meta` declarations
for models that have no recognized query terminals.

Each probe returns whether **at least one** non-NULL child key has no matching
parent row. It does not count all orphans or output row keys or values. All
components of a composite key must be non-NULL for the row to be checked.
Soft-delete scopes are not applied: physically present soft-deleted parents
count as existing, and soft-deleted children are also checked. The audit checks
physical references, not application visibility or lifecycle policies.

Probes run sequentially under a deadline for the entire connected operation.
The default is 30 seconds, and `--timeout` accepts a positive duration up to one
hour. `LIMIT 1` bounds the result, **not the scanned rows or RU**. A clean large
table might require a full scan. Review the dry-run SQL with TiDB `EXPLAIN`,
select relations, and schedule large checks appropriately. A deadline is not
an exact server-side work or RU budget.

## Interpret results

JSON reports `plan`, `results`, source `statistics`, and `diagnostics`. Each
result has `checked` and `orphan`; an unchecked result must not be read as clean.
`complete` means all selected probes finished, including probes that found
orphans. It is false for dry runs and incomplete execution. `REF005` reports
an orphan; `REF006` reports incomplete preflight or execution. After a database
error or deadline, completed results remain available and later probes stay
unchecked. Driver error text and row values are not printed.

Exit status is `0` for a valid dry-run plan or a completed audit without orphans,
`1` for schema/coverage errors or detected orphans, `2` for invalid options or
connection configuration, and `5` for execution or output errors.

Each SELECT observes its own database snapshot. Success describes the observed
data, not one snapshot across all references or a guarantee about later writes.
Some [Relation query optimizations](queries.md) assume referenced rows exist;
auditing helps verify that contract. Audit never creates foreign keys, rejects
application writes, cascades deletes, or repairs data. A concurrent integrity
guarantee still requires coordinated write paths or database constraints.

TiDB documents [transaction snapshots](https://docs.pingcap.com/tidb/stable/transaction-overview/)
and [SELECT timeouts](https://docs.pingcap.com/tidb/stable/dev-guide-timeouts-in-tidb/).
Use the [schema compatibility guide](schema-checks.md) for the broader typed
model checks and the [migration guide](migrations.md) to maintain the snapshot.
