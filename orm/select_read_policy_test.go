package orm

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"
)

func TestSelectReadPolicyRewritesAndCountScopes(t *testing.T) {
	q := relationTopNBenchmarkQuery().ReadFrom(TiFlash).MPP(MPPEnforce)
	statement, _, err := q.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"READ_FROM_STORAGE(TIFLASH[tidbgo_t0,tidbgo_t1])", "READ_FROM_STORAGE(TIFLASH[tidbgo_a0])", "LEADING(tidbgo_k0, tidbgo_t0)"} {
		if !strings.Contains(statement, want) {
			t.Fatal("missing", want, statement)
		}
	}
	if strings.Count(statement, "tidb_allow_mpp") != 1 {
		t.Fatal("MPP repeated inside derived table", statement)
	}
	count, err := Query[relationTopNVideo]().Where(Has("VideoGenres", Equal("GenreID", int64(7)))).ReadFrom(TiFlash).MPP(MPPAuto).compileCount()
	if err != nil || !strings.Contains(count.sql, "READ_FROM_STORAGE(TIFLASH[relation_topn_video_genres])") || strings.Contains(count.sql, "relation_topn_videos") {
		t.Fatal("Count did not target the remaining table", count.sql, err)
	}
	paged, err := Query[preloadUser]().Where(Has("Orders", Equal("Total", "1"))).Limit(3).ReadFrom(TiKV).MPP(MPPAuto).compileCount()
	if err != nil || !strings.HasPrefix(paged.sql, "SELECT /*+ SET_VAR(") || !strings.Contains(paged.sql, "FROM (SELECT /*+ READ_FROM_STORAGE(TIKV[tidbgo_r0])") || !strings.Contains(paged.sql, "READ_FROM_STORAGE(TIKV[tidbgo_r1]) SEMI_JOIN_REWRITE()") || strings.Count(paged.sql, "tidb_allow_mpp") != 1 {
		t.Fatal(paged.sql, err)
	}
	exists, err := Query[preloadUser]().Where(Has("Roles")).ReadFrom(TiFlash).MPP(MPPEnforce).compileExists()
	if err != nil || !strings.Contains(exists.sql, "READ_FROM_STORAGE(TIFLASH[tidbgo_r1,tidbgo_j1])") || strings.Count(exists.sql, "tidb_allow_mpp") != 1 {
		t.Fatal(exists.sql, err)
	}
}

func TestSelectReadPolicyPreloadsAndCacheIsolation(t *testing.T) {
	q := Query[preloadUser]().Preload("Profile").Preload("Orders.User").Preload("Roles").ReadFrom(TiFlash).MPP(MPPEnforce)
	c, err := q.compile()
	if err != nil || !strings.Contains(c.statement.sql, "READ_FROM_STORAGE(TIFLASH[tidbgo_t0,tidbgo_t1])") {
		t.Fatal(c.statement, err)
	}
	for _, plan := range c.preloads {
		if plan.inline {
			continue
		}
		all := compilePreloadAll(plan)
		batch, _ := compilePreloadBatch(plan, nil)
		want := "READ_FROM_STORAGE(TIFLASH[tidbgo_t0,tidbgo_t1])"
		if plan.junction != nil {
			want = "READ_FROM_STORAGE(TIFLASH[j,t])"
		}
		for _, statement := range []string{all, batch} {
			if !strings.Contains(statement, want) || strings.Count(statement, "tidb_allow_mpp") != 1 {
				t.Fatal("secondary statement policy", statement)
			}
		}
	}
	var workers sync.WaitGroup
	for _, engine := range []StorageEngine{"", TiKV, TiFlash} {
		workers.Go(func() {
			for range 20 {
				other := Query[preloadUser]().Preload("Profile").Preload("Orders.User").Preload("Roles")
				if engine != "" {
					other.ReadFrom(engine)
				}
				compiled, err := other.compile()
				if err != nil || strings.Contains(compiled.statement.sql, "SET_VAR") || engine == "" && strings.Contains(compiled.statement.sql, "READ_FROM_STORAGE") || engine != "" && !strings.Contains(compiled.statement.sql, strings.ToUpper(string(engine))+"[") {
					t.Error("policy leaked across query copies", err)
				}
			}
		})
	}
	workers.Wait()
}

func TestSelectReadPolicyScanAllAndValidation(t *testing.T) {
	state := &allTestState{columns: []string{"ID"}, values: [][]driver.Value{{int64(1)}}}
	db := openAllTestDB(t, state)
	var ids []int64
	if err := Query[aggregateRelationOrder]().Select("ID").ReadFrom(TiFlash).ScanAll(context.Background(), db, &ids); err != nil || len(ids) != 1 || !strings.Contains(state.query, "READ_FROM_STORAGE(TIFLASH[aggregate_relation_orders])") {
		t.Fatal(ids, state.query, err)
	}
	for _, q := range []*SelectQuery[aggregateRelationOrder]{
		Query[aggregateRelationOrder]().ReadFrom(""),
		Query[aggregateRelationOrder]().MPP("invalid"),
		Query[aggregateRelationOrder]().ReadFrom(TiKV).MPP(MPPEnforce),
		Query[aggregateRelationOrder]().ReadFrom(TiFlash).ForceIndex("idx_amount"),
	} {
		state.query = ""
		if _, _, err := q.Build(); err == nil {
			t.Fatal("invalid policy accepted by Build")
		}
		if _, err := q.Count(context.Background(), db); err == nil || state.query != "" {
			t.Fatal("invalid policy reached Count I/O")
		}
		if _, err := q.Exists(context.Background(), db); err == nil || state.query != "" {
			t.Fatal("invalid policy reached Exists I/O")
		}
		if err := q.Select("ID").ScanAll(context.Background(), db, &ids); err == nil || state.query != "" {
			t.Fatal("invalid policy reached ScanAll I/O")
		}
	}
}
