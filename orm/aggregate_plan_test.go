package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

type aggregatePlanDriverState struct {
	target, warnings                                 allTestState
	connections, targetConnection, warningConnection int
	statements                                       []string
}

type aggregatePlanConnector struct{ state *aggregatePlanDriverState }

func (c *aggregatePlanConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.connections++
	return &aggregatePlanConn{state: c.state, id: c.state.connections}, nil
}
func (*aggregatePlanConnector) Driver() driver.Driver { return allTestDriver{} }

type aggregatePlanConn struct {
	state *aggregatePlanDriverState
	id    int
}

func (*aggregatePlanConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*aggregatePlanConn) Close() error                        { return nil }
func (*aggregatePlanConn) Begin() (driver.Tx, error)           { return allTestTx{}, nil }
func (c *aggregatePlanConn) QueryContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.statements = append(c.state.statements, statement)
	state := &c.state.target
	if statement == "SHOW WARNINGS" {
		if state.closeCalls != 1 {
			return nil, errors.New("plan rows not closed before warning probe")
		}
		state = &c.state.warnings
		c.state.warningConnection = c.id
	} else {
		c.state.targetConnection = c.id
	}
	if state.queryErr != nil {
		return nil, state.queryErr
	}
	return &allTestRows{state: state}, nil
}

func aggregatePlanState(analyze bool) *aggregatePlanDriverState {
	state := &aggregatePlanDriverState{
		target:   allTestState{columns: explainColumnNames[:], values: [][]driver.Value{{"TableFullScan_1", "10000", "cop[tikv]", "table:a", "stats:pseudo"}}},
		warnings: allTestState{columns: []string{"Level", "Code", "Message"}, values: [][]driver.Value{{"Warning", int64(1105), "private-warning-value"}}},
	}
	if analyze {
		state.target = allTestState{columns: explainAnalyzeColumnNames[:], values: [][]driver.Value{{"TableFullScan_1", "10000", int64(10000), "cop[tikv]", "table:a", "time:1ms", "", "N/A", "N/A"}}}
	}
	return state
}

func TestAggregatePlanPinsWarningsBeforeCallbacks(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "executed"}[analyze], func(t *testing.T) {
			state := aggregatePlanState(analyze)
			db := sql.OpenDB(&aggregatePlanConnector{state: state})
			defer db.Close()
			db.SetMaxOpenConns(1)
			var events []StatementEvent
			observed := Observe(db, func(event StatementEvent) {
				if state.warningConnection == 0 || db.Stats().InUse != 0 {
					t.Error("callback ran before warnings/release")
				}
				events = append(events, event)
			}, CollectServerRU())
			var capture bytes.Buffer
			ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&capture))
			q := aggregateStatsQuery().ReadFrom(TiFlash).MPP(MPPEnforce)
			var plan AggregatePlan
			var err error
			if analyze {
				plan, err = q.ExplainAnalyze(ctx, observed)
			} else {
				plan, err = q.Explain(ctx, observed)
			}
			if err != nil || plan.WarningsError != nil {
				t.Fatalf("plan=%#v; error=%v", plan, err)
			}
			if (plan.Executed != nil) != analyze || (plan.Planned != nil) == analyze || len(plan.Warnings) != 1 {
				t.Fatalf("observation states=%#v", plan)
			}
			if plan.Requested.Engine != TiFlash || plan.Requested.MPP != MPPEnforce {
				t.Fatal(plan.Requested)
			}
			if state.targetConnection != state.warningConnection || len(state.statements) != 2 || state.statements[1] != "SHOW WARNINGS" {
				t.Fatalf("sessions/statements=%#v", state)
			}
			if len(events) != 1 || events[0].ServerRU != nil || events[0].Error != nil {
				t.Fatalf("events=%#v", events)
			}
			if events[0].Warnings == nil || !events[0].Warnings.Known || events[0].Warnings.AuxiliaryStatements != 1 {
				t.Fatal("missing plan warning observation")
			}
			if strings.Contains(capture.String(), "private-warning-value") {
				t.Fatal("warning leaked to runtime capture")
			}
			if analyze && (plan.Executed[0].PhysicalTable != "aggregate_orders" || plan.Executed[0].Model != "aggregateOrder") {
				t.Fatal(plan.Executed)
			}
			codes := map[string]check.Severity{}
			for _, diagnostic := range plan.Diagnostics() {
				codes[diagnostic.Code] = diagnostic.Severity
			}
			if codes["PLN005"] != check.SeverityWarning || codes["PLN006"] != check.SeverityWarning {
				t.Fatal(codes)
			}
			if analyze && codes["PLN003"] != check.SeverityInfo {
				t.Fatal(codes)
			}
		})
	}
}

