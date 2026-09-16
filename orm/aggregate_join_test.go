package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

func TestAggregateRelatedFieldsAndConditionalRelations(t *testing.T) {
	q := Aggregate[aggregateRelationOrder]().Select(
		Field("User.Active").As("Active"),
		CountAll().As("Total"),
		CountIf(Has("User", Equal("Active", true), Has("Orders", GreaterThan("Amount", 7)))).As("Matched"),
		SumIf("Amount", Not(Has("User"))).As("Missing"),
	).GroupBy("Active").Where(Has("User")).Having(GreaterThan("Matched", 2)).OrderBy(Desc("Matched"), Asc("Active")).ReadFrom(TiFlash).MPP(MPPEnforce)
	statement, args, err := q.Build()
	if err != nil || !reflect.DeepEqual(args, []any{true, 7, true, 7, 2, true, 7}) {
		t.Fatalf("SQL=%s args=%v err=%v", statement, args, err)
	}
	for _, fragment := range []string{
		"READ_FROM_STORAGE(TIFLASH[a,tidbgo_a1])",
		"`tidbgo_a1`.`active` AS `Active`",
		"LEFT JOIN `aggregate_relation_users` AS `tidbgo_a1` ON (`tidbgo_a1`.`id` = `a`.`user_id` AND `tidbgo_a1`.`deleted_at` IS NULL)",
		"COUNT(CASE WHEN EXISTS (SELECT",
		"SUM(CASE WHEN NOT (EXISTS (SELECT",
		"GROUP BY `tidbgo_a1`.`active`",
	} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("missing %s in %s", fragment, statement)
		}
	}
	if strings.Contains(statement, "SEMI_JOIN_REWRITE") || strings.Count(statement, "LEFT JOIN") != 1 || strings.Count(statement, "tidb_allow_mpp") != 1 {
		t.Fatal("conditional rewrite or duplicate join/hint", statement)
	}
	c, err := q.compile()
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := q.planAccessResolver(c)
	if err != nil {
		t.Fatal(err)
	}
	for alias, path := range map[string]string{"tidbgo_a1": "User", "tidbgo_r1": "User", "tidbgo_r2": "User.Orders", "tidbgo_r3": "User", "tidbgo_r4": "User", "tidbgo_r5": "User", "tidbgo_r6": "User.Orders", "tidbgo_r7": "User", "tidbgo_r8": "User.Orders"} {
		if got := resolver.resolve("table:" + alias); got.relationPath != path || got.physicalTable == "" {
			t.Fatalf("%s resolved to %#v, want %s", alias, got, path)
		}
	}
}

type aggregateJoinTenant struct {
	model.Meta `tidbgo:"table=aggregate_join_tenants"`
	TenantID   int64 `tidbgo:",pk"`
	ID         int64 `tidbgo:",pk"`
	ParentID   *int64
	Name       string
	Parent     *aggregateJoinTenant `tidbgo:"belongs_to,join=TenantID:TenantID,join=ParentID:ID"`
}

func TestAggregateRelatedNestedCompositeAndReuse(t *testing.T) {
	q := Aggregate[aggregateJoinTenant]().Select(Field("Parent.Parent.Name").As("Ancestor"), Field("Parent.Name").As("ParentName"), Min("Parent.Parent.ID").As("MinID"), CountAll().As("Count")).GroupBy("Ancestor", "ParentName")
	statement, _, err := q.Build()
	if err != nil || strings.Count(statement, "LEFT JOIN") != 2 || !strings.Contains(statement, "`tidbgo_a2`.`tenant_id` = `tidbgo_a1`.`tenant_id` AND `tidbgo_a2`.`id` = `tidbgo_a1`.`parent_id`") || !strings.Contains(statement, "MIN(`tidbgo_a2`.`id`)") {
		t.Fatal(statement, err)
	}
}

func TestAggregateRelatedFieldsRejectUnsafePaths(t *testing.T) {
	for _, field := range []string{"Orders.Amount", "Missing.Name", "Orders", "Active.X", "Name; DROP TABLE x", ".ID"} {
		_, _, err := Aggregate[aggregateRelationUser]().Select(Field(field).As("Value")).GroupBy("Value").Build()
		if err == nil {
			t.Fatal("accepted", field)
		}
	}
	// The has-one declaration alone does not prove physical uniqueness.
	if _, _, err := Aggregate[preloadUser]().Select(Field("Profile.Bio").As("Bio"), CountAll().As("Count")).GroupBy("Bio").Build(); err == nil {
		t.Fatal("accepted target key without a declared unique constraint")
	}
	if _, _, err := Aggregate[aggregateRelationOrder]().Select(Field("User.Active")).GroupBy("Active").Build(); err == nil {
		t.Fatal("accepted a dotted field without an output alias")
	}
}
