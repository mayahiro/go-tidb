package orm

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/internal/relationtopn"
)

func TestViaRelationCompilesEdgeFirstTopNAndCount(t *testing.T) {
	t.Parallel()
	query := Query[viaTopNParent]().Where(Has("Targets", Equal("ID", int64(7)))).OrderBy(Desc("ID")).Limit(20).Offset(5)
	statement, args, err := query.Build()
	want := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`v0` FROM (SELECT `tidbgo_a0`.`parent_id` FROM `via_topn_edges` AS `tidbgo_a0` WHERE `tidbgo_a0`.`parent_id` IS NOT NULL AND `tidbgo_a0`.`target_id` IS NOT NULL AND `tidbgo_a0`.`target_id` = ? ORDER BY `tidbgo_a0`.`parent_id` DESC LIMIT ? OFFSET ?) AS `tidbgo_k0` STRAIGHT_JOIN `via_topn_parents` AS `tidbgo_t0` ON (`tidbgo_k0`.`parent_id` = `tidbgo_t0`.`id`) ORDER BY `tidbgo_t0`.`id` DESC"
	if err != nil || statement != want || !reflect.DeepEqual(args, []any{int64(7), int64(20), int64(5)}) {
		t.Fatalf("SQL=%s args=%#v error=%v", statement, args, err)
	}
	shape := queryShapeForTest(t, query)
	if shape.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopN || len(shape.IndexAccesses) != 1 || shape.IndexAccesses[0].Table != "via_topn_edges" {
		t.Fatalf("shape=%#v", shape)
	}
	count, err := Query[viaTopNParent]().Where(Has("Targets", Equal("ID", int64(7)))).compileCount()
	if err != nil || count.sql != "SELECT COUNT(*) FROM `via_topn_edges` WHERE `parent_id` IS NOT NULL AND `target_id` IS NOT NULL AND `target_id` = ?" || !reflect.DeepEqual(count.arguments, []any{int64(7)}) {
		t.Fatalf("count=%#v error=%v", count, err)
	}
	// Pagination still counts the original paginated result, not all edges.
	count, err = query.compileCount()
	if err != nil || !strings.Contains(count.sql, "EXISTS") || !strings.HasPrefix(count.sql, "SELECT COUNT(*) FROM (") {
		t.Fatalf("paginated count=%#v error=%v", count, err)
	}
}

func TestViaRelationRetainsTargetPredicates(t *testing.T) {
	t.Parallel()
	for _, filter := range []Predicate{Equal("V0", "value"), And(Equal("ID", int64(7)), NotEqual("V0", "excluded"))} {
		query := Query[viaTopNParent]().Where(Has("Targets", filter)).OrderBy(Desc("ID")).Limit(20)
		statement, _, err := query.Build()
		if err != nil || !strings.Contains(statement, "JOIN `via_topn_targets` AS `tidbgo_m0`") || !strings.Contains(statement, "`tidbgo_m0`.`v0`") || strings.Contains(statement, "EXISTS") {
			t.Fatalf("SQL=%s error=%v", statement, err)
		}
		count, err := Query[viaTopNParent]().Where(Has("Targets", filter)).compileCount()
		if err != nil || !strings.Contains(count.sql, "EXISTS") {
			t.Fatalf("target predicate lost: %#v error=%v", count, err)
		}
	}
}

type viaCompilerPreloadParent struct {
	ID       int64 `tidbgo:",pk"`
	DetailID int64
	Detail   *viaTopNTarget  `tidbgo:"belongs_to,join=DetailID:ID"`
	Edges    []viaTopNEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets  []viaTopNTarget `tidbgo:"many_to_many,via=Edges.Target"`
}

func TestViaRelationTopNWithInlineAndCollectionPreloads(t *testing.T) {
	state := &preloadTestState{record: true, responses: []*preloadTestResponse{
		{columns: []string{"id", "detail_id", "id", "v0"}, values: [][]driver.Value{{int64(3), int64(9), int64(9), "detail"}}},
		{columns: []string{"parent_id", "id", "v0"}, values: [][]driver.Value{{int64(3), int64(7), "target"}}},
	}}
	parents, err := Query[viaCompilerPreloadParent]().
		Where(Has("Targets", Equal("ID", int64(7)))).OrderBy(Desc("ID")).Limit(20).
		Preload("Detail").Preload("Targets", PreloadOrderBy(Asc("Edges.V0"))).
		All(context.Background(), openPreloadTestDB(t, state))
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0].Detail == nil || parents[0].Detail.V0 != "detail" || len(parents[0].Targets) != 1 || parents[0].Targets[0].V0 != "target" || len(parents[0].Edges) != 0 {
		t.Fatalf("parents=%#v", parents)
	}
	calls := preloadCalls(state)
	if len(calls) != 2 || !strings.Contains(calls[0].query, ") AS `tidbgo_k0` STRAIGHT_JOIN ") || !strings.Contains(calls[0].query, "LEFT JOIN `via_topn_targets` AS `tidbgo_t1`") || strings.Contains(calls[0].query, "LEADING") || !strings.Contains(calls[1].query, "`j`.`parent_id` IN (?) ORDER BY `j`.`v0` ASC") || !reflect.DeepEqual(namedValues(calls[1].arguments), []any{int64(3)}) {
		t.Fatalf("calls=%#v", calls)
	}
}

