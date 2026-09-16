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

type starterAggregateRelationNode struct {
	model.Meta `tidbgo:"table=tidbgo_it_aggregate_relation_nodes"`
	ID         int64 `tidbgo:",pk"`
	ParentID   *int64
	ShopID     int64
	Status     string
	Amount     starterDecimal
	CreatedAt  *time.Time
	DeletedAt  time.Time                      `tidbgo:",soft_delete"`
	Parent     *starterAggregateRelationNode  `tidbgo:"belongs_to,join=ParentID:ID"`
	Children   []starterAggregateRelationNode `tidbgo:"has_many,join=ID:ParentID"`
	Links      []starterAggregateRelationEdge `tidbgo:"has_many,join=ID:SourceID"`
	Targets    []starterAggregateRelationNode `tidbgo:"many_to_many,via=Links.Target"`
}

type starterAggregateRelationEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_aggregate_relation_edges"`
	ID         int64 `tidbgo:",pk"`
	SourceID   int64
	TargetID   *int64
	DeletedAt  time.Time                     `tidbgo:",soft_delete"`
	Target     *starterAggregateRelationNode `tidbgo:"belongs_to,join=TargetID:ID"`
}

type starterAggregateRelationResult struct {
	model.Meta `tidbgo:"table=aggregate_relation_results"`
	Key        sql.NullString `tidbgo:"Key"`
	AllCount   int64          `tidbgo:"AllCount"`
	Count      int64          `tidbgo:"Count"`
	Total      sql.NullString `tidbgo:"Total"`
}

// TestTiDBCloudStarterAggregateRelations owns fixed source, target, and via
// data. It compares EXISTS and deduplicated JOINs without multiplying inputs.
func TestTiDBCloudStarterAggregateRelations(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_TIFLASH") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_TIFLASH=1 for connected relation aggregate tests")
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
		fatalDatabaseError(t, dsn, "pin relation aggregate fixture", err)
	}
	defer conn.Close()
	for _, definition := range []struct{ table, columns string }{
		{"tidbgo_it_aggregate_relation_nodes", "id BIGINT PRIMARY KEY, parent_id BIGINT NULL, shop_id BIGINT NOT NULL, status VARCHAR(16) NULL, amount DECIMAL(20,2) NULL, created_at DATETIME(6) NULL, deleted_at DATETIME(6) NULL, KEY parent_idx(parent_id), KEY status_parent_idx(status,parent_id)"},
		{"tidbgo_it_aggregate_relation_edges", "id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL, target_id BIGINT NULL, deleted_at DATETIME(6) NULL, KEY source_idx(source_id), KEY target_idx(target_id)"},
	} {
		if _, err := conn.ExecContext(ctx, "CREATE TABLE "+definition.table+" ("+definition.columns+")"); err != nil {
			fatalDatabaseError(t, dsn, "create owned relation fixture; pre-existing tables are never removed", err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := db.ExecContext(cleanup, "DROP TABLE "+definition.table); err != nil {
				t.Errorf("drop owned relation fixture: %s", redact.Error(err, dsn))
			}
		})
	}
	for _, edge := range []bool{false, true} {
		count, table, placeholders := 30000, "tidbgo_it_aggregate_relation_nodes", "(?,?,?,?,?,?,?)"
		if edge {
			count, table, placeholders = 20000, "tidbgo_it_aggregate_relation_edges", "(?,?,?,?)"
		}
		for start := 0; start < count; start += 500 {
			var statement strings.Builder
			statement.WriteString("INSERT INTO " + table + " VALUES ")
			args := make([]any, 0, 4000)
			for i := start; i < start+500; i++ {
				if i != start {
					statement.WriteByte(',')
				}
				statement.WriteString(placeholders)
				args = append(args, aggregateRelationFixtureRow(i, edge)...)
			}
			if _, err := conn.ExecContext(ctx, statement.String(), args...); err != nil {
				fatalDatabaseError(t, dsn, "seed relation fixture", err)
			}
		}
	}
	for _, table := range []string{"tidbgo_it_aggregate_relation_nodes", "tidbgo_it_aggregate_relation_edges"} {
		for _, statement := range []string{"ANALYZE TABLE " + table, "ALTER TABLE " + table + " SET TIFLASH REPLICA 2"} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				fatalDatabaseError(t, dsn, "prepare relation fixture", err)
			}
		}
		for {
			var ready int
			if err := conn.QueryRowContext(ctx, "SELECT AVAILABLE FROM information_schema.tiflash_replica WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&ready); err != nil {
				fatalDatabaseError(t, dsn, "inspect relation replica", err)
			}
			if ready == 1 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("relation replica readiness timed out")
			case <-time.After(time.Second):
			}
		}
	}
	t.Run("contracts", func(t *testing.T) { testAggregateRelationContracts(t, ctx, dsn) })
	for _, workload := range []string{"broad", "selective", "many_groups", "via"} {
		t.Run(workload, func(t *testing.T) {
			expectedRows := 0
			for _, variant := range []string{"auto", "tikv", "tiflash_mpp"} {
				q := starterAggregateRelationQuery(workload, variant)
				var want []starterAggregateRelationResult
				if err := q.ScanAll(ctx, conn, &want); err != nil {
					fatalDatabaseError(t, dsn, "read relation aggregate", err)
				}
				expectedRows = len(want)
				statement, args, err := q.Build()
				manual, manualArgs := aggregateRelationSQL(workload, variant, "rewrite")
				if err != nil || statement != manual || !reflect.DeepEqual(args, manualArgs) {
					t.Fatalf("builder differs from independent SQL: %s; %s; %v", statement, manual, err)
				}
				measureAggregateRelationAlternatives(t, ctx, conn, dsn, workload, variant, want)
			}
			report, err := starterAggregateRelationQuery(workload, "auto").Compare(ctx, conn, orm.AggregateCompareOptions{Case: "aggregate-relations-v1-" + workload})
			if err != nil || !report.Complete {
				fatalDatabaseError(t, dsn, "compare relation aggregate", err)
			}
			for _, variant := range report.Variants {
				if variant.Rows != int64(expectedRows) || len(variant.Samples) != 5 {
					t.Fatal("comparison lost rows or samples")
				}
				related := 0
				for _, row := range variant.Plan.Executed {
					if row.RelationPath != "" {
						related++
					}
				}
				if related == 0 {
					t.Fatal("plan lost relation paths")
				}
				if len(variant.Plan.Warnings) != 0 {
					t.Fatalf("unexpected plan warnings: %d", len(variant.Plan.Warnings))
				}
				t.Logf("Compare case=%s variant=%s rows=%d median_ms=%.3f mean_ServerRU=%.6f plan=%s related_operators=%d", workload, variant.Name, variant.Rows, variant.LatencyMS.Median, variant.ServerRU.Mean, variant.PlanStatus, related)
			}
		})
	}
}

