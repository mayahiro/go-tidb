package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type starterConditionalSource struct {
	model.Meta `tidbgo:"table=tidbgo_it_conditional_aggregates"`
	ID         int64 `tidbgo:",pk"`
	ShopID     int64
	Status     string
	Amount     starterDecimal
	CreatedAt  *time.Time
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type starterConditionalResult struct {
	model.Meta `tidbgo:"table=conditional_results"`
	Key        sql.NullString `tidbgo:"Key"`
	AllCount   int64          `tidbgo:"AllCount"`
	Count      int64          `tidbgo:"Count"`
	Total      sql.NullString `tidbgo:"Total"`
}

// TestTiDBCloudStarterConditionalAggregates compares combined metrics with IF
// expressions and separately filtered queries over one owned, fixed fixture.
func TestTiDBCloudStarterConditionalAggregates(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_TIFLASH") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_TIFLASH=1 for connected conditional tests")
	}
	config := parseTestDSN(t, dsn)
	config.ParseTime, config.Loc = true, time.UTC
	dsn = config.FormatDSN()
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "pin conditional fixture", err)
	}
	defer conn.Close()
	const table = "tidbgo_it_conditional_aggregates"
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, shop_id BIGINT NOT NULL, status VARCHAR(16) NULL, amount DECIMAL(20,2) NULL, created_at DATETIME(6) NULL, deleted_at DATETIME(6) NULL, KEY status_idx(status), KEY created_idx(created_at))"); err != nil {
		fatalDatabaseError(t, dsn, "create conditional fixture; pre-existing tables are never removed", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Errorf("drop owned conditional fixture: %s", redact.Error(err, dsn))
		}
	})
	for start := 0; start < 20000; start += 500 {
		var statement strings.Builder
		statement.WriteString("INSERT INTO " + table + " (id,shop_id,status,amount,created_at,deleted_at) VALUES ")
		args := make([]any, 0, 3000)
		for i := start; i < start+500; i++ {
			if i != start {
				statement.WriteByte(',')
			}
			statement.WriteString("(?,?,?,?,?,?)")
			args = append(args, conditionalFixtureRow(i)...)
		}
		if _, err := conn.ExecContext(ctx, statement.String(), args...); err != nil {
			fatalDatabaseError(t, dsn, "seed conditional fixture", err)
		}
	}
	for _, statement := range []string{"ANALYZE TABLE " + table, "ALTER TABLE " + table + " SET TIFLASH REPLICA 2"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			fatalDatabaseError(t, dsn, "prepare conditional fixture", err)
		}
	}
	for {
		var ready int
		if err := conn.QueryRowContext(ctx, "SELECT AVAILABLE FROM information_schema.tiflash_replica WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&ready); err != nil {
			fatalDatabaseError(t, dsn, "inspect conditional replica", err)
		}
		if ready == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("conditional replica readiness timed out")
		case <-time.After(time.Second):
		}
	}
	t.Run("contracts", func(t *testing.T) { testConditionalContracts(t, ctx, dsn) })
	for _, workload := range []string{"all_metrics", "day_metrics", "month_metrics", "rare_metrics"} {
		t.Run(workload, func(t *testing.T) {
			expectedRows := 0
			for _, variant := range []string{"auto", "tikv", "tiflash_mpp"} {
				q := starterConditionalQuery(workload, variant)
				var want []starterConditionalResult
				if err := q.ScanAll(ctx, conn, &want); err != nil {
					fatalDatabaseError(t, dsn, "scan conditional builder", err)
				}
				expectedRows = len(want)
				statement, args, err := q.Build()
				if err != nil {
					t.Fatal(err)
				}
				manual, manualArgs := conditionalMetricSQL(workload, variant, "case")
				if statement != manual || !reflect.DeepEqual(args, manualArgs) {
					t.Fatal("builder differs from independently written conditional SQL")
				}
				measureConditionalAlternatives(t, ctx, conn, dsn, workload, variant, want)
			}
			report, err := starterConditionalQuery(workload, "auto").Compare(ctx, conn, orm.AggregateCompareOptions{Case: "conditional-v1-" + workload})
			if err != nil || !report.Complete {
				fatalDatabaseError(t, dsn, "compare conditional policies", err)
			}
			for _, variant := range report.Variants {
				if variant.Rows != int64(expectedRows) || len(variant.Samples) != 5 {
					t.Fatal("conditional comparison lost rows or samples")
				}
				storageAggregations := 0
				for _, row := range variant.Plan.Executed {
					if strings.Contains(row.ID, "Agg") && row.TaskInfo().Engine != "" {
						storageAggregations++
					}
				}
				t.Logf("Compare case=%s variant=%s rows=%d median_ms=%.3f mean_ServerRU=%.6f plan=%s storage_aggregations=%d", workload, variant.Name, variant.Rows, variant.LatencyMS.Median, variant.ServerRU.Mean, variant.PlanStatus, storageAggregations)
			}
		})
	}
}

