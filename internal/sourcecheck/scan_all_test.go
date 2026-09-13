package sourcecheck

import "testing"

func TestScanAllSourcePatternsAndProjectionCoverage(t *testing.T) {
	analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Channel struct { ID int64; YouTubeID string; Title string }
func identities() {
    var ids []int64
    _ = orm.Query[Channel]().Select("ID").Limit(20).ScanAll(ctx, db, &ids)
}

func allFields() {
    var rows []Channel
    err := orm.Query[Channel]().ScanAll(ctx, db, &rows)
    _ = err
    for _, row := range rows { _ = row.ID }
}
`)
	stats := analysis.Statistics
	if stats.ResultQueries != 2 || stats.QueryPatterns != 2 || stats.AnalyzedPatterns != 2 || stats.ExplicitProjections != 1 || stats.Uncertain != 1 || stats.Analyzed != 0 {
		t.Fatalf("statistics = %#v", stats)
	}
	foundPattern := false
	for _, diagnostic := range analysis.Diagnostics {
		if diagnostic.Code == "SRC001" {
			t.Fatalf("ScanAll destination flow must remain uncertain: %#v", diagnostic)
		}
		if diagnostic.Code == "QRY003" {
			foundPattern = true
		}
	}
	if !foundPattern {
		t.Fatalf("missing unordered Limit diagnostic: %#v", analysis.Diagnostics)
	}
}

func TestScanAllSourceIndexChecksUseSourceModel(t *testing.T) {
	catalog := parseSourceSchema(t, `CREATE TABLE channel (id BIGINT PRIMARY KEY, owner_id BIGINT NOT NULL, title VARCHAR(255));`)
	analysis := analyzeSourceWithOptions(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Channel struct { ID int64; OwnerID int64; Title string }
func load() {
    var ids []int64
    _ = orm.Query[Channel]().Select("ID").Where(orm.Equal("OwnerID", 7)).OrderBy(orm.Desc("ID")).Limit(20).ForceIndex("absent").ScanAll(ctx, db, &ids)
}
`, WithSchema(catalog))
	if analysis.Statistics.AnalyzedIndexPatterns != 1 || len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "QRY006" {
		t.Fatalf("statistics = %#v; diagnostics = %#v", analysis.Statistics, analysis.Diagnostics)
	}
}
