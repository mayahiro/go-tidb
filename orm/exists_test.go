package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSelectQueryExistsReturnsTrueWithoutProjectionOrOrder(t *testing.T) {
	state := &allTestState{
		columns: []string{"exists"},
		values:  [][]driver.Value{{int64(1)}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		Limit(99).
		Offset(3)

	exists, err := query.Exists(context.Background(), database)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Fatal("Exists() = false, want true")
	}
	if got, want := state.query, "SELECT 1 FROM `scan_model` WHERE `name` = ? LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(1), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}

	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `id` FROM `scan_model` WHERE `name` = ? ORDER BY `id` DESC LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("Build() SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{"Ada", int64(99), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryExistsReturnsFalseForNoRows(t *testing.T) {
	state := &allTestState{columns: []string{"exists"}}
	database := openAllTestDB(t, state)

	exists, err := Query[scanModel]().Exists(context.Background(), database)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if exists {
		t.Fatal("Exists() = true, want false")
	}
	if got, want := state.query, "SELECT 1 FROM `scan_model` LIMIT ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{int64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryExistsKeepsKeysetPredicateWithoutOrderClause(t *testing.T) {
	state := &allTestState{
		columns: []string{"exists"},
		values:  [][]driver.Value{{int64(1)}},
	}
	database := openAllTestDB(t, state)

	exists, err := Query[scanModel]().
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		SeekAfter(uint64(10)).
		Exists(context.Background(), database)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Fatal("Exists() = false, want true")
	}
	if got, want := state.query, "SELECT 1 FROM `scan_model` WHERE `name` = ? AND (`id` < ?) LIMIT ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(10), int64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryExistsDoesNotRequireReadableProjection(t *testing.T) {
	state := &allTestState{
		columns: []string{"exists"},
		values:  [][]driver.Value{{int64(1)}},
	}
	database := openAllTestDB(t, state)

	exists, err := Query[writeOnlyModel]().Select("Value").Exists(context.Background(), database)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Fatal("Exists() = false, want true")
	}
	if got, want := state.query, "SELECT 1 FROM `write_only_model` LIMIT ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestSelectQueryExistsReportsExecutionErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")

	tests := []struct {
		name  string
		state *allTestState
		want  error
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: queryFailure},
		{name: "iteration", state: &allTestState{columns: []string{"exists"}, nextErr: iterationFailure}, want: iterationFailure},
		{
			name: "close",
			state: &allTestState{
				columns:  []string{"exists"},
				values:   [][]driver.Value{{int64(1)}},
				closeErr: closeFailure,
			},
			want: closeFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openAllTestDB(t, tt.state)
			exists, err := Query[scanModel]().Exists(context.Background(), database)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Exists() = %t, error = %v, want errors.Is(_, %v)", exists, err, tt.want)
			}
			if exists {
				t.Fatal("Exists() = true on error, want false")
			}
		})
	}
}

func TestSelectQueryExistsRejectsInvalidExecutionInputs(t *testing.T) {
	var nilQuery *SelectQuery[scanModel]
	var typedNilExecutor *sql.DB

	tests := []struct {
		name     string
		query    *SelectQuery[scanModel]
		context  context.Context
		executor QueryExecutor
		want     string
	}{
		{name: "nil context", query: Query[scanModel](), executor: nilRowsExecutor{}, want: "nil context"},
		{name: "nil executor", query: Query[scanModel](), context: context.Background(), want: "nil executor"},
		{name: "typed nil executor", query: Query[scanModel](), context: context.Background(), executor: typedNilExecutor, want: "nil executor"},
		{name: "nil query", query: nilQuery, context: context.Background(), executor: nilRowsExecutor{}, want: "nil SELECT query"},
		{name: "nil rows", query: Query[scanModel](), context: context.Background(), executor: nilRowsExecutor{}, want: "executor returned nil rows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists, err := tt.query.Exists(tt.context, tt.executor)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Exists() = %t, error = %v, want substring %q", exists, err, tt.want)
			}
			if exists {
				t.Fatal("Exists() = true on error, want false")
			}
		})
	}
}

func TestSelectQueryExistsRejectsPointerModel(t *testing.T) {
	exists, err := Query[*scanModel]().Exists(context.Background(), nilRowsExecutor{})
	if err == nil || !strings.Contains(err.Error(), "non-pointer struct") {
		t.Fatalf("Exists() = %t, error = %v, want non-pointer struct error", exists, err)
	}
	if exists {
		t.Fatal("Exists() = true on error, want false")
	}
}
