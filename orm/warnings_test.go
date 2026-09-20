package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

type warningSource struct{ ID int64 }

func warningState() *aggregatePlanDriverState {
	return &aggregatePlanDriverState{
		target:   allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
		warnings: allTestState{columns: []string{"Level", "Code", "Message"}, values: [][]driver.Value{{"Warning", int64(1292), "private-warning-value"}}},
	}
}

func TestCollectWarningsUsesClosedRowsAndSameConnection(t *testing.T) {
	for _, kind := range []string{"db", "conn", "tx"} {
		t.Run(kind, func(t *testing.T) {
			state := warningState()
			db := sql.OpenDB(&aggregatePlanConnector{state: state})
			defer db.Close()
			db.SetMaxOpenConns(1)
			ctx := context.Background()
			var executor Executor = db
			switch kind {
			case "conn":
				conn, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				executor = conn
			case "tx":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				executor = tx
			}
			var event StatementEvent
			var capture, log bytes.Buffer
			logger := NewStatementLogger(&log)
			executor = Observe(executor, func(e StatementEvent) {
				event = e
				logger(e)
				if kind == "db" && db.Stats().InUse != 0 {
					t.Error("callback before release")
				}
			}, CollectWarnings())
			ctx = WithRuntimeCapture(ctx, NewRuntimeCapture(&capture))
			var got []int64
			if err := Query[warningSource]().Select("ID").ScanAll(ctx, executor, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != 1 {
				t.Fatal(got)
			}
			w := event.Warnings
			if w == nil || !w.Known || w.Error != nil || len(w.Warnings) != 1 || w.AuxiliaryStatements != 1 || w.DiagnosticDuration <= 0 {
				t.Fatalf("warnings=%#v", w)
			}
			if state.targetConnection != state.warningConnection || len(state.statements) != 2 {
				t.Fatal(state)
			}
			if w.Warnings[0].Message != "private-warning-value" {
				t.Fatal("caller lost raw warning")
			}
			if strings.Contains(log.String()+capture.String(), "private-warning-value") {
				t.Fatal("warning leaked to log or capture")
			}
			analysis, err := runtimecapture.AnalyzeReader(&capture)
			if err != nil || len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "WRN002" || analysis.Statistics.WarningCollections != 1 {
				t.Fatalf("%#v %v", analysis, err)
			}
			if !strings.Contains(log.String(), "WRN002") {
				t.Fatal(log.String())
			}
		})
	}
}

func TestCollectWarningsFailureAndCoverage(t *testing.T) {
	for _, kind := range []string{"disabled", "empty", "query", "columns", "scan", "iteration", "close", "target", "target_scan"} {
		t.Run(kind, func(t *testing.T) {
			state := warningState()
			failure := errors.New("private-failure")
			switch kind {
			case "empty":
				state.warnings.values = nil
			case "query":
				state.warnings.queryErr = failure
			case "columns":
				state.warnings.columns = []string{"invalid"}
			case "scan":
				state.warnings.values[0][1] = "invalid"
			case "iteration":
				state.warnings.nextErr = failure
			case "close":
				state.warnings.closeErr = failure
			case "target":
				state.target.queryErr = failure
			case "target_scan":
				state.target.values[0][0] = "invalid-int"
			}
			db := sql.OpenDB(&aggregatePlanConnector{state: state})
			defer db.Close()
			var event StatementEvent
			var log, capture bytes.Buffer
			logger := NewStatementLogger(&log)
			var options []StatementObserverOption
			if kind != "disabled" {
				options = []StatementObserverOption{CollectWarnings()}
			}
			ctx := WithStatementObserver(context.Background(), func(e StatementEvent) { event = e; logger(e) }, options...)
			if kind != "target" && kind != "target_scan" {
				ctx = WithRuntimeCapture(ctx, NewRuntimeCapture(&capture))
			}
			var got []int64
			err := Query[warningSource]().Select("ID").ScanAll(ctx, db, &got)
			if (err != nil) != strings.HasPrefix(kind, "target") {
				t.Fatalf("query error=%v", err)
			}
			if db.Stats().InUse != 0 {
				t.Fatal("leaked connection")
			}
			w := event.Warnings
			switch kind {
			case "disabled":
				if w != nil || len(state.statements) != 1 {
					t.Fatal(w)
				}
			case "empty":
				if w == nil || !w.Known || w.Error != nil || w.Warnings == nil || len(w.Warnings) != 0 {
					t.Fatal(w)
				}
			default:
				if w == nil || w.Known || w.Error == nil || len(w.Diagnostics()) != 1 || w.Diagnostics()[0].Code != "WRN003" {
					t.Fatal(w)
				}
				if strings.HasPrefix(kind, "target") && w.AuxiliaryStatements != 0 {
					t.Fatal("probed after target error")
				}
			}
			if !strings.HasPrefix(kind, "target") && strings.Contains(log.String()+capture.String(), "private-failure") {
				t.Fatal("auxiliary error leaked")
			}
			if capture.Len() > 0 {
				if _, err := runtimecapture.AnalyzeReader(&capture); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCollectWarningsConflictAndUnsupportedExecutor(t *testing.T) {
	state := &serverRUObserverState{serverRU: `{"ru_consumption":2.25}`}
	db := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(e StatementEvent) { event = e }, CollectWarnings(), CollectServerRU())
	if n, err := RawExec(ctx, db, "UPDATE counters SET value=1"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if event.Warnings == nil || !errors.Is(event.Warnings.Error, ErrWarningsWithServerRU) || event.Warnings.AuxiliaryStatements != 0 || event.Warnings.Known {
		t.Fatal(event.Warnings)
	}
	assertCollectedServerRU(t, event.ServerRU, 2.25)
	ctx = WithStatementObserver(context.Background(), func(e StatementEvent) { event = e }, CollectWarnings())
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	if n, err := RawExec(ctx, executor, "UPDATE counters SET value=1"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if event.Warnings == nil || event.Warnings.Error == nil || event.Warnings.AuxiliaryStatements != 0 {
		t.Fatal(event.Warnings)
	}
}

func TestCollectWarningsConnectionFailure(t *testing.T) {
	state := warningState()
	db := sql.OpenDB(&aggregatePlanConnector{state: state})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(e StatementEvent) { event = e }, CollectWarnings())
	var values []int64
	if err := Query[warningSource]().Select("ID").ScanAll(ctx, db, &values); err == nil {
		t.Fatal("closed DB succeeded")
	}
	if event.Warnings == nil || event.Warnings.Error == nil || event.Warnings.AuxiliaryStatements != 0 || event.Warnings.Known {
		t.Fatal(event.Warnings)
	}
}

func TestPlanWarningDiagnosticsWithPartialMPP(t *testing.T) {
	p := AggregatePlan{Requested: ReadPolicy{Engine: TiFlash, MPP: MPPEnforce}, Planned: []ExplainRow{{ID: "Window_1", Task: "root"}, {ID: "TableFullScan_2", Task: "mpp[tiflash]", AccessObject: "table:a"}}, Warnings: []PlanWarning{{Level: "Warning", Code: 1105, Message: "MPP mode may be blocked because window function `sum` or its arguments are not supported now."}}}
	d := p.Diagnostics()
	if len(d) != 1 || d[0].Code != "WRN001" {
		t.Fatal(d)
	}
	v := VectorPlan{Warnings: p.Warnings}
	if got := v.Diagnostics(); len(got) != 1 || got[0].Code != "WRN001" {
		t.Fatal(got)
	}
}

func TestWarningOptionsInheritance(t *testing.T) {
	for _, kind := range []string{"capture_only", "disable_observer", "context_override", "replace_capture"} {
		t.Run(kind, func(t *testing.T) {
			state := warningState()
			db := sql.OpenDB(&aggregatePlanConnector{state: state})
			defer db.Close()
			var capture bytes.Buffer
			var event StatementEvent
			var executor Executor = db
			ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&capture), CollectWarnings())
			want := true
			switch kind {
			case "disable_observer":
				executor = Observe(db, func(StatementEvent) { t.Error("disabled observer called") }, CollectWarnings())
				ctx = WithStatementObserver(ctx, nil)
			case "context_override":
				executor = Observe(db, func(StatementEvent) { t.Error("default observer called") }, CollectWarnings())
				ctx = WithStatementObserver(WithRuntimeCapture(context.Background(), NewRuntimeCapture(&capture)), func(e StatementEvent) { event = e })
				want = false
			case "replace_capture":
				ctx = WithRuntimeCapture(ctx, NewRuntimeCapture(&capture))
				want = false
			}
			var values []int64
			if err := Query[warningSource]().Select("ID").ScanAll(ctx, executor, &values); err != nil {
				t.Fatal(err)
			}
			if (state.warningConnection != 0) != want {
				t.Fatal("incorrect inherited warning collection")
			}
			analysis, err := runtimecapture.AnalyzeReader(&capture)
			if err != nil || (analysis.Statistics.WarningCollections == 1) != want {
				t.Fatal(analysis, err)
			}
			if !want && event.Warnings != nil {
				t.Fatal("overridden warning option survived")
			}
		})
	}
}

func TestComparePublishesWarningsWithoutInterferingWithRU(t *testing.T) {
	q, state := aggregateCompareBenchmarkData(1)
	state.record = true
	db := openAggregateCompareTestDB(t, state)
	var captured bytes.Buffer
	ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&captured), CollectWarnings())
	var events []StatementEvent
	executor := Observe(db, func(e StatementEvent) { events = append(events, e) })
	report, err := q.Compare(ctx, executor, AggregateCompareOptions{Case: "warning-observation"})
	if err != nil || !report.Complete || len(state.queries) != 48 {
		t.Fatal("comparison failed", err)
	}
	for i, e := range events {
		if e.Warnings == nil {
			t.Fatal("missing warning observation")
		}
		if i < 21 {
			if !errors.Is(e.Warnings.Error, ErrWarningsWithServerRU) || e.Warnings.AuxiliaryStatements != 0 {
				t.Fatal(e.Warnings)
			}
		} else if !e.Warnings.Known || e.Warnings.AuxiliaryStatements != 1 {
			t.Fatal(e.Warnings)
		}
	}
	analysis, err := runtimecapture.AnalyzeReader(&captured)
	if err != nil || analysis.Statistics.WarningCollections != 3 || analysis.Statistics.WarningCollectionErrors != 21 {
		t.Fatal(analysis.Statistics, err)
	}
}
