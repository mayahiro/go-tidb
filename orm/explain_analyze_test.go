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

func TestSelectQueryExplainAnalyzeReturnsTiDBRuntimePlan(t *testing.T) {
	state := explainAnalyzeTestState(
		[]driver.Value{"IndexLookUp_10", "1.00", "1", "root", "", "time:1ms, loops:2, RU:0.5", "", "1 KB", "N/A"},
		[]driver.Value{"IndexRangeScan_8(Build)", "1.00", "1", "cop[tikv]", "table:scan_model, index:name(name)", "time:500us, loops:1", "range:[Ada,Ada]", "N/A", "N/A"},
		[]driver.Value{"TableRowIDScan_9(Probe)", "1.00", "1", "cop[tikv]", "table:scan_model", "time:400us, loops:1", "keep order:false", "N/A", "N/A"},
	)
	database := openAllTestDB(t, state)

	plan, err := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		ExplainAnalyze(context.Background(), database)
	if err != nil {
		t.Fatalf("ExplainAnalyze() error = %v", err)
	}
	want := []ExplainAnalyzeRow{
		{ID: "IndexLookUp_10", EstRows: 1, ActRows: 1, Task: "root", ExecutionInfo: "time:1ms, loops:2, RU:0.5", Memory: "1 KB", Disk: "N/A"},
		{ID: "IndexRangeScan_8(Build)", EstRows: 1, ActRows: 1, Task: "cop[tikv]", AccessObject: "table:scan_model, index:name(name)", ExecutionInfo: "time:500us, loops:1", OperatorInfo: "range:[Ada,Ada]", Memory: "N/A", Disk: "N/A"},
		{ID: "TableRowIDScan_9(Probe)", EstRows: 1, ActRows: 1, Task: "cop[tikv]", AccessObject: "table:scan_model", ExecutionInfo: "time:400us, loops:1", OperatorInfo: "keep order:false", Memory: "N/A", Disk: "N/A"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("ExplainAnalyze() = %#v, want %#v", plan, want)
	}
	if got, want := state.query, "EXPLAIN ANALYZE SELECT `id`, `name` FROM `scan_model` WHERE `name` = ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if len(state.arguments) != 1 || state.arguments[0].Value != "Ada" {
		t.Fatalf("arguments = %#v, want Ada", state.arguments)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryExplainAnalyzeReturnsNonNilEmptyPlan(t *testing.T) {
	state := explainAnalyzeTestState()
	database := openAllTestDB(t, state)

	plan, err := Query[scanModel]().Select("ID").ExplainAnalyze(context.Background(), database)
	if err != nil {
		t.Fatalf("ExplainAnalyze() error = %v", err)
	}
	if plan == nil || len(plan) != 0 {
		t.Fatalf("ExplainAnalyze() = %#v, want non-nil empty plan", plan)
	}
}

func TestSelectQueryExplainAnalyzeExecutesOnlyPreloadRootSelect(t *testing.T) {
	state := explainAnalyzeTestState([]driver.Value{"TableReader_1", "2", "2", "root", "", "time:1ms, loops:2", "data:TableFullScan_2", "1 KB", "N/A"})
	database := openAllTestDB(t, state)

	if _, err := Query[preloadUser]().Preload("Orders").ExplainAnalyze(context.Background(), database); err != nil {
		t.Fatalf("ExplainAnalyze() error = %v", err)
	}
	if !strings.HasPrefix(state.query, "EXPLAIN ANALYZE SELECT ") {
		t.Fatalf("query = %q, want root SELECT EXPLAIN ANALYZE", state.query)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want one root runtime plan", state.closeCalls)
	}
}

func TestSelectQueryExplainAnalyzeObservesCompletedStatement(t *testing.T) {
	state := explainAnalyzeTestState([]driver.Value{"Point_Get_1", "1", "1", "root", "table:scan_model", "time:1ms, loops:2, RU:0.2", "handle:1", "N/A", "N/A"})
	database := openAllTestDB(t, state)
	var events []StatementEvent
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		events = append(events, event)
	}, IncludeStatementArguments())

	if _, err := Query[scanModel]().Select("ID").Where(Equal("ID", int64(1))).ExplainAnalyze(ctx, database); err != nil {
		t.Fatalf("ExplainAnalyze() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	event := events[0]
	if event.Operation != StatementExplainAnalyze || event.SQL != state.query || event.ArgumentCount != 1 {
		t.Fatalf("event statement = %#v", event)
	}
	if !reflect.DeepEqual(event.Arguments, []any{int64(1)}) || event.RowsAffectedKnown || event.Error != nil {
		t.Fatalf("event result = %#v", event)
	}
	if got := statementOperationColor(StatementExplainAnalyze); got != "\x1b[93m" {
		t.Fatalf("StatementExplainAnalyze color = %q, want bright yellow", got)
	}
}

func TestSelectQueryExplainAnalyzeReportsExecutionAndResultErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")
	tests := []struct {
		name  string
		state *allTestState
		want  string
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: "query failure"},
		{name: "columns", state: &allTestState{columns: []string{"id", "actRows"}}, want: "returned columns"},
		{name: "column order", state: &allTestState{columns: []string{"id", "actRows", "estRows", "task", "access object", "execution info", "operator info", "memory", "disk"}}, want: "returned columns"},
		{name: "scan", state: explainAnalyzeTestState([]driver.Value{"Point_Get_1", "1", "not-an-integer", "root", "table:scan_model", "time:1ms", "handle:1", "N/A", "N/A"}), want: "scan EXPLAIN ANALYZE row"},
		{name: "iteration", state: &allTestState{columns: explainAnalyzeTestColumns(), nextErr: iterationFailure}, want: "iteration failure"},
		{name: "close", state: &allTestState{columns: explainAnalyzeTestColumns(), closeErr: closeFailure}, want: "close failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openAllTestDB(t, test.state)
			_, err := Query[scanModel]().Select("ID").ExplainAnalyze(context.Background(), database)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExplainAnalyze() error = %v, want substring %q", err, test.want)
			}
			if test.state.queryErr == nil && test.state.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", test.state.closeCalls)
			}
		})
	}
}

