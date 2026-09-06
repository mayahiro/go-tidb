package orm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type observeTestExecutor struct {
	ExecExecutor
	QueryExecutor
}

func TestObserveAppliesToMutationTerminals(t *testing.T) {
	value := bulkMutationModel{ID: 7, Value: 10}
	tests := []struct {
		name string
		run  func(context.Context, ExecExecutor) (int64, error)
	}{
		{"insert", Insert(&value).Exec},
		{"insert many", InsertMany([]bulkMutationModel{value}).Exec},
		{"upsert", Upsert(&value).Exec},
		{"upsert many", UpsertMany([]bulkMutationModel{value}).Exec},
		{"update", Update(&value).Exec},
		{"update many", UpdateMany([]bulkMutationModel{value}).Exec},
		{"update where", UpdateWhere[bulkMutationModel](Set("Value", int64(11))).Where(Equal("ID", int64(7))).Exec},
		{"delete", Delete(&value).Exec},
		{"delete where", DeleteWhere[bulkMutationModel](Equal("ID", int64(7))).Exec},
		{"add relation", AddRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec},
		{"remove relation", RemoveRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec},
		{"clear relation", ClearRelation[preloadUser]("Roles", uint64(7)).Exec},
		{"raw", func(ctx context.Context, executor ExecExecutor) (int64, error) {
			return RawExec(ctx, executor, "UPDATE counters SET value = ?", int64(11))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []StatementEvent
			executor := Observe(observeTestExecutor{ExecExecutor: &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}}, func(event StatementEvent) {
				events = append(events, event)
			})
			if _, err := test.run(context.Background(), executor); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Error != nil || events[0].Arguments != nil || events[0].ServerRU != nil {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestObserveAppliesToQueryTerminals(t *testing.T) {
	tests := []struct {
		name    string
		columns []string
		values  [][]driver.Value
		run     func(context.Context, QueryExecutor) error
	}{
		{"all", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Query[scanModel]().Select("ID").All(ctx, executor)
			return err
		}},
		{"first", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Query[scanModel]().Select("ID").First(ctx, executor)
			return err
		}},
		{"only", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Query[scanModel]().Select("ID").Only(ctx, executor)
			return err
		}},
		{"count", []string{"count"}, [][]driver.Value{{int64(1)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Query[scanModel]().Count(ctx, executor)
			return err
		}},
		{"exists", []string{"exists"}, [][]driver.Value{{int64(1)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Query[scanModel]().Exists(ctx, executor)
			return err
		}},
		{"raw all", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Raw[scanModel]("SELECT id FROM scan_model").All(ctx, executor)
			return err
		}},
		{"raw first", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Raw[scanModel]("SELECT id FROM scan_model").First(ctx, executor)
			return err
		}},
		{"raw only", []string{"id"}, [][]driver.Value{{int64(7)}}, func(ctx context.Context, executor QueryExecutor) error {
			_, err := Raw[scanModel]("SELECT id FROM scan_model").Only(ctx, executor)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &allTestState{columns: test.columns, values: test.values}
			database := openAllTestDB(t, state)
			var events []StatementEvent
			executor := Observe(database, func(event StatementEvent) { events = append(events, event) })
			if err := test.run(context.Background(), executor); err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Error != nil || events[0].Operation != StatementSelect {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestObserveContextOverridePreservesCapture(t *testing.T) {
	for _, disable := range []bool{false, true} {
		var output bytes.Buffer
		var defaultCalls, overrideCalls int
		base := observeTestExecutor{ExecExecutor: &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}}
		executor := Observe(base, func(StatementEvent) { defaultCalls++ }, IncludeStatementArguments(), CollectServerRU())
		capture := NewRuntimeCapture(&output)
		ctx := WithRuntimeCapture(context.Background(), capture)
		var override StatementObserver
		if !disable {
			override = func(event StatementEvent) {
				overrideCalls++
				if event.Arguments != nil || event.ServerRU != nil {
					t.Fatalf("override inherited sensitive or costly options: %#v", event)
				}
			}
		}
		ctx = WithStatementObserver(ctx, override)
		if _, err := RawExec(ctx, executor, "UPDATE counters SET value = ?", "private-value"); err != nil {
			t.Fatal(err)
		}
		if defaultCalls != 0 || (!disable && overrideCalls != 1) || (disable && overrideCalls != 0) {
			t.Fatalf("default = %d, override = %d, disable = %v", defaultCalls, overrideCalls, disable)
		}
		if capture.Err() != nil || output.Len() == 0 || strings.Contains(output.String(), "private-value") {
			t.Fatalf("capture = %s, error = %v", output.String(), capture.Err())
		}
	}
}

func TestObservePreloadSharesConfiguration(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			{columns: []string{"user_id", "id", "name"}, values: [][]driver.Value{{int64(1), int64(10), "admin"}}},
		},
	}
	database := openPreloadTestDB(t, state)
	var events []StatementEvent
	executor := Observe(database, func(event StatementEvent) { events = append(events, event) })
	var output bytes.Buffer
	ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&output))
	values, err := Query[preloadUser]().Select("ID").Preload("Roles").All(ctx, executor)
	if err != nil || len(values) != 1 || len(values[0].Roles) != 1 || values[0].Roles[0].Name != "admin" {
		t.Fatalf("values = %#v, error = %v", values, err)
	}
	if len(events) != 2 || len(preloadCalls(state)) != 2 || strings.Count(output.String(), "\n") != 2 || !strings.Contains(output.String(), `"relation":"Roles"`) {
		t.Fatalf("events = %#v, capture = %s", events, output.String())
	}
}

func TestObserveExplainIncludesErrorsWithoutRUProbe(t *testing.T) {
	state := explainTestState([]driver.Value{"Point_Get_1", "1", "root", "table:scan_model", "handle:1"})
	database := openAllTestDB(t, state)
	var event StatementEvent
	executor := Observe(database, func(current StatementEvent) { event = current }, CollectServerRU())
	if _, err := Query[scanModel]().Select("ID").Explain(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if event.Operation != StatementExplain || event.ServerRU != nil || event.Error != nil {
		t.Fatalf("event = %#v", event)
	}
	state.queryErr = errors.New("query failed")
	if _, err := Query[scanModel]().ExplainAnalyze(context.Background(), executor); !errors.Is(err, state.queryErr) || !errors.Is(event.Error, state.queryErr) {
		t.Fatalf("error = %v, event = %#v", err, event)
	}
	if event.Operation != StatementExplainAnalyze || event.ServerRU != nil {
		t.Fatalf("event = %#v", event)
	}
}

func TestObserveCaptureContextKeepsDefaultObserverAndTypedMetadata(t *testing.T) {
	var output bytes.Buffer
	var event StatementEvent
	executor := Observe(observeTestExecutor{ExecExecutor: &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}}, func(current StatementEvent) { event = current }, IncludeStatementArguments())
	ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&output))
	if _, err := UpdateWhere[bulkMutationModel](Set("Value", int64(10))).Where(Equal("ID", int64(7))).Exec(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if len(event.Arguments) != 2 || !strings.Contains(output.String(), `"terminal":"update_where"`) || !strings.Contains(output.String(), `"mutation":`) {
		t.Fatalf("event = %#v, capture = %s", event, output.String())
	}
}