func conditionalFixtureRow(i int) []any {
	var status, amount, at, deleted any = "paid", fmt.Sprintf("%d.25", i%97), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format("2006-01-02 15:04:05"), nil
	switch {
	case i%1000 == 0:
		status = "rare"
	case i%29 == 0:
		status = nil
	case i%2 == 1:
		status = "pending"
	}
	if i%17 == 0 {
		amount = nil
	}
	if i%101 == 0 {
		deleted = "2026-01-01 00:00:00"
	}
	shop := int64(i%16 + 1)
	if i < 8 {
		status = []any{"paid", "paid", "pending", nil, "paid", "cancelled", "paid", "%_!"}[i]
		amount = []any{"12.50", nil, "4.00", "7.00", "0.00", "-2.75", "15.00", "5.00"}[i]
		at = []any{"2024-01-31 23:59:59", "2024-01-31 23:59:59.999999", "2024-02-01 00:00:00", nil, "2024-02-29 23:59:59.999999", "2024-03-01 00:00:00", "2024-03-01 00:00:00", "2024-03-15 00:00:00"}[i]
		deleted = nil
		if i == 6 {
			deleted = "2026-01-01 00:00:00"
		}
		shop = 2
		if status == "paid" {
			shop = 1
		}
	}
	if i == 8 {
		status, amount = "rare", "9007199254740993.25"
	}
	return []any{int64(i + 1), shop, status, amount, at, deleted}
}

