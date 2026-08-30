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

func TestSelectQueryCountCountsPredicatesWithoutProjectionOrOrder(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(7)}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID"))

	count, err := query.Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 7 {
		t.Fatalf("Count() = %d, want 7", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `scan_model` WHERE `name` = ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryCountReturnsZeroForEmptySet(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(0)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[scanModel]().Limit(0).Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Count() = %d, want 0", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM (SELECT 1 FROM `scan_model` LIMIT ?) AS `tidbgo_count`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{int64(0)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountCountsPaginatedRowsWithoutMutatingBuilder(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(7)}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		Limit(10).
		Offset(3)

	count, err := query.Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 7 {
		t.Fatalf("Count() = %d, want 7", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM (SELECT 1 FROM `scan_model` WHERE `name` = ? LIMIT ? OFFSET ?) AS `tidbgo_count`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(10), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}

	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `id` FROM `scan_model` WHERE `name` = ? ORDER BY `id` DESC LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("Build() SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{"Ada", int64(10), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountKeepsKeysetPredicateWithoutOrderClause(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(4)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[scanModel]().
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		SeekAfter(uint64(10)).
		Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 4 {
		t.Fatalf("Count() = %d, want 4", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `scan_model` WHERE `name` = ? AND (`id` < ?)"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountDoesNotRequireReadableProjection(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(1)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[writeOnlyModel]().Select("Value").Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Count() = %d, want 1", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `write_only_model`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestSelectQueryCountReportsExecutionErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")

	tests := []struct {
		name  string
		state *allTestState
		want  error
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: queryFailure},
		{name: "no row", state: &allTestState{columns: []string{"count"}}, want: sql.ErrNoRows},
		{name: "iteration", state: &allTestState{columns: []string{"count"}, nextErr: iterationFailure}, want: iterationFailure},
		{name: "scan", state: &allTestState{columns: []string{"count"}, values: [][]driver.Value{{"invalid"}}}},
		{
			name: "close",
			state: &allTestState{
				columns:  []string{"count"},
				values:   [][]driver.Value{{int64(1)}},
				closeErr: closeFailure,
			},
			want: closeFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openAllTestDB(t, tt.state)
			count, err := Query[scanModel]().Count(context.Background(), database)
			if err == nil {
				t.Fatalf("Count() = %d, error = nil", count)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Count() = %d, error = %v, want errors.Is(_, %v)", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
			if tt.name == "scan" && !strings.Contains(err.Error(), "scan scanModel count") {
				t.Fatalf("Count() error = %v, want scan context", err)
			}
		})
	}
}

func TestSelectQueryCountRejectsInvalidExecutionInputs(t *testing.T) {
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
			count, err := tt.query.Count(tt.context, tt.executor)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Count() = %d, error = %v, want substring %q", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
		})
	}
}

func TestSelectQueryCountRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name  string
		query *SelectQuery[scanModel]
		want  string
	}{
		{name: "negative limit", query: Query[scanModel]().Limit(-1), want: "LIMIT must not be negative"},
		{name: "offset without limit", query: Query[scanModel]().Offset(1), want: "OFFSET requires LIMIT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := tt.query.Count(context.Background(), nilRowsExecutor{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Count() = %d, error = %v, want substring %q", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
		})
	}
}

func TestSelectQueryCountRejectsPointerModel(t *testing.T) {
	count, err := Query[*scanModel]().Count(context.Background(), nilRowsExecutor{})
	if err == nil || !strings.Contains(err.Error(), "non-pointer struct") {
		t.Fatalf("Count() = %d, error = %v, want non-pointer struct error", count, err)
	}
	if count != 0 {
		t.Fatalf("Count() = %d on error, want 0", count)
	}
}
