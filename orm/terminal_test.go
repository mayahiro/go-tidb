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

func TestSelectQueryFirstExecutesLimitOneWithoutMutatingBuilder(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  [][]driver.Value{{int64(1), "Ada"}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		OrderBy(Asc("ID")).
		Limit(99).
		Offset(3)

	value, err := query.First(context.Background(), database)
	if err != nil {
		t.Fatalf("First() error = %v", err)
	}
	if got, want := value, (scanModel{ID: 1, Name: "Ada"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("First() = %#v, want %#v", got, want)
	}
	if got, want := state.query, "SELECT `id`, `name` FROM `scan_model` WHERE `name` = ? ORDER BY `id` ASC LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(1), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}

	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `id`, `name` FROM `scan_model` WHERE `name` = ? ORDER BY `id` ASC LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("Build() SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{"Ada", int64(99), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryOnlyExecutesLimitTwoAndReturnsOneRow(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  [][]driver.Value{{int64(1), "Ada"}},
	}
	database := openAllTestDB(t, state)

	value, err := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		Limit(1).
		Only(context.Background(), database)
	if err != nil {
		t.Fatalf("Only() error = %v", err)
	}
	if got, want := value, (scanModel{ID: 1, Name: "Ada"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Only() = %#v, want %#v", got, want)
	}
	if got, want := state.query, "SELECT `id`, `name` FROM `scan_model` WHERE `name` = ? LIMIT ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(2)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryFirstAndOnlyReturnSQLNoRows(t *testing.T) {
	tests := []struct {
		name  string
		limit int64
		run   func(*SelectQuery[scanModel], context.Context, QueryExecutor) (scanModel, error)
	}{
		{name: "first", limit: 1, run: (*SelectQuery[scanModel]).First},
		{name: "only", limit: 2, run: (*SelectQuery[scanModel]).Only},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &allTestState{columns: []string{"id"}}
			database := openAllTestDB(t, state)

			value, err := tt.run(Query[scanModel]().Select("ID"), context.Background(), database)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("terminal() error = %v, want sql.ErrNoRows", err)
			}
			if value != (scanModel{}) {
				t.Fatalf("terminal() = %#v, want zero value", value)
			}
			if got, want := namedValues(state.arguments), []any{tt.limit}; !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments = %#v, want %#v", got, want)
			}
			if got, want := state.query, "SELECT `id` FROM `scan_model` LIMIT ?"; got != want {
				t.Fatalf("query = %q, want %q", got, want)
			}
			if state.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
			}
		})
	}
}

func TestSelectQueryOnlyRejectsMultipleRows(t *testing.T) {
	closeFailure := errors.New("close failure")
	state := &allTestState{
		columns:  []string{"id"},
		values:   [][]driver.Value{{int64(1)}, {int64(2)}},
		closeErr: closeFailure,
	}
	database := openAllTestDB(t, state)

	value, err := Query[scanModel]().Select("ID").Only(context.Background(), database)
	if !errors.Is(err, ErrMultipleRows) || !errors.Is(err, closeFailure) {
		t.Fatalf("Only() error = %v, want ErrMultipleRows and close failure", err)
	}
	if value != (scanModel{}) {
		t.Fatalf("Only() = %#v, want zero value", value)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryOnlyReportsIterationErrorAfterFirstRow(t *testing.T) {
	iterationFailure := errors.New("iteration failure")
	state := &allTestState{
		columns: []string{"id"},
		values:  [][]driver.Value{{int64(1)}},
		nextErr: iterationFailure,
	}
	database := openAllTestDB(t, state)

	value, err := Query[scanModel]().Select("ID").Only(context.Background(), database)
	if !errors.Is(err, iterationFailure) {
		t.Fatalf("Only() error = %v, want iteration failure", err)
	}
	if value != (scanModel{}) {
		t.Fatalf("Only() = %#v, want zero value", value)
	}
}

func TestSelectQuerySingleRowTerminalsReportExecutionErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	closeFailure := errors.New("close failure")

	tests := []struct {
		name  string
		state *allTestState
		want  []error
		text  string
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: []error{queryFailure}},
		{
			name: "scan and close",
			state: &allTestState{
				columns:  []string{"id"},
				values:   [][]driver.Value{{"not-an-integer"}},
				closeErr: closeFailure,
			},
			want: []error{closeFailure},
			text: "scan scanModel row",
		},
		{
			name:  "close",
			state: &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}, closeErr: closeFailure},
			want:  []error{closeFailure},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openAllTestDB(t, tt.state)
			value, err := Query[scanModel]().Select("ID").First(context.Background(), database)
			if err == nil {
				t.Fatalf("First() = %#v, error = nil", value)
			}
			for _, target := range tt.want {
				if !errors.Is(err, target) {
					t.Fatalf("First() error = %v, want errors.Is(_, %v)", err, target)
				}
			}
			if tt.text != "" && !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("First() error = %v, want substring %q", err, tt.text)
			}
		})
	}
}

func TestSelectQuerySingleRowTerminalsRejectInvalidExecutionInputs(t *testing.T) {
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
		{name: "nil rows", query: Query[scanModel]().Select("ID"), context: context.Background(), executor: nilRowsExecutor{}, want: "executor returned nil rows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.query.First(tt.context, tt.executor)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("First() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func namedValues(values []driver.NamedValue) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index].Value
	}
	return result
}
