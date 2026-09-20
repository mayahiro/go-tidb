package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

type aggregateRelationOrder struct {
	model.Meta `tidbgo:"table=aggregate_relation_orders"`
	ID         int64 `tidbgo:",pk"`
	ShopID     int64
	Amount     int64
	Status     string
	CreatedAt  *time.Time
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
	UserID     *int64
	User       *aggregateRelationUser `tidbgo:"belongs_to,join=UserID:ID"`
}

type aggregateRelationUser struct {
	model.Meta `tidbgo:"table=aggregate_relation_users"`
	ID         int64 `tidbgo:",pk"`
	Active     bool
	DeletedAt  *time.Time               `tidbgo:",soft_delete"`
	Orders     []aggregateRelationOrder `tidbgo:"has_many,join=ID:UserID"`
}

func TestAggregateRelationArgumentsAndCalendarOutputs(t *testing.T) {
	q := Aggregate[aggregateRelationOrder]().
		Select(Date("CreatedAt").As("Day"), CountIf(Equal("Status", "paid")).As("Count"), Sum("Amount").As("Total")).
		Where(Has("User", Equal("Active", true), Has("Orders", GreaterThan("Amount", int64(50))))).
		GroupBy("Day").Having(GreaterThan("Count", int64(0))).OrderBy(Desc("Count"), Asc("Day")).Limit(3).Offset(1).
		ReadFrom(TiFlash).MPP(MPPEnforce)
	statement, args, err := q.Build()
	want := "SELECT /*+ READ_FROM_STORAGE(TIFLASH[a]) SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) */ DATE(`a`.`created_at`) AS `Day`, COUNT(CASE WHEN `a`.`status` = ? THEN 1 END) AS `Count`, SUM(`a`.`amount`) AS `Total` FROM `aggregate_relation_orders` AS `a` WHERE `a`.`deleted_at` IS NULL AND EXISTS (SELECT /*+ READ_FROM_STORAGE(TIFLASH[tidbgo_r1]) */ 1 FROM `aggregate_relation_users` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`id` = `a`.`user_id`) AND `tidbgo_r1`.`deleted_at` IS NULL AND `tidbgo_r1`.`active` = ? AND EXISTS (SELECT /*+ READ_FROM_STORAGE(TIFLASH[tidbgo_r2]) SEMI_JOIN_REWRITE() */ 1 FROM `aggregate_relation_orders` AS `tidbgo_r2` WHERE (`tidbgo_r2`.`user_id` = `tidbgo_r1`.`id`) AND `tidbgo_r2`.`deleted_at` IS NULL AND `tidbgo_r2`.`amount` > ?)) GROUP BY DATE(`a`.`created_at`) HAVING COUNT(CASE WHEN `a`.`status` = ? THEN 1 END) > ? ORDER BY COUNT(CASE WHEN `a`.`status` = ? THEN 1 END) DESC, DATE(`a`.`created_at`) ASC LIMIT ? OFFSET ?"
	if err != nil || statement != want || !reflect.DeepEqual(args, []any{"paid", true, int64(50), "paid", int64(0), "paid", int64(3), int64(1)}) {
		t.Fatalf("Build = %s; %#v; %v", statement, args, err)
	}
	all, _, err := qCopy(q).WithDeleted().Build()
	if err != nil || strings.Contains(all, "`a`.`deleted_at` IS NULL") || strings.Count(all, "`deleted_at` IS NULL") != 2 {
		t.Fatalf("WithDeleted changed related scopes: %s; %v", all, err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 10 {
				got, bound, err := q.Build()
				if err != nil || got != want || !reflect.DeepEqual(bound, args) {
					t.Error("parallel Build differs", err)
				}
				bound[0] = "detached"
			}
		})
	}
	workers.Wait()
}

func TestAggregateRelationFormsAndHintScopes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		q         interface{ Build() (string, []any, error) }
		fragments []string
	}{
		{"has_one", Aggregate[preloadUser]().Select(CountAll().As("Count")).Where(Has("Profile", Contains("Bio", "%_!"))), []string{"EXISTS (SELECT 1 FROM `preload_profiles`", "`tidbgo_r1`.`user_id` = `a`.`id`", "LIKE ? ESCAPE '!'"}},
		{"composite", Aggregate[preloadTenant]().Select(CountAll().As("Count")).Where(Has("Records", Equal("Value", "ready"))), []string{"`tidbgo_r1`.`tenant_id` = `a`.`tenant_id` AND `tidbgo_r1`.`parent_id` = `a`.`id`"}},
		{"junction", Aggregate[preloadMember]().Select(CountAll().As("Count")).Where(Has("Groups", Equal("Name", "ops"))).ReadFrom(TiKV), []string{"READ_FROM_STORAGE(TIKV[tidbgo_r1,tidbgo_j1]) SEMI_JOIN_REWRITE()", "`tidbgo_j1`.`tenant_id` = `a`.`tenant_id` AND `tidbgo_j1`.`member_id` = `a`.`id`"}},
		{"via", Aggregate[viaPreloadParent]().Select(CountAll().As("Count")).Where(Has("Targets", Has("Detail", Equal("Name", "live")))).ReadFrom(TiFlash), []string{"READ_FROM_STORAGE(TIFLASH[tidbgo_r1,tidbgo_j1])", "`tidbgo_j1`.`deleted_at` IS NULL", "`tidbgo_r1`.`deleted_at` IS NULL", "READ_FROM_STORAGE(TIFLASH[tidbgo_r2])", "`tidbgo_r2`.`deleted_at` IS NULL"}},
		{"self", Aggregate[relationPredicateNode]().Select(CountAll().As("Count")).Where(Has("Parent", Has("Children", Equal("ID", uint64(9))))), []string{"`tidbgo_r1`.`id` = `a`.`parent_id`", "`tidbgo_r2`.`parent_id` = `tidbgo_r1`.`id`"}},
		{"logical", Aggregate[preloadUser]().Select(CountAll().As("Count")).Where(Or(Has("Orders", Equal("Total", "1")), Not(Has("Roles", Equal("Name", "ops"))))).ReadFrom(TiFlash), []string{"WHERE (EXISTS (SELECT /*+ READ_FROM_STORAGE(TIFLASH[tidbgo_r1]) */", "OR NOT (EXISTS (SELECT /*+ READ_FROM_STORAGE(TIFLASH[tidbgo_r2,tidbgo_j2]) */"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement, _, err := tc.q.Build()
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range tc.fragments {
				if !strings.Contains(statement, fragment) {
					t.Fatalf("missing %s in %s", fragment, statement)
				}
			}
			if tc.name == "logical" && strings.Contains(statement, "SEMI_JOIN_REWRITE") {
				t.Fatal("rewrite under Or/Not")
			}
		})
	}
}

