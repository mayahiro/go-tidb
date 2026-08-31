package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStatementObserverRecordsSelectAfterRowsFinish(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  [][]driver.Value{{int64(1), "Ada"}},
	}
	database := openAllTestDB(t, state)
	var events []StatementEvent
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		events = append(events, event)
	})

	rows, err := queryTextRows(ctx, database, "scanModel", "SELECT id, name FROM scan_model WHERE name = ?", []any{"Ada"})
	if err != nil {
		t.Fatalf("queryTextRows() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events before rows finish = %#v, want none", events)
	}
	if !rows.Next() {
		t.Fatal("rows.Next() = false, want one row")
	}
	var id int64
	var name string
	if err := rows.Scan(&id, &name); err != nil {
		t.Fatalf("rows.Scan() error = %v", err)
	}
	if err := finishRows("scanModel", rows); err != nil {
		t.Fatalf("finishRows() error = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	event := events[0]
	if event.Operation != StatementSelect || event.SQL != "SELECT id, name FROM scan_model WHERE name = ?" || event.ArgumentCount != 1 {
		t.Fatalf("event statement = %#v", event)
	}
	if event.Arguments != nil || event.StartedAt.IsZero() || event.Duration < 0 || event.RowsAffectedKnown || event.Error != nil {
		t.Fatalf("event result = %#v", event)
	}
}

func TestIncludeStatementArgumentsSnapshotsOriginalGoValues(t *testing.T) {
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	arguments := []any{"ada@example.test", int64(7)}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, IncludeStatementArguments())

	if _, err := RawExec(ctx, executor, "UPDATE users SET email = ? WHERE id = ?", arguments...); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	arguments[0] = "changed@example.test"
	if got, want := event.Arguments, []any{"ada@example.test", int64(7)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event arguments = %#v, want %#v", got, want)
	}
}

func TestStatementObserverRecordsSelectTerminalAndDriverErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	tests := []struct {
		name  string
		state *allTestState
		want  error
	}{
		{name: "no rows", state: &allTestState{columns: []string{"id"}}, want: sql.ErrNoRows},
		{name: "multiple rows", state: &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}, {int64(2)}}}, want: ErrMultipleRows},
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: queryFailure},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openAllTestDB(t, test.state)
			var event StatementEvent
			ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
				event = current
			})

			query := Query[scanModel]().Select("ID")
			var err error
			if test.name == "multiple rows" {
				_, err = query.Only(ctx, database)
			} else {
				_, err = query.First(ctx, database)
			}
			if !errors.Is(err, test.want) || !errors.Is(event.Error, test.want) {
				t.Fatalf("First() error = %v, event error = %v, want %v", err, event.Error, test.want)
			}
		})
	}
}

func TestStatementObserverRecordsBulkAndRelationMutations(t *testing.T) {
	var events []StatementEvent
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		events = append(events, event)
	})

	bulkExecutor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
	values := []bulkMutationModel{{ID: 1, Value: 10}, {ID: 2, Value: 20}}
	if _, err := InsertMany(values).Exec(ctx, bulkExecutor); err != nil {
		t.Fatalf("InsertMany().Exec() error = %v", err)
	}
	relationExecutor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	if _, err := RemoveRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec(ctx, relationExecutor); err != nil {
		t.Fatalf("RemoveRelation().Exec() error = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[0].Operation != StatementInsert || events[0].ArgumentCount != 4 || events[0].RowsAffected != 2 {
		t.Fatalf("bulk event = %#v", events[0])
	}
	if events[1].Operation != StatementDelete || events[1].ArgumentCount != 2 || events[1].RowsAffected != 1 {
		t.Fatalf("relation event = %#v", events[1])
	}
}

func TestStatementObserverRecordsTransactionLifecycle(t *testing.T) {
	callbackFailure := errors.New("callback failure")
	tests := []struct {
		name       string
		callback   func(*sql.Tx) error
		operations []StatementOperation
	}{
		{name: "commit", callback: func(*sql.Tx) error { return nil }, operations: []StatementOperation{StatementBegin, StatementCommit}},
		{name: "rollback", callback: func(*sql.Tx) error { return callbackFailure }, operations: []StatementOperation{StatementBegin, StatementRollback}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &transactionTestState{}
			database := openTransactionTestDB(t, state)
			var events []StatementEvent
			ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
				events = append(events, event)
			})

			err := Transaction(ctx, database, test.callback)
			if test.name == "commit" && err != nil {
				t.Fatalf("Transaction() error = %v", err)
			}
			if test.name == "rollback" && !errors.Is(err, callbackFailure) {
				t.Fatalf("Transaction() error = %v, want callback failure", err)
			}
			if len(events) != len(test.operations) {
				t.Fatalf("events = %#v", events)
			}
			for index, operation := range test.operations {
				if events[index].Operation != operation || events[index].SQL != string(operation) || events[index].ArgumentCount != 0 || events[index].RowsAffectedKnown || events[index].Error != nil {
					t.Fatalf("event %d = %#v", index, events[index])
				}
			}
		})
	}
}

