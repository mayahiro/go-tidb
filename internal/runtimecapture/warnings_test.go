package runtimecapture

import (
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/warningcheck"
)

func TestWarningCaptureValidationAndAggregation(t *testing.T) {
	r := runtimeAnalysisRecord(1, SourcePlan, "s1:plan")
	r.Operation = "EXPLAIN"
	r.Warnings = &Warnings{Known: true, AuxiliaryStatements: 1, DiagnosticDurationNS: 12, Summary: warningcheck.Summary{MPPBlocked: 3}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r2 := r
	r2.Sequence = 2
	r2.Warnings = &Warnings{Known: true, AuxiliaryStatements: 1, DiagnosticDurationNS: 15, Summary: warningcheck.Summary{MPPBlocked: 1, Other: 1}}
	r3 := r
	r3.Sequence = 3
	r3.Warnings = &Warnings{Failed: true}
	a := Analyze([]Record{r, r2, r3})
	if a.Statistics.WarningCollections != 2 || a.Statistics.WarningCollectionErrors != 1 || a.Statistics.AuxiliaryStatements != 2 || a.Statistics.DiagnosticDuration != 27 {
		t.Fatal(a.Statistics)
	}
	if len(a.Diagnostics) != 3 || a.Diagnostics[0].Code != "WRN001" || !strings.Contains(a.Diagnostics[0].Evidence[1].Message, "4") {
		t.Fatal(a.Diagnostics)
	}
	if !strings.Contains(FormatStatistics(a.Statistics), "warning_collections=2") {
		t.Fatal(FormatStatistics(a.Statistics))
	}
	for _, w := range []Warnings{
		{}, {Known: true}, {Known: true, AuxiliaryStatements: 2}, {Failed: true, DiagnosticDurationNS: -1},
		{Failed: true, Summary: warningcheck.Summary{MPPBlocked: 1}},
		{Known: true, AuxiliaryStatements: 1, Summary: warningcheck.Summary{Other: -1}},
	} {
		r.Warnings = &w
		if err := r.Validate(); err == nil {
			t.Fatalf("accepted invalid warnings: %#v", w)
		}
	}
}