func TestObserveTransactionInheritsWithoutContextSetup(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		state := &serverRUObserverState{serverRU: `{"ru_consumption":2.75}`}
		database := openServerRUObserverDB(t, state)
		var events []StatementEvent
		executor := Observe(database, func(event StatementEvent) { events = append(events, event) }, CollectServerRU())
		failure := errors.New("callback failed")
		err := Transaction(context.Background(), executor, func(tx Executor) error {
			if _, err := RawExec(context.Background(), tx, "UPDATE counters SET value = ?", int64(2)); err != nil {
				return err
			}
			if rollback {
				return failure
			}
			return nil
		})
		if (!rollback && err != nil) || (rollback && !errors.Is(err, failure)) {
			t.Fatal(err)
		}
		wantLast := StatementCommit
		if rollback {
			wantLast = StatementRollback
		}
		if len(events) != 3 || events[0].Operation != StatementBegin || events[1].Operation != StatementUpdate || events[2].Operation != wantLast {
			t.Fatalf("events = %#v", events)
		}
		assertCollectedServerRU(t, events[1].ServerRU, 2.75)
		target, diagnostic, executions, probes, _ := state.snapshot()
		if target == 0 || target != diagnostic || executions != 1 || probes != 1 {
			t.Fatalf("connection/statement counts = %d, %d, %d, %d", target, diagnostic, executions, probes)
		}
	}
}