func TestAggregateRelationValidationBeforeIO(t *testing.T) {
	for _, p := range []Predicate{Has("Missing"), Has("User.Active"), Has("User", Equal("ShopID", 1)), Has("User", Equal("Active", nil)), Has("User", Has("Orders", Equal("user_id", 1))), Or(Has("User")), Has("User; DROP TABLE x")} {
		q := Aggregate[aggregateRelationOrder]().Select(CountAll().As("Count")).Where(p)
		state := &allTestState{}
		db := sql.OpenDB(&allTestConnector{state: state})
		var result []int64
		err := q.ScanAll(context.Background(), db, &result)
		_ = db.Close()
		if err == nil || state.query != "" {
			t.Fatalf("invalid predicate reached I/O: %v", err)
		}
	}
}

func TestAggregateRelationPlanResolvesPaths(t *testing.T) {
	state := aggregatePlanState(true)
	state.target.values = nil
	for _, alias := range []string{"a", "tidbgo_r1", "tidbgo_j1", "tidbgo_r2"} {
		state.target.values = append(state.target.values, []driver.Value{"TableFullScan_1", "2", int64(2), "mpp[tiflash]", "table:" + alias, "time:1ms", "", "N/A", "N/A"})
	}
	db := sql.OpenDB(&aggregatePlanConnector{state: state})
	defer db.Close()
	plan, err := Aggregate[viaPreloadParent]().Select(CountAll().As("Count")).Where(Has("Targets", Has("Detail"))).ReadFrom(TiFlash).ExplainAnalyze(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	wantTables := []string{"via_parents", "via_targets", "via_edges", "via_details"}
	wantPaths := []string{"", "Targets", "Targets", "Targets.Detail"}
	for i, row := range plan.Executed {
		if row.PhysicalTable != wantTables[i] || row.RelationPath != wantPaths[i] {
			t.Fatalf("unresolved relation access: %#v", row)
		}
	}
}

func TestAggregateRelationCompareFreezesInputsAndChecksRelatedStorage(t *testing.T) {
	for _, scenario := range []string{"matched", "related_fallback", "unknown_access"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			q := Aggregate[preloadUser]().Select(CountAll().As("Count")).Where(Has("Roles", GreaterThan("ID", aggregateCompareValuer{calls: &calls})))
			if _, _, err := q.Build(); err != nil || calls != 0 {
				t.Fatal("Build evaluated Valuer", err)
			}
			state := &aggregateCompareTestState{columns: []string{"Count"}, values: [][]driver.Value{{int64(2)}}, record: true}
			state.filter = func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error, bool) {
				if !strings.HasPrefix(statement, "EXPLAIN ANALYZE") {
					return nil, nil, false
				}
				task := "cop[tikv]"
				flash := strings.Contains(statement, "TIFLASH[a]")
				if flash {
					task = "mpp[tiflash]"
				}
				values := [][]driver.Value{}
				for _, alias := range []string{"a", "tidbgo_r1", "tidbgo_j1"} {
					childTask := task
					if flash && alias == "tidbgo_j1" {
						if scenario == "related_fallback" {
							childTask = "cop[tikv]"
						}
						if scenario == "unknown_access" {
							alias = "unexpected"
						}
					}
					values = append(values, []driver.Value{"TableFullScan_1", "2", int64(2), childTask, "table:" + alias, "time:1ms", "", "N/A", "N/A"})
				}
				return &aggregateCompareTestRows{columns: explainAnalyzeColumnNames[:], values: values, closed: func() { state.last = "plan_closed" }}, nil, true
			}
			report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "related-fixed"})
			wantStatus := map[string]string{"matched": "matched", "related_fallback": "mismatch", "unknown_access": "unknown"}[scenario]
			if (err == nil) != (scenario == "matched") || calls != 1 || report.Variants[2].PlanStatus != wantStatus {
				t.Fatalf("Compare: %v calls=%d status=%s", err, calls, report.Variants[2].PlanStatus)
			}
			for _, args := range state.arguments {
				if len(args) != 0 && (len(args) != 1 || args[0].Value != int64(1)) {
					t.Fatal("unfrozen arguments", args)
				}
			}
			if scenario == "matched" {
				for _, variant := range report.Variants {
					if variant.Plan.Executed[1].RelationPath != "Roles" || variant.Plan.Executed[2].PhysicalTable != "preload_user_roles" {
						t.Fatal("Compare lost related mapping")
					}
					var capture bytes.Buffer
					if err := report.WriteCapture(&capture, variant.Name); err != nil {
						t.Fatal(err)
					}
					analysis, err := runtimecapture.AnalyzeReader(&capture, runtimecapture.WithWorkload(report.Options.Case))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := runtimecapture.NewServerRUBaseline(analysis); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
