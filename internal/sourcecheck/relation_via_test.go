package sourcecheck

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/relationtopn"
)

func TestAnalyzeViaReportsExactCompilerFallback(t *testing.T) {
	t.Parallel()
	source := "package repository\n" +
		"import \"github.com/mayahiro/go-tidb/orm\"\n" +
		"type Parent struct { ID int64 `tidbgo:\",pk\"`; Targets []Target `tidbgo:\"many_to_many,via=Edges.Target\"`; Edges []Edge `tidbgo:\"has_many,join=ID:ParentID\"` }\n" +
		"type Edge struct { ID int64 `tidbgo:\",pk\"`; ParentID int64; TargetID int64; Priority int; Target *Target `tidbgo:\"belongs_to\"` }\n" +
		"type Target struct { ID int64 `tidbgo:\",pk\"` }\n" +
		"func query() { _, _, _ = orm.Query[Parent]().Where(orm.Has(\"Targets\", orm.Equal(\"ID\", 7))).OrderBy(orm.Desc(\"ID\")).Limit(20).Build() }"
	analysis := analyzeSource(t, source)
	if analysis.Statistics.AnalyzedRelationTopNPatterns != 1 || analysis.Statistics.UncertainRelationTopNPatterns != 0 {
		t.Fatalf("statistics = %#v", analysis.Statistics)
	}
	if len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != querycheck.CodeRelationTopNFallback || len(analysis.Diagnostics[0].Evidence) != 1 || !strings.Contains(analysis.Diagnostics[0].Evidence[0].Message, relationtopn.ReasonEdgeUniqueness) {
		t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
	}
	invalid := analyzeSource(t, strings.Replace(source, "via=Edges.Target", "via=Targets.Target", 1))
	if invalid.Statistics.UncertainRelationTopNPatterns != 1 {
		t.Fatalf("invalid cycle = %#v", invalid)
	}
}

func TestAnalyzeViaUsesDeclaredEdgeKeysAndScope(t *testing.T) {
	t.Parallel()
	source := "package repository\n" +
		"import (\"github.com/mayahiro/go-tidb/model\"; \"github.com/mayahiro/go-tidb/orm\"; \"time\")\n" +
		"type Parent struct { ID int64 `tidbgo:\",pk\"`; Targets []Target `tidbgo:\"many_to_many,via=Edges.Target\"`; Edges []Edge `tidbgo:\"has_many,join=ID:ParentID\"` }\n" +
		"type Edge struct { model.Meta `tidbgo:\"table=edges\"`; ID int64 `tidbgo:\",pk\"`; ParentID *int64 `tidbgo:\"parent_key,unique=pair\"`; TargetID *int64 `tidbgo:\"target_key,unique=pair\"`; DeletedAt *time.Time `tidbgo:\"removed_at,soft_delete\"`; Priority int; Target *Target `tidbgo:\"belongs_to\"` }\n" +
		"type Target struct { ID int64 `tidbgo:\",pk\"` }\n" +
		"func query() { _, _, _ = orm.Query[Parent]().Where(orm.Has(\"Targets\", orm.Equal(\"ID\", 7))).OrderBy(orm.Desc(\"ID\")).Limit(20).Build() }"
	if analysis := analyzeSource(t, source); len(analysis.Diagnostics) != 0 || analysis.Statistics.AnalyzedRelationTopNPatterns != 1 || analysis.Statistics.UncertainRelationTopNPatterns != 0 {
		t.Fatalf("optimized source: %#v", analysis)
	}
	schema := "CREATE TABLE parent (id BIGINT PRIMARY KEY); CREATE TABLE edges (id BIGINT PRIMARY KEY, parent_key BIGINT NULL, target_key BIGINT NULL, removed_at DATETIME NULL, priority BIGINT NOT NULL, UNIQUE KEY pair_key (parent_key,target_key), KEY target_scope_parent (target_key, removed_at, parent_key));"
	matching := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, schema)))
	if len(matching.Diagnostics) != 0 || matching.Statistics.AnalyzedIndexPatterns != 1 {
		t.Fatalf("matching index: %#v", matching)
	}
	missing := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, strings.Replace(schema, ", KEY target_scope_parent (target_key, removed_at, parent_key)", "", 1))))
	if codes := sourceDiagnosticCodes(missing); !reflect.DeepEqual(codes, []string{querycheck.CodeMissingIndexPrefix}) || !strings.Contains(missing.Diagnostics[0].Evidence[0].Message, "removed_at") {
		t.Fatalf("missing edge scope index: %#v", missing)
	}
	for _, field := range []string{"Priority", "DeletedAt"} {
		invalid := source
		if field == "Priority" {
			invalid = strings.Replace(invalid, "Priority int;", "Priority int `tidbgo:\",unique=pair\"`;", 1)
		} else {
			invalid = strings.Replace(invalid, "removed_at,soft_delete", "removed_at,soft_delete,unique=pair", 1)
		}
		analysis := analyzeSource(t, invalid)
		if len(analysis.Diagnostics) != 1 || !strings.Contains(analysis.Diagnostics[0].Evidence[0].Message, relationtopn.ReasonEdgeUniqueness) {
			t.Fatalf("extra key field %s: %#v", field, analysis)
		}
	}
	primary := strings.ReplaceAll(source, "unique=pair", "pk")
	primary = strings.Replace(primary, "ID int64 `tidbgo:\",pk\"`; ParentID", "ID int64; ParentID", 1)
	if analysis := analyzeSource(t, primary); len(analysis.Diagnostics) != 0 || analysis.Statistics.AnalyzedRelationTopNPatterns != 1 {
		t.Fatalf("edge composite primary key: %#v", analysis)
	}
}