func TestObserveServerRUPinsPoolUntilRowsClose(t *testing.T) {
	state := &serverRUObserverState{
		serverRU: `{"ru_consumption":1.5}`, requireTargetRowsClose: true,
		targetColumns: []string{"id"}, targetValues: [][]driver.Value{{int64(7)}},
	}
	database := openServerRUObserverDB(t, state)
	var event StatementEvent
	executor := Observe(database, func(current StatementEvent) { event = current }, CollectServerRU())
	if _, err := Query[scanModel]().Select("ID").All(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	assertCollectedServerRU(t, event.ServerRU, 1.5)
	target, diagnostic, _, probes, rowsClosed := state.snapshot()
	if target == 0 || target != diagnostic || probes != 1 || !rowsClosed || database.Stats().InUse != 0 {
		t.Fatalf("target = %d, diagnostic = %d, probes = %d, rows closed = %v, in use = %d", target, diagnostic, probes, rowsClosed, database.Stats().InUse)
	}
}

func TestObserveReplacementAndDisabledFastPath(t *testing.T) {
	base := observeTestExecutor{ExecExecutor: &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}}
	var first, second int
	one := Observe(base, func(StatementEvent) { first++ })
	two := Observe(one, func(StatementEvent) { second++ })
	if _, err := RawExec(context.Background(), two, "DELETE FROM counters"); err != nil {
		t.Fatal(err)
	}
	if first != 0 || second != 1 || !reflect.DeepEqual(Observe(two, nil), base) {
		t.Fatalf("first = %d, second = %d, default removal failed", first, second)
	}
	if got := testing.AllocsPerRun(100, func() { executorStatementContext(context.Background(), base) }); got != 0 {
		t.Fatalf("plain executor context allocates %v times", got)
	}
	captureContext := WithRuntimeCapture(context.Background(), NewRuntimeCapture(nil))
	captured := &observedExecutor{Executor: base, observation: statementObserverContext(captureContext)}
	if got := testing.AllocsPerRun(100, func() { executorStatementContext(captureContext, captured) }); got != 0 {
		t.Fatalf("already inherited capture context allocates %v times", got)
	}
}

func TestObserveTransactionContextOverridesAndCaptureInheritance(t *testing.T) {
	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)
	var output bytes.Buffer
	var defaults, overrides int
	executor := Observe(database, func(StatementEvent) { defaults++ }, IncludeStatementArguments())
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	ctx = WithStatementObserver(ctx, func(event StatementEvent) {
		overrides++
		if event.Arguments != nil {
			t.Fatalf("override inherited bind values: %#v", event)
		}
	})
	err := Transaction(ctx, executor, func(tx Executor) error {
		if _, err := RawExec(context.Background(), tx, "UPDATE counters SET value = ?", int64(2)); err != nil {
			return err
		}
		// Removing or replacing a transaction's ordinary observer must not
		// remove the operation capture inherited from Transaction.
		if _, err := RawExec(context.Background(), Observe(tx, nil), "UPDATE counters SET value = ?", int64(3)); err != nil {
			return err
		}
		_, err := RawExec(WithStatementObserver(context.Background(), nil), tx, "UPDATE counters SET value = ?", int64(4))
		return err
	})
	if err != nil || defaults != 0 || overrides != 3 || capture.Err() != nil || strings.Count(output.String(), "\n") != 5 {
		t.Fatalf("error = %v, defaults = %d, overrides = %d, capture = %s", err, defaults, overrides, output.String())
	}
}

func TestObserveInvalidInputsDoNotExecute(t *testing.T) {
	base := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	executor := Observe(observeTestExecutor{ExecExecutor: base}, func(StatementEvent) { t.Error("unexpected event") })
	if _, err := RawExec(nil, executor, "DELETE FROM counters"); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := Query[scanModel]().Select("Missing").All(context.Background(), executor); err == nil {
		t.Fatal("invalid build accepted")
	}
	if err := Transaction(context.Background(), executor, func(Executor) error { t.Error("unexpected callback"); return nil }); err == nil {
		t.Fatal("unsupported beginner accepted")
	}
	if _, err := RawExec(context.Background(), Observe(nil, func(StatementEvent) {}), "DELETE FROM counters"); err == nil {
		t.Fatal("nil executor accepted")
	}
}

func TestObserveSharedExecutorConcurrentContexts(t *testing.T) {
	var defaults, overrides atomic.Int64
	base := observeTestExecutor{ExecExecutor: mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}}
	executor := Observe(base, func(StatementEvent) { defaults.Add(1) })
	var group sync.WaitGroup
	for i := range 40 {
		group.Go(func() {
			ctx := context.Background()
			if i%2 == 0 {
				ctx = WithStatementObserver(ctx, func(StatementEvent) { overrides.Add(1) })
			}
			if _, err := RawExec(ctx, executor, "DELETE FROM counters"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if defaults.Load() != 20 || overrides.Load() != 20 {
		t.Fatalf("defaults = %d, overrides = %d", defaults.Load(), overrides.Load())
	}
}

func BenchmarkObserveRawExec(b *testing.B) {
	base := observeTestExecutor{ExecExecutor: mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}}
	for _, mode := range []string{"plain", "context", "executor"} {
		b.Run(mode, func(b *testing.B) {
			var executor Executor = base
			ctx := context.Background()
			switch mode {
			case "context":
				ctx = WithStatementObserver(ctx, func(StatementEvent) {})
			case "executor":
				executor = Observe(base, func(StatementEvent) {})
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := RawExec(ctx, executor, "DELETE FROM counters"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