func TestStatementObserverRecordsMutationKindsAndResults(t *testing.T) {
	tests := []struct {
		name      string
		operation StatementOperation
		run       func(context.Context, *recordingExecExecutor) (int64, error)
	}{
		{
			name:      "insert",
			operation: StatementInsert,
			run: func(ctx context.Context, executor *recordingExecExecutor) (int64, error) {
				value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
				return Insert(&value).Exec(ctx, executor)
			},
		},
		{
			name:      "upsert",
			operation: StatementUpsert,
			run: func(ctx context.Context, executor *recordingExecExecutor) (int64, error) {
				value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
				return Upsert(&value).Exec(ctx, executor)
			},
		},
		{
			name:      "update",
			operation: StatementUpdate,
			run: func(ctx context.Context, executor *recordingExecExecutor) (int64, error) {
				value := mutationModel{ID: 10, Name: "Ada", Amount: mutationValue{text: "1.00"}}
				return Update(&value).Exec(ctx, executor)
			},
		},
		{
			name:      "delete",
			operation: StatementDelete,
			run: func(ctx context.Context, executor *recordingExecExecutor) (int64, error) {
				value := mutationModel{ID: 10}
				return Delete(&value).Exec(ctx, executor)
			},
		},
		{
			name:      "raw update",
			operation: StatementUpdate,
			run: func(ctx context.Context, executor *recordingExecExecutor) (int64, error) {
				return RawExec(ctx, executor, "UPDATE counters SET value = value + 1 WHERE id = ?", int64(10))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecExecutor{result: mutationResult{lastInsertID: 91, rowsAffected: 3}}
			var events []StatementEvent
			ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
				events = append(events, event)
			})

			affected, err := test.run(ctx, executor)
			if err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			if affected != 3 || len(events) != 1 {
				t.Fatalf("affected = %d, events = %#v", affected, events)
			}
			event := events[0]
			if event.Operation != test.operation || event.SQL != executor.query || event.ArgumentCount != len(executor.arguments) {
				t.Fatalf("event statement = %#v", event)
			}
			if !event.RowsAffectedKnown || event.RowsAffected != 3 || event.Error != nil {
				t.Fatalf("event result = %#v", event)
			}
		})
	}
}

func TestStatementObserverRecordsMutationErrorWithoutAffectedRows(t *testing.T) {
	executionFailure := errors.New("execution failure")
	executor := &recordingExecExecutor{err: executionFailure}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	})
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}

	if _, err := Insert(&value).Exec(ctx, executor); !errors.Is(err, executionFailure) {
		t.Fatalf("Insert().Exec() error = %v", err)
	}
	if !errors.Is(event.Error, executionFailure) || event.RowsAffectedKnown {
		t.Fatalf("event = %#v", event)
	}
}

