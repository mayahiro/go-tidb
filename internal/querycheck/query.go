package querycheck

import (
	"strconv"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
)

const (
	// CodeOffsetPagination reports a positive OFFSET that skips rows before a
	// page can be returned.
	CodeOffsetPagination = "QRY002"
	// CodeUnorderedPagination reports a positive LIMIT without ORDER BY.
	CodeUnorderedPagination = "QRY003"
	// CodeLeadingWildcardFilter reports a LIKE predicate without a fixed
	// leading literal.
	CodeLeadingWildcardFilter = "QRY004"
	// CodeRelationTopNFallback reports a collection-filtered ordered LIMIT that
	// could not use the relation-first compiler rewrite.
	CodeRelationTopNFallback = "QRY005"

	// PaginationReference documents TiDB pagination guidance.
	PaginationReference = "https://docs.pingcap.com/developer/dev-guide-paginate-results/"
	// LikeReference documents TiDB LIKE behavior.
	LikeReference = "https://docs.pingcap.com/tidb/stable/string-functions/#like"
	// RelationTopNReference documents TiDB TopN and Limit pushdown.
	RelationTopNReference = "https://docs.pingcap.com/tidb/stable/topn-limit-push-down/"
)

// Diagnostics applies bind-free query-pattern checks to one validated query
// shape. It returns diagnostics in stable rule order and does not access a
// database or physical schema.
func Diagnostics(shape queryshape.Query) []check.Diagnostic {
	returnsRows := !shape.Limit.Set || shape.Limit.Positive
	diagnosticCount := leadingWildcardDiagnosticCount(shape.Predicates)
	if returnsRows && shape.Offset.Set && shape.Offset.Positive {
		diagnosticCount++
	}
	if returnsRows && shape.Limit.Set && shape.Limit.Positive && len(shape.Order) == 0 {
		diagnosticCount++
	}
	if returnsRows && shape.Compiler.Rewrite == queryshape.CompilerRewriteRelationTopNFallback {
		diagnosticCount++
	}

	diagnostics := make([]check.Diagnostic, 0, diagnosticCount)
	if returnsRows && shape.Offset.Set && shape.Offset.Positive {
		diagnostics = append(diagnostics, OffsetPaginationDiagnostic(shape.Model, shape.Offset.Value))
	}
	if returnsRows && shape.Limit.Set && shape.Limit.Positive && len(shape.Order) == 0 {
		diagnostics = append(diagnostics, UnorderedPaginationDiagnostic(shape.Model))
	}
	if returnsRows && shape.Compiler.Rewrite == queryshape.CompilerRewriteRelationTopNFallback {
		diagnostics = append(diagnostics, relationTopNFallbackDiagnostic(
			shape.Model,
			shape.Compiler.Relation,
			shape.Compiler.Reason,
		))
	}
	return appendLeadingWildcardDiagnostics(diagnostics, shape.Predicates, shape.Model)
}

// OffsetPaginationDiagnostic describes one positive OFFSET use.
func OffsetPaginationDiagnostic(model string, offset int64) check.Diagnostic {
	message := "SELECT for " + model + " uses a positive OFFSET and skips rows before returning a page"
	if offset > 0 {
		message = "SELECT for " + model + " skips " + strconv.FormatInt(offset, 10) + " rows before returning a page"
	}
	return check.Diagnostic{
		Code:         CodeOffsetPagination,
		Severity:     check.SeverityWarning,
		Title:        "OFFSET pagination cost grows with the offset",
		Message:      message,
		Suggestion:   "Prefer SeekAfter keyset pagination when the application can retain a stable cursor",
		Suppressible: true,
		Reference:    PaginationReference,
	}
}

// UnorderedPaginationDiagnostic describes one positive LIMIT without ORDER BY.
func UnorderedPaginationDiagnostic(model string) check.Diagnostic {
	return check.Diagnostic{
		Code:         CodeUnorderedPagination,
		Severity:     check.SeverityWarning,
		Title:        "Pagination has no deterministic order",
		Message:      "SELECT for " + model + " applies LIMIT without ORDER BY",
		Suggestion:   "Add OrderBy unless returning an arbitrary subset is intentional",
		Suppressible: true,
		Reference:    PaginationReference,
	}
}

func relationTopNFallbackDiagnostic(model, relation, reason string) check.Diagnostic {
	message := "SELECT for " + model + " combines a collection relation with ORDER BY and LIMIT, but the compiler could not safely move TopN to the relation source"
	if relation != "" {
		message = "SELECT for " + model + " combines collection relation " + relation + " with ORDER BY and LIMIT, but the compiler could not safely move TopN to the relation source"
	}
	evidence := make([]check.Evidence, 0, 1)
	if reason != "" {
		evidence = append(evidence, check.Evidence{Message: "Relation-first TopN was not applied because " + reason})
	}
	return check.Diagnostic{
		Code:         CodeRelationTopNFallback,
		Severity:     check.SeverityWarning,
		Title:        "Relation-filter TopN uses the EXISTS fallback",
		Message:      message,
		Evidence:     evidence,
		Suggestion:   "Verify the fallback with Explain or ExplainAnalyze and make the relation target primary key and root ordering explicit when the same semantics allow it",
		Suppressible: true,
		Reference:    RelationTopNReference,
	}
}

func leadingWildcardDiagnosticCount(predicates []queryshape.Predicate) int {
	count := 0
	for index := range predicates {
		current := predicates[index]
		if current.Operator == queryshape.PredicateContains || current.Operator == queryshape.PredicateHasSuffix {
			count++
		}
		count += leadingWildcardDiagnosticCount(current.Children)
	}
	return count
}

func appendLeadingWildcardDiagnostics(diagnostics []check.Diagnostic, predicates []queryshape.Predicate, scope string) []check.Diagnostic {
	for index := range predicates {
		current := predicates[index]
		switch current.Operator {
		case queryshape.PredicateContains, queryshape.PredicateHasSuffix:
			diagnostics = append(diagnostics, LeadingWildcardDiagnostic(
				scope,
				current.Field,
				current.Operator == queryshape.PredicateHasSuffix,
			))
		case queryshape.PredicateHasRelation:
			diagnostics = appendLeadingWildcardDiagnostics(diagnostics, current.Children, scope+"."+current.Relation)
		default:
			diagnostics = appendLeadingWildcardDiagnostics(diagnostics, current.Children, scope)
		}
	}
	return diagnostics
}

// LeadingWildcardDiagnostic describes one Contains or HasSuffix predicate.
func LeadingWildcardDiagnostic(scope, field string, suffix bool) check.Diagnostic {
	constructor := "Contains"
	if suffix {
		constructor = "HasSuffix"
	}
	return check.Diagnostic{
		Code:         CodeLeadingWildcardFilter,
		Severity:     check.SeverityWarning,
		Title:        "LIKE predicate starts with a wildcard",
		Message:      constructor + " on " + scope + "." + field + " builds a LIKE pattern without a fixed leading literal",
		Suggestion:   "Use Equal or HasPrefix when the required matching semantics allow it, and verify the actual access path with Explain or ExplainAnalyze",
		Suppressible: true,
		Reference:    LikeReference,
	}
}
