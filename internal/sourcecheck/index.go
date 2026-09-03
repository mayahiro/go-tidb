package sourcecheck

import (
	"go/ast"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/modelmeta"
	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/queryshape"
)

func (analyzer *sourceAnalyzer) recordSchemaIndexPattern(
	call *ast.CallExpr,
	summary sourceQuerySummary,
	relationTopN sourceRelationTopNAnalysis,
) {
	pattern := summary.pattern
	if pattern.limit.state != sourceBoundPositive || pattern.order == sourceOrderAbsent {
		return
	}
	analyzer.analysis.Statistics.IndexPatterns++

	var access queryshape.IndexAccess
	var ok bool
	if relationTopN.candidate {
		if relationTopN.exact && relationTopN.decision.Rewrite == queryshape.CompilerRewriteRelationTopN {
			access, ok = analyzer.sourceRelationTopNIndexAccess(summary, relationTopN)
		}
	} else {
		access, ok = analyzer.sourceRootIndexAccess(summary)
	}
	if !ok {
		analyzer.analysis.Statistics.UncertainIndexPatterns++
		return
	}
	analyzer.analysis.Statistics.AnalyzedIndexPatterns++
	diagnostics := querycheck.IndexAccessDiagnostics(
		summary.model.name,
		[]queryshape.IndexAccess{access},
		analyzer.configuration.catalog,
	)
	for _, diagnostic := range diagnostics {
		schemaLocation := diagnostic.Location
		diagnostic.Location = analyzer.sourceLocation(call.Pos())
		if schemaLocation.Line != 0 || schemaLocation.Column != 0 {
			diagnostic.Evidence = append(diagnostic.Evidence, check.Evidence{
				Message:  "Schema table declaration",
				Location: schemaLocation,
			})
		}
		analyzer.appendPatternDiagnostic(diagnostic)
	}
}

func (analyzer *sourceAnalyzer) sourceRelationTopNIndexAccess(
	summary sourceQuerySummary,
	analysis sourceRelationTopNAnalysis,
) (queryshape.IndexAccess, bool) {
	pattern := summary.pattern
	if analysis.relation.kind == modelmeta.RelationManyToMany {
		if !analysis.predicate.indexExact || pattern.orderTerms.count == 0 || !sourceOrderTermsUniform(pattern.orderTerms) ||
			analysis.relation.junctionTable == "" || len(analysis.relation.junctionSourceColumns) == 0 ||
			len(analysis.relation.junctionTargetColumns) == 0 {
			return queryshape.IndexAccess{}, false
		}
		return queryshape.IndexAccess{
			Kind:            queryshape.IndexAccessRelationTopN,
			Table:           analysis.relation.junctionTable,
			Relation:        analysis.relation.name,
			EqualityColumns: append([]string(nil), analysis.relation.junctionTargetColumns...),
			OrderColumns:    append([]string(nil), analysis.relation.junctionSourceColumns...),
		}, true
	}
	target, exists := analyzer.models[analysis.relation.target]
	if !exists || target.physical == nil || target.physical.ambiguous || !analysis.predicate.indexExact ||
		pattern.orderTerms.count == 0 || !sourceOrderTermsUniform(pattern.orderTerms) {
		return queryshape.IndexAccess{}, false
	}

	equalityColumns := make([]string, 0, len(analysis.predicate.equalityFields)+1)
	for _, field := range analysis.predicate.equalityFields {
		column, exists := target.physical.columns[field]
		if !exists {
			return queryshape.IndexAccess{}, false
		}
		equalityColumns = appendSourceColumn(equalityColumns, column)
	}
	if target.physical.softDeleteColumn != "" {
		equalityColumns = appendSourceColumn(equalityColumns, target.physical.softDeleteColumn)
	}
	orderColumns := make([]string, 0, len(analysis.relation.targetFields))
	for _, field := range analysis.relation.targetFields {
		column, exists := target.physical.columns[field]
		if !exists {
			return queryshape.IndexAccess{}, false
		}
		orderColumns = append(orderColumns, column)
	}
	return queryshape.IndexAccess{
		Kind:            queryshape.IndexAccessRelationTopN,
		Table:           target.physical.table,
		Relation:        analysis.relation.name,
		EqualityColumns: equalityColumns,
		OrderColumns:    orderColumns,
	}, true
}

func (analyzer *sourceAnalyzer) sourceRootIndexAccess(summary sourceQuerySummary) (queryshape.IndexAccess, bool) {
	model, exists := analyzer.models[summary.model]
	pattern := summary.pattern
	index := pattern.index
	if !exists || model.physical == nil || model.physical.ambiguous || index == nil || !index.indexPredicatesKnown ||
		!pattern.orderTermsKnown || pattern.withDeleted == sourceToggleUnknown ||
		pattern.orderTerms.count == 0 || !sourceOrderTermsUniform(pattern.orderTerms) {
		return queryshape.IndexAccess{}, false
	}

	equalityColumns := make([]string, 0, len(index.equalityFields)+1)
	for _, field := range index.equalityFields {
		column, exists := model.physical.columns[field]
		if !exists {
			return queryshape.IndexAccess{}, false
		}
		equalityColumns = appendSourceColumn(equalityColumns, column)
	}
	if model.physical.softDeleteColumn != "" && pattern.withDeleted != sourceTogglePresent {
		equalityColumns = appendSourceColumn(equalityColumns, model.physical.softDeleteColumn)
	}

	orderColumns := make([]string, 0, pattern.orderTerms.count)
	for index := 0; index < pattern.orderTerms.count; index++ {
		term := pattern.orderTerms.at(index)
		column, exists := model.physical.columns[term.field]
		if !exists {
			return queryshape.IndexAccess{}, false
		}
		orderColumns = append(orderColumns, column)
	}
	return queryshape.IndexAccess{
		Kind:            queryshape.IndexAccessRootOrderedLimit,
		Table:           model.physical.table,
		EqualityColumns: equalityColumns,
		OrderColumns:    orderColumns,
	}, true
}

func sourceOrderTermsUniform(terms sourceOrderTerms) bool {
	direction := terms.first.direction
	for index := 1; index < terms.count; index++ {
		if terms.at(index).direction != direction {
			return false
		}
	}
	return true
}

func appendSourceColumn(columns []string, column string) []string {
	for _, existing := range columns {
		if strings.EqualFold(existing, column) {
			return columns
		}
	}
	return append(columns, column)
}