func TestSelectQueryExplainAnalyzeRejectsInvalidExecutionInputs(t *testing.T) {
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := test.query.ExplainAnalyze(test.context, test.executor)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ExplainAnalyze() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func BenchmarkSelectQueryExplainAnalyze(b *testing.B) {
	state := explainAnalyzeTestState(
		[]driver.Value{"IndexLookUp_10", "1.00", "1", "root", "", "time:1ms, loops:2, RU:0.5", "", "1 KB", "N/A"},
		[]driver.Value{"IndexRangeScan_8(Build)", "1.00", "1", "cop[tikv]", "table:scan_model, index:name(name)", "time:500us, loops:1", "range:[Ada,Ada]", "N/A", "N/A"},
		[]driver.Value{"TableRowIDScan_9(Probe)", "1.00", "1", "cop[tikv]", "table:scan_model", "time:400us, loops:1", "keep order:false", "N/A", "N/A"},
	)
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("DB.Close() error = %v", err)
		}
	})
	query := Query[scanModel]().Select("ID", "Name").Where(Equal("Name", "Ada"))
	ctx := context.Background()
	var plan []ExplainAnalyzeRow
	var err error

	b.ReportAllocs()
	for b.Loop() {
		plan, err = query.ExplainAnalyze(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = int64(len(plan))
}

func explainAnalyzeTestState(values ...[]driver.Value) *allTestState {
	return &allTestState{columns: explainAnalyzeTestColumns(), values: values}
}

func explainAnalyzeTestColumns() []string {
	return append([]string(nil), explainAnalyzeColumnNames[:]...)
}
