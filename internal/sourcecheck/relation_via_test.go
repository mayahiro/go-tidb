package sourcecheck

import (
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
	if len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != querycheck.CodeRelationTopNFallback || len(analysis.Diagnostics[0].Evidence) != 1 || !strings.Contains(analysis.Diagnostics[0].Evidence[0].Message, relationtopn.ReasonReadThrough) {
		t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
	}
	invalid := analyzeSource(t, strings.Replace(source, "via=Edges.Target", "via=Targets.Target", 1))
	if invalid.Statistics.UncertainRelationTopNPatterns != 1 {
		t.Fatalf("invalid cycle = %#v", invalid)
	}
}