func TestAggregatePlanRetainsResultOnWarningFailure(t *testing.T) {
	failure := errors.New("warning probe denied")
	state := aggregatePlanState(false)
	state.warnings.queryErr = failure
	db := sql.OpenDB(&aggregatePlanConnector{state: state})
	defer db.Close()
	plan, err := aggregateStatsQuery().Explain(context.Background(), db)
	if err != nil || len(plan.Planned) != 1 || !errors.Is(plan.WarningsError, failure) || plan.Warnings != nil || db.Stats().InUse != 0 {
		t.Fatalf("plan=%#v; err=%v", plan, err)
	}
}

func TestAggregatePlanTargetFailuresAndBorrowedSessions(t *testing.T) {
	for _, kind := range []string{"query", "iteration", "close", "columns"} {
		t.Run(kind, func(t *testing.T) {
			state := aggregatePlanState(false)
			failure := errors.New("target failure")
			switch kind {
			case "query":
				state.target.queryErr = failure
			case "iteration":
				state.target.nextErr = failure
			case "close":
				state.target.closeErr = failure
			case "columns":
				state.target.columns = []string{"unsupported"}
			}
			db := sql.OpenDB(&aggregatePlanConnector{state: state})
			defer db.Close()
			_, err := aggregateStatsQuery().Explain(context.Background(), db)
			if err == nil || state.warningConnection != 0 || db.Stats().InUse != 0 {
				t.Fatalf("error=%v; state=%#v", err, state)
			}
			if kind != "columns" && !errors.Is(err, failure) {
				t.Fatal(err)
			}
		})
	}
	for _, transactional := range []bool{false, true} {
		state := aggregatePlanState(false)
		db := sql.OpenDB(&aggregatePlanConnector{state: state})
		var executor QueryExecutor
		var release func() error
		if transactional {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			executor, release = tx, tx.Rollback
		} else {
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			executor, release = conn, conn.Close
		}
		_, err := aggregateStatsQuery().Explain(context.Background(), executor)
		if err != nil || db.Stats().InUse != 1 {
			t.Fatalf("borrowed session closed: %v", err)
		}
		_ = release()
		_ = db.Close()
	}
}

func TestPlanTaskClassificationAndUnknownDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		task string
		want PlanTask
	}{
		{"root", PlanTask{Kind: "root", Known: true}},
		{"cop[tikv]", PlanTask{Engine: TiKV, Kind: "cop", Known: true}},
		{"cop[tiflash]", PlanTask{Engine: TiFlash, Kind: "cop", Known: true}},
		{"batchCop[tiflash]", PlanTask{Engine: TiFlash, Kind: "batchCop", Known: true}},
		{"mpp[tiflash]", PlanTask{Engine: TiFlash, Kind: "mpp", Known: true}},
		{"new[tiflash]", PlanTask{}},
		{"cop[unknown]", PlanTask{}},
	} {
		if got := (ExplainRow{Task: tc.task}).TaskInfo(); got != tc.want {
			t.Fatalf("%s=%#v", tc.task, got)
		}
		if got := (ExplainAnalyzeRow{Task: tc.task}).TaskInfo(); got != tc.want {
			t.Fatal(got)
		}
	}
	plan := AggregatePlan{Requested: ReadPolicy{Engine: TiFlash, MPP: MPPEnforce}, Executed: ExplainAnalyzePlan{
		{ID: "HashAgg_1", Task: "root"}, {ID: "HashAgg_2", Task: "mpp[tiflash]"}, {ID: "TableFullScan_3", Task: "mpp[tiflash]", AccessObject: "table:a"},
	}}
	if got := plan.Diagnostics(); len(got) != 0 {
		t.Fatal(got)
	}
	plan.Executed[1].Task = "future[tiflash]"
	plan.Executed[2].Task = "cop[tiflash]"
	if got := plan.Diagnostics(); len(got) != 0 {
		t.Fatal("unknown task treated as proof of missing MPP", got)
	}
	copy := append(ExplainAnalyzePlan(nil), plan.Executed...)
	_ = plan.Diagnostics()
	if !reflect.DeepEqual(copy, plan.Executed) {
		t.Fatal("diagnostics mutated plan")
	}
}
