package sourcecheck

import (
	"fmt"
	"go/token"
	"sort"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/schemacompat"
	"github.com/mayahiro/go-tidb/schema"
)

const codeUncertainSchemaModel = "SRC002"

func (analyzer *sourceAnalyzer) noteSchemaModel(key sourceTypeKey, position token.Pos) {
	if analyzer.schemaModels == nil || key.name == "" {
		return
	}
	if _, exists := analyzer.schemaModels[key]; !exists {
		analyzer.schemaModels[key] = position
	}
}

func (analyzer *sourceAnalyzer) recordSchemaModels() {
	for key, model := range analyzer.models {
		if model.physical != nil && model.physical.declared {
			analyzer.noteSchemaModel(key, model.structure.Pos())
		}
	}
	analyzer.expandSchemaModels()
	statistics := &analyzer.analysis.Statistics
	statistics.SchemaModels = len(analyzer.schemaModels)
	if analyzer.configuration.catalog == nil {
		statistics.UncertainSchemaModels = statistics.SchemaModels
		analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, check.Diagnostic{
			Code:       "CMP001",
			Severity:   check.SeverityError,
			Title:      "Schema catalog is unavailable",
			Message:    "schema compatibility requires a non-nil catalog returned by schema.Parse",
			Suggestion: "Parse a self-contained TiDB CREATE TABLE snapshot before running the compatibility check",
		})
		return
	}
	keys := make([]sourceTypeKey, 0, len(analyzer.schemaModels))
	for key := range analyzer.schemaModels {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].packagePath != keys[j].packagePath {
			return keys[i].packagePath < keys[j].packagePath
		}
		return keys[i].name < keys[j].name
	})
	for _, key := range keys {
		model := analyzer.models[key]
		if model == nil || model.physical == nil || model.ambiguous || model.physical.ambiguous || len(model.fields) == 0 {
			statistics.UncertainSchemaModels++
			position := analyzer.schemaModels[key]
			if model != nil {
				position = model.structure.Pos()
			}
			analyzer.appendSchemaDiagnostic(check.Diagnostic{
				Code:         codeUncertainSchemaModel,
				Severity:     check.SeverityInfo,
				Title:        "Source model schema check is incomplete",
				Message:      fmt.Sprintf("model %s has no complete table, column, and key mapping resolvable from the analyzed source", key.name),
				Suggestion:   "Include the model source and use directly mapped structs, or run check.Schema with application types to validate aliases, embedded fields, and other unresolved mappings",
				Suppressible: true,
			}, position, schema.Position{})
			continue
		}
		statistics.AnalyzedSchemaModels++
		analyzer.checkSchemaModel(model)
	}
	analyzer.recordSchemaRelations(keys)
}

func (analyzer *sourceAnalyzer) checkSchemaModel(model *sourceModel) {
	table, exists := analyzer.configuration.catalog.Table(model.physical.table)
	if !exists {
		analyzer.appendSchemaDiagnostic(check.Diagnostic{
			Code:       "CMP002",
			Severity:   check.SeverityError,
			Title:      "Model table is missing",
			Message:    fmt.Sprintf("model %s maps to table %q, but that table is absent from the SQL snapshot", model.name, model.physical.table),
			Suggestion: "Use a snapshot containing this table, or correct the model table mapping",
		}, model.structure.Pos(), schema.Position{})
		return
	}

	mapped := make(map[string]struct{}, len(model.fields))
	for _, field := range model.fields {
		column := model.physical.columns[field]
		mapped[strings.ToLower(column)] = struct{}{}
		if _, exists := table.Column(column); !exists {
			analyzer.appendSchemaDiagnostic(check.Diagnostic{
				Code:       "CMP003",
				Severity:   check.SeverityError,
				Title:      "Model column is missing",
				Message:    fmt.Sprintf("%s.%s maps to column %q, but table %q does not contain it", model.name, field, column, table.Name()),
				Suggestion: "Correct the field mapping or add the physical column before using this model",
			}, sourceSchemaFieldPosition(model, field), table.Position())
		}
	}
	if columns := sourceSchemaKeyColumns(model, model.primaryFields); len(columns) != 0 && !schemacompat.EqualColumns(columns, table.PrimaryKeyColumns()) {
		analyzer.appendSchemaDiagnostic(check.Diagnostic{
			Code:       "CMP007",
			Severity:   check.SeverityError,
			Title:      "Primary key mapping does not match",
			Message:    fmt.Sprintf("model %s declares primary key (%s), but table %s declares (%s)", model.name, strings.Join(columns, ", "), table.Name(), strings.Join(table.PrimaryKeyColumns(), ", ")),
			Suggestion: "Make the ordered pk fields match the physical primary key",
		}, sourceSchemaFieldPosition(model, model.primaryFields[0]), table.Position())
	}
	for _, key := range model.uniqueKeys {
		columns := sourceSchemaKeyColumns(model, key.fields)
		if schemacompat.HasUniqueKey(table, columns) {
			continue
		}
		analyzer.appendSchemaDiagnostic(check.Diagnostic{
			Code:       "CMP015",
			Severity:   check.SeverityError,
			Title:      "Candidate unique key is not physically constrained",
			Message:    fmt.Sprintf("model %s declares candidate unique key %q over (%s), but table %s has no unconditional primary or unique key proving that combination", model.name, key.name, strings.Join(columns, ", "), table.Name()),
			Suggestion: "Add a matching primary or unique key to the SQL schema, or correct the model unique group",
			Reference:  "https://docs.pingcap.com/tidb/stable/constraints/",
		}, sourceSchemaFieldPosition(model, key.fields[0]), table.Position())
	}
	for _, column := range table.Columns() {
		if _, exists := mapped[strings.ToLower(column.Name())]; exists || column.Nullable() || column.HasDefault() || column.DatabaseGenerated() {
			continue
		}
		analyzer.appendSchemaDiagnostic(check.Diagnostic{
			Code:         "CMP010",
			Severity:     check.SeverityWarning,
			Title:        "Required database column is absent from the model",
			Message:      fmt.Sprintf("%s.%s is NOT NULL without a default or database generation, but model %s does not map it", table.Name(), column.Name(), model.name),
			Suggestion:   "Map the column when this model inserts rows, or suppress this diagnostic for an intentionally read-only or partial model",
			Suppressible: true,
		}, model.structure.Pos(), column.Position())
	}
}

func sourceSchemaKeyColumns(model *sourceModel, fields []string) []string {
	columns := make([]string, len(fields))
	for i, field := range fields {
		columns[i] = model.physical.columns[field]
	}
	return columns
}

func sourceSchemaFieldPosition(model *sourceModel, name string) token.Pos {
	for _, field := range model.structure.Fields.List {
		for _, identifier := range field.Names {
			if identifier.Name == name {
				return identifier.Pos()
			}
		}
	}
	return model.structure.Pos()
}

func (analyzer *sourceAnalyzer) appendSchemaDiagnostic(diagnostic check.Diagnostic, position token.Pos, physical schema.Position) {
	diagnostic.Location = analyzer.sourceLocation(position)
	if physical.Line != 0 {
		diagnostic.Evidence = []check.Evidence{{
			Message:  "SQL snapshot declaration",
			Location: check.Location{Line: physical.Line, Column: physical.Column},
		}}
	}
	// Model checks run once per distinct model. Unlike query-pattern diagnostics,
	// multiple missing columns or keys may legitimately share one source position.
	analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, diagnostic)
}
