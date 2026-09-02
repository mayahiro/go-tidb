package schema

import "strings"

// Position identifies a one-based location in the parsed SQL source.
type Position struct {
	// Offset is the one-based byte offset in the SQL source.
	Offset int
	// Line is the one-based source line.
	Line int
	// Column is the one-based byte column within Line.
	Column int
}

// Catalog is an immutable collection of parsed CREATE TABLE definitions.
// Table lookup follows TiDB's case-insensitive table-name comparison.
type Catalog struct {
	tables []Table
	byName map[string]int
}

// Tables returns tables in source order as detached values.
func (c *Catalog) Tables() []Table {
	if c == nil {
		return nil
	}
	tables := make([]Table, len(c.tables))
	copy(tables, c.tables)
	return tables
}

// Table returns the table matching name without regard to ASCII letter case.
func (c *Catalog) Table(name string) (Table, bool) {
	if c == nil {
		return Table{}, false
	}
	index, ok := c.byName[foldIdentifier(name)]
	if !ok {
		return Table{}, false
	}
	return c.tables[index], true
}

// Table is immutable metadata for one physical CREATE TABLE definition.
type Table struct {
	schemaName string
	name       string
	position   Position
	columns    []Column
	byColumn   map[string]int
	indexes    []Index
}

// SchemaName returns the optional schema qualifier from CREATE TABLE.
func (t Table) SchemaName() string { return t.schemaName }

// Name returns the physical table name without a schema qualifier.
func (t Table) Name() string { return t.name }

// Position returns the location of the table name in the SQL source.
func (t Table) Position() Position { return t.position }

// Columns returns columns in declaration order as detached values.
func (t Table) Columns() []Column { return append([]Column(nil), t.columns...) }

// Column returns the column matching name without regard to ASCII letter case.
func (t Table) Column(name string) (Column, bool) {
	index, ok := t.byColumn[foldIdentifier(name)]
	if !ok {
		return Column{}, false
	}
	return t.columns[index], true
}

// Indexes returns primary, unique, and ordinary indexes in declaration order.
// Inline column constraints occur at the position of their column.
func (t Table) Indexes() []Index {
	return append([]Index(nil), t.indexes...)
}

// PrimaryKeyColumns returns the physical primary-key columns in index order.
func (t Table) PrimaryKeyColumns() []string {
	for _, index := range t.indexes {
		if index.primary {
			return index.Columns()
		}
	}
	return nil
}

// Column is immutable metadata for one physical table column.
type Column struct {
	name          string
	typeName      string
	position      Position
	nullable      bool
	unsigned      bool
	hasDefault    bool
	generated     bool
	autoIncrement bool
	autoRandom    bool
}

// Name returns the physical column name.
func (c Column) Name() string { return c.name }

// TypeName returns the normalized uppercase SQL base type name.
func (c Column) TypeName() string { return c.typeName }

// Position returns the location of the column name in the SQL source.
func (c Column) Position() Position { return c.position }

// Nullable reports whether the physical column permits NULL.
func (c Column) Nullable() bool { return c.nullable }

// Unsigned reports whether the physical column uses the UNSIGNED attribute.
func (c Column) Unsigned() bool { return c.unsigned }

// HasDefault reports whether the physical column declares DEFAULT.
func (c Column) HasDefault() bool { return c.hasDefault }

// Generated reports whether the physical column is generated from an
// expression.
func (c Column) Generated() bool { return c.generated }

// AutoIncrement reports whether TiDB generates the column with AUTO_INCREMENT.
func (c Column) AutoIncrement() bool { return c.autoIncrement }

// AutoRandom reports whether TiDB generates the column with AUTO_RANDOM.
func (c Column) AutoRandom() bool { return c.autoRandom }

// DatabaseGenerated reports whether INSERT may omit the column because TiDB
// generates it from AUTO_INCREMENT, AUTO_RANDOM, or a generated expression.
func (c Column) DatabaseGenerated() bool {
	return c.generated || c.autoIncrement || c.autoRandom
}

// Index is immutable metadata for one primary, unique, or ordinary index.
type Index struct {
	name          string
	position      Position
	columns       []string
	primary       bool
	unique        bool
	hasExpression bool
	specialized   bool
	partial       bool
	invisible     bool
}

// Name returns the declared index name, or PRIMARY for a primary key.
func (i Index) Name() string { return i.name }

// Position returns the location of the index definition in the SQL source.
func (i Index) Position() Position { return i.position }

// Columns returns simple indexed columns in index order. Expression and
// prefix-length parts are omitted and can be detected with HasExpression.
func (i Index) Columns() []string { return append([]string(nil), i.columns...) }

// Primary reports whether the index is the table primary key.
func (i Index) Primary() bool { return i.primary }

// Unique reports whether the index is declared PRIMARY or UNIQUE. Use
// ProvidesUnconditionalUniqueness when partial-index scope matters.
func (i Index) Unique() bool { return i.primary || i.unique }

// HasExpression reports whether any index part is an expression, a prefix
// length, or another form that is not a complete simple column reference with
// an optional ASC or DESC direction.
func (i Index) HasExpression() bool { return i.hasExpression }

// ProvidesUnconditionalUniqueness reports whether the index enforces
// uniqueness for the complete listed columns across every table row.
// Invisible unique indexes still enforce their constraint.
func (i Index) ProvidesUnconditionalUniqueness() bool {
	return i.Unique() && !i.hasExpression && !i.specialized && !i.partial
}

// SupportsDefaultColumnLookup reports whether the default TiDB optimizer can
// use the index for an unconditional lookup over the complete listed columns.
// Expression, prefix-length, specialized, partial, and invisible indexes do
// not establish this coverage.
func (i Index) SupportsDefaultColumnLookup() bool {
	return !i.hasExpression && !i.specialized && !i.partial && !i.invisible
}

func foldIdentifier(identifier string) string {
	return strings.ToLower(identifier)
}
