package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

func TestCollectServerRUPinsSQLDBForMutation(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":4.25}`}
	database := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	affected, err := RawExec(ctx, database, "UPDATE counters SET value = value + 1 WHERE id = ?", int64(7))
	if err != nil || affected != 1 {
		t.Fatalf("RawExec() = %d, %v, want 1, nil", affected, err)
	}
	targetConnection, serverRUConnection, executions, serverRUQueries, rowsClosed := state.snapshot()
	if targetConnection == 0 || targetConnection != serverRUConnection {
		t.Fatalf("target connection = %d, ServerRU connection = %d, want same nonzero connection", targetConnection, serverRUConnection)
	}
	if executions != 1 || serverRUQueries != 1 || rowsClosed {
		t.Fatalf("executions = %d, ServerRU queries = %d, rows closed = %v", executions, serverRUQueries, rowsClosed)
	}
	if database.Stats().InUse != 0 {
		t.Fatalf("database in-use connections = %d, want 0", database.Stats().InUse)
	}
	if acquiredAt := state.connectionAcquired(); event.StartedAt.Before(acquiredAt) {
		t.Fatalf("target started at %s before connection acquisition completed at %s", event.StartedAt, acquiredAt)
	}
	assertCollectedServerRU(t, event.ServerRU, 4.25)
	if event.Error != nil || !event.RowsAffectedKnown || event.RowsAffected != 1 {
		t.Fatalf("StatementEvent = %#v", event)
	}
}

func TestCollectServerRUQueriesAfterSelectRowsClose(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{
		serverRU:               `{"ru_consumption":1.5}`,
		requireTargetRowsClose: true,
		targetColumns:          []string{"id", "name"},
		targetValues:           [][]driver.Value{{int64(1), "Ada"}},
	}
	database := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	values, err := Query[scanModel]().Select("ID", "Name").All(ctx, database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(values) != 1 || values[0].ID != 1 || values[0].Name != "Ada" {
		t.Fatalf("All() values = %#v", values)
	}
	targetConnection, serverRUConnection, _, serverRUQueries, rowsClosed := state.snapshot()
	if !rowsClosed || targetConnection != serverRUConnection || serverRUQueries != 1 {
		t.Fatalf("target connection = %d, ServerRU connection = %d, ServerRU queries = %d, rows closed = %v", targetConnection, serverRUConnection, serverRUQueries, rowsClosed)
	}
	assertCollectedServerRU(t, event.ServerRU, 1.5)
}

func TestCollectServerRUUsesActiveSQLTransaction(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":2.75}`}
	database := openServerRUObserverDB(t, state)
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("DB.BeginTx() error = %v", err)
	}
	t.Cleanup(func() {
		_ = transaction.Rollback()
	})
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	if _, err := RawExec(ctx, transaction, "DELETE FROM counters WHERE id = ?", int64(9)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	targetConnection, serverRUConnection, _, serverRUQueries, _ := state.snapshot()
	if targetConnection != serverRUConnection || serverRUQueries != 1 {
		t.Fatalf("target connection = %d, ServerRU connection = %d, queries = %d", targetConnection, serverRUConnection, serverRUQueries)
	}
	assertCollectedServerRU(t, event.ServerRU, 2.75)
}

func TestCollectServerRUUsesCallerOwnedSQLConnectionWithoutClosingIt(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":2.25}`}
	database := openServerRUObserverDB(t, state)
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("DB.Conn() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := connection.Close(); closeErr != nil {
			t.Errorf("Conn.Close() error = %v", closeErr)
		}
	})
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	if _, err := RawExec(ctx, connection, "UPDATE counters SET value = ? WHERE id = ?", int64(3), int64(4)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), "UPDATE counters SET value = 4 WHERE id = 4"); err != nil {
		t.Fatalf("caller-owned connection after collection error = %v", err)
	}
	targetConnection, serverRUConnection, executions, serverRUQueries, _ := state.snapshot()
	if targetConnection != serverRUConnection || executions != 2 || serverRUQueries != 1 {
		t.Fatalf("target connection = %d, ServerRU connection = %d, executions = %d, queries = %d", targetConnection, serverRUConnection, executions, serverRUQueries)
	}
	assertCollectedServerRU(t, event.ServerRU, 2.25)
}

