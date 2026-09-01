package orm

import "testing"

func TestPlanAccessObjectTableUsesExactTableToken(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"table:tidbgo_t1":                                  "tidbgo_t1",
		"table:tidbgo_t1, index:PRIMARY(id)":               "tidbgo_t1",
		"partition:p0, table:tidbgo_t1, index:PRIMARY(id)": "tidbgo_t1",
		"table:tidbgo_t1, table:tidbgo_t2":                 "",
		"table:":                                           "",
		"index:PRIMARY(id)":                                "",
		"not-table:tidbgo_t1":                              "",
		"":                                                 "",
	}
	for accessObject, want := range tests {
		if got := planAccessObjectTable(accessObject); got != want {
			t.Fatalf("planAccessObjectTable(%q) = %q, want %q", accessObject, got, want)
		}
	}
}

func TestPlanAccessResolverDoesNotGuessAmbiguousPhysicalRelationPath(t *testing.T) {
	t.Parallel()

	resolver := planAccessResolver{
		root: planAccessBinding{
			alias:         "tidbgo_r0",
			physicalTable: "nodes",
			model:         "Node",
		},
		hasRoot: true,
	}
	resolver.add(planAccessBinding{
		alias:         "tidbgo_r1",
		physicalTable: "nodes",
		model:         "Node",
		relationPath:  "Parent",
	})

	alias := resolver.resolve("table:tidbgo_r1")
	if alias.physicalTable != "nodes" || alias.model != "Node" || alias.relationPath != "Parent" {
		t.Fatalf("alias resolution = %#v", alias)
	}
	physical := resolver.resolve("table:nodes")
	if physical.physicalTable != "nodes" || physical.model != "Node" || physical.relationPath != "" {
		t.Fatalf("physical resolution = %#v", physical)
	}
}

func TestPlanAccessResolverUsesOverflowAfterFixedBindings(t *testing.T) {
	t.Parallel()

	resolver := planAccessResolver{}
	bindings := []planAccessBinding{
		{alias: "alias1", physicalTable: "table1"},
		{alias: "alias2", physicalTable: "table2"},
		{alias: "alias3", physicalTable: "table3"},
		{alias: "alias4", physicalTable: "table4"},
		{alias: "alias5", physicalTable: "table5", model: "Model5", relationPath: "Items.Detail"},
	}
	for index := range bindings {
		resolver.add(bindings[index])
	}
	if resolver.fixedCount != fixedPlanAccessBindingCount || len(resolver.additional) != 1 {
		t.Fatalf("resolver storage = fixed %d, overflow %d", resolver.fixedCount, len(resolver.additional))
	}
	resolved := resolver.resolve("table:alias5")
	if resolved.physicalTable != "table5" || resolved.model != "Model5" || resolved.relationPath != "Items.Detail" {
		t.Fatalf("overflow resolution = %#v", resolved)
	}
}
