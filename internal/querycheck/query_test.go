package querycheck

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
)

func TestDiagnosticsReportsBindFreeQueryPatternsInStableOrder(t *testing.T) {
	t.Parallel()

	shape := queryshape.Query{
		Model:  "Video",
		Limit:  queryshape.Bound{Set: true, Positive: true},
		Offset: queryshape.Bound{Set: true, Positive: true},
		Predicates: []queryshape.Predicate{
			{Operator: queryshape.PredicateContains, Field: "Title"},
			{
				Operator: queryshape.PredicateHasRelation,
				Relation: "Genres",
				Children: []queryshape.Predicate{{Operator: queryshape.PredicateHasSuffix, Field: "Name"}},
			},
		},
		Compiler: queryshape.CompilerDecision{
			Rewrite:  queryshape.CompilerRewriteRelationTopNFallback,
			Relation: "Genres",
			Reason:   "root order is not the primary key",
		},
	}
	diagnostics := Diagnostics(shape)
	wantCodes := []string{
		CodeOffsetPagination,
		CodeUnorderedPagination,
		CodeRelationTopNFallback,
		CodeLeadingWildcardFilter,
		CodeLeadingWildcardFilter,
	}
	if got := queryCheckCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if !strings.Contains(diagnostics[0].Message, "positive OFFSET") {
		t.Fatalf("offset diagnostic = %#v", diagnostics[0])
	}
	if !strings.Contains(diagnostics[2].Evidence[0].Message, "root order") {
		t.Fatalf("fallback diagnostic = %#v", diagnostics[2])
	}
	if !strings.Contains(diagnostics[3].Message, "Video.Title") || !strings.Contains(diagnostics[4].Message, "Video.Genres.Name") {
		t.Fatalf("wildcard diagnostics = %#v", diagnostics[3:])
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible {
			t.Fatalf("diagnostic = %#v, want suppressible warning", diagnostic)
		}
	}
}

func TestDiagnosticsUsesExactInProcessOffsetWithoutRequiringIt(t *testing.T) {
	t.Parallel()

	shape := queryshape.Query{
		Model:  "Video",
		Order:  []queryshape.OrderTerm{{Column: "id"}},
		Limit:  queryshape.Bound{Set: true, Positive: true, Value: 20},
		Offset: queryshape.Bound{Set: true, Positive: true, Value: 40},
	}
	diagnostics := Diagnostics(shape)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "skips 40 rows") {
		t.Fatalf("Diagnostics() = %#v, want exact local offset", diagnostics)
	}
}

func TestDiagnosticsSkipsPaginationAndFallbackWhenLimitIsZero(t *testing.T) {
	t.Parallel()

	shape := queryshape.Query{
		Model:  "Video",
		Limit:  queryshape.Bound{Set: true},
		Offset: queryshape.Bound{Set: true, Positive: true},
		Compiler: queryshape.CompilerDecision{
			Rewrite: queryshape.CompilerRewriteRelationTopNFallback,
		},
	}
	if diagnostics := Diagnostics(shape); diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("Diagnostics() = %#v, want non-nil empty", diagnostics)
	}
}

func BenchmarkDiagnostics(b *testing.B) {
	shape := queryshape.Query{
		Model:  "Video",
		Limit:  queryshape.Bound{Set: true, Positive: true},
		Offset: queryshape.Bound{Set: true, Positive: true},
		Predicates: []queryshape.Predicate{{
			Operator: queryshape.PredicateContains,
			Field:    "Title",
		}},
	}
	var diagnostics []check.Diagnostic
	b.ReportAllocs()
	for b.Loop() {
		diagnostics = Diagnostics(shape)
	}
	queryCheckDiagnosticSink = diagnostics
}

func queryCheckCodes(diagnostics []check.Diagnostic) []string {
	codes := make([]string, len(diagnostics))
	for index := range diagnostics {
		codes[index] = diagnostics[index].Code
	}
	return codes
}

var queryCheckDiagnosticSink []check.Diagnostic