func aggregateRelationFixtureRow(i int, edge bool) []any {
	root := int64(i%10000 + 1)
	if edge {
		var target, deleted any = int64(10001 + i), nil
		if root == 1 {
			target = int64(10001)
		} // Duplicate edges must not multiply root 1.
		if root == 3 {
			deleted = "2025-01-01 00:00:00"
		}
		if root == 5 {
			if i < 10000 {
				target = nil
			} else {
				target = int64(999999)
			}
		}
		return []any{int64(i + 1), root, target, deleted}
	}
	var parent, deleted, amount, at any = nil, nil, fmt.Sprintf("%d.25", i%97), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format("2006-01-02 15:04:05")
	status := "paid"
	if i%2 == 1 {
		status = "pending"
	}
	if i < 6 {
		amount = []any{"10.00", nil, "30.00", "40.00", "50.00", "0.00"}[i]
		status = "paid"
		if i == 0 {
			at = nil
		}
		if i == 2 {
			status = "pending"
		}
		if i == 3 {
			deleted = "2025-01-01 00:00:00"
		}
	}
	if i >= 10000 {
		parent, amount, status = root, "1.00", "match"
		if root%1000 == 0 {
			status = "rare"
		}
		if root == 2 {
			if i < 20000 {
				deleted = "2025-01-01 00:00:00"
			} else {
				status = "other"
			}
		}
		if root == 5 {
			if i < 20000 {
				parent = nil
			} else {
				parent = int64(999999)
			}
		}
	}
	return []any{int64(i + 1), parent, root % 16, status, amount, at, deleted}
}

