package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type updateManyBenchmarkRow struct {
	model.Meta `tidbgo:"table=tidbgo_it_update_shapes"`
	ID         int64 `tidbgo:",pk"`
	V0         int64
	V1         string
}

// BenchmarkTiDBCloudStarterUpdateMany measures only DML inside a transaction
// that is rolled back after every sample. It excludes BEGIN, ROLLBACK, fixture
// work, result verification, and the separate same-session RU samples.
func BenchmarkTiDBCloudStarterUpdateMany(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping connected bulk UPDATE benchmark")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	database := openTestDatabase(b, dsn)
	verifyConnectedTarget(b, ctx, database, dsn)
	installUpdateManyComparisonFixture(b, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve bulk UPDATE benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close bulk UPDATE benchmark connection: %s", redact.Error(err, dsn))
		}
	})
	config := parseTestDSN(b, dsn)
	b.Logf("interpolateParams=%t clientFoundRows=%t; DML only, rollback samples, not committed operation cost", config.InterpolateParams, config.ClientFoundRows)
	for _, count := range []int{25, 100, 500} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			for _, mode := range []string{"loop", "update_many", "join", "join_hint"} {
				b.Run(mode, func(b *testing.B) {
					benchmarkUpdateManyDML(b, ctx, connection, dsn, count, mode)
				})
			}
		})
	}
}

func benchmarkUpdateManyDML(b *testing.B, ctx context.Context, connection *sql.Conn, dsn string, count int, mode string) {
	b.Helper()
	values := make([]*updateManyBenchmarkRow, count)
	for i := range values {
		values[i] = &updateManyBenchmarkRow{ID: int64(i + 1), V0: int64(i + 1001), V1: fmt.Sprintf("value-%d", i+1)}
	}
	var query string
	var args []any
	if mode == "join" || mode == "join_hint" {
		query, args = updateManyCandidateSQL(mode, count)
	}
	execute := func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
		switch mode {
		case "update_many":
			return orm.UpdateMany(values, "V0", "V1").Exec(ctx, executor)
		case "loop":
			var affected int64
			for _, value := range values {
				current, err := orm.Update(value, "V0", "V1").Exec(ctx, executor)
				if err != nil {
					return affected, err
				}
				affected += current
			}
			return affected, nil
		default:
			return orm.RawExec(ctx, executor, query, args...)
		}
	}
	run := func(timed, collect, verify bool) writeBenchmarkObservation {
		tx, err := connection.BeginTx(ctx, nil)
		if err != nil {
			fatalDatabaseError(b, dsn, "begin bulk UPDATE benchmark", err)
		}
		defer tx.Rollback()
		var metrics writeBenchmarkObservation
		executionContext := ctx
		if collect {
			executionContext = orm.WithStatementObserver(ctx, metrics.observe, orm.CollectServerRU())
		}
		if timed {
			b.StartTimer()
		}
		affected, err := execute(executionContext, tx)
		if timed {
			b.StopTimer()
		}
		if err != nil {
			fatalDatabaseError(b, dsn, "execute bulk UPDATE benchmark", err)
		}
		if affected != int64(count) {
			b.Fatalf("affected rows=%d want=%d", affected, count)
		}
		if verify {
			var matching int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_update_shapes WHERE id <= ? AND v0 = id + 1000 AND v1 = CONCAT('value-', id)", count).Scan(&matching); err != nil {
				fatalDatabaseError(b, dsn, "verify bulk UPDATE benchmark result", err)
			}
			if matching != count {
				b.Fatalf("matching updated rows=%d want=%d", matching, count)
			}
		}
		if err := tx.Rollback(); err != nil {
			fatalDatabaseError(b, dsn, "roll back bulk UPDATE benchmark", err)
		}
		return metrics
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	run(false, false, true)
	for range b.N {
		run(true, false, false)
	}
	wantStatements := 1
	if mode == "loop" {
		wantStatements = count
	}
	var totalRU float64
	for range 3 {
		metrics := run(false, true, true)
		if err := metrics.validate(wantStatements, false); err != nil {
			b.Fatal(err)
		}
		totalRU += metrics.ru
	}
	b.ReportMetric(totalRU/3, "DML-ServerRU/op")
	b.ReportMetric(float64(wantStatements), "DML-statements/op")
}
