package queryshape

import "testing"

func TestQueryFingerprintIsStableAndExcludesBoundValues(t *testing.T) {
	t.Parallel()

	first := fingerprintFixture()
	second := fingerprintFixture()
	second.Limit.Value = 500
	second.Offset.Value = 200
	if got, want := first.Fingerprint(), second.Fingerprint(); got != want {
		t.Fatalf("fingerprints differ for bind-only values: %q, %q", got, want)
	}
	const want = "q1:a97c556a2a2f618b182293312356015df3124eb3f5f6d5e07291a867cb60be4b"
	if got := first.Fingerprint(); got != want {
		t.Fatalf("Fingerprint() = %q, want %q", got, want)
	}
}

func TestQueryFingerprintChangesWithSQLOrCompilerShape(t *testing.T) {
	t.Parallel()

	base := fingerprintFixture().Fingerprint()
	tests := map[string]func(*Query){
		"table": func(query *Query) {
			query.Table = "archived_orders"
		},
		"projection": func(query *Query) {
			query.Projection = append(query.Projection, "total")
		},
		"predicate operator": func(query *Query) {
			query.Predicates[0].Operator = PredicateNotEqual
		},
		"IN arity": func(query *Query) {
			query.Predicates[0].ValueCount = 3
		},
		"order": func(query *Query) {
			query.Order[0].Direction = OrderAscending
		},
		"preload": func(query *Query) {
			query.Preloads[0].Projection = append(query.Preloads[0].Projection, "email")
		},
		"compiler": func(query *Query) {
			query.Compiler.Rewrite = CompilerRewriteRelationTopN
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query := fingerprintFixture()
			mutate(&query)
			if got := query.Fingerprint(); got == base {
				t.Fatalf("Fingerprint() = %q, want a value different from base", got)
			}
		})
	}
}

func BenchmarkQueryFingerprint(b *testing.B) {
	query := fingerprintFixture()
	b.ReportAllocs()
	for b.Loop() {
		queryFingerprintSink = query.Fingerprint()
	}
}

func fingerprintFixture() Query {
	return Query{
		Model:      "Order",
		Table:      "orders",
		Projection: []string{"id", "user_id"},
		Predicates: []Predicate{{
			Operator:   PredicateEqual,
			Table:      "orders",
			Field:      "UserID",
			Column:     "user_id",
			ValueCount: 1,
		}},
		Order: []OrderTerm{{
			Field:     "ID",
			Column:    "id",
			Direction: OrderDescending,
		}},
		SeekAfter: true,
		Limit:     Bound{Set: true, Positive: true, Value: 100},
		Offset:    Bound{Set: true, Positive: true, Value: 20},
		Preloads: []Preload{{
			Path:       "User",
			Relation:   "User",
			Kind:       "belongs-to",
			Table:      "users",
			Projection: []string{"id"},
			Inline:     true,
		}},
		Compiler: CompilerDecision{Rewrite: CompilerRewriteNone},
		IndexAccesses: []IndexAccess{{
			Kind:            IndexAccessRootOrderedLimit,
			Table:           "orders",
			EqualityColumns: []string{"user_id"},
			OrderColumns:    []string{"id"},
		}},
	}
}

var queryFingerprintSink string