func starterConditionalQuery(workload, variant string) *orm.AggregateQuery[starterConditionalSource] {
	q := orm.Aggregate[starterConditionalSource]()
	if workload == "day_metrics" {
		q.Select(orm.Date("CreatedAt").As("Key")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	}
	if workload == "month_metrics" {
		q.Select(orm.YearMonth("CreatedAt").As("Key")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	}
	status := "paid"
	if workload == "rare_metrics" {
		status = "rare"
	} else {
		q.Select(orm.CountAll().As("AllCount"))
	}
	q.Select(orm.CountIf(orm.Equal("Status", status)).As("Count"), orm.SumIf("Amount", orm.Equal("Status", status)).As("Total"))
	if variant == "tikv" {
		q.ReadFrom(orm.TiKV)
	}
	if variant == "tiflash_mpp" {
		q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
	}
	return q
}

// All fragments are fixed fixture SQL, independent of the aggregate compiler.
func conditionalMetricSQL(workload, variant, method string) (string, []any) {
	hint := ""
	if variant == "tikv" {
		hint = "/*+ READ_FROM_STORAGE(TIKV[a]) */ "
	}
	if variant == "tiflash_mpp" {
		hint = "/*+ READ_FROM_STORAGE(TIFLASH[a]) SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) */ "
	}
	key := ""
	if workload == "day_metrics" {
		key = "DATE(`a`.`created_at`)"
	}
	if workload == "month_metrics" {
		key = "EXTRACT(YEAR_MONTH FROM `a`.`created_at`)"
	}
	columns := ""
	if key != "" {
		columns = key + " AS `Key`, "
	}
	status := "paid"
	if workload == "rare_metrics" {
		status = "rare"
	}
	var args []any
	switch method {
	case "base":
		columns += "COUNT(*) AS `AllCount`"
	case "filtered":
		columns += "COUNT(*) AS `Count`, SUM(`a`.`amount`) AS `Total`"
		args = []any{status}
	default:
		if workload != "rare_metrics" {
			columns += "COUNT(*) AS `AllCount`, "
		}
		if method == "if" {
			columns += "COUNT(IF(`a`.`status` = ?, 1, NULL)) AS `Count`, SUM(IF(`a`.`status` = ?, `a`.`amount`, NULL)) AS `Total`"
		} else {
			columns += "COUNT(CASE WHEN `a`.`status` = ? THEN 1 END) AS `Count`, SUM(CASE WHEN `a`.`status` = ? THEN `a`.`amount` END) AS `Total`"
		}
		args = []any{status, status}
	}
	statement := "SELECT " + hint + columns + " FROM `tidbgo_it_conditional_aggregates` AS `a` WHERE `a`.`deleted_at` IS NULL"
	if method == "filtered" {
		statement += " AND `a`.`status` = ?"
	}
	if key != "" {
		statement += " GROUP BY " + key + " ORDER BY " + key + " ASC"
	}
	return statement, args
}

func readConditionalAlternative(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, workload, variant, method string) ([]starterConditionalResult, time.Duration, float64) {
	t.Helper()
	var duration time.Duration
	var ru float64
	read := func(mode string) []starterConditionalResult {
		statement, args := conditionalMetricSQL(workload, variant, mode)
		started := time.Now()
		rows, err := orm.Raw[starterConditionalResult](statement, args...).All(ctx, conn)
		duration += time.Since(started)
		if err != nil {
			fatalDatabaseError(t, dsn, "read conditional SQL reference", err)
		}
		value, err := orm.LastServerRU(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "measure conditional SQL RU", err)
		}
		ru += value
		return rows
	}
	if method != "separate" {
		rows := read(method)
		return rows, duration, ru
	}
	if workload == "rare_metrics" {
		rows := read("filtered")
		return rows, duration, ru
	}
	all, filtered := read("base"), read("filtered")
	started := time.Now()
	index := make(map[sql.NullString]int, len(all))
	for i, row := range all {
		index[row.Key] = i
	}
	for _, row := range filtered {
		i, found := index[row.Key]
		if !found {
			t.Fatal("filtered group has no corresponding input group")
		}
		all[i].Count, all[i].Total = row.Count, row.Total
	}
	duration += time.Since(started)
	return all, duration, ru
}

func measureConditionalAlternatives(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, workload, variant string, want []starterConditionalResult) {
	t.Helper()
	methods := []string{"case", "if", "separate"}
	latency, ru := [3][]float64{}, [3]float64{}
	for round := range 7 {
		for step := range methods {
			i := (round + step) % len(methods)
			got, elapsed, value := readConditionalAlternative(t, ctx, conn, dsn, workload, variant, methods[i])
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("conditional values/order differ for %s", methods[i])
			}
			if round >= 2 {
				latency[i] = append(latency[i], float64(elapsed)/1e6)
				ru[i] += value
			}
		}
	}
	for i, method := range methods {
		slices.Sort(latency[i])
		statements := 1
		if method == "separate" && workload != "rare_metrics" {
			statements = 2
		}
		t.Logf("SQL case=%s variant=%s method=%s statements=%d rows=%d median_ms=%.3f mean_ServerRU=%.6f", workload, variant, method, statements, len(want), latency[i][2], ru[i]/5)
	}
}