type viaNullableParent struct {
	ID      int64             `tidbgo:",pk"`
	Edges   []viaNullableEdge `tidbgo:"has_many,join=ID:ParentID"`
	Targets []viaTopNTarget   `tidbgo:"many_to_many,via=Edges.Target"`
}

type viaNullableEdge struct {
	ID        int64          `tidbgo:",pk"`
	ParentID  *int64         `tidbgo:",unique=pair"`
	TargetID  *int64         `tidbgo:",unique=pair"`
	DeletedAt time.Time      `tidbgo:",soft_delete"`
	Target    *viaTopNTarget `tidbgo:"belongs_to"`
}

func TestViaRelationFiltersEdgeScopeBeforeLimitAndCount(t *testing.T) {
	t.Parallel()
	query := Query[viaNullableParent]().Where(Has("Targets", Equal("ID", int64(7)))).OrderBy(Asc("ID")).Limit(2)
	statement, _, err := query.Build()
	if err != nil || !strings.Contains(statement, "WHERE `tidbgo_a0`.`deleted_at` IS NULL AND `tidbgo_a0`.`parent_id` IS NOT NULL AND `tidbgo_a0`.`target_id` IS NOT NULL AND `tidbgo_a0`.`target_id` = ? ORDER BY") {
		t.Fatalf("SQL=%s error=%v", statement, err)
	}
	shape := queryShapeForTest(t, query)
	if len(shape.IndexAccesses) != 1 || !reflect.DeepEqual(shape.IndexAccesses[0].EqualityColumns, []string{"target_id", "deleted_at"}) {
		t.Fatalf("index accesses=%#v", shape.IndexAccesses)
	}
	count, err := Query[viaNullableParent]().Where(Has("Targets", Equal("ID", int64(7)))).compileCount()
	if err != nil || count.sql != "SELECT COUNT(*) FROM `via_nullable_edge` WHERE `deleted_at` IS NULL AND `parent_id` IS NOT NULL AND `target_id` IS NOT NULL AND `target_id` = ?" {
		t.Fatalf("count=%#v error=%v", count, err)
	}
}

type viaCompilerCompositeParent struct {
	Tenant  int64                        `tidbgo:",pk"`
	ID      int64                        `tidbgo:",pk"`
	Edges   []viaCompilerCompositeEdge   `tidbgo:"has_many,join=Tenant:Tenant,join=ID:ParentID"`
	Targets []viaCompilerCompositeTarget `tidbgo:"many_to_many,via=Edges.Target"`
}

type viaCompilerCompositeEdge struct {
	Tenant   int64 `tidbgo:",pk"`
	ParentID int64 `tidbgo:",pk"`
	TargetID int64 `tidbgo:",pk"`
	V0       int64
	Target   *viaCompilerCompositeTarget `tidbgo:"belongs_to,join=Tenant:Tenant,join=TargetID:ID"`
}

type viaCompilerCompositeTarget struct {
	Tenant int64 `tidbgo:",pk"`
	ID     int64 `tidbgo:",pk"`
}

func TestViaRelationCompositeSharedKey(t *testing.T) {
	t.Parallel()
	query := Query[viaCompilerCompositeParent]().Where(Has("Targets", And(Equal("Tenant", int64(3)), Equal("ID", int64(7))))).OrderBy(Asc("Tenant"), Desc("ID")).Limit(20)
	statement, args, err := query.Build()
	if err != nil || !strings.Contains(statement, "ORDER BY `tidbgo_a0`.`tenant` ASC, `tidbgo_a0`.`parent_id` DESC LIMIT ?") || strings.Count(statement, "`tidbgo_a0`.`tenant` IS NOT NULL") != 1 || !reflect.DeepEqual(args, []any{int64(3), int64(7), int64(20)}) {
		t.Fatalf("SQL=%s args=%#v error=%v", statement, args, err)
	}
	count, err := Query[viaCompilerCompositeParent]().Where(Has("Targets", Equal("Tenant", int64(3)), Equal("ID", int64(7)))).compileCount()
	if err != nil || strings.Contains(count.sql, "EXISTS") || !strings.Contains(count.sql, "`target_id` = ?") {
		t.Fatalf("count=%#v error=%v", count, err)
	}
}

func TestViaRelationRetainsStructuralFallbacks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		predicate Predicate
		reason    string
	}{
		{Has("Targets", In("ID", []int64{1, 2})), relationtopn.ReasonTargetUniqueness},
		{Or(Has("Targets", Equal("ID", int64(7))), Equal("ID", int64(3))), relationtopn.ReasonNestedCollection},
	} {
		query := Query[viaTopNParent]().Where(test.predicate).OrderBy(Desc("ID")).Limit(20)
		shape := queryShapeForTest(t, query)
		if shape.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopNFallback || shape.Compiler.Reason != test.reason {
			t.Fatalf("compiler=%#v", shape.Compiler)
		}
		statement, _, err := query.Build()
		if err != nil || !strings.Contains(statement, "EXISTS") {
			t.Fatalf("SQL=%s error=%v", statement, err)
		}
	}
	if _, _, err := Query[viaTopNParent]().Where(Has("Targets", Equal("ID", nil))).OrderBy(Desc("ID")).Limit(20).Build(); err == nil {
		t.Fatal("optimized query accepted a nil Equal argument")
	}
}
