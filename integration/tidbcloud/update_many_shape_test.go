package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/orm"
)

// This opt-in comparison deliberately executes several candidate SQL shapes
// and consumes extra RU without asserting a performance threshold.
func TestTiDBCloudStarterUpdateManySQLShapes(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected bulk UPDATE comparison")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	installUpdateManyComparisonFixture(t, ctx, database, dsn)
	config := parseTestDSN(t, dsn)
	t.Logf("interpolateParams=%t clientFoundRows=%t", config.InterpolateParams, config.ClientFoundRows)
	for _, count := range []int{25, 100, 500} {
		for _, shape := range []string{"case", "join", "join_hint"} {
			query, args := updateManyCandidateSQL(shape, count)
			var durations, costs []float64
			for sample := range 4 {
				tx, err := database.BeginTx(ctx, nil)
				if err != nil {
					fatalDatabaseError(t, dsn, "begin update comparison", err)
				}
				start := time.Now()
				result, err := tx.ExecContext(ctx, query, args...)
				elapsed := time.Since(start)
				if err != nil {
					_ = tx.Rollback()
					fatalDatabaseError(t, dsn, "execute update comparison "+shape, err)
				}
				ru, err := orm.LastServerRU(ctx, tx)
				if err != nil {
					_ = tx.Rollback()
					fatalDatabaseError(t, dsn, "read comparison RU", err)
				}
				affected, err := result.RowsAffected()
				if err != nil || affected != int64(count) {
					_ = tx.Rollback()
					t.Fatalf("shape %s affected=%d error=%v", shape, affected, err)
				}
				var matching int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_update_shapes WHERE id <= ? AND v0 = id + 1000 AND v1 = CONCAT('value-', id)", count).Scan(&matching); err != nil || matching != count {
					_ = tx.Rollback()
					t.Fatalf("shape %s matching=%d error=%s", shape, matching, redact.Error(err, dsn))
				}
				if err := tx.Rollback(); err != nil {
					fatalDatabaseError(t, dsn, "roll back update comparison", err)
				}
				if sample > 0 {
					durations = append(durations, float64(elapsed)/float64(time.Millisecond))
					costs = append(costs, ru)
				}
			}
			slices.Sort(durations)
			slices.Sort(costs)
			t.Logf("rows=%d shape=%s median_ms=%.3f median_ServerRU=%.6f args=%d SQL_bytes=%d", count, shape, durations[1], costs[1], len(args), len(query))
		}
	}
}

func installUpdateManyComparisonFixture(t testing.TB, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()
	if _, err := database.ExecContext(ctx, "CREATE TABLE tidbgo_it_update_shapes (id BIGINT PRIMARY KEY, v0 BIGINT NOT NULL, v1 VARCHAR(64) NOT NULL)"); err != nil {
		fatalDatabaseError(t, dsn, "create update comparison fixture; an existing table is never removed", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := database.ExecContext(cleanup, "DROP TABLE tidbgo_it_update_shapes"); err != nil {
			t.Errorf("drop update comparison fixture: %s", redact.Error(err, dsn))
		}
	})
	var insert strings.Builder
	insert.WriteString("INSERT INTO tidbgo_it_update_shapes VALUES ")
	for i := range 1000 {
		if i != 0 {
			insert.WriteByte(',')
		}
		fmt.Fprintf(&insert, "(%d,0,'before')", i+1)
	}
	if _, err := database.ExecContext(ctx, insert.String()); err != nil {
		fatalDatabaseError(t, dsn, "seed update comparison", err)
	}
}

func updateManyCandidateSQL(shape string, count int) (string, []any) {
	var query strings.Builder
	var args []any
	if shape == "case" {
		query.WriteString("UPDATE tidbgo_it_update_shapes SET v0 = CASE id ")
		for i := 1; i <= count; i++ {
			query.WriteString("WHEN ? THEN ? ")
			args = append(args, int64(i), int64(i+1000))
		}
		query.WriteString("ELSE v0 END, v1 = CASE id ")
		for i := 1; i <= count; i++ {
			query.WriteString("WHEN ? THEN ? ")
			args = append(args, int64(i), fmt.Sprintf("value-%d", i))
		}
		query.WriteString("ELSE v1 END WHERE id IN (")
		for i := 1; i <= count; i++ {
			if i != 1 {
				query.WriteByte(',')
			}
			query.WriteByte('?')
			args = append(args, int64(i))
		}
		query.WriteByte(')')
		return query.String(), args
	}
	query.WriteString("UPDATE ")
	if shape == "join_hint" {
		query.WriteString("/*+ LEADING(v,t) INL_JOIN(t) */ ")
	}
	query.WriteString("tidbgo_it_update_shapes AS t JOIN (")
	for i := 1; i <= count; i++ {
		if i == 1 {
			query.WriteString("SELECT ? AS id, ? AS v0, ? AS v1")
		} else {
			query.WriteString(" UNION ALL SELECT ?, ?, ?")
		}
		args = append(args, int64(i), int64(i+1000), fmt.Sprintf("value-%d", i))
	}
	query.WriteString(") AS v ON t.id = v.id SET t.v0 = v.v0, t.v1 = v.v1")
	return query.String(), args
}
