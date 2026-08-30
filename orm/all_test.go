package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

type allTestState struct {
	columns     []string
	values      [][]driver.Value
	queryErr    error
	nextErr     error
	closeErr    error
	query       string
	arguments   []driver.NamedValue
	closeCalls  int
	nextErrSent bool
}

type allTestConnector struct {
	state *allTestState
}

func (c *allTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &allTestConn{state: c.state}, nil
}

func (*allTestConnector) Driver() driver.Driver {
	return allTestDriver{}
}

type allTestDriver struct{}

func (allTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("all test driver requires OpenDB")
}

type allTestConn struct {
	state *allTestState
}

func (*allTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*allTestConn) Close() error {
	return nil
}

func (*allTestConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (c *allTestConn) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	if c.state.queryErr != nil {
		return nil, c.state.queryErr
	}
	c.state.query = query
	c.state.arguments = append([]driver.NamedValue(nil), arguments...)
	return &allTestRows{state: c.state}, nil
}

type allTestRows struct {
	state  *allTestState
	index  int
	closed bool
}

func (r *allTestRows) Columns() []string {
	return r.state.columns
}

func (r *allTestRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.state.closeCalls++
	return r.state.closeErr
}

func (r *allTestRows) Next(destination []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	if r.index < len(r.state.values) {
		copy(destination, r.state.values[r.index])
		r.index++
		return nil
	}
	if r.state.nextErr != nil && !r.state.nextErrSent {
		r.state.nextErrSent = true
		return r.state.nextErr
	}
	return io.EOF
}

type nilRowsExecutor struct{}

func (nilRowsExecutor) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func TestSelectQueryAllExecutesAndScansWithExplicitExecutor(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "name"},
		values: [][]driver.Value{
			{int64(1), "Ada"},
			{int64(2), "Grace"},
		},
	}
	database := openAllTestDB(t, state)

	values, err := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		OrderBy(Asc("ID")).
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if got, want := values, []scanModel{{ID: 1, Name: "Ada"}, {ID: 2, Name: "Grace"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
	if got, want := state.query, "SELECT `id`, `name` FROM `scan_model` WHERE `name` = ? ORDER BY `id` ASC"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if len(state.arguments) != 1 || state.arguments[0].Value != "Ada" {
		t.Fatalf("arguments = %#v, want Ada", state.arguments)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryAllReturnsNonNilEmptySlice(t *testing.T) {
	state := &allTestState{columns: []string{"id"}}
	database := openAllTestDB(t, state)

	values, err := Query[scanModel]().Select("ID").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if values == nil || len(values) != 0 {
		t.Fatalf("values = %#v, want non-nil empty slice", values)
	}
}

func TestSelectQueryAllReportsQueryIterationAndCloseErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")

	tests := []struct {
		name  string
		state *allTestState
		want  []error
	}{
		{
			name:  "query",
			state: &allTestState{queryErr: queryFailure},
			want:  []error{queryFailure},
		},
		{
			name: "iteration",
			state: &allTestState{
				columns: []string{"id"},
				values:  [][]driver.Value{{int64(1)}},
				nextErr: iterationFailure,
			},
			want: []error{iterationFailure},
		},
		{
			name: "close",
			state: &allTestState{
				columns:  []string{"id"},
				closeErr: closeFailure,
			},
			want: []error{closeFailure},
		},
		{
			name: "scan and close",
			state: &allTestState{
				columns:  []string{"id"},
				values:   [][]driver.Value{{"not-an-integer"}},
				closeErr: closeFailure,
			},
			want: []error{closeFailure},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openAllTestDB(t, tt.state)
			values, err := Query[scanModel]().Select("ID").All(context.Background(), database)
			if err == nil {
				t.Fatalf("All() values = %#v, error = nil", values)
			}
			for _, target := range tt.want {
				if !errors.Is(err, target) {
					t.Fatalf("All() error = %v, want errors.Is(_, %v)", err, target)
				}
			}
			if tt.state.queryErr == nil && tt.state.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", tt.state.closeCalls)
			}
		})
	}
}

func TestSelectQueryAllRejectsInvalidExecutionInputs(t *testing.T) {
	t.Parallel()

	var typedNilExecutor *sql.DB
	var nilQuery *SelectQuery[scanModel]
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
			t.Parallel()
			_, err := tt.query.All(tt.context, tt.executor)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("All() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func openAllTestDB(t *testing.T, state *allTestState) *sql.DB {
	t.Helper()
	database := sql.OpenDB(&allTestConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("DB.Close() error = %v", err)
		}
	})
	return database
}