func TestCollectServerRUFailureDoesNotReplaceTargetResult(t *testing.T) {
	t.Parallel()

	want := errors.New("ServerRU query failed")
	state := &serverRUObserverState{serverRUErr: want}
	database := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	affected, err := RawExec(ctx, database, "UPDATE counters SET value = ?", int64(2))
	if err != nil || affected != 1 {
		t.Fatalf("RawExec() = %d, %v, want 1, nil", affected, err)
	}
	if event.Error != nil || event.ServerRU == nil || event.ServerRU.Known || event.ServerRU.AuxiliaryStatements != 1 || !errors.Is(event.ServerRU.Error, want) {
		t.Fatalf("StatementEvent = %#v", event)
	}
	if database.Stats().InUse != 0 {
		t.Fatalf("database in-use connections = %d, want 0", database.Stats().InUse)
	}
}

func TestCollectServerRUAttemptsDiagnosticAfterTargetFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("target failed")
	state := &serverRUObserverState{serverRU: `{"ru_consumption":0.75}`, targetErr: want}
	database := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	if _, err := RawExec(ctx, database, "UPDATE counters SET value = ?", int64(2)); !errors.Is(err, want) {
		t.Fatalf("RawExec() error = %v, want %v", err, want)
	}
	if !errors.Is(event.Error, want) {
		t.Fatalf("StatementEvent error = %v, want %v", event.Error, want)
	}
	assertCollectedServerRU(t, event.ServerRU, 0.75)
}

func TestCollectServerRUReportsUnsupportedExecutorWithoutChangingTarget(t *testing.T) {
	t.Parallel()

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	affected, err := RawExec(ctx, executor, "UPDATE counters SET value = ?", int64(2))
	if err != nil || affected != 1 {
		t.Fatalf("RawExec() = %d, %v, want 1, nil", affected, err)
	}
	if event.ServerRU == nil || event.ServerRU.Error == nil || event.ServerRU.AuxiliaryStatements != 0 {
		t.Fatalf("StatementEvent ServerRU = %#v", event.ServerRU)
	}
	if !strings.Contains(event.ServerRU.Error.Error(), "requires *sql.DB, *sql.Conn, or *sql.Tx") {
		t.Fatalf("StatementEvent ServerRU error = %v", event.ServerRU.Error)
	}
}

func TestCollectServerRUSkipsUnclassifiedRawExec(t *testing.T) {
	t.Parallel()

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 0}}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())

	if _, err := RawExec(ctx, executor, "CREATE TEMPORARY TABLE counters_copy (id BIGINT)"); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	if event.Operation != StatementExec || event.ServerRU != nil {
		t.Fatalf("StatementEvent = %#v", event)
	}
}

func TestRuntimeCaptureCollectsServerRUAtScopeBoundary(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":3.125}`}
	database := openServerRUObserverDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())

	if _, err := RawExec(ctx, database, "UPDATE counters SET value = ?", int64(3)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || records[0].ServerRU == nil {
		t.Fatalf("runtime records = %#v", records)
	}
	if got := records[0].ServerRU; !got.Known || got.Value != 3.125 || got.AuxiliaryStatements != 1 || got.Error != "" {
		t.Fatalf("runtime ServerRU = %#v", got)
	}
	analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("AnalyzeReader() error = %v", err)
	}
	statistics := analysis.Statistics
	if statistics.Statements != 1 || statistics.AuxiliaryStatements != 1 || statistics.ServerRUSamples != 1 || statistics.ServerRUErrors != 0 || statistics.ServerRUTotal != 3.125 {
		t.Fatalf("runtime statistics = %#v", statistics)
	}
}

func TestServerRUCollectionSharedByObserverAndRuntimeCapture(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":2}`}
	database := openServerRUObserverDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	}, CollectServerRU())
	ctx = WithRuntimeCapture(ctx, capture, CollectServerRU())

	if _, err := RawExec(ctx, database, "DELETE FROM counters WHERE id = ?", int64(3)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	_, _, _, serverRUQueries, _ := state.snapshot()
	if serverRUQueries != 1 {
		t.Fatalf("ServerRU queries = %d, want 1", serverRUQueries)
	}
	assertCollectedServerRU(t, event.ServerRU, 2)
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || records[0].ServerRU == nil || records[0].ServerRU.Value != 2 {
		t.Fatalf("runtime records = %#v", records)
	}
}

