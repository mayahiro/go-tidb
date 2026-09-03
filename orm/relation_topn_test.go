package orm

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/model"
)

type relationTopNUnprovenVideo struct {
	model.Meta `tidbgo:"table=relation_topn_unproven_videos"`
	ID         int64 `tidbgo:",pk"`
	Title      string
	Links      []relationTopNUnprovenLink `tidbgo:"has_many,join=ID:VideoID"`
}

type relationTopNUnprovenLink struct {
	model.Meta `tidbgo:"table=relation_topn_unproven_links"`
	ID         int64 `tidbgo:",pk"`
	VideoID    int64
	GenreID    int64
}

type relationTopNSoftVideo struct {
	model.Meta `tidbgo:"table=relation_topn_soft_videos"`
	ID         int64                  `tidbgo:",pk"`
	DeletedAt  time.Time              `tidbgo:",soft_delete"`
	Links      []relationTopNSoftLink `tidbgo:"has_many,join=ID:VideoID"`
}

type relationTopNSoftLink struct {
	model.Meta `tidbgo:"table=relation_topn_soft_links"`
	VideoID    int64     `tidbgo:",pk"`
	GenreID    int64     `tidbgo:",pk"`
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type relationTopNTenantVideo struct {
	model.Meta `tidbgo:"table=relation_topn_tenant_videos"`
	TenantID   int64                         `tidbgo:",pk"`
	ID         int64                         `tidbgo:",pk"`
	Links      []relationTopNTenantVideoLink `tidbgo:"has_many,join=TenantID:TenantID,join=ID:VideoID"`
}

type relationTopNTenantVideoLink struct {
	model.Meta `tidbgo:"table=relation_topn_tenant_video_links"`
	TenantID   int64 `tidbgo:",pk"`
	VideoID    int64 `tidbgo:",pk"`
	GenreID    int64 `tidbgo:",pk"`
}

func TestSelectQueryBuildsRelationFirstTopN(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := relationTopNBenchmarkQuery().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`title`, `tidbgo_t1`.`id`, `tidbgo_t1`.`name` FROM (SELECT `tidbgo_a0`.`video_id` FROM `relation_topn_video_genres` AS `tidbgo_a0` WHERE `tidbgo_a0`.`genre_id` = ? ORDER BY `tidbgo_a0`.`video_id` DESC LIMIT ?) AS `tidbgo_k0` JOIN `relation_topn_videos` AS `tidbgo_t0` ON (`tidbgo_k0`.`video_id` = `tidbgo_t0`.`id`) LEFT JOIN `relation_topn_makers` AS `tidbgo_t1` ON (`tidbgo_t0`.`maker_id` = `tidbgo_t1`.`id`) ORDER BY `tidbgo_t0`.`id` DESC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{int64(7), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryBuildsManyToManyRelationFirstTopN(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := relationTopNManyToManyBenchmarkQuery().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`email` FROM (SELECT `tidbgo_a0`.`user_id` FROM `preload_user_roles` AS `tidbgo_a0` WHERE `tidbgo_a0`.`role_id` = ? ORDER BY `tidbgo_a0`.`user_id` DESC LIMIT ?) AS `tidbgo_k0` JOIN `preload_users` AS `tidbgo_t0` ON (`tidbgo_k0`.`user_id` = `tidbgo_t0`.`id`) ORDER BY `tidbgo_t0`.`id` DESC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{uint64(7), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestManyToManyRelationFirstTopNRequiresCompleteTargetPrimaryKeyEquality(t *testing.T) {
	t.Parallel()

	optimized := Query[preloadMember]().
		Where(Has("Groups", And(Equal("TenantID", uint64(7)), Equal("ID", uint64(9))))).
		OrderBy(Desc("TenantID"), Desc("ID")).
		Limit(20)
	if rewrite := relationTopNShapeForTest(optimized)(t).Compiler.Rewrite; rewrite != queryshape.CompilerRewriteRelationTopN {
		t.Fatalf("complete target key rewrite = %q, want relation TopN", rewrite)
	}
	sqlText, arguments, err := optimized.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"FROM `preload_member_groups` AS `tidbgo_a0`",
		"WHERE (`tidbgo_a0`.`group_tenant_id` = ? AND `tidbgo_a0`.`group_id` = ?)",
		"ORDER BY `tidbgo_a0`.`tenant_id` DESC, `tidbgo_a0`.`member_id` DESC LIMIT ?",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("SQL = %q, want fragment %q", sqlText, fragment)
		}
	}
	if got, want := arguments, []any{uint64(7), uint64(9), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}

	fallback := Query[preloadMember]().
		Where(Has("Groups", Equal("ID", uint64(9)))).
		OrderBy(Desc("TenantID"), Desc("ID")).
		Limit(20)
	compiler := relationTopNShapeForTest(fallback)(t).Compiler
	if compiler.Rewrite != queryshape.CompilerRewriteRelationTopNFallback || !strings.Contains(compiler.Reason, "target primary key") {
		t.Fatalf("partial target key compiler decision = %#v", compiler)
	}
}

func TestManyToManyRelationFirstTopNPreservesTargetSoftDeleteScope(t *testing.T) {
	t.Parallel()

	sqlText, _, err := Query[softDeleteChannel]().
		WithDeleted().
		Where(Has("Tags", Equal("ID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"FROM `soft_delete_channel_tags` AS `tidbgo_a0` JOIN `soft_delete_tags` AS `tidbgo_m0`",
		"WHERE `tidbgo_m0`.`deleted_at` IS NULL AND `tidbgo_m0`.`id` = ?",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("SQL = %q, want fragment %q", sqlText, fragment)
		}
	}
	if strings.Contains(sqlText, "`tidbgo_t0`.`deleted_at` IS NULL") {
		t.Fatalf("SQL = %q, want no root soft-delete scope", sqlText)
	}
}

func TestManyToManyRelationFirstTopNPreservesNestedTargetPredicate(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadGraph]().
		Where(Has(
			"Tags",
			Equal("ID", uint64(7)),
			Has("Node", Equal("Value", "active")),
		)).
		OrderBy(Desc("ID")).
		Limit(20).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"FROM `preload_graph_tags` AS `tidbgo_a0` JOIN `preload_graph_tag_targets` AS `tidbgo_m0`",
		"WHERE `tidbgo_m0`.`id` = ? AND EXISTS (SELECT 1 FROM `preload_graph_nodes` AS `tidbgo_r1`",
		"(`tidbgo_r1`.`id` = `tidbgo_m0`.`node_id`) AND `tidbgo_r1`.`value` = ?",
		"ORDER BY `tidbgo_a0`.`graph_id` DESC LIMIT ?",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("SQL = %q, want fragment %q", sqlText, fragment)
		}
	}
	if got, want := arguments, []any{uint64(7), "active", int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestManyToManyRelationFirstTopNJoinsTargetForAdditionalPredicate(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadUser]().
		Where(Has("Roles", Equal("ID", uint64(7)), Equal("Name", "admin"))).
		OrderBy(Desc("ID")).
		Limit(20).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"FROM `preload_user_roles` AS `tidbgo_a0` JOIN `preload_roles` AS `tidbgo_m0`",
		"WHERE `tidbgo_m0`.`id` = ? AND `tidbgo_m0`.`name` = ?",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("SQL = %q, want fragment %q", sqlText, fragment)
		}
	}
	if got, want := arguments, []any{uint64(7), "admin", int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestRelationFirstTopNPreservesOffsetAndCompositeKeyOrder(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[relationTopNTenantVideo]().
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("TenantID"), Desc("ID")).
		Limit(20).
		Offset(40).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `tidbgo_t0`.`tenant_id`, `tidbgo_t0`.`id` FROM (SELECT `tidbgo_a0`.`tenant_id`, `tidbgo_a0`.`video_id` FROM `relation_topn_tenant_video_links` AS `tidbgo_a0` WHERE `tidbgo_a0`.`genre_id` = ? ORDER BY `tidbgo_a0`.`tenant_id` DESC, `tidbgo_a0`.`video_id` DESC LIMIT ? OFFSET ?) AS `tidbgo_k0` JOIN `relation_topn_tenant_videos` AS `tidbgo_t0` ON (`tidbgo_k0`.`tenant_id` = `tidbgo_t0`.`tenant_id` AND `tidbgo_k0`.`video_id` = `tidbgo_t0`.`id`) ORDER BY `tidbgo_t0`.`tenant_id` DESC, `tidbgo_t0`.`id` DESC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{int64(7), int64(20), int64(40)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestRelationFirstTopNFallsBackForRootPredicates(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[relationTopNVideo]().
		Select("Title").
		Where(
			Equal("Title", "published"),
			Has("VideoGenres", And(Equal("GenreID", int64(7)), IsNotNull("VideoID"))),
		).
		OrderBy(Asc("ID")).
		Limit(10).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `title` FROM `relation_topn_videos` AS `tidbgo_r0` WHERE `tidbgo_r0`.`title` = ? AND EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `relation_topn_video_genres` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`video_id` = `tidbgo_r0`.`id`) AND (`tidbgo_r1`.`genre_id` = ? AND `tidbgo_r1`.`video_id` IS NOT NULL)) ORDER BY `tidbgo_r0`.`id` ASC LIMIT ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"published", int64(7), int64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestRelationFirstTopNFallsBackForRootSoftDeleteScope(t *testing.T) {
	t.Parallel()

	query := Query[relationTopNSoftVideo]().
		Select("ID").
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20)
	sqlText, _, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"`tidbgo_r0`.`deleted_at` IS NULL",
		"`tidbgo_r1`.`deleted_at` IS NULL",
		"`tidbgo_r1`.`genre_id` = ?",
	} {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("SQL = %q, want fragment %q", sqlText, fragment)
		}
	}

	withDeletedSQL, _, err := query.WithDeleted().Build()
	if err != nil {
		t.Fatalf("WithDeleted Build() error = %v", err)
	}
	if strings.Contains(withDeletedSQL, "`tidbgo_t0`.`deleted_at` IS NULL") {
		t.Fatalf("WithDeleted SQL = %q, want no root soft-delete scope", withDeletedSQL)
	}
	if !strings.Contains(withDeletedSQL, "`tidbgo_a0`.`deleted_at` IS NULL") {
		t.Fatalf("WithDeleted SQL = %q, want relation soft-delete scope", withDeletedSQL)
	}
}

