package migrate

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mayahiro/go-tidb/schema"
)

type lintSchema struct {
	tables  map[string]*lintTable
	unknown bool
}
type lintTable struct {
	columns map[string]schema.Column
	indexes map[string]lintIndex
}
type lintIndex struct {
	columns         []string
	unique, complex bool
}

func loadLintSchema(source string) (*lintSchema, error) {
	world := &lintSchema{tables: make(map[string]*lintTable)}
	catalog, err := schema.Parse(source)
	if err != nil && !errors.Is(err, schema.ErrNoCreateTables) {
		return nil, err
	}
	for _, table := range catalog.Tables() {
		if table.SchemaName() != "" {
			return nil, fmt.Errorf("migrate: lint snapshot must use unqualified table names")
		}
		world.tables[fold(table.Name())] = lintTableFrom(table)
	}
	// schema.Parse ignores ALTER and other statements. Do not silently treat
	// those effects as incorporated in the snapshot. Empty snapshots are valid.
	statements, err := splitSQL(source)
	if err != nil {
		return nil, err
	}
	for _, statement := range statements {
		ts, err := tokens(statement)
		if err != nil {
			return nil, err
		}
		if hasWords(ts, "CREATE", "TABLE") {
			continue
		}
		// Snapshot-generated TiFlash replica settings do not affect this catalog.
		if hasWords(ts, "ALTER", "TABLE") && len(ts) >= 7 && hasWords(ts[3:], "SET", "TIFLASH", "REPLICA") {
			continue
		}
		world.unknown = true
	}
	return world, nil
}

func lintTableFrom(table schema.Table) *lintTable {
	t := &lintTable{columns: make(map[string]schema.Column), indexes: make(map[string]lintIndex)}
	for _, column := range table.Columns() {
		t.columns[fold(column.Name())] = column
	}
	for _, idx := range table.Indexes() {
		columns := idx.Columns()
		for i := range columns {
			columns[i] = fold(columns[i])
		}
		t.indexes[fold(idx.Name())] = lintIndex{columns, idx.Unique(), idx.HasExpression() || !idx.SupportsDefaultColumnLookup()}
	}
	return t
}

func (c *lintCheck) table(name string) *lintTable {
	if c.world == nil || c.world.unknown {
		return nil
	}
	t, ok := c.world.tables[fold(name)]
	if !ok {
		c.issue("error", fmt.Sprintf("table %q does not exist in the prior schema", name))
	}
	return t
}

func (c *lintCheck) createTable(table schema.Table, guard bool) {
	name := fold(table.Name())
	expected := lintTableFrom(table)
	existing, exists := c.world.tables[name]
	if !exists {
		c.world.tables[name] = expected
		return
	}
	if !guard {
		c.issue("error", fmt.Sprintf("table %q already exists in the prior schema", table.Name()))
		return
	}
	if len(existing.columns) != len(expected.columns) || len(existing.indexes) != len(expected.indexes) {
		c.issue("error", fmt.Sprintf("existing table %q has different columns or indexes", table.Name()))
		return
	}
	for _, col := range table.Columns() {
		name := fold(col.Name())
		prev, exists := existing.columns[name]
		if !exists {
			c.issue("error", fmt.Sprintf("existing table %q lacks column %q", table.Name(), name))
			continue
		}
		c.compareColumn(prev, col)
	}
	for _, definition := range table.Indexes() {
		name := fold(definition.Name())
		idx := expected.indexes[name]
		prev, exists := existing.indexes[name]
		if !exists {
			c.issue("error", fmt.Sprintf("existing table %q lacks index %q", table.Name(), name))
			continue
		}
		if prev.complex || idx.complex {
			c.issue("unverified", fmt.Sprintf("index %q has attributes outside the simple-index comparison", name))
			continue
		}
		if !slices.Equal(prev.columns, idx.columns) || prev.unique != idx.unique {
			c.issue("error", fmt.Sprintf("existing table %q has a different index %q", table.Name(), name))
		}
	}
	c.issue("unverified", "a skipped CREATE TABLE is compared only by basic column attributes and simple keys; full definition equality is not verified")
}

func (c *lintCheck) compareColumn(a, b schema.Column) {
	if a.Nullable() != b.Nullable() || a.Unsigned() != b.Unsigned() || a.Generated() != b.Generated() || a.AutoIncrement() != b.AutoIncrement() || a.AutoRandom() != b.AutoRandom() || a.VectorDimensions() != b.VectorDimensions() {
		c.issue("error", fmt.Sprintf("existing column %q has different basic attributes", b.Name()))
		return
	}
	if a.TypeName() != b.TypeName() {
		c.issue("unverified", fmt.Sprintf("column %q has different declared types (%s and %s); type normalization is not compared", b.Name(), a.TypeName(), b.TypeName()))
	}
	// SHOW CREATE may normalize type aliases and add an implicit DEFAULT NULL.
	// This catalog does not retain enough detail to compare those definitions.
	c.issue("unverified", fmt.Sprintf("column %q already exists; lengths, precision, defaults, and expressions are not compared", b.Name()))
}

func (c *lintCheck) addIndex(table, name string, index lintIndex, guard bool) {
	t := c.table(table)
	if t == nil {
		return
	}
	if existing, exists := t.indexes[fold(name)]; exists {
		if !guard {
			c.issue("error", fmt.Sprintf("index %q already exists in table %q", name, table))
			return
		}
		if existing.complex {
			c.issue("unverified", "existing index has attributes outside the simple-index comparison")
			return
		}
		if !slices.Equal(existing.columns, index.columns) || existing.unique != index.unique {
			c.issue("error", fmt.Sprintf("existing index %q has a different definition", name))
		}
		return
	}
	for _, column := range index.columns {
		if _, ok := t.columns[column]; !ok {
			c.issue("error", fmt.Sprintf("index %q references missing column %q", name, column))
			return
		}
	}
	t.indexes[fold(name)] = index
}

func (c *lintCheck) dropIndex(table, name string, guard bool) {
	t := c.table(table)
	if t == nil {
		return
	}
	if _, exists := t.indexes[fold(name)]; !exists && !guard {
		c.issue("error", fmt.Sprintf("index %q does not exist in table %q", name, table))
		return
	}
	delete(t.indexes, fold(name))
}
