package orm

import (
	"reflect"
	"strings"
	"testing"
)

func TestBulkMutationStatementCountUsesExactAutomaticBatching(t *testing.T) {
	tests := []struct {
		name  string
		count func() (int, error)
		want  int
	}{
		{
			name: "empty insert",
			count: func() (int, error) {
				return InsertMany([]bulkMutationModel(nil)).StatementCount()
			},
			want: 0,
		},
		{
			name: "one insert statement at boundary",
			count: func() (int, error) {
				return InsertMany(make([]bulkMutationModel, maxMutationParameters/2)).StatementCount()
			},
			want: 1,
		},
		{
			name: "two insert statements after boundary",
			count: func() (int, error) {
				return InsertMany(make([]bulkMutationModel, maxMutationParameters/2+1)).StatementCount()
			},
			want: 2,
		},
		{
			name: "generated only rows retain safety bound",
			count: func() (int, error) {
				return InsertMany(make([]mutationOnlyAutoRandom, maxMutationParameters+1)).StatementCount()
			},
			want: 2,
		},
		{
			name: "two upsert statements after boundary",
			count: func() (int, error) {
				return UpsertMany(make([]bulkMutationModel, maxMutationParameters/2+1), "Value").StatementCount()
			},
			want: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.count()
			if err != nil {
				t.Fatalf("StatementCount() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("StatementCount() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBulkMutationStatementCountValidatesWithoutExecutingValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	count, err := InsertMany([]mutationModel{{
		Name:   "Ada",
		Amount: mutationValue{calls: &calls, text: "12.30"},
	}}).StatementCount()
	if err != nil {
		t.Fatalf("StatementCount() error = %v", err)
	}
	if count != 1 || calls != 0 {
		t.Fatalf("StatementCount() = %d, Value() calls = %d, want 1 and 0", count, calls)
	}
}

func TestBulkMutationStatementCountDoesNotInspectPointerElements(t *testing.T) {
	t.Parallel()

	count, err := InsertMany([]*mutationModel{nil, nil}).StatementCount()
	if err != nil {
		t.Fatalf("StatementCount() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("StatementCount() = %d, want 1", count)
	}
}

func TestBulkMutationStatementCountReportsInvalidQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		count func() (int, error)
		want  string
	}{
		{
			name: "nil insert query",
			count: func() (int, error) {
				var query *InsertManyQuery[mutationModel]
				return query.StatementCount()
			},
			want: "nil bulk INSERT query",
		},
		{
			name: "nil upsert query",
			count: func() (int, error) {
				var query *UpsertManyQuery[mutationModel]
				return query.StatementCount()
			},
			want: "nil bulk UPSERT query",
		},
		{
			name: "invalid upsert field",
			count: func() (int, error) {
				return UpsertMany([]mutationModel{{}}, "Missing").StatementCount()
			},
			want: "not a mapped scalar field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if count, err := test.count(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StatementCount() = %d, %v, want error containing %q", count, err, test.want)
			}
		})
	}
}

func TestSelectQueryEstimateAllStatementsUsesStaticBounds(t *testing.T) {
	t.Parallel()

	assertStatementEstimate(t, Query[preloadUser](), StatementCountEstimate{
		Minimum:      1,
		Maximum:      1,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Profile"), StatementCountEstimate{
		Minimum:      1,
		Maximum:      1,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders"), StatementCountEstimate{
		Minimum:      1,
		Maximum:      2,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders").Preload("Roles"), StatementCountEstimate{
		Minimum:      1,
		Maximum:      3,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders").Where(Equal("Email", "ada@example.test")), StatementCountEstimate{
		Minimum: 1,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders").Limit(10_001), StatementCountEstimate{
		Minimum:      1,
		Maximum:      4,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadTenant]().Preload("Records").Where(Equal("TenantID", uint64(7))).Limit(5_001), StatementCountEstimate{
		Minimum:      1,
		Maximum:      4,
		MaximumKnown: true,
	})
}

func TestSelectQueryEstimateAllStatementsHandlesNestedCollections(t *testing.T) {
	t.Parallel()

	assertStatementEstimate(t, Query[preloadOrder]().Preload("User.Orders").Limit(5_001), StatementCountEstimate{
		Minimum:      1,
		Maximum:      3,
		MaximumKnown: true,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders.User.Orders").Limit(1), StatementCountEstimate{
		Minimum: 1,
	})
	assertStatementEstimate(t, Query[preloadUser]().Preload("Orders.User.Orders").Limit(0), StatementCountEstimate{
		Minimum:      1,
		Maximum:      1,
		MaximumKnown: true,
	})
}

func TestSelectQueryEstimateAllStatementsReportsInvalidQueries(t *testing.T) {
	t.Parallel()

	if estimate, err := Query[preloadUser]().Preload("Missing").EstimateAllStatements(); err == nil || !strings.Contains(err.Error(), "not a mapped relation") {
		t.Fatalf("EstimateAllStatements() = %#v, %v, want invalid relation error", estimate, err)
	}
	var query *SelectQuery[preloadUser]
	if estimate, err := query.EstimateAllStatements(); err == nil || !strings.Contains(err.Error(), "nil SELECT query") {
		t.Fatalf("nil EstimateAllStatements() = %#v, %v, want nil query error", estimate, err)
	}
}

func TestSelectQueryEstimateAllStatementsDoesNotExecuteValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	value := observedValuer{calls: &calls, text: "12.30"}
	estimate, err := Query[valuerPredicateModel]().
		Select("ID").
		Where(Equal("Value", value)).
		Limit(1).
		EstimateAllStatements()
	if err != nil {
		t.Fatalf("EstimateAllStatements() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if !estimate.Exact() || estimate.Maximum != 1 {
		t.Fatalf("EstimateAllStatements() = %#v, want exact 1", estimate)
	}
}

func TestStatementCountEstimateExact(t *testing.T) {
	t.Parallel()

	if !(StatementCountEstimate{Minimum: 2, Maximum: 2, MaximumKnown: true}).Exact() {
		t.Fatal("equal known bounds must be exact")
	}
	for _, estimate := range []StatementCountEstimate{
		{Minimum: 1, Maximum: 2, MaximumKnown: true},
		{Minimum: 1},
		{},
	} {
		if estimate.Exact() {
			t.Fatalf("Exact() = true for %#v", estimate)
		}
	}
}

func assertStatementEstimate[T any](t *testing.T, query *SelectQuery[T], want StatementCountEstimate) {
	t.Helper()

	got, err := query.EstimateAllStatements()
	if err != nil {
		t.Fatalf("EstimateAllStatements() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EstimateAllStatements() = %#v, want %#v", got, want)
	}
}
