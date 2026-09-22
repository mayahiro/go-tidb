package sourcecheck

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/modelmeta"
	"github.com/mayahiro/go-tidb/internal/referencecheck"
	"github.com/mayahiro/go-tidb/internal/schemacompat"
	"github.com/mayahiro/go-tidb/schema"
)

func sourceSchemaRelationFields(model *sourceModel) []*ast.Field {
	if model == nil || model.physical == nil {
		return nil
	}
	return model.physical.relations
}

func (analyzer *sourceAnalyzer) expandSchemaModels() {
	var queue []sourceTypeKey
	for key := range analyzer.schemaModels {
		queue = append(queue, key)
	}
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].packagePath != queue[j].packagePath {
			return queue[i].packagePath < queue[j].packagePath
		}
		return queue[i].name < queue[j].name
	})
	for i := 0; i < len(queue); i++ {
		model := analyzer.models[queue[i]]
		for _, field := range sourceSchemaRelationFields(model) {
			target, _, ok := sourceRelationFieldShape(model.file, field.Type)
			if !ok {
				continue
			}
			if _, exists := analyzer.schemaModels[target]; !exists {
				analyzer.noteSchemaModel(target, field.Pos())
				queue = append(queue, target)
			}
		}
	}
}

func (analyzer *sourceAnalyzer) recordSchemaRelations(keys []sourceTypeKey) {
	for _, key := range keys {
		model := analyzer.models[key]
		for _, field := range sourceSchemaRelationFields(model) {
			stats := &analyzer.analysis.Statistics
			stats.SchemaRelations++
			fieldName := "<embedded>"
			if len(field.Names) > 0 {
				fieldName = field.Names[0].Name
			}
			relation, ok := analyzer.resolveSourceRelation(key, fieldName)
			target := analyzer.models[relation.target]
			if !ok || model.physical == nil || model.physical.ambiguous ||
				target == nil || target.physical == nil || target.physical.ambiguous ||
				relation.kind == modelmeta.RelationManyToMany && relation.junctionTable == "" {
				stats.UncertainSchemaRelations++
				analyzer.appendSchemaDiagnostic(check.Diagnostic{
					Code: "SRC003", Severity: check.SeverityInfo,
					Title:        "Source relation schema check is incomplete",
					Message:      fmt.Sprintf("%s.%s has no fully resolved reference mapping", model.name, fieldName),
					Suggestion:   "Include source and target model declarations and resolve relation keys before auditing data",
					Suppressible: true,
				}, field.Pos(), schema.Position{})
				continue
			}
			stats.AnalyzedSchemaRelations++
			location := analyzer.sourceLocation(field.Pos())
			prefix := filepath.ToSlash(filepath.Dir(location.Path))
			if prefix == "." {
				prefix = ""
			} else {
				prefix += "/"
			}
			name := prefix + model.name + "." + relation.name
			sourceColumns := sourceSchemaKeyColumns(model, relation.sourceFields)
			targetColumns := sourceSchemaKeyColumns(target, relation.targetFields)
			var refs []referencecheck.Reference
			switch relation.kind {
			case modelmeta.RelationBelongsTo:
				refs = append(refs, referencecheck.Reference{
					Name: name, Location: location,
					ChildTable: model.physical.table, ChildColumns: sourceColumns,
					ParentTable: target.physical.table, ParentColumns: targetColumns,
				})
			case modelmeta.RelationHasOne, modelmeta.RelationHasMany:
				refs = append(refs, referencecheck.Reference{
					Name: name, Location: location,
					ChildTable: target.physical.table, ChildColumns: targetColumns,
					ParentTable: model.physical.table, ParentColumns: sourceColumns,
				})
				if relation.kind == modelmeta.RelationHasOne {
					analyzer.checkRelationUnique(field, name, target.physical.table, targetColumns, false)
				}
			case modelmeta.RelationManyToMany:
				refs = append(refs, referencecheck.Reference{
					Name: name + "#source", Location: location,
					ChildTable: relation.junctionTable, ChildColumns: relation.junctionSourceColumns,
					ParentTable: model.physical.table, ParentColumns: sourceColumns,
				}, referencecheck.Reference{
					Name: name + "#target", Location: location,
					ChildTable: relation.junctionTable, ChildColumns: relation.junctionTargetColumns,
					ParentTable: target.physical.table, ParentColumns: targetColumns,
				})
				tag, _, _ := sourceModelTag(field)
				declaration, _ := modelmeta.ParseRelation(tag, true)
				if declaration.Via == "" {
					pair := append(append([]string(nil), relation.junctionSourceColumns...), relation.junctionTargetColumns...)
					analyzer.checkRelationUnique(field, name, relation.junctionTable, pair, true)
				}
			}
			for _, ref := range refs {
				analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, referencecheck.Validate(analyzer.configuration.catalog, ref)...)
				analyzer.analysis.References = append(analyzer.analysis.References, ref)
			}
		}
	}
}

func (analyzer *sourceAnalyzer) checkRelationUnique(field *ast.Field, name, tableName string, columns []string, exact bool) {
	table, exists := analyzer.configuration.catalog.Table(tableName)
	if !exists {
		// The reference check reports missing inputs.
		return
	}
	unique := schemacompat.HasUniqueKey(table, columns)
	if exact {
		unique = false
		for _, index := range table.Indexes() {
			if !index.ProvidesUnconditionalUniqueness() || len(index.Columns()) != len(columns) {
				continue
			}
			match := true
			for _, part := range index.Columns() {
				found := false
				for _, column := range columns {
					if strings.EqualFold(part, column) {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if match {
				unique = true
				break
			}
		}
	}
	if !unique {
		code := "CMP011"
		if exact {
			code = "CMP012"
		}
		analyzer.appendSchemaDiagnostic(check.Diagnostic{
			Code: code, Severity: check.SeverityError,
			Title:      "Relation cardinality lacks a physical unique key",
			Message:    fmt.Sprintf("%s requires a unique key over %s(%s)", name, tableName, strings.Join(columns, ", ")),
			Suggestion: "Constrain to-one identity or the exact pure junction pair in the SQL schema",
		}, field.Pos(), table.Position())
	}
}
