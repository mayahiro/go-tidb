package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

func TestCompileSelectPreservesClauseAndArgumentOrder(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID", "Name"},
		predicates: []predicate{
			{operator: predicateGreaterThanOrEqual, field: "ID", values: []any{uint64(10)}},
			{operator: predicateEqual, field: "Name", values: []any{"Ada"}},
		},
		orderBy: []orderTerm{
			{field: "Name", direction: orderAscending},
			{field: "ID", direction: orderDescending},
		},
		pagination: pagination{
			limit:     100,
			offset:    20,
			limitSet:  true,
			offsetSet: true,
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `id`, `name` FROM `scan_model` WHERE `id` >= ? AND `name` = ? ORDER BY `name` ASC, `id` DESC LIMIT ? OFFSET ?"
	if got := statement.statement.sql; got != wantSQL {
		t.Fatalf("SQL = %q, want %q", got, wantSQL)
	}
	if got, want := statement.arguments, []any{uint64(10), "Ada", int64(100), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectQuotesOrderColumnsFromMetadata(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType: reflect.TypeFor[reservedSelectModel](),
		orderBy: []orderTerm{
			{field: "Value", direction: orderDescending},
			{field: "ID", direction: orderAscending},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id`, `select` FROM `order` ORDER BY `select` DESC, `id` ASC"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if statement.arguments != nil {
		t.Fatalf("arguments = %#v, want nil", statement.arguments)
	}
}

func TestCompileSelectOrderDoesNotRequireProjectionReadCapability(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[mixedReadModel](),
		projection: []string{"ID"},
		orderBy:    []orderTerm{{field: "Value", direction: orderAscending}},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id` FROM `mixed_read_model` ORDER BY `value` ASC"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

func TestCompileSelectPreservesExplicitZeroPagination(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType: reflect.TypeFor[reservedSelectModel](),
		pagination: pagination{
			limitSet:  true,
			offsetSet: true,
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id`, `select` FROM `order` LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if got, want := statement.arguments, []any{int64(0), int64(0)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectClausesReuseCachedScanPlan(t *testing.T) {
	t.Parallel()

	modelType := reflect.TypeFor[reservedSelectModel]()
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		t.Fatalf("DescribeType() error = %v", err)
	}
	base, err := compileDefaultSelect(descriptor)
	if err != nil {
		t.Fatalf("compileDefaultSelect() error = %v", err)
	}
	compiled, err := compileSelect(&selectQuery{
		modelType:  modelType,
		orderBy:    []orderTerm{{field: "ID", direction: orderAscending}},
		pagination: pagination{limit: 1, limitSet: true},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if compiled.statement.scanPlan != base.scanPlan {
		t.Fatal("SELECT clauses did not reuse the immutable scan plan")
	}
	if got, want := base.sql, "SELECT `id`, `select` FROM `order`"; got != want {
		t.Fatalf("cached base SQL = %q, want %q", got, want)
	}
}

func TestCompileSelectRejectsInvalidOrderAndPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		orderBy    []orderTerm
		pagination pagination
		want       string
	}{
		{
			name:    "unknown order field",
			orderBy: []orderTerm{{field: "Missing", direction: orderAscending}},
			want:    "not a mapped scalar field",
		},
		{
			name:    "ignored order field",
			orderBy: []orderTerm{{field: "Ignored", direction: orderAscending}},
			want:    "not a mapped scalar field",
		},
		{
			name: "duplicate order field",
			orderBy: []orderTerm{
				{field: "ID", direction: orderAscending},
				{field: "ID", direction: orderDescending},
			},
			want: "repeats field",
		},
		{
			name:    "unknown order direction",
			orderBy: []orderTerm{{field: "ID", direction: orderDirection(255)}},
			want:    "unknown direction",
		},
		{
			name:       "offset without limit",
			pagination: pagination{offset: 1, offsetSet: true},
			want:       "OFFSET requires LIMIT",
		},
		{
			name:       "negative limit",
			pagination: pagination{limit: -1, limitSet: true},
			want:       "LIMIT must not be negative",
		},
		{
			name:       "negative offset",
			pagination: pagination{limit: 1, offset: -1, limitSet: true, offsetSet: true},
			want:       "OFFSET must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[scanModel](),
				projection: []string{"ID"},
				orderBy:    tt.orderBy,
				pagination: tt.pagination,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileSelect() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
