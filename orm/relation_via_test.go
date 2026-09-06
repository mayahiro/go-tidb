package orm

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/internal/relationtopn"
	"github.com/mayahiro/go-tidb/model"
)

type viaPreloadParent struct {
	model.Meta     `tidbgo:"table=via_parents"`
	ID             int64               `tidbgo:",pk"`
	Targets        []viaPreloadTarget  `tidbgo:"many_to_many,via=Edges.Target"`
	TargetPointers []*viaPreloadTarget `tidbgo:"many_to_many,via=Edges.Target"`
	Edges          []viaPreloadEdge    `tidbgo:"has_many,join=ID:ParentID"`
}

type viaPreloadEdge struct {
	model.Meta `tidbgo:"table=via_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	TargetID   *int64
	Priority   int               `tidbgo:"position"`
	DeletedAt  *time.Time        `tidbgo:",soft_delete"`
	Computed   int               `tidbgo:",computed"`
	Target     *viaPreloadTarget `tidbgo:"belongs_to"`
}

type viaPreloadTarget struct {
	model.Meta `tidbgo:"table=via_targets"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	Priority   int `tidbgo:"position"`
	DetailID   *int64
	DeletedAt  *time.Time        `tidbgo:",soft_delete"`
	Detail     *viaPreloadDetail `tidbgo:"belongs_to"`
	Children   []viaPreloadChild `tidbgo:"has_many,join=ID:TargetID"`
}
type viaPreloadDetail struct {
	model.Meta `tidbgo:"table=via_details"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}
type viaPreloadChild struct {
	ID       int64 `tidbgo:",pk"`
	TargetID int64
}

func TestViaPreloadHydratesTargetsWithoutEdges(t *testing.T) {
	for _, path := range []string{"Targets", "TargetPointers"} {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bounded=%t", path, bounded), func(t *testing.T) {
				state := &preloadTestState{record: true, responses: []*preloadTestResponse{
					{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}, {int64(2)}}},
					{columns: []string{"parent_id", "name", "id"}, values: [][]driver.Value{
						{int64(1), "b", int64(20)}, {int64(2), "c", int64(30)},
						{int64(1), "a", int64(10)}, {int64(1), "a", int64(10)},
					}},
				}}
				query := Query[viaPreloadParent]().Select("ID").Preload(path, PreloadFields("Name"), PreloadOrderBy(Asc("Edges.Priority"), Desc("ID")))
				if bounded {
					query = query.Where(In("ID", []int64{1, 2}))
				}
				parents, err := query.All(context.Background(), openPreloadTestDB(t, state))
				if err != nil {
					t.Fatal(err)
				}
				if len(parents) != 2 || len(parents[0].Edges) != 0 {
					t.Fatalf("parents = %#v", parents)
				}
				var ids []int64
				if path == "Targets" {
					for _, target := range parents[0].Targets {
						ids = append(ids, target.ID)
					}
					if len(parents[1].Targets) != 1 || parents[1].Targets[0].ID != 30 {
						t.Fatal("incorrect second parent")
					}
				} else {
					for _, target := range parents[0].TargetPointers {
						ids = append(ids, target.ID)
					}
					if parents[0].TargetPointers[1] == parents[0].TargetPointers[2] {
						t.Fatal("reused target pointer")
					}
				}
				if !reflect.DeepEqual(ids, []int64{20, 10, 10}) {
					t.Fatalf("ids = %v", ids)
				}
				calls := preloadCalls(state)
				want := "SELECT `j`.`parent_id`, `t`.`name`, `t`.`id` FROM `via_edges` AS `j` JOIN `via_targets` AS `t` ON (`t`.`id` = `j`.`target_id`) WHERE `t`.`deleted_at` IS NULL AND `j`.`deleted_at` IS NULL"
				args := []any{}
				if bounded {
					want += " AND `j`.`parent_id` IN (?, ?)"
					args = []any{int64(1), int64(2)}
				}
				want += " ORDER BY `j`.`position` ASC, `t`.`id` DESC"
				if len(calls) != 2 || calls[1].query != want || !reflect.DeepEqual(namedValues(calls[1].arguments), args) {
					t.Fatalf("calls = %#v, want %s", calls, want)
				}
			})
		}
	}
}

func TestViaPreloadNestedTargetRelations(t *testing.T) {
	state := &preloadTestState{record: true, responses: []*preloadTestResponse{
		{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
		{columns: []string{"parent_id", "name", "id", "detail_id", "detail_name", "detail_deleted_at"}, values: [][]driver.Value{{int64(1), "target", int64(2), int64(3), "detail", nil}}},
		{columns: []string{"id", "target_id"}, values: [][]driver.Value{{int64(4), int64(2)}}},
	}}
	parents, err := Query[viaPreloadParent]().Select("ID").
		Preload("Targets", PreloadFields("Name"), PreloadOrderBy(Asc("Edges.Priority")), PreloadWithDeleted()).
		Preload("Targets.Detail").Preload("Targets.Children").
		All(context.Background(), openPreloadTestDB(t, state))
	if err != nil {
		t.Fatal(err)
	}
	target := parents[0].Targets[0]
	if target.Detail == nil || target.Detail.Name != "detail" || len(target.Children) != 1 || target.Children[0].ID != 4 {
		t.Fatalf("target = %#v", target)
	}
	calls := preloadCalls(state)
	if len(calls) != 3 || !strings.Contains(calls[1].query, "LEFT JOIN `via_details`") || !strings.Contains(calls[1].query, "`tidbgo_t1`.`deleted_at` IS NULL") || strings.Contains(calls[1].query, "`j`.`deleted_at` IS NULL") || strings.Contains(calls[1].query, "`t`.`deleted_at` IS NULL") {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestViaPreloadRejectsInvalidOptions(t *testing.T) {
	for _, terms := range [][]OrderTerm{
		{Asc("Edges.Missing")}, {Asc("Missing.Priority")}, {Asc("Edges.Target.ID")},
		{Asc("Edges.Computed")}, {Asc("Edges.Priority"), Desc("Edges.Priority")},
		{Asc("Edges.Priority; DROP TABLE via_edges")}, {OrderTerm{value: orderTerm{field: "Edges.Priority", direction: 99}}},
	} {
		if _, _, err := Query[viaPreloadParent]().Preload("Targets", PreloadOrderBy(terms...)).Build(); err == nil {
			t.Fatalf("accepted terms %#v", terms)
		}
	}
	if _, _, err := Query[viaPreloadParent]().Preload("Targets", PreloadFields("Edges.Priority")).Build(); err == nil {
		t.Fatal("edge payload accepted in target projection")
	}
}

func TestViaHasRetainsExistenceAndSoftDeleteChecks(t *testing.T) {
	query := Query[viaPreloadParent]().Where(Has("Targets", Equal("ID", int64(7))))
	sqlText, _, err := query.OrderBy(Desc("ID")).Limit(20).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */", "`tidbgo_j1`.`deleted_at` IS NULL", "`tidbgo_r1`.`deleted_at` IS NULL"} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("missing %q in %s", fragment, sqlText)
		}
	}
	shape := queryShapeForTest(t, query)
	if shape.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopNFallback || shape.Compiler.Reason != relationtopn.ReasonReadThrough {
		t.Fatalf("compiler = %#v", shape.Compiler)
	}
	if shape.Predicates[0].Via != "Edges.Target" || shape.Predicates[0].JunctionSoftDeleteColumn != "deleted_at" {
		t.Fatalf("predicate = %#v", shape.Predicates[0])
	}
	count, err := Query[viaPreloadParent]().Where(Has("Targets", Equal("ID", int64(7)))).compileCount()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(count.sql, "SELECT COUNT(*) FROM `via_parents`") || !strings.Contains(count.sql, "EXISTS") || !strings.Contains(count.sql, "`tidbgo_j1`.`deleted_at` IS NULL") {
		t.Fatalf("count = %s", count.sql)
	}
}

func TestViaShapeDistinguishesEdgeOrderingAndScopes(t *testing.T) {
	query := func() *SelectQuery[viaPreloadParent] { return Query[viaPreloadParent]().Select("ID") }
	edge := queryShapeForTest(t, query().Preload("Targets", PreloadOrderBy(Asc("Edges.Priority"))))
	target := queryShapeForTest(t, query().Preload("Targets", PreloadOrderBy(Asc("Priority"))))
	deleted := queryShapeForTest(t, query().Preload("Targets", PreloadOrderBy(Asc("Edges.Priority")), PreloadWithDeleted()))
	if edge.Fingerprint() == target.Fingerprint() || edge.Fingerprint() == deleted.Fingerprint() {
		t.Fatal("fingerprint collision")
	}
	preload := edge.Preloads[0]
	if preload.Via != "Edges.Target" || !preload.Order[0].Junction || preload.JunctionSoftDeleteColumn != "deleted_at" {
		t.Fatalf("shape = %#v", preload)
	}
	if deleted.Preloads[0].JunctionSoftDeleteColumn != "" || deleted.Preloads[0].SoftDeleteColumn != "" {
		t.Fatal("inactive scopes captured")
	}
	data, err := json.Marshal(edge)
	if err != nil {
		t.Fatal(err)
	}
	var restored queryshape.Query
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Fingerprint() != edge.Fingerprint() {
		t.Fatal("shape changed in artifact round trip")
	}
}

func TestViaRelationMutationsAreRejected(t *testing.T) {
	queries := []interface{ Build() (string, []any, error) }{
		AddRelation[viaPreloadParent]("Targets", int64(1), int64(2)), RemoveRelation[viaPreloadParent]("Targets", int64(1), int64(2)), ClearRelation[viaPreloadParent]("Targets", int64(1)),
		AddRelation[viaPreloadParent]("Targets", int64(1), []int64{}...), RemoveRelation[viaPreloadParent]("Targets", int64(1), []int64{}...),
	}
	for _, query := range queries {
		if _, _, err := query.Build(); err == nil || !strings.Contains(err.Error(), "pure many-to-many") {
			t.Fatalf("Build = %v", err)
		}
	}
}

func TestViaWithoutSoftDeleteStillRetainsRootCount(t *testing.T) {
	compiled, err := Query[viaBenchmarkParent]().Where(Has("Targets", Equal("ID", int64(1)))).compileCount()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(compiled.sql, "SELECT COUNT(*) FROM `via_parents`") || !strings.Contains(compiled.sql, "EXISTS") {
		t.Fatalf("unproven edge cardinality shortcut: %s", compiled.sql)
	}
}

type viaEdgeOnlyParent struct {
	ID      int64                `tidbgo:",pk"`
	Edges   []viaEdgeOnly        `tidbgo:"has_many,join=ID:ParentID"`
	Targets []viaBenchmarkTarget `tidbgo:"many_to_many,via=Edges.Target"`
}
type viaEdgeOnly struct {
	ID        int64 `tidbgo:",pk"`
	ParentID  int64
	TargetID  int64
	DeletedAt *time.Time          `tidbgo:",soft_delete"`
	Target    *viaBenchmarkTarget `tidbgo:"belongs_to"`
}

func TestViaWithDeletedAcceptsEdgeOnlyScopeWithoutMutatingCachedPlan(t *testing.T) {
	for _, include := range []bool{false, true, false} {
		query := Query[viaEdgeOnlyParent]()
		if include {
			query.Preload("Targets", PreloadWithDeleted())
		} else {
			query.Preload("Targets")
		}
		compiled, err := query.compile()
		if err != nil {
			t.Fatal(err)
		}
		plan := compiled.preloads[0]
		all := compilePreloadAll(plan)
		batch, _ := compilePreloadBatch(plan, []preloadKey{{component: int64(1)}})
		for _, sqlText := range []string{all, batch} {
			if strings.Contains(sqlText, "`j`.`deleted_at` IS NULL") == include {
				t.Fatalf("include=%t: %s", include, sqlText)
			}
		}
	}
	if _, _, err := Query[viaBenchmarkParent]().Preload("Targets", PreloadWithDeleted()).Build(); err == nil {
		t.Fatal("accepted missing scope")
	}
}

type viaCompositeParent struct {
	TenantID int64                `tidbgo:",pk"`
	ID       int64                `tidbgo:",pk"`
	Edges    []viaCompositeEdge   `tidbgo:"has_many,join=TenantID:TenantID,join=ID:ParentID"`
	Targets  []viaCompositeTarget `tidbgo:"many_to_many,via=Edges.Target"`
}
type viaCompositeEdge struct {
	TenantID int64
	ParentID int64
	TargetID int64
	Priority int
	Target   *viaCompositeTarget `tidbgo:"belongs_to,join=TenantID:TenantID,join=TargetID:ID"`
}
type viaCompositeTarget struct {
	TenantID int64 `tidbgo:",pk"`
	ID       int64 `tidbgo:",pk"`
}

func TestViaPreloadCompositeKeysAndSharedTenantColumn(t *testing.T) {
	state := &preloadTestState{record: true, responses: []*preloadTestResponse{
		{columns: []string{"tenant_id", "id"}, values: [][]driver.Value{{int64(1), int64(2)}}},
		{columns: []string{"tenant_id", "parent_id", "target_tenant_id", "target_id"}, values: [][]driver.Value{{int64(1), int64(2), int64(1), int64(3)}}},
	}}
	values, err := Query[viaCompositeParent]().Where(Equal("TenantID", int64(1))).Preload("Targets", PreloadOrderBy(Asc("Edges.Priority"))).All(context.Background(), openPreloadTestDB(t, state))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || len(values[0].Targets) != 1 || values[0].Targets[0].ID != 3 {
		t.Fatalf("values = %#v", values)
	}
	calls := preloadCalls(state)
	if len(calls) != 2 || !strings.Contains(calls[1].query, "`t`.`tenant_id` = `j`.`tenant_id` AND `t`.`id` = `j`.`target_id`") || !reflect.DeepEqual(namedValues(calls[1].arguments), []any{int64(1), int64(2)}) {
		t.Fatalf("calls = %#v", calls)
	}
}
