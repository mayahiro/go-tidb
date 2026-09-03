// Package querycheck applies offline checks to neutral query shapes.
package querycheck

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const (
	// CodeIndexCheckUnavailable reports invalid physical input needed by the
	// schema-aware query check.
	CodeIndexCheckUnavailable = "QRY006"
	// CodeMissingIndexPrefix reports an ordered limited access without a
	// matching default-usable direct-column physical index prefix.
	CodeMissingIndexPrefix = "QRY007"

	indexReference = "https://docs.pingcap.com/developer/dev-guide-index-best-practice/"
)

// IndexDiagnostics compares high-confidence ordered accesses in shape with a
// parsed physical schema without claiming which access path TiDB will choose.
func IndexDiagnostics(shape queryshape.Query, catalog *physicalschema.Catalog) []check.Diagnostic {
	var evidence []check.Evidence
	if catalog == nil || len(shape.IndexAccesses) != 0 {
		evidence = fingerprintEvidence(shape.Fingerprint())
	}
	return indexAccessDiagnostics(shape.Model, shape.IndexAccesses, catalog, evidence)
}

// IndexAccessDiagnostics compares neutral ordered accesses with a parsed
// physical schema without requiring a complete executable query shape.
func IndexAccessDiagnostics(model string, accesses []queryshape.IndexAccess, catalog *physicalschema.Catalog) []check.Diagnostic {
	return indexAccessDiagnostics(model, accesses, catalog, nil)
}

func indexAccessDiagnostics(
	model string,
	accesses []queryshape.IndexAccess,
	catalog *physicalschema.Catalog,
	evidence []check.Evidence,
) []check.Diagnostic {
	diagnostics := make([]check.Diagnostic, 0, len(accesses))
	if catalog == nil {
		return append(diagnostics, unavailableDiagnostic(
			model,
			"schema-aware query diagnostics require a non-nil catalog returned by schema.Parse",
			check.Location{},
			evidence,
		))
	}

	for index := range accesses {
		access := accesses[index]
		table, exists := catalog.Table(access.Table)
		if !exists {
			diagnostics = append(diagnostics, unavailableDiagnostic(
				model,
				fmt.Sprintf("query access table %q is absent from the SQL snapshot", access.Table),
				check.Location{},
				evidence,
			))
			continue
		}
		missing := missingTableColumns(table, access)
		if len(missing) != 0 {
			diagnostics = append(diagnostics, unavailableDiagnostic(
				model,
				fmt.Sprintf("query access columns (%s) are absent from table %q", strings.Join(missing, ", "), table.Name()),
				schemaLocation(table.Position()),
				evidence,
			))
			continue
		}
		if tableHasAccessIndex(table, access) {
			continue
		}
		diagnostics = append(diagnostics, missingIndexDiagnostic(
			model,
			table,
			access,
			requiredIndexColumns(access),
			evidence,
		))
	}
	return diagnostics
}

func unavailableDiagnostic(model, message string, location check.Location, evidence []check.Evidence) check.Diagnostic {
	return check.Diagnostic{
		Code:       CodeIndexCheckUnavailable,
		Severity:   check.SeverityError,
		Title:      "Query index check is unavailable",
		Message:    "SELECT for " + model + " cannot compare its ordered access with the physical schema because " + message,
		Evidence:   append([]check.Evidence(nil), evidence...),
		Suggestion: "Use a self-contained schema snapshot containing every table and column needed by each analyzed ordered access",
		Location:   location,
	}
}

func missingIndexDiagnostic(
	model string,
	table physicalschema.Table,
	access queryshape.IndexAccess,
	columns []string,
	evidence []check.Evidence,
) check.Diagnostic {
	accessName := "root SELECT"
	if access.Kind == queryshape.IndexAccessRelationTopN {
		accessName = "relation-first TopN for " + model + "." + access.Relation
	}
	return check.Diagnostic{
		Code:     CodeMissingIndexPrefix,
		Severity: check.SeverityWarning,
		Title:    "Ordered limited access has no matching index prefix",
		Message: accessName + " filters and orders " + table.Name() +
			", but the SQL snapshot has no default-usable direct-column index whose prefix covers (" + strings.Join(columns, ", ") + ")",
		Evidence: append([]check.Evidence{{
			Message: "Candidate index prefix: " + table.Name() + "(" + strings.Join(columns, ", ") + ")",
		}}, evidence...),
		Suggestion:   "Verify the generated query with ExplainAnalyze and add this prefix when the observed plan scans unnecessary rows",
		Location:     schemaLocation(table.Position()),
		Suppressible: true,
		Reference:    indexReference,
	}
}

func fingerprintEvidence(fingerprint string) []check.Evidence {
	return []check.Evidence{{Message: "Query fingerprint: " + fingerprint}}
}

func requiredIndexColumns(access queryshape.IndexAccess) []string {
	columns := make([]string, 0, len(access.EqualityColumns)+len(access.OrderColumns))
	for _, column := range access.EqualityColumns {
		columns = appendIdentifier(columns, column)
	}
	for _, column := range access.OrderColumns {
		columns = appendIdentifier(columns, column)
	}
	return columns
}

func missingTableColumns(table physicalschema.Table, access queryshape.IndexAccess) []string {
	var missing []string
	for _, column := range access.EqualityColumns {
		if _, exists := table.Column(column); !exists {
			missing = appendIdentifier(missing, column)
		}
	}
	for _, column := range access.OrderColumns {
		if _, exists := table.Column(column); !exists {
			missing = appendIdentifier(missing, column)
		}
	}
	return missing
}

func tableHasAccessIndex(table physicalschema.Table, access queryshape.IndexAccess) bool {
	equalityColumns := uniqueIdentifiers(access.EqualityColumns)
	orderColumns := make([]string, 0, len(access.OrderColumns))
	for _, column := range access.OrderColumns {
		if !containsIdentifier(equalityColumns, column) {
			orderColumns = appendIdentifier(orderColumns, column)
		}
	}
	requiredLength := len(equalityColumns) + len(orderColumns)
	if requiredLength == 0 {
		return false
	}

	for _, index := range table.Indexes() {
		if !index.SupportsDefaultColumnLookup() {
			continue
		}
		columns := index.Columns()
		if index.ProvidesUnconditionalUniqueness() && len(columns) != 0 && identifiersContainAll(equalityColumns, columns) {
			return true
		}
		if len(columns) < requiredLength || !equalIdentifierSets(columns[:len(equalityColumns)], equalityColumns) {
			continue
		}
		matched := true
		for orderIndex := range orderColumns {
			if !strings.EqualFold(columns[len(equalityColumns)+orderIndex], orderColumns[orderIndex]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func identifiersContainAll(values, required []string) bool {
	for _, value := range required {
		if !containsIdentifier(values, value) {
			return false
		}
	}
	return true
}

func uniqueIdentifiers(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = appendIdentifier(result, value)
	}
	return result
}

func appendIdentifier(values []string, value string) []string {
	if containsIdentifier(values, value) {
		return values
	}
	return append(values, value)
}

func containsIdentifier(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func equalIdentifierSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make([]string, 0, len(left))
	for _, value := range left {
		if containsIdentifier(seen, value) || !containsIdentifier(right, value) {
			return false
		}
		seen = append(seen, value)
	}
	return true
}

func schemaLocation(position physicalschema.Position) check.Location {
	return check.Location{Line: position.Line, Column: position.Column}
}