func testAggregateRelationContracts(t *testing.T, ctx context.Context, dsn string) {
	for _, interpolate := range []bool{false, true} {
		config := parseTestDSN(t, dsn)
		config.InterpolateParams = interpolate
		db := openTestDatabase(t, config.FormatDSN())
		for _, engine := range []orm.StorageEngine{orm.TiKV, orm.TiFlash} {
			for _, tc := range []struct {
				name      string
				predicate orm.Predicate
				all       bool
				count     int64
				total     string
			}{
				{"duplicates", orm.Has("Children", orm.Equal("Status", "match")), false, 3, "40.00"},
				{"source_deleted", orm.Has("Children", orm.Equal("Status", "match")), true, 4, "80.00"},
				{"not", orm.Not(orm.Has("Children", orm.Equal("Status", "match"))), false, 2, "50.00"},
				{"empty", orm.Has("Parent"), false, 0, ""},
				{"nested", orm.Has("Children", orm.Has("Parent", orm.Equal("Status", "paid"))), false, 3, "10.00"},
				{"via", orm.Has("Targets", orm.Equal("Status", "match")), false, 2, "10.00"},
				{"via_source_deleted", orm.Has("Targets", orm.Equal("Status", "match")), true, 3, "50.00"},
				{"or", orm.Or(orm.Has("Children", orm.Equal("Status", "match")), orm.Equal("ID", int64(5))), false, 4, "90.00"},
				{"all_null", orm.And(orm.Equal("ID", int64(2)), orm.Has("Children")), false, 1, ""},
			} {
				q := orm.Aggregate[starterAggregateRelationNode]().Select(orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).Where(orm.LessThanOrEqual("ID", int64(6)), tc.predicate).ReadFrom(engine)
				if tc.all {
					q.WithDeleted()
				}
				var got []starterAggregateRelationResult
				if err := q.ScanAll(ctx, db, &got); err != nil {
					fatalDatabaseError(t, dsn, "relation contract "+tc.name, err)
				}
				if len(got) != 1 || got[0].Count != tc.count || got[0].Total.Valid != (tc.total != "") || got[0].Total.String != tc.total {
					t.Fatalf("%s interpolate=%t engine=%s: %#v", tc.name, interpolate, engine, got)
				}
			}
			for _, key := range []orm.AggregateExpression{orm.Date("CreatedAt"), orm.YearMonth("CreatedAt")} {
				q := orm.Aggregate[starterAggregateRelationNode]().Select(key.As("Key"), orm.CountAll().As("AllCount"), orm.CountIf(orm.Equal("Status", "paid")).As("Count"), orm.SumIf("Amount", orm.Equal("Status", "paid")).As("Total")).Where(orm.LessThanOrEqual("ID", int64(6)), orm.Has("Children", orm.Equal("Status", "match"))).GroupBy("Key").Having(orm.GreaterThan("Count", int64(0))).OrderBy(orm.Asc("Key")).Limit(2).ReadFrom(engine)
				var got []starterAggregateRelationResult
				if err := q.ScanAll(ctx, db, &got); err != nil {
					fatalDatabaseError(t, dsn, "relation calendar conditional contract", err)
				}
				if len(got) != 2 || got[0].Key.Valid || got[0].AllCount != 1 || got[0].Count != 1 || got[0].Total.String != "10.00" || !got[1].Key.Valid || got[1].AllCount != 2 || got[1].Count != 1 || got[1].Total.String != "0.00" || !got[1].Total.Valid {
					t.Fatalf("calendar conditional: %#v", got)
				}
			}
			var children []starterAggregateRelationResult
			if err := orm.Aggregate[starterAggregateRelationNode]().Select(orm.CountAll().As("Count"), orm.Sum("Amount").As("Total")).Where(orm.Between("ID", int64(10001), int64(10006)), orm.Has("Parent")).ReadFrom(engine).ScanAll(ctx, db, &children); err != nil {
				fatalDatabaseError(t, dsn, "nullable relation keys", err)
			}
			if len(children) != 1 || children[0].Count != 3 || children[0].Total.String != "3.00" {
				t.Fatalf("nullable relation keys: %#v", children)
			}
		}
	}
}

