package tidbcloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/mayahiro/go-tidb/orm"
)

// TestTiDBCloudStarterIndexPlans isolates index hints from ORM SQL shape,
// statistics, and covering indexes. It records observations, not RU thresholds.
func TestTiDBCloudStarterIndexPlans(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if os.Getenv("TIDBGO_TEST_ORDERED_LIST") != "1" || dsn == "" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_ORDERED_LIST=1 for the index plan comparison")
	}
	config := parseTestDSN(t, dsn)
	if !validTestDatabaseName(config.DBName) || config.TLS == nil || config.TLS.InsecureSkipVerify || config.AllowFallbackToPlaintext {
		t.Fatal("index plan comparison requires a dedicated tidbgo_test_ database with verified TLS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	installOrderedListFixture(t, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve index plan connection", err)
	}
	defer connection.Close()
	var version string
	if err := connection.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		fatalDatabaseError(t, dsn, "read index plan server version", err)
	}
	t.Logf("server=%s interpolateParams=%t", version, config.InterpolateParams)
	logIndexPlanState(t, ctx, connection, dsn, "session", "SHOW VARIABLES WHERE Variable_name IN ('tidb_enable_non_prepared_plan_cache', 'tidb_enable_prepared_plan_cache', 'tidb_opt_ordering_index_selectivity_threshold', 'tidb_opt_prefer_ordering_index', 'tidb_cost_model_version', 'tidb_index_lookup_size', 'tidb_init_chunk_size', 'tidb_max_chunk_size', 'tidb_index_join_batch_size', 'tidb_executor_concurrency', 'tidb_analyze_column_options', 'tidb_stats_load_sync_wait', 'tidb_stats_load_pseudo_timeout', 'tidb_enable_paging')")
	for _, phase := range []string{"fresh", "analyzed", "covering"} {
		if phase == "covering" {
			if _, err := connection.ExecContext(ctx, "ALTER TABLE tidbgo_it_ordered_links ADD INDEX owner_order_extra (owner_id, added_at, id, target_id)"); err != nil {
				fatalDatabaseError(t, dsn, "add covering index to owned fixture", err)
			}
		}
		if phase != "fresh" {
			for _, table := range []string{"tidbgo_it_ordered_links", "tidbgo_it_ordered_targets"} {
				if _, err := connection.ExecContext(ctx, "ANALYZE TABLE "+table+" ALL COLUMNS"); err != nil {
					fatalDatabaseError(t, dsn, "analyze owned fixture", err)
				}
				logIndexPlanState(t, ctx, connection, dsn, phase+"/"+table, "SHOW WARNINGS")
			}
		}
		logIndexPlanState(t, ctx, connection, dsn, phase, "SHOW STATS_META WHERE Table_name IN ('tidbgo_it_ordered_links', 'tidbgo_it_ordered_targets')")
		logIndexPlanState(t, ctx, connection, dsn, phase, "SHOW STATS_HEALTHY WHERE Table_name IN ('tidbgo_it_ordered_links', 'tidbgo_it_ordered_targets')")
		for _, tc := range []orderedListCase{
			{name: "first_50", owner: 1, limit: 50},
			{name: "last", owner: 1, limit: 50, offset: 10800},
			{name: "large_limit", owner: 1, limit: 5000},
			{name: "few_matches", owner: 3, limit: 50},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				compareIndexPlans(t, ctx, connection, dsn, phase, tc)
			})
		}
	}
}

type indexPlanObservation struct {
	Phase     string                   `json:"phase"`
	Case      string                   `json:"case"`
	Shape     string                   `json:"shape"`
	SQL       string                   `json:"sql"`
	Arguments []any                    `json:"arguments"`
	Rows      int                      `json:"rows"`
	RU        []float64                `json:"ru"`
	Millis    []float64                `json:"ms"`
	Plans     []orm.ExplainAnalyzePlan `json:"plans"`
}

