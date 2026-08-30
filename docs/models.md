# Struct model metadata

[日本語](models_ja.md)

The `model` package inspects application-owned Go structs without generated
files or a database connection. Metadata is cached by the non-pointer struct
type and is shared by offline tooling and the scalar query runtime.

## Define a model

```go
package app

import (
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type User struct {
	model.Meta `tidbgo:"table=users"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Email      string `tidbgo:"email_address"`
	DeletedAt  time.Time `tidbgo:",soft_delete"`
	OrderCount int64 `tidbgo:"order_count,computed"`
	Password   string `tidbgo:"-"`
	Orders     []Order `tidbgo:"has_many"`
}
```

Exported fields are mapped in declaration order. Scalar `tidbgo` tags use this
fixed grammar:

```text
tidbgo:"[column_name][,option...]"
```

The first value is an optional column name. An empty first value uses the
deterministic snake_case form of the Go field, so `ID` maps to `id` and
`CreatedAt` maps to `created_at`. Remaining values are options. Struct-tag
namespaces other than `tidbgo`, including `db`, are ignored. They do not rename
or exclude fields; use `tidbgo` for both operations.

These declarations are both valid and intentionally different policies:

```go
ID uint64 `tidbgo:",pk"`   // Infer the id column from the Go field.
ID uint64 `tidbgo:"id,pk"` // Pin the physical column name explicitly.
```

An explicit column name may equal the inferred name when an application wants
all physical names written down. `tidbgo:"pkk"` therefore means a column named
`pkk`; an unknown option such as `tidbgo:",pkk"` is rejected. Intent-oriented
diagnostics belong to separate lint tooling instead of parser heuristics.

Use `tidbgo:"-"` by itself to exclude a field completely. Unexported fields are
ignored.

The default table name is the deterministic snake_case form of the declared
Go type, such as `user` for `User` and `user_role` for `UserRole`. Embed the
zero-size `model.Meta` marker with `tidbgo:"table=name"` only when the physical
name differs. Table names must be simple SQL identifiers of at most 64 bytes.

Mark each primary-key field with the `pk` option after the column position,
such as `tidbgo:",pk"` or `tidbgo:"account_id,pk"`. Multiple marked fields
form a composite primary key in struct declaration order. No field name is
implicitly treated as a primary key, and a model without a declared key
remains valid for metadata inspection.

Add `auto_random` to the one TiDB `AUTO_RANDOM` primary-key field. It must be
a non-pointer signed or unsigned integer and must also have `pk`. A single-row
`orm.Insert` omits the field and assigns `sql.Result.LastInsertId` back to it.
A bulk insert omits it but cannot populate individual generated IDs.

Use `computed` for a field populated only by an aliased raw-query result, such
as `COUNT(*) AS order_count`. Computed fields are excluded from base-table
SELECT, INSERT, and UPDATE statements and cannot be used as primary keys,
predicates, ordering fields, or relation keys.

Use `soft_delete` on at most one `time.Time` or `*time.Time` field when a
nullable deletion timestamp controls logical deletion:

```go
DeletedAt time.Time `tidbgo:",soft_delete"`
```

For a non-pointer field, `go-tidb` maps the Go zero time to SQL `NULL` on writes
and SQL `NULL` back to the zero time on reads. This replaces a separate
`nullzero` option. A pointer field follows ordinary nullable Go semantics:
nil maps to `NULL`, while a non-nil pointer, including one pointing to the zero
time, is an explicit value. Other scalar fields do not receive zero-to-NULL
conversion; use a pointer or an `sql.Scanner` type for an ordinary nullable
column.

The field must be a physical non-primary-key field and cannot also be
`auto_random` or `computed`. Query and mutation behavior is described in the
[query guide](queries.md) and [mutation guide](mutations.md).

## Define relations

Use `*T` for `belongs_to` and `has_one`. Use `[]T` or `[]*T` for `has_many`
and `many_to_many`:

```go
type Order struct {
	model.Meta `tidbgo:"table=orders"`
	ID         int64 `tidbgo:",pk,auto_random"`
	UserID     int64
	User       *User `tidbgo:"belongs_to"`
}
```

For a direct relation with one relevant primary-key field, `go-tidb` applies
these deterministic Go-field conventions when no `join` option is present:

- `belongs_to`: `<relation field><target primary key>` to the target primary
  key, such as `UserID:ID`
- `has_one` and `has_many`: the source primary key to
  `<source type><source primary key>`, such as `ID:UserID`

If either side differs or uses a composite key, declare ordered joins:

```go
Records []Record `tidbgo:"has_many,join=TenantID:TenantID,join=ID:ParentID"`
```

Many-to-many mappings always name the physical junction mapping explicitly:

```go
Roles []Role `tidbgo:"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID"`
```

Each `source` option maps a source Go field to a junction column. Each
`target` option maps a junction column to a target Go field. Repeating either
option preserves composite-key order. A relation kind must be the first tag
value. Relation fields are excluded from the scalar field list.

Relation values are ordinary Go values and can be assigned and inspected
directly:

```go
user.Orders = []Order{{ID: 1}}
order.User = &user
```

`go-tidb` does not add loaded-state bookkeeping to model fields. Code that
executes a query remains responsible for knowing which relations it requested.
Relation fields do not perform I/O or lazy loading, and assigning a field does
not persist a relation.

Exported anonymous structs are flattened depth first. Duplicate columns,
invalid SQL identifiers, recursive embedding, unsupported field types, invalid
model marker placement, and unsupported tag options produce a validation
error.

## Inspect metadata

```go
metadata, err := model.Describe[User]()
if err != nil {
	return err
}

