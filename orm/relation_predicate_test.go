package orm

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type relationPredicateNode struct {
	model.Meta `tidbgo:"table=relation_predicate_nodes"`
	ID         uint64 `tidbgo:",pk"`
	ParentID   *uint64
	Parent     *relationPredicateNode  `tidbgo:"belongs_to,join=ParentID:ID"`
	Children   []relationPredicateNode `tidbgo:"has_many,join=ID:ParentID"`
}

func TestSelectQueryBuildsDirectRelationPredicatesOffline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    interface{ Build() (string, []any, error) }
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "has many",
			query:   Query[preloadUser]().Select("ID").Where(Has("Orders")),
			wantSQL: "SELECT `id` FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT 1 FROM `preload_orders` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`user_id` = `tidbgo_r0`.`id`))",
		},
		{
			name:     "has one",
			query:    Query[preloadUser]().Select("ID").Where(Has("Profile", Contains("Bio", "TiDB"))),
			wantSQL:  "SELECT `id` FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT 1 FROM `preload_profiles` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`bio` LIKE ? ESCAPE '!')",
			wantArgs: []any{"%TiDB%"},
		},
		{
			name:     "belongs to",
			query:    Query[preloadOrder]().Select("ID").Where(Has("User", Equal("Email", "ada@example.com"))),
			wantSQL:  "SELECT `id` FROM `preload_orders` AS `tidbgo_r0` WHERE EXISTS (SELECT 1 FROM `preload_users` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`id` = `tidbgo_r0`.`user_id`) AND `tidbgo_r1`.`email` = ?)",
			wantArgs: []any{"ada@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sqlText, arguments, err := tt.query.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if sqlText != tt.wantSQL {
				t.Fatalf("SQL = %q, want %q", sqlText, tt.wantSQL)
			}
			if !reflect.DeepEqual(arguments, tt.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", arguments, tt.wantArgs)
			}
		})
	}
}