func TestStatementObserverCanBeDisabledInDerivedContext(t *testing.T) {
	calls := 0
	ctx := WithStatementObserver(context.Background(), func(StatementEvent) {
		calls++
	})
	ctx = WithStatementObserver(ctx, nil)
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
	executor := &recordingExecExecutor{result: mutationResult{lastInsertID: 1, rowsAffected: 1}}

	if _, err := Insert(&value).Exec(ctx, executor); err != nil {
		t.Fatalf("Insert().Exec() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("observer calls = %d, want 0", calls)
	}
}

func TestStatementLoggerWritesPlainOutputWithoutArgumentValues(t *testing.T) {
	var output bytes.Buffer
	logger := NewStatementLogger(&output)
	logger(StatementEvent{
		Operation:         StatementUpdate,
		SQL:               "UPDATE users SET secret = ? WHERE id = ?",
		ArgumentCount:     2,
		StartedAt:         time.Date(2026, time.August, 30, 12, 47, 35, 66_000_000, time.Local),
		Duration:          9*time.Millisecond + 419*time.Microsecond,
		RowsAffected:      1,
		RowsAffectedKnown: true,
	})

	want := "[tidbgo] 12:47:35.066 UPDATE   9.419ms args=2 affected=1 UPDATE users SET secret = ? WHERE id = ?\n"
	if output.String() != want {
		t.Fatalf("logger output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("buffer output contains ANSI color = %q", output.String())
	}
}

func TestStatementLoggerColorsOperationAndErrorWhenEnabled(t *testing.T) {
	var output bytes.Buffer
	logger := &statementLogger{writer: &output, color: true}
	logger.observe(StatementEvent{
		Operation: StatementDelete,
		SQL:       "DELETE FROM users WHERE id = ?",
		StartedAt: time.Date(2026, time.August, 30, 12, 47, 35, 0, time.Local),
		Duration:  time.Millisecond,
		Error:     errors.New("delete failed"),
	})

	if !strings.Contains(output.String(), "\x1b[35mDELETE\x1b[0m") {
		t.Fatalf("logger operation color output = %q", output.String())
	}
	if !strings.Contains(output.String(), "error=\x1b[31mdelete failed\x1b[0m") {
		t.Fatalf("logger error color output = %q", output.String())
	}
}

func TestStatementLoggerWritesExplicitArgumentValuesSeparatelyFromSQL(t *testing.T) {
	var output bytes.Buffer
	logger := NewStatementLogger(&output)
	logger(StatementEvent{
		Operation:     StatementSelect,
		SQL:           "SELECT id FROM users WHERE email = ? AND tenant_id = ?",
		ArgumentCount: 2,
		Arguments:     []any{"ada@example.test", int64(7)},
	})

	if !strings.Contains(output.String(), `args=2 values=["ada@example.test", 7] SELECT id FROM users`) {
		t.Fatalf("logger output = %q", output.String())
	}
	if strings.Contains(output.String(), `email = "ada@example.test"`) {
		t.Fatalf("logger interpolated a value into SQL = %q", output.String())
	}
}

func TestStatementLoggerWritesServerRUAndDiagnosticCost(t *testing.T) {
	var output bytes.Buffer
	logger := NewStatementLogger(&output)
	logger(StatementEvent{
		Operation: StatementSelect,
		SQL:       "SELECT id FROM users",
		Duration:  9 * time.Millisecond,
		ServerRU: &ServerRUObservation{
			Value:               4.34885578125,
			Known:               true,
			DiagnosticDuration:  3 * time.Millisecond,
			AuxiliaryStatements: 1,
		},
	})

	if got := output.String(); !strings.Contains(got, "SELECT   9ms diagnostic=3ms auxiliary=1 server_ru=4.34885578125 args=0") {
		t.Fatalf("logger output = %q", got)
	}
}

func TestStatementLoggerEscapesTerminalControlCharacters(t *testing.T) {
	var output bytes.Buffer
	logger := NewStatementLogger(&output)
	logger(StatementEvent{
		Operation: StatementSelect,
		SQL:       "SELECT\n'\x1b[31m'",
		Error:     errors.New("failed\rnext"),
	})

	if strings.Contains(output.String(), "\nSELECT\n") || strings.Contains(output.String(), "\x1b[31m") {
		t.Fatalf("logger emitted terminal control characters = %q", output.String())
	}
	if !strings.Contains(output.String(), `SELECT\n'\x1b[31m'`) || !strings.Contains(output.String(), `failed\rnext`) {
		t.Fatalf("logger escaped output = %q", output.String())
	}
}

func TestStatementLoggerSerializesConcurrentWrites(t *testing.T) {
	var output bytes.Buffer
	logger := NewStatementLogger(&output)
	const count = 100
	var group sync.WaitGroup
	group.Add(count)
	for range count {
		go func() {
			defer group.Done()
			logger(StatementEvent{Operation: StatementSelect, SQL: "SELECT 1"})
		}()
	}
	group.Wait()

	if lines := strings.Count(output.String(), "\n"); lines != count {
		t.Fatalf("logger line count = %d, want %d", lines, count)
	}
}

func TestStatementLoggerAcceptsNilWriter(t *testing.T) {
	NewStatementLogger(nil)(StatementEvent{Operation: StatementSelect, SQL: "SELECT 1"})
}

func BenchmarkInsertExecWithStatementObserver(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	executor := mutationBenchmarkExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	ctx := WithStatementObserver(context.Background(), func(StatementEvent) {})
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkInsertExecWithStatementLogger(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	executor := mutationBenchmarkExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	ctx := WithStatementObserver(context.Background(), NewStatementLogger(io.Discard))
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkInsertExecWithStatementLoggerAndArguments(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	executor := mutationBenchmarkExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	ctx := WithStatementObserver(context.Background(), NewStatementLogger(io.Discard), IncludeStatementArguments())
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}