func compareIndexPlans(t *testing.T, ctx context.Context, connection *sql.Conn, dsn, phase string, tc orderedListCase) {
	t.Helper()
	shapes := []string{"plain", "explicit", "raw_default", "raw_force", "primary_index"}
	if phase == "covering" {
		shapes = append(shapes, "covering_index")
	}
	observations := make([]indexPlanObservation, len(shapes))
	queries := make([]*orm.SelectQuery[orderedListLinkModel], len(shapes))
	for i, shape := range shapes {
		observation := &observations[i]
		observation.Phase, observation.Case, observation.Shape = phase, tc.name, shape
		switch shape {
		case "raw_default":
			observation.SQL, observation.Arguments = orderedListSQL(t, "default", tc)
		case "raw_force":
			observation.SQL, observation.Arguments = orderedListSQL(t, "force_index", tc)
		default:
			query := orderedListBaseQuery(tc)
			switch shape {
			case "explicit":
				query.ForceIndex("owner_order")
			case "covering_index":
				query.ForceIndex("owner_order_extra")
			case "primary_index":
				query.ForceIndex("PRIMARY")
			}
			var err error
			observation.SQL, observation.Arguments, err = query.Build()
			if err != nil {
				t.Fatal(err)
			}
			queries[i] = query
		}
	}
	plain, explicit := observations[0], observations[1]
	const hint = " FORCE INDEX (`owner_order`)"
	if strings.Count(explicit.SQL, hint) != 1 || strings.Replace(explicit.SQL, hint, "", 1) != plain.SQL || !reflect.DeepEqual(explicit.Arguments, plain.Arguments) {
		t.Fatal("plain and explicit ORM queries must differ only by the index hint")
	}
	var reference []orderedListRow
	for sample := range 8 {
		for position := range len(shapes) {
			mode := (sample + position) % len(shapes)
			observation := &observations[mode]
			start := time.Now()
			var rows []orderedListRow
			if queries[mode] == nil {
				rows = readOrderedList(t, ctx, connection, dsn, observation.SQL, observation.Arguments)
			} else {
				rows = readCompiledOrderedList(t, ctx, connection, dsn, queries[mode])
			}
			elapsed := time.Since(start)
			ru, err := orm.LastServerRU(ctx, connection)
			if err != nil {
				fatalDatabaseError(t, dsn, "read index plan RU", err)
			}
			if sample == 0 && position == 0 {
				reference = rows
				verifyOrderedListRows(t, reference, tc)
			}
			if !reflect.DeepEqual(rows, reference) {
				t.Fatalf("shape=%s returned different IDs, order, or values", observation.Shape)
			}
			if sample != 0 {
				observation.RU = append(observation.RU, ru)
				observation.Millis = append(observation.Millis, float64(elapsed)/float64(time.Millisecond))
			}
		}
	}
	explainSamples := 1
	if tc.name == "large_limit" || tc.name == "last" {
		explainSamples = 7
	}
	for sample := range explainSamples {
		for position := range len(shapes) {
			observation := &observations[(sample+position)%len(shapes)]
			plan := explainOrderedList(t, ctx, connection, dsn, observation.SQL, observation.Arguments)
			observation.Plans = append(observation.Plans, plan)
			requireNoTiDBWarnings(t, ctx, connection, dsn, "index plan "+observation.Shape)
		}
	}
	for _, observation := range observations {
		observation.Rows = len(reference)
		data, err := json.Marshal(observation)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("index_plan_observation=%s", data)
		ru, millis := slices.Clone(observation.RU), slices.Clone(observation.Millis)
		slices.Sort(ru)
		slices.Sort(millis)
		t.Logf("phase=%s case=%s shape=%s rows=%d median_ServerRU=%.6f median_ms=%.3f", phase, tc.name, observation.Shape, observation.Rows, ru[len(ru)/2], millis[len(millis)/2])
	}
}

func logIndexPlanState(t *testing.T, ctx context.Context, connection *sql.Conn, dsn, phase, statement string) {
	t.Helper()
	rows, err := connection.QueryContext(ctx, statement)
	if err != nil {
		var serverError *mysql.MySQLError
		if errors.As(err, &serverError) && (serverError.Number == 1044 || serverError.Number == 1142 || serverError.Number == 1227) {
			t.Logf("index_plan_state=%s %s unavailable: database permission error %d", phase, statement, serverError.Number)
			return
		}
		fatalDatabaseError(t, dsn, "read index plan statistics", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]string
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(columns))
		for i := range destinations {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			fatalDatabaseError(t, dsn, "scan index plan statistics", err)
		}
		record := make(map[string]string, len(columns))
		for i, column := range columns {
			record[column] = string(values[i])
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		fatalDatabaseError(t, dsn, "finish index plan statistics", err)
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("index_plan_state=%s %s %s", phase, statement, data)
}
