package referencecheck

import (
	"slices"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/schema"
)

func TestReferenceSchema(t *testing.T) {
	ref := Reference{Name: "Child.Parent", ChildTable: "child", ChildColumns: []string{"tenant", "parent_id"}, ParentTable: "parent", ParentColumns: []string{"tenant", "id"}}
	const ddl = "CREATE TABLE child (id BIGINT PRIMARY KEY,tenant INT,parent_id BIGINT,KEY parent_key(parent_id,tenant)); CREATE TABLE parent (tenant INT,id BIGINT,PRIMARY KEY(tenant,id));"
	for _, tc := range []struct {
		name, sql string
		codes     []string
	}{
		{"matching", ddl, nil},
		{"missing parent", strings.Split(ddl, " CREATE TABLE parent")[0], []string{"REF001"}},
		{"signedness", strings.Replace(ddl, "parent_id BIGINT", "parent_id BIGINT UNSIGNED", 1), []string{"REF002"}},
		{"type", strings.Replace(ddl, "parent_id BIGINT", "parent_id INT", 1), []string{"REF002"}},
		{"lookup", strings.Replace(ddl, ",KEY parent_key(parent_id,tenant)", "", 1), []string{"REF004"}},
		{"unique", strings.Replace(ddl, "PRIMARY KEY(tenant,id)", "KEY parent_key(tenant,id)", 1), []string{"REF003"}},
		{"invisible", strings.Replace(ddl, "KEY parent_key(parent_id,tenant)", "KEY parent_key(parent_id,tenant) INVISIBLE", 1), []string{"REF004"}},
		{"prefix", strings.Replace(ddl, "KEY parent_key(parent_id,tenant)", "KEY parent_key(parent_id(3),tenant)", 1), []string{"REF004"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, err := schema.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			var codes []string
			for _, d := range Validate(catalog, ref) {
				codes = append(codes, d.Code)
				if (d.Code == "REF004") != (d.Severity == check.SeverityWarning && d.Suppressible) {
					t.Fatalf("policy: %+v", d)
				}
			}
			if !slices.Equal(codes, tc.codes) {
				t.Fatalf("codes=%v want=%v", codes, tc.codes)
			}
		})
	}
}

func TestOrphanSQL(t *testing.T) {
	ref := Reference{ChildTable: "children", ChildColumns: []string{"tenant_id", "parent_id"}, ParentTable: "parents", ParentColumns: []string{"tenant_id", "id"}}
	query, err := SQL(ref)
	want := "SELECT EXISTS (SELECT 1 FROM `children` AS c WHERE c.`tenant_id` IS NOT NULL AND c.`parent_id` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM `parents` AS p WHERE p.`tenant_id` = c.`tenant_id` AND p.`id` = c.`parent_id`) LIMIT 1)"
	if err != nil || query != want {
		t.Fatalf("query=%q err=%v", query, err)
	}
	for _, invalid := range []Reference{
		{ChildTable: "bad`;DELETE", ChildColumns: ref.ChildColumns, ParentTable: ref.ParentTable, ParentColumns: ref.ParentColumns},
		{ChildTable: ref.ChildTable, ParentTable: ref.ParentTable},
		{ChildTable: ref.ChildTable, ChildColumns: []string{"id", "ID"}, ParentTable: ref.ParentTable, ParentColumns: ref.ParentColumns},
		{ChildTable: ref.ChildTable, ChildColumns: []string{"id"}, ParentTable: ref.ParentTable, ParentColumns: ref.ParentColumns},
	} {
		if _, err := SQL(invalid); err == nil {
			t.Fatal("accepted invalid mapping")
		}
	}
}
