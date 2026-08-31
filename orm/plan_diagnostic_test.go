package orm

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestExplainAnalyzePlanDiagnosticsReportsHighConfidenceRuntimeFacts(t *testing.T) {
	t.Parallel()

	plan := ExplainAnalyzePlan{
		{
			ID:           "│ └─TableFullScan_18",
			EstRows:      10_000,
			ActRows:      20_000,
			AccessObject: "table:videos",
			OperatorInfo: "range:[private-value,private-value], keep order:false, stats:pseudo",
			Disk:         "N/A",
		},
		{
			ID:           "├─IndexRangeScan_20(Build)",
			EstRows:      10,
			ActRows:      1_000,
			AccessObject: "table:video_genres, index:genre_video(genre_id, video_id)",
			OperatorInfo: "stats:partial[genre_id:unInitialized]",
			Disk:         "N/A",
		},
		{
			ID:      "Selection_21",
			EstRows: 10,
			ActRows: 1_000,
			Disk:    "N/A",
		},
		{
			ID:      "HashAgg_22",
			EstRows: 1,
			ActRows: 1,
			Disk:    "600.0 MB",
		},
	}

	diagnostics := plan.Diagnostics()
	wantCodes := []string{
		codePlanIncompleteStatistics,
		codePlanEstimateDivergence,
		codePlanLargeTableFullScan,
		codePlanDiskUsage,
	}
	if got := queryDiagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if len(diagnostics[0].Evidence) != 2 || len(diagnostics[1].Evidence) != 1 || len(diagnostics[2].Evidence) != 1 || len(diagnostics[3].Evidence) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible || diagnostic.Reference == "" {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
		for _, evidence := range diagnostic.Evidence {
			if strings.Contains(evidence.Message, "private-value") {
				t.Fatalf("diagnostic exposed operator range value: %#v", diagnostic)
			}
		}
	}
}

func TestExplainAnalyzePlanDiagnosticsUsesConservativeThresholds(t *testing.T) {
	t.Parallel()

	plan := ExplainAnalyzePlan{
		{ID: "TableFullScan_1", EstRows: 100, ActRows: planFullScanRowsThreshold - 1, Disk: "N/A"},
		{ID: "Selection_2", EstRows: 10, ActRows: planEstimateRowsThreshold - 1, Disk: "0 Bytes"},
		{ID: "Selection_3", EstRows: math.NaN(), ActRows: 100_000, Disk: "unknown"},
		{ID: "Selection_4", EstRows: math.Inf(1), ActRows: 100_000},
		{ID: "Selection_5", EstRows: -1, ActRows: 100_000},
		{ID: "Selection_6", EstRows: 10_000, ActRows: -1},
	}
	if diagnostics := plan.Diagnostics(); diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("Diagnostics() = %#v, want non-nil empty", diagnostics)
	}
}

func TestExplainAnalyzePlanDiagnosticsIncludesExactThresholdsAndZeroEstimate(t *testing.T) {
	t.Parallel()

	plan := ExplainAnalyzePlan{
		{ID: "TableFullScan_1", EstRows: planFullScanRowsThreshold, ActRows: planFullScanRowsThreshold, Disk: "N/A"},
		{ID: "Selection_2", EstRows: 10, ActRows: planEstimateRowsThreshold, Disk: "N/A"},
		{ID: "Selection_3", EstRows: 0, ActRows: planEstimateRowsThreshold, Disk: "1 KB"},
	}
	want := []string{codePlanEstimateDivergence, codePlanLargeTableFullScan, codePlanDiskUsage}
	if got := queryDiagnosticCodes(plan.Diagnostics()); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestExplainAnalyzePlanDiagnosticsReturnsDetachedValues(t *testing.T) {
	t.Parallel()

	plan := ExplainAnalyzePlan{{ID: "TableFullScan_1", EstRows: 10_000, ActRows: 10_000}}
	first := plan.Diagnostics()
	first[0].Code = "CHANGED"
	first[0].Evidence[0].Message = "CHANGED"
	second := plan.Diagnostics()
	if second[0].Code != codePlanLargeTableFullScan || second[0].Evidence[0].Message == "CHANGED" {
		t.Fatalf("second Diagnostics() = %#v", second)
	}

	var empty ExplainAnalyzePlan
	if diagnostics := empty.Diagnostics(); diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("nil plan Diagnostics() = %#v, want non-nil empty", diagnostics)
	}
}

func TestPositivePlanUsageRecognizesTiDBUnits(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{
		"N/A":       false,
		"":          false,
		"0 Bytes":   false,
		"4 KB":      true,
		"600.0 MB":  true,
		"1.13 GB":   true,
		"1 mystery": false,
	} {
		if got := positivePlanUsage(value); got != want {
			t.Fatalf("positivePlanUsage(%q) = %t, want %t", value, got, want)
		}
	}
}

func BenchmarkExplainAnalyzePlanDiagnosticsClean(b *testing.B) {
	plan := ExplainAnalyzePlan{
		{ID: "Limit_1", EstRows: 20, ActRows: 20, Disk: "N/A"},
		{ID: "└─IndexRangeScan_2", EstRows: 20, ActRows: 20, AccessObject: "table:videos, index:PRIMARY(id)", Disk: "N/A"},
	}
	var diagnostics []check.Diagnostic
	b.ReportAllocs()
	for b.Loop() {
		diagnostics = plan.Diagnostics()
	}
	queryDiagnosticSink = diagnostics
}

func BenchmarkExplainAnalyzePlanDiagnosticsWithWarnings(b *testing.B) {
	plan := ExplainAnalyzePlan{
		{ID: "TableReader_1", EstRows: 20, ActRows: 20, Disk: "N/A"},
		{ID: "└─TableFullScan_2", EstRows: 10_000, ActRows: 100_000, AccessObject: "table:videos", OperatorInfo: "stats:pseudo", Disk: "N/A"},
		{ID: "HashAgg_3", EstRows: 1, ActRows: 1, Disk: "4 KB"},
	}
	var diagnostics []check.Diagnostic
	b.ReportAllocs()
	for b.Loop() {
		diagnostics = plan.Diagnostics()
	}
	queryDiagnosticSink = diagnostics
}