func starterAggregateRelationQuery(workload, variant string) *orm.AggregateQuery[starterAggregateRelationNode] {
	q := orm.Aggregate[starterAggregateRelationNode]()
	if workload == "broad" {
		q.Select(orm.YearMonth("CreatedAt").As("Key")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	}
	if workload == "many_groups" {
		q.Select(orm.Field("ID").As("Key")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	}
	if workload == "via" {
		q.Select(orm.Field("ShopID").As("Key")).GroupBy("Key").OrderBy(orm.Asc("Key"))
	}
	q.Select(orm.CountAll().As("AllCount"), orm.CountIf(orm.Equal("Status", "paid")).As("Count"), orm.SumIf("Amount", orm.Equal("Status", "paid")).As("Total"))
	relation, status := "Children", "match"
	if workload == "via" {
		relation = "Targets"
	}
	if workload == "selective" {
		status = "rare"
	}
	q.Where(orm.LessThanOrEqual("ID", int64(10000)), orm.Has(relation, orm.Equal("Status", status)))
	if variant == "tikv" {
		q.ReadFrom(orm.TiKV)
	}
	if variant == "tiflash_mpp" {
		q.ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
	}
	return q
}

func aggregateRelationSQL(workload, variant, method string) (string, []any) {
	hint := func(aliases string, root bool) string {
		if variant == "auto" {
			return ""
		}
		engine := "TIKV"
		if variant == "tiflash_mpp" {
			engine = "TIFLASH"
		}
		text := "READ_FROM_STORAGE(" + engine + "[" + aliases + "]) "
		if root && variant == "tiflash_mpp" {
			text += "SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) "
		}
		return text
	}
	comment := func(text string) string {
		if text == "" {
			return ""
		}
		return "/*+ " + text + "*/ "
	}
	key := ""
	switch workload {
	case "broad":
		key = "EXTRACT(YEAR_MONTH FROM `a`.`created_at`)"
	case "many_groups":
		key = "`a`.`id`"
	case "via":
		key = "`a`.`shop_id`"
	}
	selectKey, group := "", ""
	if key != "" {
		selectKey = key + " AS `Key`, "
		group = " GROUP BY " + key + " ORDER BY " + key + " ASC"
	}
	root := "SELECT " + comment(hint("a", true)) + selectKey + "COUNT(*) AS `AllCount`, COUNT(CASE WHEN `a`.`status` = ? THEN 1 END) AS `Count`, SUM(CASE WHEN `a`.`status` = ? THEN `a`.`amount` END) AS `Total` FROM `tidbgo_it_aggregate_relation_nodes` AS `a`"
	aliases, from, correlation, scope, joinKey := "tidbgo_r1", "`tidbgo_it_aggregate_relation_nodes` AS `tidbgo_r1`", "`tidbgo_r1`.`parent_id` = `a`.`id`", "`tidbgo_r1`.`deleted_at` IS NULL", "`tidbgo_r1`.`parent_id`"
	if workload == "via" {
		aliases = "tidbgo_r1,tidbgo_j1"
		from = "`tidbgo_it_aggregate_relation_edges` AS `tidbgo_j1` JOIN `tidbgo_it_aggregate_relation_nodes` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`target_id`)"
		correlation = "`tidbgo_j1`.`source_id` = `a`.`id`"
		scope += " AND `tidbgo_j1`.`deleted_at` IS NULL"
		joinKey = "`tidbgo_j1`.`source_id`"
	}
	status := "match"
	if workload == "selective" {
		status = "rare"
	}
	if method == "join" {
		return root + " JOIN (SELECT " + comment(hint(aliases, false)) + "DISTINCT " + joinKey + " AS source_id FROM " + from + " WHERE " + scope + " AND `tidbgo_r1`.`status` = ?) AS matched ON matched.source_id = `a`.`id` WHERE `a`.`deleted_at` IS NULL AND `a`.`id` <= ?" + group, []any{"paid", "paid", status, int64(10000)}
	}
	innerHint := hint(aliases, false)
	if method == "rewrite" {
		innerHint += "SEMI_JOIN_REWRITE() "
	}
	return root + " WHERE `a`.`deleted_at` IS NULL AND `a`.`id` <= ? AND EXISTS (SELECT " + comment(innerHint) + "1 FROM " + from + " WHERE (" + correlation + ") AND " + scope + " AND `tidbgo_r1`.`status` = ?)" + group, []any{"paid", "paid", int64(10000), status}
}

func measureAggregateRelationAlternatives(t *testing.T, ctx context.Context, conn *sql.Conn, dsn, workload, variant string, want []starterAggregateRelationResult) {
	methods := []string{"rewrite", "exists", "join"}
	durations := make([][]float64, 3)
	rus := make([][]float64, 3)
	for round := 0; round < 7; round++ {
		for step := range methods {
			i := (round + step) % len(methods)
			statement, args := aggregateRelationSQL(workload, variant, methods[i])
			started := time.Now()
			got, err := orm.Raw[starterAggregateRelationResult](statement, args...).All(ctx, conn)
			elapsed := float64(time.Since(started)) / float64(time.Millisecond)
			if err != nil {
				fatalDatabaseError(t, dsn, "read relation alternative "+methods[i], err)
			}
			ru, err := orm.LastServerRU(ctx, conn)
			if err != nil {
				fatalDatabaseError(t, dsn, "read relation RU", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("unequal results: %s/%s/%s", workload, variant, methods[i])
			}
			if round >= 2 {
				durations[i] = append(durations[i], elapsed)
				rus[i] = append(rus[i], ru)
			}
		}
	}
	for i, method := range methods {
		slices.Sort(durations[i])
		mean := 0.0
		for _, ru := range rus[i] {
			mean += ru / float64(len(rus[i]))
		}
		t.Logf("Alternative case=%s variant=%s method=%s rows=%d median_ms=%.3f mean_ServerRU=%.6f", workload, variant, method, len(want), durations[i][2], mean)
	}
}
