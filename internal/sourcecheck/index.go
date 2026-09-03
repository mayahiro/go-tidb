package sourcecheck

import (
	"go/ast"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/queryshape"
)

func (analyzer *sourceAnalyzer) recordSchemaIndexPattern(call *ast.CallExpr, summary sourceQuerySummary) {
	pattern := summary.pattern
	if pattern.limit.state != sourceBoundPositive || pattern.order == sourceOrderAbsent {
		return
	}
	analyzer.analysis.Statistics.IndexPatterns++

	access, ok := analyzer.sourceRootIndexAccess(summary)
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

func (analyzer *sourceAnalyzer) sourceRootIndexAccess(summary sourceQuerySummary) (queryshape.IndexAccess, bool) {
	model, exists := analyzer.models[summary.model]
	pattern := summary.pattern
	index := pattern.index
	if !exists || model.physical == nil || model.physical.ambiguous || index == nil || !index.indexPredicatesKnown ||
		!index.orderTermsKnown || index.withDeleted == sourceToggleUnknown ||
		len(index.orderTerms) == 0 || !sourceOrderTermsUniform(index.orderTerms) {
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
	if model.physical.softDeleteColumn != "" && index.withDeleted != sourceTogglePresent {
		equalityColumns = appendSourceColumn(equalityColumns, model.physical.softDeleteColumn)
	}

	orderColumns := make([]string, 0, len(index.orderTerms))
	for _, term := range index.orderTerms {
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

func sourceOrderTermsUniform(terms []sourceOrderTerm) bool {
	direction := terms[0].direction
	for _, term := range terms[1:] {
		if term.direction != direction {
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
