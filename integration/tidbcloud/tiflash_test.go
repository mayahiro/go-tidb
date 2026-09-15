package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

// TestTiDBCloudStarterTiFlash compares the same fixed dataset and SQL semantics.
// Explicit opt-in bounds the cost of creating a columnar replica.
func TestTiDBCloudStarterTiFlash(t *testing.T) {
	if os.Getenv("TIDBGO_TEST_TIFLASH") != "1" || os.Getenv(testDSNEnvironment) == "" {
		t.Skip("set TIDBGO_TEST_TIFLASH=1 and TIDBGO_TEST_DSN for connected TiFlash tests")
	}
	dsn := os.Getenv(testDSNEnvironment)
	parseTestDSN(t, dsn)
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	const table = "tidbgo_it_tiflash_aggregates"
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, shop_id BIGINT NOT NULL, amount DECIMAL(20,2) NULL, deleted_at DATETIME(6) NULL, KEY shop_idx(shop_id))"); err != nil {
		fatalDatabaseError(t, dsn, "create TiFlash fixture; pre-existing tables are never removed", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Errorf("drop owned TiFlash fixture: %s", redact.Error(err, dsn))
		}
	})
	for start := 0; start < 20000; start += 500 {
		var insert strings.Builder
		insert.WriteString("INSERT INTO " + table + " (id,shop_id,amount) VALUES ")
		args := make([]any, 0, 1500)
		for i := start; i < start+500; i++ {
			if i != start {
				insert.WriteByte(',')
			}
			insert.WriteString("(?,?,?)")
			var amount any = fmt.Sprintf("%d.25", i%97)
			if i%17 == 0 {
				amount = nil
			}
			args = append(args, int64(i+1), int64(i%100), amount)
		}
		if _, err := db.ExecContext(ctx, insert.String(), args...); err != nil {
			fatalDatabaseError(t, dsn, "seed TiFlash fixture", err)
		}
	}
	if _, err := db.ExecContext(ctx, "ANALYZE TABLE "+table); err != nil {
		fatalDatabaseError(t, dsn, "analyze fixture statistics", err)
	}
	missing, err := starterAggregateQuery("wide_scan", "tiflash_mpp").Explain(ctx, db)
	if err != nil {
		fatalDatabaseError(t, dsn, "explain missing replica", err)
	}
	if missing.WarningsError != nil || len(missing.Warnings) == 0 {
		t.Fatal("missing replica must retain hint warnings")
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" SET TIFLASH REPLICA 2"); err != nil {
		fatalDatabaseError(t, dsn, "enable owned TiFlash replica", err)
	}
	for {
		var available int
		if err := db.QueryRowContext(ctx, "SELECT AVAILABLE FROM information_schema.tiflash_replica WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&available); err != nil {
			fatalDatabaseError(t, dsn, "inspect TiFlash replica", err)
		}
		if available == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("TiFlash initial replica readiness timed out")
		case <-time.After(time.Second):
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "pin TiFlash connection", err)
	}
	defer conn.Close()
	var originalAllow, originalEnforce string
	if err := conn.QueryRowContext(ctx, "SELECT @@tidb_allow_mpp, @@tidb_enforce_mpp").Scan(&originalAllow, &originalEnforce); err != nil {
		fatalDatabaseError(t, dsn, "read session policy", err)
	}
	variants := []struct{ name, hint string }{
		{"auto", ""},
		{"tikv", "/*+ READ_FROM_STORAGE(TIKV[a]) */ "},
		{"tiflash_mpp", "/*+ READ_FROM_STORAGE(TIFLASH[a]) SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) */ "},
	}
	for _, workload := range []struct{ name, group, filter string }{
		{"wide_scan", "shop_id", ""},
		{"small_range", "shop_id", " AND id <= 20"},
		{"selective_index", "shop_id", " AND shop_id = 7"},
		{"many_groups", "id", ""},
	} {
		t.Run(workload.name, func(t *testing.T) {
			sqlFor := func(hint string) string {
				return "SELECT " + hint + "`" + workload.group + "`, COUNT(*), SUM(amount) FROM " + table + " AS a WHERE deleted_at IS NULL" + workload.filter + " GROUP BY `" + workload.group + "` ORDER BY `" + workload.group + "`"
			}
			want := readTiFlashComparison(t, ctx, conn, dsn, sqlFor(""))
			for _, variant := range variants {
				q := starterAggregateQuery(workload.name, variant.name)
				for range 2 {
					if got := readTiFlashComparison(t, ctx, conn, dsn, sqlFor(variant.hint)); !reflect.DeepEqual(got, want) {
						t.Fatal("warm-up result mismatch")
					}
					var got []tiFlashComparisonRow
					if err := q.ScanAll(ctx, conn, &got); err != nil {
						fatalDatabaseError(t, dsn, "warm aggregate builder", err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("aggregate warm-up values mismatch")
					}
				}
			}
			for round := range 3 {
				for step := range variants {
					variant := variants[(round+step)%len(variants)]
					q := starterAggregateQuery(workload.name, variant.name)
					for methodStep := range 2 {
						method := (methodStep + round) % 2
						started := time.Now()
						var got []tiFlashComparisonRow
						if method == 0 {
							got = readTiFlashComparison(t, ctx, conn, dsn, sqlFor(variant.hint))
						} else if err := q.ScanAll(ctx, conn, &got); err != nil {
							fatalDatabaseError(t, dsn, "execute aggregate builder", err)
						}
						elapsed := time.Since(started)
						// SHOW WARNINGS changes last-query state on Starter. Read RU
						// immediately; inspect warnings in a separate plan execution.
						ru, err := orm.LastServerRU(ctx, conn)
						if err != nil {
							fatalDatabaseError(t, dsn, "read comparison ServerRU", err)
						}
						if ru <= 0 {
							t.Fatal("comparison did not retain the target SELECT's ServerRU")
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatal("measured result values/order mismatch")
						}
						t.Logf("dataset=aggregate_v1 case=%s round=%d variant=%s method=%s rows=%d latency=%s ServerRU=%.6f", workload.name, round, variant.name, []string{"database_sql", "aggregate"}[method], len(got), elapsed, ru)
					}
				}
			}
			for _, variant := range variants {
				q := starterAggregateQuery(workload.name, variant.name)
				planned, err := q.Explain(ctx, conn)
				if err != nil {
					fatalDatabaseError(t, dsn, "explain aggregate", err)
				}
				if planned.WarningsError != nil || len(planned.Warnings) != 0 || planned.Planned == nil || planned.Executed != nil {
					t.Fatalf("unexpected planned observation: %#v", planned)
				}
				executed, err := q.ExplainAnalyze(ctx, conn)
				if err != nil {
					fatalDatabaseError(t, dsn, "analyze aggregate", err)
				}
				if executed.WarningsError != nil || len(executed.Warnings) != 0 || executed.Executed == nil || executed.Planned != nil {
					t.Fatalf("unexpected executed observation: %#v", executed)
				}
				for _, diagnostic := range executed.Diagnostics() {
					if diagnostic.Code == "PLN005" || diagnostic.Code == "PLN006" {
						t.Fatal(diagnostic)
					}
				}
				for _, row := range executed.Executed {
					if strings.Contains(row.AccessObject, "table:a") && (row.PhysicalTable != table || !row.TaskInfo().Known) {
						t.Fatalf("unresolved aggregate access: %#v", row)
					}
				}
				rows, err := conn.QueryContext(ctx, "EXPLAIN ANALYZE "+sqlFor(variant.hint))
				if err != nil {
					fatalDatabaseError(t, dsn, "observe comparison plan", err)
				}
				var tasks []string
				for rows.Next() {
					var id, estimated, actual, task, access, execution, operator, memory, disk string
					if err := rows.Scan(&id, &estimated, &actual, &task, &access, &execution, &operator, &memory, &disk); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(task, "[") {
						tasks = append(tasks, task+" "+access)
					}
				}
				if err := rows.Err(); err != nil {
					fatalDatabaseError(t, dsn, "read comparison plan", err)
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				t.Logf("variant=%s executed_tasks=%v warnings=%v", variant.name, tasks, tiFlashWarnings(t, ctx, conn, dsn))
				joined := strings.Join(tasks, " ")
				if variant.name == "tikv" && !strings.Contains(joined, "[tikv]") || variant.name == "tiflash_mpp" && !strings.Contains(joined, "mpp[tiflash]") {
					t.Fatal("execution did not match requested engine")
				}
			}
		})
	}
	t.Run("aggregate_contracts", func(t *testing.T) { testStarterAggregateContracts(t, ctx, conn, dsn) })
	var allow, enforce string
	if err := conn.QueryRowContext(ctx, "SELECT @@tidb_allow_mpp, @@tidb_enforce_mpp").Scan(&allow, &enforce); err != nil {
		fatalDatabaseError(t, dsn, "verify session policy", err)
	}
	if allow != originalAllow || enforce != originalEnforce {
		t.Fatal("statement MPP settings leaked to the connection")
	}
}

type starterAggregateSource struct {
	model.Meta `tidbgo:"table=tidbgo_it_tiflash_aggregates"`
	ID         int64 `tidbgo:",pk"`
	ShopID     int64
	Amount     starterDecimal
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

func starterAggregateQuery(workload, variant string) *orm.AggregateQuery[starterAggregateSource] {
	group := "ShopID"
	if workload == "many_groups" {
		group = "ID"
	}
	q := orm.Aggregate[starterAggregateSource]().Select(orm.Field(group).As("Key"), orm.CountAll().As("Count"), orm.Sum("Amount").As("Sum")).GroupBy(group).OrderBy(orm.Asc("Key"))
	if workload == "small_range" {
		q.Where(orm.LessThanOrEqual("ID", int64(20)))
	}
	if workload == "selective_index" {
		q.Where(orm.Equal("ShopID", int64(7)))
	}
	if variant == "tikv" {
		q.ReadFrom(orm.TiKV)
	}
	if variant == "tiflash_mpp" {
		q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
	}
	return q
}

func testStarterAggregateContracts(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string) {
	t.Helper()
	type numeric struct {
		Count, NonNull, Distinct       int64
		Sum, Average, Minimum, Maximum sql.NullString
	}
	var expected numeric
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*), COUNT(amount), COUNT(DISTINCT shop_id), SUM(amount), AVG(amount), MIN(amount), MAX(amount) FROM tidbgo_it_tiflash_aggregates WHERE deleted_at IS NULL").Scan(&expected.Count, &expected.NonNull, &expected.Distinct, &expected.Sum, &expected.Average, &expected.Minimum, &expected.Maximum); err != nil {
		fatalDatabaseError(t, dsn, "read aggregate function reference", err)
	}
	wantPaged := readTiFlashComparison(t, ctx, conn, dsn, "SELECT shop_id, COUNT(*), SUM(amount) FROM tidbgo_it_tiflash_aggregates WHERE deleted_at IS NULL GROUP BY shop_id HAVING COUNT(*) > 10 ORDER BY SUM(amount) DESC, shop_id ASC LIMIT 2 OFFSET 1")
	for _, engine := range []orm.StorageEngine{orm.TiKV, orm.TiFlash} {
		q := orm.Aggregate[starterAggregateSource]().Select(orm.CountAll().As("Count"), orm.Count("Amount").As("NonNull"), orm.CountDistinct("ShopID").As("Distinct"), orm.Sum("Amount").As("Sum"), orm.Avg("Amount").As("Average"), orm.Min("Amount").As("Minimum"), orm.Max("Amount").As("Maximum")).ReadFrom(engine)
		var result []numeric
		if err := q.ScanAll(ctx, conn, &result); err != nil {
			fatalDatabaseError(t, dsn, "populated aggregate functions", err)
		}
		if !reflect.DeepEqual(result, []numeric{expected}) {
			t.Fatalf("aggregate functions=%#v; want=%#v", result, expected)
		}
		if err := q.Where(orm.Equal("ID", int64(0))).ScanAll(ctx, conn, &result); err != nil {
			fatalDatabaseError(t, dsn, "empty aggregate", err)
		}
		if !reflect.DeepEqual(result, []numeric{{}}) {
			t.Fatalf("empty aggregate=%#v", result)
		}
		var nulls []numeric
		q = orm.Aggregate[starterAggregateSource]().Select(orm.CountAll().As("Count"), orm.Sum("Amount").As("Sum")).Where(orm.Equal("ID", int64(1))).ReadFrom(engine)
		if err := q.ScanAll(ctx, conn, &nulls); err != nil {
			fatalDatabaseError(t, dsn, "NULL aggregate", err)
		}
		if len(nulls) != 1 || nulls[0].Count != 1 || nulls[0].Sum.Valid {
			t.Fatal(nulls)
		}
		var paged []struct {
			Key   int64
			Count int64
			Sum   starterDecimal
		}
		// Both output names shadow SQL function names; source IDs are not grouped.
		q2 := orm.Aggregate[starterAggregateSource]().Select(orm.Field("ShopID").As("Key"), orm.CountAll().As("Count"), orm.Sum("Amount").As("Sum")).GroupBy("ShopID").Having(orm.GreaterThan("Count", int64(10))).OrderBy(orm.Desc("Sum"), orm.Asc("Key")).Limit(2).Offset(1).ReadFrom(engine)
		if err := q2.ScanAll(ctx, conn, &paged); err != nil {
			fatalDatabaseError(t, dsn, "HAVING and paging", err)
		}
		if len(paged) != len(wantPaged) {
			t.Fatal("unexpected grouped pagination")
		}
		for i, got := range paged {
			want := wantPaged[i]
			if got.Key != want.Key || got.Count != want.Count || got.Sum.text != want.Sum.String {
				t.Fatalf("paged result %d=%#v; want=%#v", i, got, want)
			}
		}
	}
	if _, err := conn.ExecContext(ctx, "UPDATE tidbgo_it_tiflash_aggregates SET deleted_at='2026-09-15 00:00:00' WHERE id=20000"); err != nil {
		fatalDatabaseError(t, dsn, "soft delete fixture row", err)
	}
	q := orm.Aggregate[starterAggregateSource]().Select(orm.CountAll().As("Count")).ReadFrom(orm.TiFlash)
	var counts []int64
	if err := q.ScanAll(ctx, conn, &counts); err != nil {
		fatalDatabaseError(t, dsn, "active aggregate", err)
	}
	if !reflect.DeepEqual(counts, []int64{19999}) {
		t.Fatal(counts)
	}
	if err := q.WithDeleted().ScanAll(ctx, conn, &counts); err != nil {
		fatalDatabaseError(t, dsn, "all-row aggregate", err)
	}
	if !reflect.DeepEqual(counts, []int64{20000}) {
		t.Fatal(counts)
	}
}

type tiFlashComparisonRow struct {
	Key, Count int64
	Sum        sql.NullString
}

func readTiFlashComparison(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, statement string) []tiFlashComparisonRow {
	t.Helper()
	rows, err := conn.QueryContext(ctx, statement)
	if err != nil {
		fatalDatabaseError(t, dsn, "execute comparison SELECT", err)
	}
	defer rows.Close()
	values := make([]tiFlashComparisonRow, 0)
	for rows.Next() {
		var value tiFlashComparisonRow
		if err := rows.Scan(&value.Key, &value.Count, &value.Sum); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		fatalDatabaseError(t, dsn, "scan comparison SELECT", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return values
}

func tiFlashWarnings(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		fatalDatabaseError(t, dsn, "read comparison warnings", err)
	}
	defer rows.Close()
	var warnings []string
	for rows.Next() {
		var level, message string
		var code int
		if err := rows.Scan(&level, &code, &message); err != nil {
			t.Fatal(err)
		}
		warnings = append(warnings, fmt.Sprintf("%s %d %s", level, code, message))
	}
	if err := rows.Err(); err != nil {
		fatalDatabaseError(t, dsn, "scan comparison warnings", err)
	}
	return warnings
}
