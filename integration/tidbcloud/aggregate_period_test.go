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

type starterPeriodSource struct {
	model.Meta `tidbgo:"table=tidbgo_it_period_aggregates"`
	ID         int64 `tidbgo:",pk"`
	CreatedAt  *time.Time
	StampedAt  *time.Time
	StoreID    int64
	Amount     starterDecimal
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type starterPeriodResult struct {
	model.Meta `tidbgo:"table=period_results"`
	Key        sql.NullString `tidbgo:"Key"`
	Count      int64          `tidbgo:"Count"`
	Total      sql.NullString `tidbgo:"Total"`
	Store      int64          `tidbgo:"Store"`
}

var periodBoundaryTimes = []string{
	"", "2023-12-31 23:59:59.999999", "2024-01-01 00:00:00",
	"2024-02-29 23:59:59.999999", "2024-03-01 00:00:00",
	"2024-12-31 23:59:59.999999", "2025-01-01 00:00:00",
}

// TestTiDBCloudStarterPeriodAggregates owns one fixed calendar fixture. It checks
// calendar boundaries and compares native keys with equivalent formatting SQL.
func TestTiDBCloudStarterPeriodAggregates(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_TIFLASH") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_TIFLASH=1 for connected calendar tests")
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
		fatalDatabaseError(t, dsn, "pin calendar fixture", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET time_zone = '+00:00'"); err != nil {
		fatalDatabaseError(t, dsn, "set fixture time zone", err)
	}
	const table = "tidbgo_it_period_aggregates"
	if _, err := conn.ExecContext(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, created_at DATETIME(6) NULL, stamped_at TIMESTAMP(6) NULL, store_id BIGINT NOT NULL, amount DECIMAL(20,2) NULL, deleted_at DATETIME(6) NULL, KEY created_idx(created_at), KEY stamped_idx(stamped_at))"); err != nil {
		fatalDatabaseError(t, dsn, "create calendar fixture; pre-existing tables are never removed", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Errorf("drop owned calendar fixture: %s", redact.Error(err, dsn))
		}
	})
	base := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)
	for start := 0; start < 20000; start += 500 {
		var statement strings.Builder
		statement.WriteString("INSERT INTO " + table + " (id,created_at,stamped_at,store_id,amount,deleted_at) VALUES ")
		args := make([]any, 0, 3000)
		for i := start; i < start+500; i++ {
			if i != start {
				statement.WriteByte(',')
			}
			statement.WriteString("(?,?,?,?,?,?)")
			var at any = base.Add(time.Duration(i) * time.Hour).Format("2006-01-02 15:04:05.999999")
			if i < len(periodBoundaryTimes) {
				at = periodBoundaryTimes[i]
				if at == "" {
					at = nil
				}
			}
			var amount, deleted any = int64(i%97 + 1), nil
			if i%19 == 0 {
				amount = nil
			}
			if i >= len(periodBoundaryTimes) && i%101 == 0 {
				deleted = "2026-01-01 00:00:00"
			}
			args = append(args, int64(i+1), at, at, int64(i%16), amount, deleted)
		}
		if _, err := conn.ExecContext(ctx, statement.String(), args...); err != nil {
			fatalDatabaseError(t, dsn, "seed calendar fixture", err)
		}
	}
	for _, statement := range []string{"ANALYZE TABLE " + table, "ALTER TABLE " + table + " SET TIFLASH REPLICA 2"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			fatalDatabaseError(t, dsn, "prepare calendar fixture", err)
		}
	}
	for {
		var ready int
		if err := conn.QueryRowContext(ctx, "SELECT AVAILABLE FROM information_schema.tiflash_replica WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table).Scan(&ready); err != nil {
			fatalDatabaseError(t, dsn, "inspect calendar replica", err)
		}
		if ready == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("calendar replica readiness timed out")
		case <-time.After(time.Second):
		}
	}
	t.Run("boundaries", func(t *testing.T) { testPeriodBoundaries(t, ctx, dsn) })
	for _, workload := range []string{"date_week", "date_all", "month_all", "date_stores"} {
		t.Run(workload, func(t *testing.T) {
			expectedRows := 0
			for _, variant := range []string{"auto", "tikv", "tiflash_mpp"} {
				q := starterPeriodQuery(workload, variant)
				statement, args, err := q.Build()
				if err != nil {
					t.Fatal(err)
				}
				var typed []starterPeriodResult
				if err := q.ScanAll(ctx, conn, &typed); err != nil {
					fatalDatabaseError(t, dsn, "scan calendar aggregate", err)
				}
				expectedRows = len(typed)
				// The alternative preserves SQL DATE/integer result types, NULLs,
				// filtering and ordering, and includes a year in monthly keys.
				formatted := strings.ReplaceAll(statement, "DATE(`a`.`created_at`)", "CAST(DATE_FORMAT(`a`.`created_at`, '%Y-%m-%d') AS DATE)")
				formatted = strings.ReplaceAll(formatted, "EXTRACT(YEAR_MONTH FROM `a`.`created_at`)", "CAST(DATE_FORMAT(`a`.`created_at`, '%Y%m') AS SIGNED)")
				if got := readPeriodSQL(t, ctx, conn, dsn, formatted, args); !reflect.DeepEqual(got, typed) {
					t.Fatal("formatted SQL and typed result values/order differ")
				}
				measurePeriodAlternatives(t, ctx, conn, dsn, workload, variant, statement, formatted, args, typed)
			}
			report, err := starterPeriodQuery(workload, "auto").Compare(ctx, conn, orm.AggregateCompareOptions{Case: "calendar-v1-" + workload})
			if err != nil || !report.Complete {
				fatalDatabaseError(t, dsn, "compare calendar policies", err)
			}
			for _, variant := range report.Variants {
				if variant.Rows != int64(expectedRows) || len(variant.Samples) != 5 {
					t.Fatal("calendar comparison lost result or sample coverage")
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
	var zone string
	if err := conn.QueryRowContext(ctx, "SELECT @@session.time_zone").Scan(&zone); err != nil || zone != "+00:00" {
		t.Fatalf("calendar operations changed session time zone: %q, %s", zone, redact.Error(err, dsn))
	}
}

func starterPeriodQuery(workload, variant string) *orm.AggregateQuery[starterPeriodSource] {
	expr := orm.Date("CreatedAt")
	if workload == "month_all" {
		expr = orm.YearMonth("CreatedAt")
	}
	q := orm.Aggregate[starterPeriodSource]().Select(expr.As("Key"), orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	if workload == "date_week" {
		q.Where(orm.GreaterThanOrEqual("CreatedAt", time.Date(2024, 2, 26, 0, 0, 0, 0, time.UTC)), orm.LessThan("CreatedAt", time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC)))
	}
	if workload == "date_all" {
		q.Having(orm.IsNotNull("Key"))
	}
	if workload == "date_stores" {
		q.Select(orm.Field("StoreID").As("Store")).GroupBy("Store").OrderBy(orm.Asc("Store"))
	}
	if variant == "tikv" {
		q.ReadFrom(orm.TiKV)
	}
	if variant == "tiflash_mpp" {
		q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
	}
	return q
}

func readPeriodSQL(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, statement string, args []any) []starterPeriodResult {
	t.Helper()
	result, err := orm.Raw[starterPeriodResult](statement, args...).All(ctx, conn)
	if err != nil {
		fatalDatabaseError(t, dsn, "read calendar SQL reference", err)
	}
	return result
}

func measurePeriodAlternatives(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, workload, variant, native, formatted string, args []any, want []starterPeriodResult) {
	t.Helper()
	statements := []string{native, formatted}
	latency := [2][]float64{}
	ru := [2]float64{}
	for round := range 7 {
		for step := range 2 {
			index := (round + step) % 2
			started := time.Now()
			got := readPeriodSQL(t, ctx, conn, dsn, statements[index], args)
			duration := time.Since(started)
			value, err := orm.LastServerRU(ctx, conn)
			if err != nil {
				fatalDatabaseError(t, dsn, "measure calendar SQL RU", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("calendar SQL alternative changed results")
			}
			if round >= 2 {
				latency[index] = append(latency[index], float64(duration)/1e6)
				ru[index] += value
			}
		}
	}
	for i, method := range []string{"native", "format_cast"} {
		slices.Sort(latency[i])
		t.Logf("SQL case=%s variant=%s method=%s rows=%d median_ms=%.3f mean_ServerRU=%.6f", workload, variant, method, len(want), latency[i][2], ru[i]/5)
	}
}

func testPeriodBoundaries(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{time.UTC, jst} {
		for _, interpolate := range []bool{false, true} {
			t.Run(fmt.Sprintf("loc_%s/interpolate_%t", loc, interpolate), func(t *testing.T) {
				config := parseTestDSN(t, dsn)
				config.Loc, config.ParseTime, config.InterpolateParams = loc, true, interpolate
				currentDSN := config.FormatDSN()
				db := openTestDatabase(t, currentDSN)
				conn, err := db.Conn(ctx)
				if err != nil {
					fatalDatabaseError(t, currentDSN, "pin calendar boundary session", err)
				}
				defer conn.Close()
				for _, zone := range []struct {
					statement string
					loc       *time.Location
				}{{"SET time_zone = '+00:00'", time.UTC}, {"SET time_zone = '+09:00'", jst}} {
					if _, err := conn.ExecContext(ctx, zone.statement); err != nil {
						fatalDatabaseError(t, currentDSN, "set calendar boundary session", err)
					}
					for _, field := range []string{"CreatedAt", "StampedAt"} {
						for _, month := range []bool{false, true} {
							expr := orm.Date(field)
							if month {
								expr = orm.YearMonth(field)
							}
							q := orm.Aggregate[starterPeriodSource]().Select(expr.As("Key"), orm.CountAll().As("Count")).GroupBy("Key").Where(orm.LessThanOrEqual("ID", int64(len(periodBoundaryTimes)))).OrderBy(orm.Asc("Key"))
							want := periodBoundaryExpected(t, field == "StampedAt", month, loc, zone.loc)
							for _, engine := range []orm.StorageEngine{orm.TiKV, orm.TiFlash} {
								var got []starterPeriodResult
								if err := q.ReadFrom(engine).ScanAll(ctx, conn, &got); err != nil {
									fatalDatabaseError(t, currentDSN, "scan calendar boundaries", err)
								}
								if !reflect.DeepEqual(got, want) {
									t.Fatalf("field=%s month=%t session=%s engine=%s got=%#v want=%#v", field, month, zone.loc, engine, got, want)
								}
							}
							// Store_id deliberately collides with the grouped physical
							// store_id column under TiDB's case-insensitive lookup.
							alias := orm.Aggregate[starterPeriodSource]().Select(expr.As("Store_id"), orm.Field("StoreID").As("Store"), orm.CountAll().As("Count")).GroupBy("Store_id", "Store").Where(orm.LessThanOrEqual("ID", int64(len(periodBoundaryTimes)))).Having(orm.IsNotNull("Store_id"))
							var collision []struct {
								Store_id     sql.NullString
								Store, Count int64
							}
							if err := alias.ScanAll(ctx, conn, &collision); err != nil || len(collision) != len(periodBoundaryTimes)-1 {
								t.Fatalf("calendar alias bound to physical grouped column: rows=%d error=%s", len(collision), redact.Error(err, currentDSN))
							}
							// NULL predicates, HAVING, and paging reference the selected
							// expression even when the source has a different field name.
							var page []starterPeriodResult
							if err := q.Having(orm.IsNotNull("Key")).Limit(1).Offset(1).ScanAll(ctx, conn, &page); err != nil || !reflect.DeepEqual(page, want[2:3]) {
								t.Fatalf("calendar paging mismatch: %s", redact.Error(err, currentDSN))
							}
						}
					}
				}
			})
		}
	}
	t.Run("date_types_and_empty", func(t *testing.T) {
		config := parseTestDSN(t, dsn)
		config.ParseTime = false
		currentDSN := config.FormatDSN()
		db := openTestDatabase(t, currentDSN)
		q := orm.Aggregate[starterPeriodSource]().Select(orm.Date("CreatedAt").As("Day")).GroupBy("Day").Where(orm.Equal("ID", int64(2)))
		var text []string
		if err := q.ScanAll(ctx, db, &text); err != nil || !reflect.DeepEqual(text, []string{"2023-12-31"}) {
			t.Fatalf("DATE without parseTime: %v %s", text, redact.Error(err, currentDSN))
		}
		var dates []time.Time
		if err := q.ScanAll(ctx, db, &dates); err == nil {
			t.Fatal("DATE without parseTime unexpectedly scanned into time.Time")
		}
		if err := q.Where(orm.Equal("ID", int64(0))).ScanAll(ctx, db, &text); err != nil || text == nil || len(text) != 0 {
			t.Fatal("empty calendar grouping did not return an empty slice")
		}
	})
}

func periodBoundaryExpected(t *testing.T, timestamp, month bool, driverLoc, sessionLoc *time.Location) []starterPeriodResult {
	t.Helper()
	counts := make(map[string]int64)
	for _, text := range periodBoundaryTimes {
		if text == "" {
			counts[""]++
			continue
		}
		at, err := time.Parse("2006-01-02 15:04:05.999999", text)
		if err != nil {
			t.Fatal(err)
		}
		if timestamp {
			at = at.In(sessionLoc)
		}
		key := fmt.Sprintf("%d", at.Year()*100+int(at.Month()))
		if !month {
			key = time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, driverLoc).Format(time.RFC3339Nano)
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	want := make([]starterPeriodResult, len(keys))
	for i, key := range keys {
		want[i] = starterPeriodResult{Key: sql.NullString{String: key, Valid: key != ""}, Count: counts[key]}
	}
	return want
}
