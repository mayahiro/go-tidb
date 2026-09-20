package tidbcloud

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/orm"
)

// TestTiDBCloudStarterWarningState checks the session state used by diagnostics.
func TestTiDBCloudStarterWarningState(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("set TIDBGO_TEST_DSN for connected warning tests")
	}
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	const table = "tidbgo_it_warning_state"
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY)"); err != nil {
		fatalDatabaseError(t, dsn, "create warning fixture", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Errorf("cleanup warning fixture: %s", redact.Error(err, dsn))
		}
	})
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1),(2)"); err != nil {
		fatalDatabaseError(t, dsn, "seed warning fixture", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "pin warnings", err)
	}
	defer conn.Close()
	query := func() {
		var id int64
		if err := conn.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE id = CAST(? AS SIGNED)", "1-not-a-number").Scan(&id); err != nil {
			fatalDatabaseError(t, dsn, "query warning fixture", err)
		}
		if id != 1 {
			t.Fatal("unexpected query result")
		}
	}
	warnings := func() int {
		rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
		if err != nil {
			fatalDatabaseError(t, dsn, "read warnings", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var level, message string
			var code int
			if err := rows.Scan(&level, &code, &message); err != nil {
				fatalDatabaseError(t, dsn, "scan warning", err)
			}
			n++
		}
		if err := rows.Err(); err != nil {
			fatalDatabaseError(t, dsn, "finish warnings", err)
		}
		return n
	}
	ru := func() float64 {
		value, err := orm.LastServerRU(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "read warning query RU", err)
		}
		return value
	}
	query()
	directWarnings := warnings()
	ruAfterWarnings := ru()
	query()
	directRU := ru()
	warningsAfterRU := warnings()
	t.Logf("direct warnings=%d RU after warnings=%.6f direct RU=%.6f warnings after RU=%d", directWarnings, ruAfterWarnings, directRU, warningsAfterRU)
	if directWarnings == 0 || directRU <= 0 {
		t.Fatal("fixture did not establish warnings and positive RU")
	}
	if ruAfterWarnings != 0 || warningsAfterRU != 0 {
		t.Fatal("diagnostic probe interference changed; review the collection policy")
	}
	testWarningObservation(t, ctx, conn, dsn)
}

type warningRow struct{ ID int64 }

func testWarningObservation(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string) {
	query := orm.Raw[warningRow]("SELECT id FROM tidbgo_it_warning_state WHERE id=CAST(? AS SIGNED)", "1-not-a-number")
	var event orm.StatementEvent
	var captured bytes.Buffer
	captureCtx := orm.WithRuntimeCapture(ctx, orm.NewRuntimeCapture(&captured), orm.CollectWarnings())
	observed := orm.Observe(conn, func(e orm.StatementEvent) { event = e })
	got, err := query.All(captureCtx, observed)
	if err != nil {
		fatalDatabaseError(t, dsn, "observed warnings", err)
	}
	if len(got) != 1 || got[0].ID != 1 || event.Warnings == nil || !event.Warnings.Known || len(event.Warnings.Warnings) == 0 || event.Warnings.Error != nil {
		t.Fatal("warnings were not observed correctly")
	}
	analysis, err := runtimecapture.AnalyzeReader(&captured)
	if err != nil || len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "WRN002" {
		t.Fatal("warning capture analysis failed", err)
	}
	both := orm.WithStatementObserver(ctx, func(e orm.StatementEvent) { event = e }, orm.CollectWarnings(), orm.CollectServerRU())
	if _, err = query.All(both, conn); err != nil {
		fatalDatabaseError(t, dsn, "combined probes", err)
	}
	if !errors.Is(event.Warnings.Error, orm.ErrWarningsWithServerRU) || event.Warnings.Known || event.ServerRU == nil || !event.ServerRU.Known || event.ServerRU.Value <= 0 {
		t.Fatal("diagnostic conflict policy failed")
	}
	warningCtx := orm.WithStatementObserver(ctx, func(e orm.StatementEvent) { event = e }, orm.CollectWarnings())
	if n, err := orm.RawExec(warningCtx, conn, "INSERT IGNORE INTO tidbgo_it_warning_state VALUES (1)"); err != nil || n != 0 {
		fatalDatabaseError(t, dsn, "mutation warning", err)
	}
	if event.Warnings == nil || !event.Warnings.Known || len(event.Warnings.Warnings) != 1 {
		t.Fatal("mutation warning not collected")
	}
	for _, query := range []*orm.RawQuery[warningRow]{
		orm.Raw[warningRow]("SELECT id FROM tidbgo_it_warning_state WHERE id=?", int64(1)),
		orm.Raw[warningRow]("SELECT id FROM tidbgo_it_warning_state WHERE id=CAST(? AS SIGNED)", "1-not-a-number"),
	} {
		var timings [2][]time.Duration
		for round := 0; round < 9; round++ {
			for step := 0; step < 2; step++ {
				method := (round + step) % 2
				measurementCtx := ctx
				if method == 1 {
					measurementCtx = warningCtx
				}
				started := time.Now()
				rows, err := query.All(measurementCtx, conn)
				elapsed := time.Since(started)
				if err != nil {
					fatalDatabaseError(t, dsn, "warning latency measurement", err)
				}
				if !reflect.DeepEqual(rows, []warningRow{{ID: 1}}) {
					t.Fatal("latency results differ")
				}
				if round >= 2 {
					timings[method] = append(timings[method], elapsed)
				}
			}
		}
		for i := range timings {
			sort.Slice(timings[i], func(a, b int) bool { return timings[i][a] < timings[i][b] })
		}
		t.Logf("warning rows=%d small SELECT median without collection=%s with collection=%s samples=%d", len(event.Warnings.Warnings), timings[0][3], timings[1][3], len(timings[0]))
	}
}