func testConditionalContracts(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	for _, interpolate := range []bool{false, true} {
		t.Run(fmt.Sprintf("interpolate_%t", interpolate), func(t *testing.T) {
			config := parseTestDSN(t, dsn)
			config.InterpolateParams = interpolate
			currentDSN := config.FormatDSN()
			db := openTestDatabase(t, currentDSN)
			for _, engine := range []orm.StorageEngine{orm.TiKV, orm.TiFlash} {
				for _, tc := range []struct {
					name      string
					condition orm.Predicate
					onlyID    int64
					deleted   bool
					count     int64
					total     sql.NullString
				}{
					{"paid", orm.Equal("Status", "paid"), -1, false, 3, sql.NullString{String: "12.50", Valid: true}},
					{"no match", orm.Equal("Status", "absent"), -1, false, 0, sql.NullString{}},
					{"all NULL amounts", orm.Equal("Status", "paid"), 2, false, 1, sql.NullString{}},
					{"empty", orm.Equal("Status", "paid"), 0, false, 0, sql.NullString{}},
					{"with deleted", orm.Equal("Status", "paid"), -1, true, 4, sql.NullString{String: "27.50", Valid: true}},
					{"unknown under NOT", orm.Not(orm.Equal("Status", "paid")), -1, false, 3, sql.NullString{String: "6.25", Valid: true}},
					{"NULL predicate", orm.IsNull("Status"), -1, false, 1, sql.NullString{String: "7.00", Valid: true}},
					{"escaped LIKE", orm.Contains("Status", "%_!"), -1, false, 1, sql.NullString{String: "5.00", Valid: true}},
					{"exact decimal", orm.Equal("Status", "rare"), 9, false, 1, sql.NullString{String: "9007199254740993.25", Valid: true}},
				} {
					q := orm.Aggregate[starterConditionalSource]().Select(orm.CountIf(tc.condition).As("Count"), orm.SumIf("Amount", tc.condition).As("Total")).ReadFrom(engine)
					if tc.onlyID < 0 {
						q.Where(orm.LessThanOrEqual("ID", int64(8)))
					} else {
						q.Where(orm.Equal("ID", tc.onlyID))
					}
					if tc.deleted {
						q.WithDeleted()
					}
					var got []starterConditionalResult
					if err := q.ScanAll(ctx, db, &got); err != nil {
						fatalDatabaseError(t, currentDSN, tc.name, err)
					}
					want := []starterConditionalResult{{Count: tc.count, Total: tc.total}}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s engine=%s got=%#v want=%#v", tc.name, engine, got, want)
					}
				}
				q := orm.Aggregate[starterConditionalSource]().Select(orm.Field("ShopID").As("Key"), orm.CountAll().As("AllCount"), orm.CountIf(orm.Equal("Status", "paid")).As("Count"), orm.SumIf("Amount", orm.Equal("Status", "paid")).As("Total")).GroupBy("Key").OrderBy(orm.Asc("Key")).Where(orm.LessThanOrEqual("ID", int64(8))).ReadFrom(engine)
				var groups []starterConditionalResult
				if err := q.ScanAll(ctx, db, &groups); err != nil {
					fatalDatabaseError(t, currentDSN, "preserve unmatched groups", err)
				}
				if len(groups) != 2 || groups[1].AllCount != 4 || groups[1].Count != 0 || groups[1].Total.Valid {
					t.Fatal("conditional filter removed a group or converted NULL to zero", groups)
				}
				if err := q.Where(orm.Equal("ID", int64(0))).ScanAll(ctx, db, &groups); err != nil || groups == nil || len(groups) != 0 {
					t.Fatal("empty grouped conditional query", redact.Error(err, currentDSN))
				}
				alias := orm.Aggregate[starterConditionalSource]().Select(orm.Field("ShopID").As("Shop"), orm.CountIf(orm.Equal("Status", "paid")).As("Shop_id")).GroupBy("Shop").Where(orm.LessThanOrEqual("ID", int64(8))).Having(orm.GreaterThan("Shop_id", int64(0))).OrderBy(orm.Desc("Shop_id"), orm.Asc("Shop")).ReadFrom(engine)
				var collisions []struct{ Shop, Shop_id int64 }
				if err := alias.ScanAll(ctx, db, &collisions); err != nil || len(collisions) != 1 || collisions[0].Shop != 1 || collisions[0].Shop_id != 3 {
					t.Fatal("HAVING/order used a colliding source column", redact.Error(err, currentDSN))
				}
				for _, key := range []orm.AggregateExpression{orm.Date("CreatedAt"), orm.YearMonth("CreatedAt")} {
					calendar := orm.Aggregate[starterConditionalSource]().Select(key.As("Key"), orm.CountIf(orm.Equal("Status", "paid")).As("Count"), orm.SumIf("Amount", orm.Equal("Status", "paid")).As("Total")).GroupBy("Key").Where(orm.LessThanOrEqual("ID", int64(8))).Having(orm.GreaterThan("Count", int64(0))).OrderBy(orm.Asc("Key")).Limit(1).Offset(1).ReadFrom(engine)
					var page []starterConditionalResult
					if err := calendar.ScanAll(ctx, db, &page); err != nil || len(page) != 1 || page[0].Count != 1 || !page[0].Total.Valid || page[0].Total.String != "0.00" {
						t.Fatal("calendar conditional HAVING/paging", redact.Error(err, currentDSN))
					}
				}
			}
		})
	}
}
