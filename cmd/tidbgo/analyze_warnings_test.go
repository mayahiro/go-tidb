package main

import (
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/internal/warningcheck"
	cli "github.com/mayahiro/nagicli-go"
)

func TestAnalyzeCapturedWarnings(t *testing.T) {
	r := runtimeCommandRecord(1, "s1:warning")
	r.Source = runtimecapture.SourcePlan
	r.Operation = "EXPLAIN"
	r.Warnings = &runtimecapture.Warnings{Known: true, AuxiliaryStatements: 1, Summary: warningcheck.Summary{MPPBlocked: 2}}
	input := runtimeCaptureInput(t, r)
	for _, args := range [][]string{{"analyze"}, {"analyze", "--json"}, {"analyze", "--suppress", "WRN001=reviewed"}} {
		result := runApplicationWithInput(t, input, args...)
		if result.Status() != cli.StatusSuccess || !strings.Contains(string(result.Stdout()), "WRN001") {
			t.Fatalf("output=%s err=%s", result.Stdout(), result.Stderr())
		}
		if len(args) > 1 && args[1] == "--suppress" && !strings.Contains(string(result.Stdout()), "SUPPRESSED") {
			t.Fatal(string(result.Stdout()))
		}
	}
}
