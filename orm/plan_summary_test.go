package orm

import (
	"math"
	"testing"
)

func TestPlanSummaryKeepsFinalAndPartialCardinalitySeparate(t *testing.T) {
	p := AggregatePlan{Executed: ExplainAnalyzePlan{
		{ID: "HashAgg_1", Task: "root", EstRows: 10, ActRows: 2},
		{ID: "└─TableReader_2", Task: "root", EstRows: 100, ActRows: 4},
		{ID: "  └─ExchangeSender_3", Task: "mpp[tiflash]", EstRows: 100, ActRows: 4},
		{ID: "    └─HashAgg_4", Task: "mpp[tiflash]", EstRows: 100, ActRows: 4},
		{ID: "      └─Selection_5", Task: "mpp[tiflash]", EstRows: 1000, ActRows: 50},
		{ID: "        └─TableFullScan_6", Task: "mpp[tiflash]", EstRows: 10000, ActRows: 20000},
	}}
	s := p.Summary()
	if !s.Executed || !s.TreeKnown || !s.Result.ActualKnown || s.Result.Actual != 2 || len(s.Operators) != 6 {
		t.Fatal(s)
	}
	if s.Operators[3].Children[0].Actual != 50 || s.Operators[3].Output.Actual != 4 || s.Operators[3].Task.Kind != "mpp" {
		t.Fatal(s.Operators[3])
	}
	if s.Operators[0].Children[0].Actual != 4 {
		t.Fatal("summed a subtree", s)
	}
	if len(s.Operators[5].Children) != 0 {
		t.Fatal("scan has invented input")
	}
}

func TestPlanSummaryPreservesUnknowns(t *testing.T) {
	p := AggregatePlan{Planned: []ExplainRow{{ID: "HashAgg_1", Task: "root", EstRows: math.NaN()}, {ID: "future->Scan_2", Task: "future"}}}
	s := p.Summary()
	if s.Executed || s.TreeKnown || s.Result.ActualKnown || s.Operators[0].Output.EstimatedKnown || s.Operators[1].Task.Known {
		t.Fatal(s)
	}
	p.Planned[1].ID = "└─TableFullScan_2"
	s = p.Summary()
	if !s.TreeKnown || s.Operators[0].Children[0].ActualKnown {
		t.Fatal(s)
	}
	if (AggregatePlan{}).Summary().TreeKnown {
		t.Fatal("empty plan known")
	}
}
