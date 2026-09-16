package sourcecheck

import (
	"strings"
	"testing"
)

func TestAggregateSourceProjectionAndGrouping(t *testing.T) {
	for _, tc := range []struct{ query, problem string }{
		{`orm.Aggregate[Order]().Select(orm.Field("ShopID"),orm.CountAll().As("Total")).GroupBy("ShopID")`, ""},
		{`orm.Aggregate[Order]().Select(orm.Field("ShopID").As("One"),orm.Field("ShopID").As("Two"),orm.CountAll().As("Total")).GroupBy("One")`, ""},
		{`orm.Aggregate[Order]().Select(orm.Field("ShopID").As("One"),orm.Field("ShopID").As("Two"),orm.CountAll().As("Total")).GroupBy("One","Two")`, "duplicates a group expression"},
		{`orm.Aggregate[Order]().Select(orm.Field("ShopID"),orm.CountAll().As("Total"))`, "absent from GroupBy"},
		{`orm.Aggregate[Order]().Select(orm.CountAll())`, "requires an exported"},
		{`orm.Aggregate[Order]().Select(orm.CountAll().As("Total"),orm.Sum("Amount").As("Total"))`, "duplicated"},
		{`orm.Aggregate[Order]().Select(orm.Sum("Amount").As("Total")).GroupBy("Total")`, "must reference"},
		{`orm.Aggregate[Order]().Select(orm.Field("Parent.Name").As("Name"),orm.CountIf(orm.Has("Children")).As("Total")).GroupBy("Name").Window(orm.RowNumber().Over(spec).As("Rank"))`, ""},
	} {
		analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Order struct { ShopID int64; Amount int64 }
func check() { _,_,_ = `+tc.query+`.Build() }
`)
		if analysis.Statistics.AggregatePatterns != 1 || analysis.Statistics.AnalyzedAggregatePatterns != 1 || analysis.Statistics.QueryPatterns != 0 {
			t.Fatal(analysis.Statistics)
		}
		if tc.problem == "" {
			if len(analysis.Diagnostics) != 0 {
				t.Fatal(analysis.Diagnostics)
			}
		} else if len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "AGG001" || !strings.Contains(analysis.Diagnostics[0].Message, tc.problem) {
			t.Fatal(analysis.Diagnostics)
		}
	}
}

func TestAggregateSourceConservativeFlow(t *testing.T) {
	for _, tc := range []struct {
		body  string
		known bool
	}{
		{`q:=orm.Aggregate[Order]().Select(orm.CountAll().As("Total"));q.Build()`, true},
		{`q:=orm.Aggregate[Order]().Select(orm.Field("ID"));if flag {q.GroupBy("ID")};q.Build()`, false},
		{`q:=orm.Aggregate[Order]().Select(orm.Field("ID"));escape(q);q.Build()`, false},
		{`q:=orm.Aggregate[Order]().Select(orm.Field("ID"));alias:=q;alias.GroupBy("ID");q.Build()`, false},
		{`orm.Aggregate[Order]().Select(expressions...).Build()`, false},
		{`orm.Aggregate[Order]().Select(orm.CountAll().As(name)).Build()`, false},
	} {
		analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Order struct{ ID int64 }
func check(){`+tc.body+`}`)
		if analysis.Statistics.AggregatePatterns != 1 || (analysis.Statistics.AnalyzedAggregatePatterns == 1) != tc.known || len(analysis.Diagnostics) != 0 {
			t.Fatal(tc.body, analysis)
		}
	}
	analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
func check(orm service){ orm.Aggregate().Select().Build() }`)
	if analysis.Statistics.AggregatePatterns != 0 {
		t.Fatal("shadowed orm", analysis)
	}
}