func TestSelectQueryBuildsManyToManyRelationPredicateOffline(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadUser]().
		Select("ID", "Email").
		Where(Has("Roles", Equal("Name", "admin"))).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id`, `email` FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryBuildsCompositeRelationPredicatesOffline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    interface{ Build() (string, []any, error) }
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "direct",
			query:    Query[preloadTenant]().Where(Has("Records", Equal("Value", "ready"))),
			wantSQL:  "SELECT `tenant_id`, `id` FROM `preload_tenants` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_records` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`tenant_id` = `tidbgo_r0`.`tenant_id` AND `tidbgo_r1`.`parent_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`value` = ?)",
			wantArgs: []any{"ready"},
		},
		{
			name:     "many to many",
			query:    Query[preloadMember]().Where(Has("Groups", Equal("Name", "operators"))),
			wantSQL:  "SELECT `tenant_id`, `id` FROM `preload_members` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_member_groups` AS `tidbgo_j1` JOIN `preload_groups` AS `tidbgo_r1` ON (`tidbgo_r1`.`tenant_id` = `tidbgo_j1`.`group_tenant_id` AND `tidbgo_r1`.`id` = `tidbgo_j1`.`group_id`) WHERE (`tidbgo_j1`.`tenant_id` = `tidbgo_r0`.`tenant_id` AND `tidbgo_j1`.`member_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)",
			wantArgs: []any{"operators"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sqlText, arguments, err := tt.query.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if sqlText != tt.wantSQL {
				t.Fatalf("SQL = %q, want %q", sqlText, tt.wantSQL)
			}
			if !reflect.DeepEqual(arguments, tt.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", arguments, tt.wantArgs)
			}
		})
	}
}

func TestSelectQueryBuildsNestedRelationPredicatesWithScopedAliases(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadUser]().
		Select("ID").
		Where(
			Equal("Email", "owner@example.com"),
			Has("Orders",
				GreaterThan("ID", uint64(10)),
				Has("User", Equal("Email", "buyer@example.com")),
			),
		).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `preload_users` AS `tidbgo_r0` WHERE `tidbgo_r0`.`email` = ? AND EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_orders` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`id` > ? AND EXISTS (SELECT 1 FROM `preload_users` AS `tidbgo_r2` WHERE (`tidbgo_r2`.`id` = `tidbgo_r1`.`user_id`) AND `tidbgo_r2`.`email` = ?))"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"owner@example.com", uint64(10), "buyer@example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryBuildsSelfRelationPredicateWithDistinctAliases(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[relationPredicateNode]().
		Select("ID").
		Where(Has("Children", Equal("ID", uint64(2)))).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `relation_predicate_nodes` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `relation_predicate_nodes` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`parent_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`id` = ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{uint64(2)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryBuildsLogicalRelationPredicates(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadUser]().
		Select("ID").
		Where(Or(Has("Orders"), Not(Has("Roles", Equal("Name", "suspended"))))).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `preload_users` AS `tidbgo_r0` WHERE (EXISTS (SELECT 1 FROM `preload_orders` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`user_id` = `tidbgo_r0`.`id`)) OR NOT (EXISTS (SELECT 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)))"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"suspended"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryUsesSemiJoinRewriteOnlyForPositiveConjunctiveCollectionFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    interface{ Build() (string, []any, error) }
		wantHint bool
	}{
		{
			name:     "filtered has many",
			query:    Query[preloadUser]().Where(Has("Orders", Equal("Total", "10.00"))),
			wantHint: true,
		},
		{
			name:  "unfiltered has many",
			query: Query[preloadUser]().Where(Has("Orders")),
		},
		{
			name:  "has one",
			query: Query[preloadUser]().Where(Has("Profile", Equal("Bio", "public"))),
		},
		{
			name:  "negated collection",
			query: Query[preloadUser]().Where(Not(Has("Orders", Equal("Total", "10.00")))),
		},
		{
			name:  "disjunctive collection",
			query: Query[preloadUser]().Where(Or(Has("Orders", Equal("Total", "10.00")), Equal("Email", "ada@example.com"))),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sqlText, _, err := tt.query.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if got := strings.Contains(sqlText, relationSemiJoinRewriteHint); got != tt.wantHint {
				t.Fatalf("SQL = %q, hint present = %t, want %t", sqlText, got, tt.wantHint)
			}
		})
	}
}

func TestSelectQueryRelationPredicateDoesNotPreload(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"id", "email"},
				values:  [][]driver.Value{{int64(1), "ada@example.com"}},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().
		Select("ID", "Email").
		Where(Has("Roles", Equal("Name", "admin"))).
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || users[0].ID != 1 || users[0].Email != "ada@example.com" {
		t.Fatalf("users = %#v, want one selected user", users)
	}
	if users[0].Roles != nil || users[0].Orders != nil || users[0].Profile != nil {
		t.Fatalf("relations = Roles %#v, Orders %#v, Profile %#v, want no hydration", users[0].Roles, users[0].Orders, users[0].Profile)
	}

	calls := preloadCalls(state)
	if len(calls) != 1 {
		t.Fatalf("query count = %d, want 1", len(calls))
	}
	wantSQL := "SELECT `id`, `email` FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)"
	if calls[0].query != wantSQL {
		t.Fatalf("query = %q, want %q", calls[0].query, wantSQL)
	}
	if got, want := namedValues(calls[0].arguments), []any{"admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRelationPredicateCanBeCombinedWithExplicitPreload(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"email", "id"},
				values:  [][]driver.Value{{"ada@example.com", int64(1)}},
			},
			{
				columns: []string{"user_id", "id", "name"},
				values: [][]driver.Value{
					{int64(1), int64(10), "admin"},
					{int64(1), int64(11), "reader"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().
		Select("Email").
		Where(Has("Roles", Equal("Name", "admin"))).
		Preload("Roles").
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || len(users[0].Roles) != 2 || users[0].Roles[0].Name != "admin" || users[0].Roles[1].Name != "reader" {
		t.Fatalf("users = %#v, want the matching user with the full explicitly preloaded relation", users)
	}

	calls := preloadCalls(state)
	if len(calls) != 2 {
		t.Fatalf("query count = %d, want 2", len(calls))
	}
	wantParentSQL := "SELECT `email`, `id` FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)"
	if calls[0].query != wantParentSQL {
		t.Fatalf("parent query = %q, want %q", calls[0].query, wantParentSQL)
	}
	if got, want := namedValues(calls[1].arguments), []any{int64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRelationPredicatesApplyToExistsAndCount(t *testing.T) {
	t.Parallel()

	query := Query[preloadUser]().Where(Has("Roles", Equal("Name", "admin")))
	exists, err := query.compileExists()
	if err != nil {
		t.Fatalf("compileExists() error = %v", err)
	}
	wantExistsSQL := "SELECT 1 FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?) LIMIT ?"
	if exists.sql != wantExistsSQL {
		t.Fatalf("Exists SQL = %q, want %q", exists.sql, wantExistsSQL)
	}
	if got, want := exists.arguments, []any{"admin", int64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Exists arguments = %#v, want %#v", got, want)
	}

	count, err := query.compileCount()
	if err != nil {
		t.Fatalf("compileCount() error = %v", err)
	}
	wantCountSQL := "SELECT COUNT(*) FROM `preload_users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `preload_user_roles` AS `tidbgo_j1` JOIN `preload_roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?)"
	if count.sql != wantCountSQL {
		t.Fatalf("Count SQL = %q, want %q", count.sql, wantCountSQL)
	}
	if got, want := count.arguments, []any{"admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Count arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRelationPredicatesValidateOffline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		predicate Predicate
		want      string
	}{
		{name: "unknown relation", predicate: Has("Missing"), want: "not a mapped relation field"},
		{name: "scalar field", predicate: Has("Email"), want: "not a mapped relation field"},
		{name: "zero target predicate", predicate: Has("Orders", Predicate{}), want: "unknown operator 0"},
		{name: "unknown target field", predicate: Has("Orders", Equal("Missing", 1)), want: "not a mapped scalar field"},
		{name: "unknown nested relation", predicate: Has("Orders", Has("Missing")), want: "not a mapped relation field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Query[preloadUser]().Select("ID").Where(tt.predicate).Build()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCompileSelectRejectsMalformedRelationPredicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		predicate predicate
		want      string
	}{
		{
			name:      "Has values",
			predicate: predicate{operator: predicateHasRelation, field: "Orders", values: []any{1}},
			want:      "must not contain values",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[preloadUser](),
				projection: []string{"ID"},
				predicates: []predicate{tt.predicate},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileSelect() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSelectQueryRelationPredicateDoesNotExecuteCustomKeyValuerOffline(t *testing.T) {
	preloadCustomKeyValueCalls = 0

	_, _, err := Query[preloadCustomParent]().Select("ID").Where(Has("Children")).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if preloadCustomKeyValueCalls != 0 {
		t.Fatalf("custom key Value() calls = %d, want 0", preloadCustomKeyValueCalls)
	}
}
