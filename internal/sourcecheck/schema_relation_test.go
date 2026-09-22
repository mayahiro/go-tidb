package sourcecheck

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/referencecheck"
)

const referenceSource = "package application\n" +
	"import (\"github.com/mayahiro/go-tidb/model\";\"time\")\n" +
	"type Parent struct { model.Meta; ID int64 `tidbgo:\",pk\"`; DeletedAt *time.Time `tidbgo:\",soft_delete\"`; Children []Child `tidbgo:\"has_many,join=ID:ParentID\"` }\n" +
	"type Child struct { ID int64 `tidbgo:\",pk\"`; ParentID *int64; Parent *Parent `tidbgo:\"belongs_to,join=ParentID:ID\"` }\n"

const referenceSQL = "CREATE TABLE parent (id BIGINT PRIMARY KEY, deleted_at DATETIME NULL); CREATE TABLE child (id BIGINT PRIMARY KEY,parent_id BIGINT NULL,KEY parent_lookup(parent_id));"

func TestSourceSchemaReferences(t *testing.T) {
	t.Parallel()
	analysis := analyzeSourceWithOptions(t, referenceSource, WithSchema(parseSourceSchema(t, referenceSQL)))
	if len(analysis.Diagnostics) != 0 || analysis.Statistics.SchemaModels != 2 || analysis.Statistics.AnalyzedSchemaRelations != 2 || analysis.Statistics.UncertainSchemaRelations != 0 || len(analysis.References) != 2 {
		t.Fatalf("analysis=%+v", analysis)
	}
	for _, ref := range analysis.References {
		if ref.ChildTable != "child" || ref.ParentTable != "parent" || !slices.Equal(ref.ChildColumns, []string{"parent_id"}) || !slices.Equal(ref.ParentColumns, []string{"id"}) {
			t.Fatalf("reference direction=%+v", ref)
		}
		query, err := referencecheck.SQL(ref)
		if err != nil || strings.Contains(query, "deleted_at") || !strings.Contains(query, "IS NOT NULL") {
			t.Fatalf("physical probe=%s %v", query, err)
		}
	}
	if again := analyzeSourceWithOptions(t, referenceSource, WithSchema(parseSourceSchema(t, referenceSQL))); !reflect.DeepEqual(analysis, again) {
		t.Fatal("reference output must be deterministic")
	}
	if plain := analyzeSource(t, referenceSource); plain.Statistics.SchemaRelations != 0 || len(plain.References) != 0 {
		t.Fatal("reference analysis must require --schema")
	}
}

func TestSourceRelationSchemaFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source, sql, code string }{
		{"missing column", referenceSource, strings.ReplaceAll(referenceSQL, "parent_id", "other_id"), "REF001"},
		{"incompatible types", referenceSource, strings.Replace(referenceSQL, "parent_id BIGINT", "parent_id BIGINT UNSIGNED", 1), "REF002"},
		{"parent uniqueness", referenceSource, strings.Replace(referenceSQL, "id BIGINT PRIMARY KEY", "id BIGINT, KEY id_lookup(id)", 1), "REF003"},
		{"child index", referenceSource, strings.Replace(referenceSQL, ",KEY parent_lookup(parent_id)", "", 1), "REF004"},
		{"has one identity", strings.Replace(referenceSource, "Children []Child `tidbgo:\"has_many", "Children *Child `tidbgo:\"has_one", 1), referenceSQL, "CMP011"},
		{"unresolved join", strings.Replace(referenceSource, "join=ID:ParentID", "join=ID:Unknown", 1), referenceSQL, "SRC003"},
		{"unresolved embedding", strings.Replace(referenceSource, "type Child struct {", "type Base struct { Extra string }; type Child struct { Base;", 1), referenceSQL, "SRC003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis := analyzeSourceWithOptions(t, tc.source, WithSchema(parseSourceSchema(t, tc.sql)))
			if !slices.Contains(sourceDiagnosticCodes(analysis), tc.code) {
				t.Fatalf("want %s: %+v", tc.code, analysis)
			}
			if tc.code == "SRC003" && analysis.Statistics.UncertainSchemaRelations == 0 {
				t.Fatal("missing incomplete coverage")
			}
		})
	}
}

func TestSourceReferenceCompositeJunctionAndSelf(t *testing.T) {
	source := "package application\nimport \"github.com/mayahiro/go-tidb/model\"\n" +
		"type Node struct {model.Meta; Tenant int64 `tidbgo:\",pk\"`; ID int64 `tidbgo:\",pk\"`; ParentTenant *int64; ParentID *int64; Parent *Node `tidbgo:\"belongs_to,join=ParentTenant:Tenant,join=ParentID:ID\"`; Tags []Tag `tidbgo:\"many_to_many,through=node_tags,source=Tenant:tenant_id,source=ID:node_id,target=tag_id:ID\"`};type Tag struct{ID int64 `tidbgo:\",pk\"`}"
	sql := "CREATE TABLE node(tenant BIGINT,id BIGINT,parent_tenant BIGINT,parent_id BIGINT,PRIMARY KEY(tenant,id),KEY parent_lookup(parent_tenant,parent_id)); CREATE TABLE tag(id BIGINT PRIMARY KEY); CREATE TABLE node_tags(tenant_id BIGINT,node_id BIGINT,tag_id BIGINT,UNIQUE KEY pair_key(tenant_id,node_id,tag_id),KEY target_lookup(tag_id));"
	analysis := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, sql)))
	if len(analysis.Diagnostics) != 0 || len(analysis.References) != 3 || analysis.Statistics.SchemaModels != 2 {
		t.Fatalf("analysis=%+v", analysis)
	}
	if ref := analysis.References[0]; ref.ChildTable != "node" || ref.ParentTable != "node" || len(ref.ChildColumns) != 2 {
		t.Fatalf("self reference=%+v", ref)
	}
	missing := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, strings.Replace(sql, "UNIQUE KEY pair_key", "KEY pair_key", 1))))
	if !slices.Contains(sourceDiagnosticCodes(missing), "CMP012") {
		t.Fatalf("pair uniqueness: %+v", missing)
	}
}