func TestRelationFirstTopNFallsBackWhenUniquenessIsUnproven(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[relationTopNUnprovenVideo]().
		Select("ID").
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `relation_topn_unproven_videos` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `relation_topn_unproven_links` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`video_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`genre_id` = ?) ORDER BY `tidbgo_r0`.`id` DESC LIMIT ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{int64(7), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestRelationTopNQueryShapeRecordsFallbackReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		shape  func(testing.TB) queryshape.Query
		reason string
	}{
		{
			name: "unproven uniqueness",
			shape: relationTopNShapeForTest(Query[relationTopNUnprovenVideo]().
				Where(Has("Links", Equal("GenreID", "private-genre"))).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "does not prove at most one matching row",
		},
		{
			name: "different order",
			shape: relationTopNShapeForTest(Query[relationTopNVideo]().
				Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
				OrderBy(Desc("Title")).
				Limit(20)),
			reason: "ORDER BY does not exactly match",
		},
		{
			name: "logical group",
			shape: relationTopNShapeForTest(Query[relationTopNVideo]().
				Where(And(Has("VideoGenres", Equal("GenreID", int64(7))), Equal("Title", "published"))).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "nested in a logical group",
		},
		{
			name: "multiple collection predicates",
			shape: relationTopNShapeForTest(Query[relationTopNVideo]().
				Where(
					Has("VideoGenres", Equal("GenreID", int64(7))),
					Has("VideoGenres", Equal("GenreID", int64(8))),
				).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "more than one collection Has",
		},
		{
			name: "seek cursor",
			shape: relationTopNShapeForTest(Query[relationTopNVideo]().
				Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID")).
				SeekAfter(int64(100)).
				Limit(20)),
			reason: "uses SeekAfter",
		},
		{
			name: "root predicate",
			shape: relationTopNShapeForTest(Query[relationTopNVideo]().
				Where(
					Has("VideoGenres", Equal("GenreID", int64(7))),
					Equal("Title", "private-title"),
				).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "root predicate",
		},
		{
			name: "root soft delete scope",
			shape: relationTopNShapeForTest(Query[relationTopNSoftVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "root default soft-delete scope",
		},
		{
			name: "many to many",
			shape: relationTopNShapeForTest(Query[preloadUser]().
				Where(Has("Roles", Equal("Name", "private-role"))).
				OrderBy(Desc("ID")).
				Limit(20)),
			reason: "target primary key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			compiler := tt.shape(t).Compiler
			if compiler.Rewrite != queryshape.CompilerRewriteRelationTopNFallback || !strings.Contains(compiler.Reason, tt.reason) {
				t.Fatalf("compiler decision = %#v, want fallback reason %q", compiler, tt.reason)
			}
			if strings.Contains(compiler.Reason, "private-") {
				t.Fatalf("compiler reason exposed bind value: %#v", compiler)
			}
		})
	}
}

func TestRelationTopNQueryShapeDistinguishesOptimizedAndNonPaginatedQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		shape func(testing.TB) queryshape.Query
		want  queryshape.CompilerRewrite
	}{
		{name: "optimized", shape: relationTopNShapeForTest(relationTopNBenchmarkQuery()), want: queryshape.CompilerRewriteRelationTopN},
		{
			name: "optimized with deleted",
			shape: relationTopNShapeForTest(Query[relationTopNSoftVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID")).
				Limit(20).
				WithDeleted()),
			want: queryshape.CompilerRewriteRelationTopN,
		},
		{
			name: "no limit",
			shape: relationTopNShapeForTest(Query[relationTopNUnprovenVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID"))),
			want: queryshape.CompilerRewriteNone,
		},
		{
			name: "zero limit",
			shape: relationTopNShapeForTest(Query[relationTopNUnprovenVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID")).
				Limit(0)),
			want: queryshape.CompilerRewriteNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.shape(t).Compiler.Rewrite; got != tt.want {
				t.Fatalf("compiler rewrite = %q, want %q", got, tt.want)
			}
		})
	}
}

func relationTopNShapeForTest[T any](query *SelectQuery[T]) func(testing.TB) queryshape.Query {
	return func(t testing.TB) queryshape.Query {
		return queryShapeForTest(t, query)
	}
}
