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

func TestSelectQueryExplainReturnsTiDBPlan(t *testing.T) {
	state := explainTestState(
		[]driver.Value{"IndexLookUp_10", "1.00", "root", "", ""},
		[]driver.Value{"IndexRangeScan_8(Build)", "1.00", "cop[tikv]", "table:scan_model, index:name(name)", "range:[Ada,Ada]"},
		[]driver.Value{"TableRowIDScan_9(Probe)", "1.00", "cop[tikv]", "table:scan_model", "keep order:false"},
	)
	database := openAllTestDB(t, state)

	plan, err := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		Explain(context.Background(), database)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	want := []ExplainRow{
		{ID: "IndexLookUp_10", EstRows: 1, Task: "root"},
		{ID: "IndexRangeScan_8(Build)", EstRows: 1, Task: "cop[tikv]", AccessObject: "table:scan_model, index:name(name)", OperatorInfo: "range:[Ada,Ada]"},
		{ID: "TableRowIDScan_9(Probe)", EstRows: 1, Task: "cop[tikv]", AccessObject: "table:scan_model", OperatorInfo: "keep order:false"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("Explain() = %#v, want %#v", plan, want)
	}
	if got, want := state.query, "EXPLAIN SELECT `id`, `name` FROM `scan_model` WHERE `name` = ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if len(state.arguments) != 1 || state.arguments[0].Value != "Ada" {
		t.Fatalf("arguments = %#v, want Ada", state.arguments)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryExplainReturnsNonNilEmptyPlan(t *testing.T) {
	state := explainTestState()
	database := openAllTestDB(t, state)

	plan, err := Query[scanModel]().Select("ID").Explain(context.Background(), database)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if plan == nil || len(plan) != 0 {
		t.Fatalf("Explain() = %#v, want non-nil empty plan", plan)
	}
}

func TestSelectQueryExplainDescribesOnlyPreloadRootSelect(t *testing.T) {
	state := explainTestState([]driver.Value{"TableReader_1", "2", "root", "", "data:TableFullScan_2"})
	database := openAllTestDB(t, state)

	if _, err := Query[preloadUser]().Preload("Orders").Explain(context.Background(), database); err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if !strings.HasPrefix(state.query, "EXPLAIN SELECT ") {
		t.Fatalf("query = %q, want root SELECT EXPLAIN", state.query)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want one root plan", state.closeCalls)
	}
}

func TestSelectQueryExplainObservesCompletedStatement(t *testing.T) {
	state := explainTestState([]driver.Value{"Point_Get_1", "1", "root", "table:scan_model", "handle:1"})
	database := openAllTestDB(t, state)
	var events []StatementEvent
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		events = append(events, event)
	}, IncludeStatementArguments())

	if _, err := Query[scanModel]().Select("ID").Where(Equal("ID", int64(1))).Explain(ctx, database); err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	event := events[0]
	if event.Operation != StatementExplain || event.SQL != state.query || event.ArgumentCount != 1 {
		t.Fatalf("event statement = %#v", event)
	}
	if !reflect.DeepEqual(event.Arguments, []any{int64(1)}) || event.RowsAffectedKnown || event.Error != nil {
		t.Fatalf("event result = %#v", event)
	}
	if got := statementOperationColor(StatementExplain); got != "\x1b[96m" {
		t.Fatalf("StatementExplain color = %q, want bright cyan", got)
	}
}

func TestSelectQueryExplainReportsExecutionAndResultErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")
	tests := []struct {
		name  string
		state *allTestState
		want  string
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: "query failure"},
		{name: "columns", state: &allTestState{columns: []string{"id", "task"}}, want: "returned columns"},
		{name: "column order", state: &allTestState{columns: []string{"id", "task", "estRows", "access object", "operator info"}}, want: "returned columns"},
		{name: "scan", state: explainTestState([]driver.Value{"Point_Get_1", "not-a-number", "root", "table:scan_model", "handle:1"}), want: "scan EXPLAIN row"},
		{name: "iteration", state: &allTestState{columns: explainTestColumns(), nextErr: iterationFailure}, want: "iteration failure"},
		{name: "close", state: &allTestState{columns: explainTestColumns(), closeErr: closeFailure}, want: "close failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openAllTestDB(t, test.state)
			_, err := Query[scanModel]().Select("ID").Explain(context.Background(), database)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Explain() error = %v, want substring %q", err, test.want)
			}
			if test.state.queryErr == nil && test.state.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", test.state.closeCalls)
			}
		})
	}
}

func TestSelectQueryExplainRejectsInvalidExecutionInputs(t *testing.T) {
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
			_, err := test.query.Explain(test.context, test.executor)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Explain() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func BenchmarkSelectQueryExplain(b *testing.B) {
	state := explainTestState(
		[]driver.Value{"IndexLookUp_10", "1.00", "root", "", ""},
		[]driver.Value{"IndexRangeScan_8(Build)", "1.00", "cop[tikv]", "table:scan_model, index:name(name)", "range:[Ada,Ada]"},
		[]driver.Value{"TableRowIDScan_9(Probe)", "1.00", "cop[tikv]", "table:scan_model", "keep order:false"},
	)
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Errorf("DB.Close() error = %v", err)
		}
	})
	query := Query[scanModel]().Select("ID", "Name").Where(Equal("Name", "Ada"))
	ctx := context.Background()
	var plan []ExplainRow
	var err error

	b.ReportAllocs()
	for b.Loop() {
		plan, err = query.Explain(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = int64(len(plan))
}

func explainTestState(values ...[]driver.Value) *allTestState {
	return &allTestState{columns: explainTestColumns(), values: values}
}

func explainTestColumns() []string {
	return append([]string(nil), explainColumnNames[:]...)
}
