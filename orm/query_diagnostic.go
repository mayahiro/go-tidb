package orm

import (
	"strconv"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/model"
)

const (
	codeInvalidQuery          = "QRY001"
	codeOffsetPagination      = "QRY002"
	codeUnorderedPagination   = "QRY003"
	codeLeadingWildcardFilter = "QRY004"
	codeRelationTopNFallback  = "QRY005"

	paginationReference   = "https://docs.pingcap.com/developer/dev-guide-paginate-results/"
	likeReference         = "https://docs.pingcap.com/tidb/stable/string-functions/#like"
	relationTopNReference = "https://docs.pingcap.com/tidb/stable/topn-limit-push-down/"
)

// Diagnostics checks the current SELECT builder without accessing a database
// or executing custom driver.Valuer implementations.
//
// It first applies the same metadata and query validation as Build, then
// reports valid query shapes that can be expensive or nondeterministic. The
// result is ordered deterministically and is a non-nil empty slice when no
// diagnostics are found. Terminal-specific behavior is outside this builder
// check because the same SelectQuery can be passed to multiple terminals.
func (q *SelectQuery[T]) Diagnostics() []check.Diagnostic {
	diagnostics := make([]check.Diagnostic, 0)
	compiled, err := q.compile()
	if err != nil {
		return append(diagnostics, check.Diagnostic{
			Code:         codeInvalidQuery,
			Severity:     check.SeverityError,
			Title:        "Invalid SELECT query",
			Message:      err.Error(),
			Suggestion:   "Fix the model metadata or query structure before executing this builder",
			Suppressible: false,
		})
	}

	selection := &q.selection
	modelName := compiled.statement.scanPlan.modelType.Name()
	returnsRows := !selection.pagination.limitSet || selection.pagination.limit > 0
	relationTopN := relationTopNAnalysis{}
	if descriptor, describeErr := model.DescribeType(compiled.statement.scanPlan.modelType); describeErr == nil {
		relationTopN, _ = analyzeRelationTopN(descriptor, selection)
	}
	relationTopNFallback := returnsRows && relationTopN.candidate && !relationTopN.optimized
	diagnosticCount := leadingWildcardDiagnosticCount(selection.predicates)
	if returnsRows && selection.pagination.offsetSet && selection.pagination.offset > 0 {
		diagnosticCount++
	}
	if returnsRows && selection.pagination.limitSet && len(selection.orderBy) == 0 {
		diagnosticCount++
	}
	if relationTopNFallback {
		diagnosticCount++
	}
	diagnostics = make([]check.Diagnostic, 0, diagnosticCount)
	if returnsRows && selection.pagination.offsetSet && selection.pagination.offset > 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codeOffsetPagination,
			Severity:     check.SeverityWarning,
			Title:        "OFFSET pagination cost grows with the offset",
			Message:      "SELECT for " + modelName + " skips " + strconv.FormatInt(selection.pagination.offset, 10) + " rows before returning a page",
			Suggestion:   "Prefer SeekAfter keyset pagination when the application can retain a stable cursor",
			Suppressible: true,
			Reference:    paginationReference,
		})
	}
	if returnsRows && selection.pagination.limitSet && len(selection.orderBy) == 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codeUnorderedPagination,
			Severity:     check.SeverityWarning,
			Title:        "Pagination has no deterministic order",
			Message:      "SELECT for " + modelName + " applies LIMIT without ORDER BY",
			Suggestion:   "Add OrderBy unless returning an arbitrary subset is intentional",
			Suppressible: true,
			Reference:    paginationReference,
		})
	}
	if relationTopNFallback {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:     codeRelationTopNFallback,
			Severity: check.SeverityWarning,
			Title:    "Relation-filter TopN uses the EXISTS fallback",
			Message: "SELECT for " + modelName + " combines collection relation " + relationTopN.relationName +
				" with ORDER BY and LIMIT, but the compiler could not safely move TopN to the relation source",
			Evidence: []check.Evidence{{
				Message: "Relation-first TopN was not applied because " + relationTopN.reason,
			}},
			Suggestion:   "Verify the fallback with Explain or ExplainAnalyze and make the relation target primary key and root ordering explicit when the same semantics allow it",
			Suppressible: true,
			Reference:    relationTopNReference,
		})
	}
	return appendLeadingWildcardDiagnostics(diagnostics, selection.predicates, modelName)
}

func leadingWildcardDiagnosticCount(predicates []predicate) int {
	count := 0
	for index := range predicates {
		current := predicates[index]
		if current.operator == predicateContains || current.operator == predicateHasSuffix {
			count++
		}
		count += leadingWildcardDiagnosticCount(current.children)
	}
	return count
}

func appendLeadingWildcardDiagnostics(diagnostics []check.Diagnostic, predicates []predicate, scope string) []check.Diagnostic {
	for index := range predicates {
		current := predicates[index]
		switch current.operator {
		case predicateContains, predicateHasSuffix:
			constructor := "Contains"
			if current.operator == predicateHasSuffix {
				constructor = "HasSuffix"
			}
			diagnostics = append(diagnostics, check.Diagnostic{
				Code:         codeLeadingWildcardFilter,
				Severity:     check.SeverityWarning,
				Title:        "LIKE predicate starts with a wildcard",
				Message:      constructor + " on " + scope + "." + current.field + " builds a LIKE pattern without a fixed leading literal",
				Suggestion:   "Use Equal or HasPrefix when the required matching semantics allow it, and verify the actual access path with Explain or ExplainAnalyze",
				Suppressible: true,
				Reference:    likeReference,
			})
		case predicateHasRelation:
			diagnostics = appendLeadingWildcardDiagnostics(diagnostics, current.children, scope+"."+current.field)
		default:
			diagnostics = appendLeadingWildcardDiagnostics(diagnostics, current.children, scope)
		}
	}
	return diagnostics
}