for _, field := range metadata.Fields() {
	fmt.Println(field.GoName(), field.ColumnName(), field.IsPrimaryKey())
}

fmt.Println(metadata.TableName())
primaryKey := metadata.PrimaryKeyFields()
softDeleteField, hasSoftDelete := metadata.SoftDeleteField()

for _, relation := range metadata.Relations() {
	fmt.Println(relation.GoName(), relation.Kind())
}
```

`Describe[User]` and `Describe[*User]` return the same cached immutable
descriptor. Inspection does not invoke methods on `User`, read environment
credentials, or perform network I/O.

## Check model intent

`model.Describe` rejects metadata that cannot be executed safely. The separate
`check` package also reports valid declarations that may not match application
intent:

```go
diagnostics := check.Model[User]()
```

`check.ModelType` accepts a runtime `reflect.Type` when a caller already has
one. Both APIs are deterministic, execute no user methods, read no
configuration, and perform no database I/O. Applications explicitly list
their model types; this check does not require source scanning or a generated
registry.

| Code | Severity | Meaning |
| --- | --- | --- |
| `MOD001` | error | The model type or executable metadata is invalid |
| `MOD002` | warning | An exported field has a `db` tag that go-tidb ignores |
| `MOD003` | warning | An unexported field has unused `tidbgo` metadata |
| `MOD004` | warning | The column position resembles a known option or a one-edit typo of one |
| `MOD005` | info | The model has no primary key and cannot use primary-key update or delete |
| `MOD006` | warning | A custom field can scan but cannot be bound as a database argument |
| `MOD007` | warning | A custom field can be bound but cannot be scanned |

`MOD001` is not suppressible because the runtime cannot compile the model.
The other diagnostics describe valid or ignored declarations and set
`Suppressible` to true. A built-in suppression configuration is not currently
provided; callers decide how each returned diagnostic affects their tests or
tooling.

`MOD004` never changes mapping behavior. The first tag value remains a column
name. The rule warns only when that value differs from the inferred column and
is at most one edit from a known option such as `pk`, so an explicit
default-equal column remains quiet. A physical column with the warned name is
still valid.

Physical column types, indexes, and constraints are outside this model-intent
check. Parse a TiDB `CREATE TABLE` snapshot with `schema.Parse` and pass its
catalog to `check.Schema` when those facts should be compared offline. See the
[schema compatibility guide](schema-checks.md).

## Supported scalar representations

The current slice recognizes:

- Boolean values
- Signed and unsigned integer values
- Floating-point values
- Strings
- Byte slices, including `json.RawMessage`
- `time.Time`
- Named forms and pointer forms of the preceding types
- Types implementing `sql.Scanner` or `driver.Valuer`

An application can select its own decimal or identifier library. `go-tidb` records
whether the field or its address implements the standard database interfaces
and does not import a decimal package for user-owned models.

## Current boundary

Model metadata intentionally does not duplicate SQL column types, indexes, or
physical constraints. The separate `schema` and `check` packages can compare
those facts from a SQL snapshot without changing the model. The query runtime
can compile SELECT and mutation statements offline and execute them through an
explicitly supplied `database/sql` executor. `belongs_to` and `has_one`
preloads use deterministic inline
`LEFT JOIN`s, while `has_many` and pure `many_to_many` preloads use
deterministic secondary queries. They populate ordinary relation fields and
support dot-separated nested paths, target projection, and collection
ordering.

See the [scalar query guide](queries.md) and runnable
[starter app example](../examples/starter-app/README.md).
