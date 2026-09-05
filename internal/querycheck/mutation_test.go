package querycheck

import (
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

func TestMutationIndexDiagnostics(t *testing.T) {
	id := mutationTestPredicate(queryshape.PredicateEqual, "id")
	a := mutationTestPredicate(queryshape.PredicateEqual, "a")
	b := mutationTestPredicate(queryshape.PredicateLessThan, "b")
	empty := mutationTestPredicate(queryshape.PredicateIn, "unreferenced_column")
	empty.EmptyList = true
	all := empty
	all.Operator = queryshape.PredicateNotIn
	for _, test := range []struct {
		name       string
		indexes    string
		predicates []queryshape.MutationPredicate
		softDelete string
		status     MutationIndexStatus
		code       string
	}{
		{"primary_with_residual_or", "PRIMARY KEY (id)", []queryshape.MutationPredicate{id, mutationTestLogical(queryshape.PredicateOr, a, b)}, "", MutationIndexChecked, ""},
		{"composite_first_column", "KEY ab (a,b)", []queryshape.MutationPredicate{a}, "", MutationIndexChecked, ""},
		{"composite_missing_first_column", "KEY ab (a,b)", []queryshape.MutationPredicate{b}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"range_on_first_column", "KEY ba (b,a)", []queryshape.MutationPredicate{b, a}, "", MutationIndexChecked, ""},
		{"additional_unindexed_filter", "KEY by_a (a)", []queryshape.MutationPredicate{a, b}, "", MutationIndexChecked, ""},
		{"null", "KEY by_deleted (deleted_at)", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateIsNull, "deleted_at")}, "", MutationIndexChecked, ""},
		{"in", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateIn, "a")}, "", MutationIndexChecked, ""},
		{"between", "KEY by_b (b)", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateBetween, "b")}, "", MutationIndexChecked, ""},
		{"no_index", "", []queryshape.MutationPredicate{a, b}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"implicit_soft_delete", "KEY by_deleted (deleted_at)", []queryshape.MutationPredicate{a}, "deleted_at", MutationIndexChecked, ""},
		{"with_deleted", "KEY by_deleted (deleted_at)", []queryshape.MutationPredicate{a}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"case_insensitive", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateEqual, "A")}, "", MutationIndexChecked, ""},
		{"or_shared_leading_column", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, a, mutationTestPredicate(queryshape.PredicateGreaterThanOrEqual, "a"))}, "", MutationIndexChecked, ""},
		{"or_of_conjunctions", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, mutationTestLogical(queryshape.PredicateAnd, a, b), mutationTestLogical(queryshape.PredicateAnd, a, id))}, "", MutationIndexChecked, ""},
		{"or_index_merge", "KEY by_a (a), KEY by_b (b)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, a, b)}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"or_unindexed_branch", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, a, b)}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"negative", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateNot, a)}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"like", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateHasPrefix, "a")}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"positive_bound_with_like", "PRIMARY KEY (id)", []queryshape.MutationPredicate{id, mutationTestPredicate(queryshape.PredicateContains, "a")}, "", MutationIndexChecked, ""},
		{"empty_in", "", []queryshape.MutationPredicate{empty}, "", MutationIndexChecked, ""},
		{"empty_in_and_unbounded", "", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateAnd, empty, a)}, "", MutationIndexChecked, ""},
		{"false_or_indexed", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, empty, a)}, "", MutationIndexChecked, ""},
		{"empty_not_in", "PRIMARY KEY (id)", []queryshape.MutationPredicate{all}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"true_or_indexed", "KEY by_a (a)", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateOr, all, a)}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"negated_true", "", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateNot, all)}, "", MutationIndexChecked, ""},
		{"negated_false", "", []queryshape.MutationPredicate{mutationTestLogical(queryshape.PredicateNot, empty)}, "", MutationIndexChecked, CodeMutationMissingIndex},
		{"expression_index", "KEY expression_index ((a + 1))", []queryshape.MutationPredicate{a}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"prefix_index", "KEY prefix_index (a(8))", []queryshape.MutationPredicate{a}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"invisible_index", "KEY invisible_index (a) /*!80000 INVISIBLE */", []queryshape.MutationPredicate{a}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"partial_index", "KEY partial_index (a) WHERE b > 0", []queryshape.MutationPredicate{a}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"specialized_index", "FULLTEXT KEY special_index (a)", []queryshape.MutationPredicate{a}, "", MutationIndexUncertain, CodeMutationIndexUncertain},
		{"usable_with_unsupported_index", "PRIMARY KEY (id), KEY prefix_index (a(8))", []queryshape.MutationPredicate{id, a}, "", MutationIndexChecked, ""},
		{"missing_column", "", []queryshape.MutationPredicate{mutationTestPredicate(queryshape.PredicateEqual, "missing")}, "", MutationIndexUnavailable, CodeIndexCheckUnavailable},
		{"missing_soft_delete_column", "PRIMARY KEY (id)", []queryshape.MutationPredicate{id}, "missing", MutationIndexUnavailable, CodeIndexCheckUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			extra := ""
			if test.indexes != "" {
				extra = ", " + test.indexes
			}
			catalog, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT, a VARCHAR(32), b BIGINT, deleted_at DATETIME" + extra + ");")
			if err != nil {
				t.Fatal(err)
			}
			result := MutationIndexDiagnostics(queryshape.Mutation{Model: "Row", Table: "ROWS", Predicates: test.predicates, SoftDeleteColumn: test.softDelete}, catalog)
			if result.Status != test.status {
				t.Fatalf("status = %v, want %v, diagnostics = %#v", result.Status, test.status, result.Diagnostics)
			}
			if test.code == "" {
				if len(result.Diagnostics) != 0 {
					t.Fatalf("unexpected diagnostics = %#v", result.Diagnostics)
				}
				return
			}
			if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != test.code {
				t.Fatalf("diagnostics = %#v, want %s", result.Diagnostics, test.code)
			}
			diagnostic := result.Diagnostics[0]
			wantSeverity := check.SeverityWarning
			if test.status == MutationIndexUncertain {
				wantSeverity = check.SeverityInfo
			} else if test.status == MutationIndexUnavailable {
				wantSeverity = check.SeverityError
			}
			if diagnostic.Severity != wantSeverity || diagnostic.Suppressible != (test.status != MutationIndexUnavailable) {
				t.Fatalf("diagnostic policy = %#v", diagnostic)
			}
			if diagnostic.Location.Line == 0 || !strings.Contains(diagnostic.Message, "Row") {
				t.Fatalf("diagnostic location/model = %#v", diagnostic)
			}
			if test.status != MutationIndexUnavailable && !strings.Contains(diagnostic.Suggestion, "EXPLAIN ANALYZE executes the write") {
				t.Fatalf("missing DML safety boundary: %#v", diagnostic)
			}
		})
	}
}

