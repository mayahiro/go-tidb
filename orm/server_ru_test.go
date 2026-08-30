package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestLastServerRUReadsPinnedSQLConnection(t *testing.T) {
	t.Parallel()

	state := &allTestState{
		columns: []string{"@@tidb_last_query_info"},
		values:  [][]driver.Value{{`{"txn_scope":"global","start_ts":1,"ru_consumption":4.34885578125}`}},
	}
	database := openAllTestDB(t, state)
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("DB.Conn() error = %v", err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("Conn.Close() error = %v", err)
		}
	})

	got, err := LastServerRU(context.Background(), connection)
	if err != nil {
		t.Fatalf("LastServerRU() error = %v", err)
	}
	if want := 4.34885578125; got != want {
		t.Fatalf("LastServerRU() = %v, want %v", got, want)
	}
	if state.query != lastServerRUQuery || len(state.arguments) != 0 {
		t.Fatalf("query = %q, arguments = %#v", state.query, state.arguments)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestLastServerRUReadsActiveSQLTransaction(t *testing.T) {
	t.Parallel()

	state := &allTestState{
		columns: []string{"@@tidb_last_query_info"},
		values:  [][]driver.Value{{`{"ru_consumption":0}`}},
	}
	database := openAllTestDB(t, state)
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("DB.BeginTx() error = %v", err)
	}
	t.Cleanup(func() {
		_ = transaction.Rollback()
	})

	got, err := LastServerRU(context.Background(), transaction)
	if err != nil {
		t.Fatalf("LastServerRU() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("LastServerRU() = %v, want 0", got)
	}
}

func TestLastServerRUDoesNotEmitStatementEvent(t *testing.T) {
	t.Parallel()

	state := serverRUState(`{"ru_consumption":1}`)
	database := openAllTestDB(t, state)
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("DB.Conn() error = %v", err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("Conn.Close() error = %v", err)
		}
	})
	calls := 0
	ctx := WithStatementObserver(context.Background(), func(StatementEvent) {
		calls++
	})

	if _, err := LastServerRU(ctx, connection); err != nil {
		t.Fatalf("LastServerRU() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("StatementObserver calls = %d, want 0", calls)
	}
}

func TestLastServerRURejectsInvalidInputsAndResponses(t *testing.T) {
	t.Parallel()

	queryFailure := errors.New("query failure")
	tests := []struct {
		name    string
		context context.Context
		state   *allTestState
		nilConn bool
		want    string
	}{
		{name: "nil context", state: serverRUState(`{"ru_consumption":1}`), want: "nil context"},
		{name: "nil session", context: context.Background(), nilConn: true, want: "nil session"},
		{name: "query error", context: context.Background(), state: &allTestState{queryErr: queryFailure}, want: "query failure"},
		{name: "SQL NULL", context: context.Background(), state: &allTestState{columns: []string{"value"}, values: [][]driver.Value{{nil}}}, want: "converting NULL to string"},
		{name: "invalid JSON", context: context.Background(), state: serverRUState(`not-json`), want: "decode last ServerRU"},
		{name: "missing RU", context: context.Background(), state: serverRUState(`{"txn_scope":"global"}`), want: "did not report ru_consumption"},
		{name: "null RU", context: context.Background(), state: serverRUState(`{"ru_consumption":null}`), want: "did not report ru_consumption"},
		{name: "negative RU", context: context.Background(), state: serverRUState(`{"ru_consumption":-1}`), want: "invalid ru_consumption"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var connection *sql.Conn
			if !test.nilConn {
				database := openAllTestDB(t, test.state)
				var err error
				connection, err = database.Conn(context.Background())
				if err != nil {
					t.Fatalf("DB.Conn() error = %v", err)
				}
				t.Cleanup(func() {
					if err := connection.Close(); err != nil {
						t.Errorf("Conn.Close() error = %v", err)
					}
				})
			}

			_, err := LastServerRU(test.context, connection)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LastServerRU() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func BenchmarkLastServerRU(b *testing.B) {
	state := serverRUState(`{"txn_scope":"global","start_ts":1,"ru_consumption":4.34885578125}`)
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("DB.Close() error = %v", err)
		}
	})
	connection, err := database.Conn(context.Background())
	if err != nil {
		b.Fatalf("DB.Conn() error = %v", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("Conn.Close() error = %v", err)
		}
	})

	var serverRU float64
	b.ReportAllocs()
	for b.Loop() {
		serverRU, err = LastServerRU(context.Background(), connection)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = int64(serverRU)
}

func serverRUState(raw string) *allTestState {
	return &allTestState{
		columns: []string{"@@tidb_last_query_info"},
		values:  [][]driver.Value{{raw}},
	}
}
