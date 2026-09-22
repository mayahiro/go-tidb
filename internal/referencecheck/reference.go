// Package referencecheck validates declared logical references and compiles
// read-only orphan probes. It never requires a physical foreign key.
package referencecheck

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/modelmeta"
	"github.com/mayahiro/go-tidb/internal/schemacompat"
	"github.com/mayahiro/go-tidb/schema"
)

// Reference describes an ordered child-to-parent key mapping. Names identify
// the declaring Go relation, not a database constraint.
type Reference struct {
	Name          string         `json:"name"`
	ChildTable    string         `json:"child_table"`
	ChildColumns  []string       `json:"child_columns"`
	ParentTable   string         `json:"parent_table"`
	ParentColumns []string       `json:"parent_columns"`
	Location      check.Location `json:"location"`
}

// Validate checks physical columns, base types, signedness, parent identity,
// and lookup prefixes. It does not inspect live rows or compare SQL type sizes.
func Validate(catalog *schema.Catalog, ref Reference) []check.Diagnostic {
	var diagnostics []check.Diagnostic
	add := func(code string, severity check.Severity, message, suggestion string) {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code: code, Severity: severity, Title: "Logical reference schema check",
			Message: ref.Name + ": " + message, Suggestion: suggestion,
			Location: ref.Location, Suppressible: severity != check.SeverityError,
		})
	}
	if err := validateMapping(ref); err != nil || catalog == nil {
		add("REF001", check.SeverityError, "reference mapping or schema catalog is invalid", "Resolve the reference mapping and supply a parsed SQL snapshot")
		return diagnostics
	}
	child, childOK := catalog.Table(ref.ChildTable)
	parent, parentOK := catalog.Table(ref.ParentTable)
	if !childOK || !parentOK {
		add("REF001", check.SeverityError, "referencing or referenced table is absent from the snapshot", "Include both tables in the SQL snapshot")
		return diagnostics
	}
	for i, name := range ref.ChildColumns {
		left, leftOK := child.Column(name)
		right, rightOK := parent.Column(ref.ParentColumns[i])
		if !leftOK || !rightOK {
			add("REF001", check.SeverityError, fmt.Sprintf("reference column %s.%s or %s.%s is absent", child.Name(), name, parent.Name(), ref.ParentColumns[i]), "Correct the relation column mapping or the SQL snapshot")
			continue
		}
		if referenceType(left.TypeName()) != referenceType(right.TypeName()) || left.Unsigned() != right.Unsigned() {
			add("REF002", check.SeverityError, fmt.Sprintf("reference columns %s.%s and %s.%s have different SQL base types or signedness", child.Name(), name, parent.Name(), right.Name()), "Align the physical key types; also review sizes, collations, and custom Go representations")
		}
	}
	if !schemacompat.HasUniqueKey(parent, ref.ParentColumns) {
		add("REF003", check.SeverityError, "referenced columns lack an unconditional primary or unique key", "Constrain the referenced identity with a complete-column primary or unique key")
	}
	for _, side := range []struct {
		table   schema.Table
		columns []string
	}{{child, ref.ChildColumns}, {parent, ref.ParentColumns}} {
		if !schemacompat.HasIndexPrefix(side.table, side.columns) {
			add("REF004", check.SeverityWarning, fmt.Sprintf("table %s lacks a visible complete-column index prefix over (%s)", side.table.Name(), strings.Join(side.columns, ", ")), "Review reference lookup and audit plans before adding an index; a matching prefix does not guarantee low RU")
		}
	}
	return diagnostics
}

func referenceType(value string) string {
	switch value {
	case "INTEGER":
		return "INT"
	case "BOOL", "BOOLEAN":
		return "TINYINT"
	case "NUMERIC", "DEC":
		return "DECIMAL"
	default:
		return value
	}
}

func validateMapping(ref Reference) error {
	if !modelmeta.ValidSQLIdentifier(ref.ChildTable) || !modelmeta.ValidSQLIdentifier(ref.ParentTable) || len(ref.ChildColumns) == 0 || len(ref.ChildColumns) != len(ref.ParentColumns) {
		return fmt.Errorf("invalid reference table or key mapping")
	}
	for _, columns := range [][]string{ref.ChildColumns, ref.ParentColumns} {
		seen := make(map[string]bool, len(columns))
		for _, column := range columns {
			key := strings.ToLower(column)
			if !modelmeta.ValidSQLIdentifier(column) || seen[key] {
				return fmt.Errorf("invalid reference column mapping")
			}
			seen[key] = true
		}
	}
	return nil
}

// SQL returns a one-row boolean probe for physical orphan references. NULL in
// any child key component exempts that row. Soft-delete scopes are not applied.
// LIMIT bounds returned rows, not the amount of data scanned by TiDB.
func SQL(ref Reference) (string, error) {
	if err := validateMapping(ref); err != nil {
		return "", err
	}
	var query strings.Builder
	query.WriteString("SELECT EXISTS (SELECT 1 FROM `")
	query.WriteString(ref.ChildTable)
	query.WriteString("` AS c WHERE ")
	for i, column := range ref.ChildColumns {
		if i > 0 {
			query.WriteString(" AND ")
		}
		fmt.Fprintf(&query, "c.`%s` IS NOT NULL", column)
	}
	query.WriteString(" AND NOT EXISTS (SELECT 1 FROM `")
	query.WriteString(ref.ParentTable)
	query.WriteString("` AS p WHERE ")
	for i, column := range ref.ChildColumns {
		if i > 0 {
			query.WriteString(" AND ")
		}
		fmt.Fprintf(&query, "p.`%s` = c.`%s`", ref.ParentColumns[i], column)
	}
	query.WriteString(") LIMIT 1)")
	return query.String(), nil
}
