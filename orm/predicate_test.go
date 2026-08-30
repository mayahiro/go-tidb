package orm

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

func TestCompileSelectPreservesNestedPredicateAndArgumentOrder(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID", "Name"},
		predicates: []predicate{
			{operator: predicateGreaterThanOrEqual, field: "ID", values: []any{uint64(10)}},
			{
				operator: predicateOr,
				children: []predicate{
					{operator: predicateEqual, field: "Name", values: []any{"Ada"}},
					{
						operator: predicateAnd,
						children: []predicate{
							{operator: predicateNotEqual, field: "Name", values: []any{"Grace"}},
							{operator: predicateBetween, field: "ID", values: []any{uint64(20), uint64(30)}},
						},
					},
				},
			},
			{
				operator: predicateNot,
				children: []predicate{
					{operator: predicateIsNull, field: "Nickname"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `id`, `name` FROM `scan_model` WHERE `id` >= ? AND (`name` = ? OR (`name` <> ? AND `id` BETWEEN ? AND ?)) AND NOT (`nickname` IS NULL)"
	if statement.statement.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", statement.statement.sql, wantSQL)
	}
	if got, want := statement.arguments, []any{uint64(10), "Ada", "Grace", uint64(20), uint64(30)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectComparisonPredicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		operator predicateOperator
		want     string
	}{
		{name: "equal", operator: predicateEqual, want: "="},
		{name: "not equal", operator: predicateNotEqual, want: "<>"},
		{name: "greater than", operator: predicateGreaterThan, want: ">"},
		{name: "greater than or equal", operator: predicateGreaterThanOrEqual, want: ">="},
		{name: "less than", operator: predicateLessThan, want: "<"},
		{name: "less than or equal", operator: predicateLessThanOrEqual, want: "<="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statement, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[scanModel](),
				projection: []string{"ID"},
				predicates: []predicate{{operator: tt.operator, field: "ID", values: []any{uint64(7)}}},
			})
			if err != nil {
				t.Fatalf("compileSelect() error = %v", err)
			}
			wantSQL := "SELECT `id` FROM `scan_model` WHERE `id` " + tt.want + " ?"
			if statement.statement.sql != wantSQL {
				t.Fatalf("SQL = %q, want %q", statement.statement.sql, wantSQL)
			}
			if got, want := statement.arguments, []any{uint64(7)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCompileSelectQuotesPredicateColumnFromMetadata(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType: reflect.TypeFor[reservedSelectModel](),
		predicates: []predicate{
			{operator: predicateEqual, field: "Value", values: []any{"ready"}},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id`, `select` FROM `order` WHERE `select` = ?"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

func TestCompileSelectStringPatternsEscapeLiteralWildcards(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID"},
		predicates: []predicate{
			{operator: predicateContains, field: "Name", values: []any{`C:\50%_!寿司`}},
			{operator: predicateHasPrefix, field: "Name", values: []any{"go_"}},
			{operator: predicateHasSuffix, field: "Name", values: []any{"100%!"}},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `scan_model` WHERE `name` LIKE ? ESCAPE '!' AND `name` LIKE ? ESCAPE '!' AND `name` LIKE ? ESCAPE '!'"
	if statement.statement.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", statement.statement.sql, wantSQL)
	}
	if got, want := statement.arguments, []any{`%C:\50!%!_!!寿司%`, "go!_%", "%100!%!!"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

type namedPattern string

type namedPatternModel struct {
	Value namedPattern
}

func TestCompileSelectStringPatternsAcceptEmptyAndNamedStrings(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[namedPatternModel](),
		projection: []string{"Value"},
		predicates: []predicate{
			{operator: predicateContains, field: "Value", values: []any{namedPattern("")}},
			{operator: predicateHasPrefix, field: "Value", values: []any{""}},
			{operator: predicateHasSuffix, field: "Value", values: []any{""}},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.arguments, []any{"%%", "%", "%"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectInPredicatesAndEmptyConstants(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID"},
		predicates: []predicate{
			{operator: predicateIn, field: "ID", values: []any{uint64(1), uint64(2), uint64(3)}},
			{operator: predicateNotIn, field: "Name", values: []any{"Ada", "Grace"}},
			{operator: predicateIn, field: "ID"},
			{operator: predicateNotIn, field: "Name"},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `scan_model` WHERE `id` IN (?, ?, ?) AND `name` NOT IN (?, ?) AND FALSE AND TRUE"
	if statement.statement.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", statement.statement.sql, wantSQL)
	}
	if got, want := statement.arguments, []any{uint64(1), uint64(2), uint64(3), "Ada", "Grace"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectNullPredicateDoesNotRequireValueCapability(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID"},
		predicates: []predicate{
			{operator: predicateIsNull, field: "Amount"},
			{operator: predicateIsNotNull, field: "Amount"},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id` FROM `scan_model` WHERE `amount` IS NULL AND `amount` IS NOT NULL"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if statement.arguments != nil {
		t.Fatalf("arguments = %#v, want nil", statement.arguments)
	}
}

type observedValuer struct {
	calls *int
	text  string
}

func (value observedValuer) Value() (driver.Value, error) {
	(*value.calls)++
	return value.text, nil
}

type valuerPredicateModel struct {
	ID    uint64
	Value observedValuer
}

func TestCompileSelectKeepsValuerArgumentWithoutExecutingIt(t *testing.T) {
	t.Parallel()

	calls := 0
	value := observedValuer{calls: &calls, text: "12.30"}
	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[valuerPredicateModel](),
		projection: []string{"ID"},
		predicates: []predicate{
			{operator: predicateEqual, field: "Value", values: []any{value}},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id` FROM `valuer_predicate_model` WHERE `value` = ?"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if got, want := statement.arguments, []any{value}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectDoesNotMutateCachedDefaultStatement(t *testing.T) {
	t.Parallel()

	modelType := reflect.TypeFor[reservedSelectModel]()
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		t.Fatalf("DescribeType() error = %v", err)
	}
	base, err := compileDefaultSelect(descriptor)
	if err != nil {
		t.Fatalf("base compileDefaultSelect() error = %v", err)
	}
	filtered, err := compileSelect(&selectQuery{
		modelType: modelType,
		predicates: []predicate{
			{operator: predicateEqual, field: "ID", values: []any{uint64(1)}},
		},
	})
	if err != nil {
		t.Fatalf("filtered compileSelect() error = %v", err)
	}
	again, err := compileDefaultSelect(descriptor)
	if err != nil {
		t.Fatalf("second base compileDefaultSelect() error = %v", err)
	}
	if again != base {
		t.Fatal("filtered SELECT replaced the cached default statement")
	}
	if filtered.statement.scanPlan != base.scanPlan {
		t.Fatal("filtered SELECT did not reuse the immutable scan plan")
	}
	if got, want := base.sql, "SELECT `id`, `select` FROM `order`"; got != want {
		t.Fatalf("cached base SQL = %q, want %q", got, want)
	}
	if got, want := filtered.arguments, []any{uint64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectRejectsInvalidPredicates(t *testing.T) {
	t.Parallel()

	var nilID *uint64
	tests := []struct {
		name      string
		predicate predicate
		want      string
	}{
		{name: "unknown operator", predicate: predicate{operator: predicateOperator(255)}, want: "unknown operator"},
		{name: "unknown field", predicate: predicate{operator: predicateEqual, field: "Missing", values: []any{1}}, want: "not a mapped scalar field"},
		{name: "ignored field", predicate: predicate{operator: predicateEqual, field: "Ignored", values: []any{"value"}}, want: "not a mapped scalar field"},
		{name: "scalar child", predicate: predicate{operator: predicateEqual, field: "ID", values: []any{1}, children: []predicate{{operator: predicateIsNull, field: "ID"}}}, want: "must not contain child"},
		{name: "comparison without value", predicate: predicate{operator: predicateEqual, field: "ID"}, want: "exactly 1 value"},
		{name: "comparison with two values", predicate: predicate{operator: predicateEqual, field: "ID", values: []any{1, 2}}, want: "exactly 1 value"},
		{name: "between with one value", predicate: predicate{operator: predicateBetween, field: "ID", values: []any{1}}, want: "exactly 2 value"},
		{name: "pattern without value", predicate: predicate{operator: predicateContains, field: "Name"}, want: "exactly 1 value"},
		{name: "pattern with two values", predicate: predicate{operator: predicateHasPrefix, field: "Name", values: []any{"a", "b"}}, want: "exactly 1 value"},
		{name: "pattern non-string field", predicate: predicate{operator: predicateHasSuffix, field: "ID", values: []any{"1"}}, want: "requires a string field"},
		{name: "pattern non-string value", predicate: predicate{operator: predicateContains, field: "Name", values: []any{1}}, want: "requires a string value"},
		{name: "null with value", predicate: predicate{operator: predicateIsNull, field: "ID", values: []any{1}}, want: "must not contain values"},
		{name: "scan-only comparison", predicate: predicate{operator: predicateEqual, field: "Amount", values: []any{scanDecimal{text: "1.00"}}}, want: "cannot be used as a database argument"},
		{name: "nil comparison", predicate: predicate{operator: predicateEqual, field: "ID", values: []any{nil}}, want: "must not be nil"},
		{name: "typed nil IN", predicate: predicate{operator: predicateIn, field: "ID", values: []any{nilID}}, want: "must not be nil"},
		{name: "empty AND", predicate: predicate{operator: predicateAnd}, want: "at least two children"},
		{name: "one-child OR", predicate: predicate{operator: predicateOr, children: []predicate{{operator: predicateIsNull, field: "ID"}}}, want: "at least two children"},
		{name: "empty NOT", predicate: predicate{operator: predicateNot}, want: "exactly one child"},
		{name: "two-child NOT", predicate: predicate{operator: predicateNot, children: []predicate{{operator: predicateIsNull, field: "ID"}, {operator: predicateIsNotNull, field: "ID"}}}, want: "exactly one child"},
		{name: "logical field", predicate: predicate{operator: predicateAnd, field: "ID", children: []predicate{{operator: predicateIsNull, field: "ID"}, {operator: predicateIsNotNull, field: "ID"}}}, want: "must not contain a field or values"},
		{name: "logical value", predicate: predicate{operator: predicateOr, values: []any{1}, children: []predicate{{operator: predicateIsNull, field: "ID"}, {operator: predicateIsNotNull, field: "ID"}}}, want: "must not contain a field or values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[scanModel](),
				projection: []string{"ID"},
				predicates: []predicate{tt.predicate},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileSelect() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