func TestRuntimeCaptureAnalyzesRepeatedWritesWithCollectedServerRU(t *testing.T) {
	t.Parallel()

	for _, upsert := range []bool{false, true} {
		state := &serverRUObserverState{serverRU: `{"ru_consumption":1.25}`}
		database := openServerRUObserverDB(t, state)
		var output bytes.Buffer
		capture := NewRuntimeCapture(&output)
		ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
		for index := range 2 {
			value := bulkMutationModel{ID: int64(index + 1), Value: 10}
			var err error
			if upsert {
				_, err = Upsert(&value).Exec(ctx, database)
			} else {
				_, err = Insert(&value).Exec(ctx, database)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := capture.Err(); err != nil {
			t.Fatal(err)
		}
		analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(output.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "RUN004" {
			t.Fatalf("upsert=%t diagnostics = %#v", upsert, analysis.Diagnostics)
		}
		found := false
		for _, evidence := range analysis.Diagnostics[0].Evidence {
			if evidence.Message == "Captured statement ServerRU: total=2.5, samples=2/2, collection_errors=0" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing captured RU evidence: %#v", analysis.Diagnostics[0])
		}
		targetConnection, ruConnection, targetCount, ruCount, _ := state.snapshot()
		if targetConnection != ruConnection || targetCount != 2 || ruCount != 2 {
			t.Fatalf("unexpected DB work: target connection=%d RU connection=%d target statements=%d RU statements=%d", targetConnection, ruConnection, targetCount, ruCount)
		}
	}
}

func TestRuntimeCaptureAggregatesTargetAndServerRUDiagnosticsSeparately(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":4.25}`}
	database := openServerRUObserverDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
	if _, err := RawExec(ctx, database, "UPDATE counters SET value = ? WHERE id = ?", int64(1), int64(7)); err != nil {
		t.Fatalf("first RawExec() error = %v", err)
	}
	if _, err := RawExec(ctx, database, "DELETE FROM counters WHERE id = ?", int64(8)); err != nil {
		t.Fatalf("second RawExec() error = %v", err)
	}
	analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("AnalyzeReader() error = %v", err)
	}
	statistics := analysis.Statistics
	if statistics.Statements != 2 || statistics.AuxiliaryStatements != 2 || statistics.ServerRUSamples != 2 || statistics.ServerRUErrors != 0 || statistics.ServerRUTotal != 8.5 {
		t.Fatalf("runtime statistics = %#v", statistics)
	}
	if statistics.TargetDuration < 0 || statistics.DiagnosticDuration < 0 {
		t.Fatalf("runtime timing = %#v", statistics)
	}
}

func TestRuntimeCaptureIsolatesServerRUFromInheritedObserverMutation(t *testing.T) {
	t.Parallel()

	state := &serverRUObserverState{serverRU: `{"ru_consumption":4.25}`}
	database := openServerRUObserverDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		if event.ServerRU != nil {
			event.ServerRU.Value = 999
		}
	})
	ctx = WithRuntimeCapture(ctx, capture, CollectServerRU())
	if _, err := RawExec(ctx, database, "UPDATE counters SET value = ?", int64(1)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || records[0].ServerRU == nil || records[0].ServerRU.Value != 4.25 {
		t.Fatalf("runtime records = %#v", records)
	}
}

func assertCollectedServerRU(t testing.TB, observation *ServerRUObservation, want float64) {
	t.Helper()
	if observation == nil || !observation.Known || observation.Value != want || observation.AuxiliaryStatements != 1 || observation.DiagnosticDuration < 0 || observation.Error != nil {
		t.Fatalf("ServerRUObservation = %#v, want known value %v", observation, want)
	}
}

type serverRUObserverState struct {
	mutex                  sync.Mutex
	nextConnection         int
	targetConnection       int
	serverRUConnection     int
	targetExecutions       int
	serverRUQueries        int
	targetRowsClosed       bool
	requireTargetRowsClose bool
	targetColumns          []string
	targetValues           [][]driver.Value
	serverRU               string
	serverRUErr            error
	targetErr              error
	connectionAcquiredAt   time.Time
}

func (state *serverRUObserverState) snapshot() (int, int, int, int, bool) {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	return state.targetConnection, state.serverRUConnection, state.targetExecutions, state.serverRUQueries, state.targetRowsClosed
}

func (state *serverRUObserverState) connectionAcquired() time.Time {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	return state.connectionAcquiredAt
}

type serverRUObserverConnector struct {
	state *serverRUObserverState
}

func (connector *serverRUObserverConnector) Connect(context.Context) (driver.Conn, error) {
	connector.state.mutex.Lock()
	connector.state.nextConnection++
	id := connector.state.nextConnection
	connector.state.connectionAcquiredAt = time.Now()
	connector.state.mutex.Unlock()
	return &serverRUObserverConn{state: connector.state, id: id}, nil
}

func (*serverRUObserverConnector) Driver() driver.Driver {
	return serverRUObserverDriver{}
}

type serverRUObserverDriver struct{}

func (serverRUObserverDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("server RU observer test driver requires OpenDB")
}

type serverRUObserverConn struct {
	state *serverRUObserverState
	id    int
}

func (*serverRUObserverConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*serverRUObserverConn) Close() error {
	return nil
}

func (*serverRUObserverConn) Begin() (driver.Tx, error) {
	return serverRUObserverTx{}, nil
}

func (connection *serverRUObserverConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.state.mutex.Lock()
	connection.state.targetConnection = connection.id
	connection.state.targetExecutions++
	err := connection.state.targetErr
	connection.state.mutex.Unlock()
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (connection *serverRUObserverConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if query == lastServerRUQuery {
		connection.state.mutex.Lock()
		connection.state.serverRUConnection = connection.id
		connection.state.serverRUQueries++
		closed := connection.state.targetRowsClosed
		requireClose := connection.state.requireTargetRowsClose
		raw := connection.state.serverRU
		err := connection.state.serverRUErr
		connection.state.mutex.Unlock()
		if requireClose && !closed {
			return nil, errors.New("ServerRU queried before target rows closed")
		}
		if err != nil {
			return nil, err
		}
		return &serverRUObserverRows{
			columns: []string{"@@tidb_last_query_info"},
			values:  [][]driver.Value{{raw}},
		}, nil
	}

	connection.state.mutex.Lock()
	connection.state.targetConnection = connection.id
	columns := append([]string(nil), connection.state.targetColumns...)
	values := append([][]driver.Value(nil), connection.state.targetValues...)
	connection.state.mutex.Unlock()
	return &serverRUObserverRows{
		columns: columns,
		values:  values,
		onClose: func() {
			connection.state.mutex.Lock()
			connection.state.targetRowsClosed = true
			connection.state.mutex.Unlock()
		},
	}, nil
}

type serverRUObserverTx struct{}

func (serverRUObserverTx) Commit() error {
	return nil
}

func (serverRUObserverTx) Rollback() error {
	return nil
}

type serverRUObserverRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	closed  bool
	onClose func()
}

func (rows *serverRUObserverRows) Columns() []string {
	return rows.columns
}

func (rows *serverRUObserverRows) Close() error {
	if rows.closed {
		return nil
	}
	rows.closed = true
	if rows.onClose != nil {
		rows.onClose()
	}
	return nil
}

func (rows *serverRUObserverRows) Next(destination []driver.Value) error {
	if rows.closed || rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

func openServerRUObserverDB(t testing.TB, state *serverRUObserverState) *sql.DB {
	t.Helper()
	database := sql.OpenDB(&serverRUObserverConnector{state: state})
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("DB.Close() error = %v", err)
		}
	})
	return database
}

func BenchmarkRawExecWithServerRUCollection(b *testing.B) {
	state := &serverRUObserverState{serverRU: `{"ru_consumption":4.25}`}
	database := openServerRUObserverDB(b, state)
	ctx := WithStatementObserver(context.Background(), func(StatementEvent) {}, CollectServerRU())
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = RawExec(ctx, database, "UPDATE counters SET value = ? WHERE id = ?", int64(1), int64(7))
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkRawExecWithRuntimeCaptureAndServerRU(b *testing.B) {
	state := &serverRUObserverState{serverRU: `{"ru_consumption":4.25}`}
	database := openServerRUObserverDB(b, state)
	capture := NewRuntimeCapture(discardRuntimeCaptureWriter{})
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = RawExec(ctx, database, "UPDATE counters SET value = ? WHERE id = ?", int64(1), int64(7))
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}