func TestMutationIndexDiagnosticsUnavailableCatalog(t *testing.T) {
	shape := queryshape.Mutation{Model: "Row", Table: "missing"}
	empty, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT PRIMARY KEY);")
	if err != nil {
		t.Fatal(err)
	}
	for _, catalog := range []*physicalschema.Catalog{nil, empty} {
		result := MutationIndexDiagnostics(shape, catalog)
		if result.Status != MutationIndexUnavailable || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != CodeIndexCheckUnavailable || result.Diagnostics[0].Suppressible {
			t.Fatalf("result = %#v", result)
		}
	}
}

func TestMutationIndexDiagnosticsUnsupportedOperators(t *testing.T) {
	catalog, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT PRIMARY KEY);")
	if err != nil {
		t.Fatal(err)
	}
	for _, operator := range []queryshape.PredicateOperator{
		queryshape.PredicateNotEqual, queryshape.PredicateNotIn, queryshape.PredicateIsNotNull,
		queryshape.PredicateContains, queryshape.PredicateHasSuffix, "future_operator",
	} {
		shape := queryshape.Mutation{Model: "Row", Table: "rows", Predicates: []queryshape.MutationPredicate{mutationTestPredicate(operator, "id")}}
		if result := MutationIndexDiagnostics(shape, catalog); result.Status != MutationIndexUncertain {
			t.Fatalf("operator %q must stay uncertain: %#v", operator, result)
		}
	}
}

func mutationTestPredicate(operator queryshape.PredicateOperator, column string) queryshape.MutationPredicate {
	return queryshape.MutationPredicate{Operator: operator, Column: column}
}

func mutationTestLogical(operator queryshape.PredicateOperator, children ...queryshape.MutationPredicate) queryshape.MutationPredicate {
	return queryshape.MutationPredicate{Operator: operator, Children: children}
}
