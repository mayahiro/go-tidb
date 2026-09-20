package warningcheck

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestSummaryClassifiesWithoutLeakingValues(t *testing.T) {
	var s Summary
	s.Add("Warning", 1105, "MPP mode may be blocked because window function `sum` or its arguments are not supported now.")
	s.Add("Warning", 1105, "MPP mode may be blocked because private-value")
	s.Add("Warning", 1105, "unrelated private-value")
	s.Add("Warning", 1292, "invalid number private-value")
	s.Add("Note", 1105, "MPP mode may be blocked because private-value")
	if s != (Summary{MPPBlocked: 2, Other: 2, Notes: 1}) {
		t.Fatal(s)
	}
	d := s.Diagnostics(true)
	if len(d) != 3 || d[0].Code != "WRN001" || d[1].Code != "WRN002" || d[2].Code != "WRN003" {
		t.Fatal(d)
	}
	encoded, _ := json.Marshal(d)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatal("raw warning leaked")
	}
	if got := (Summary{Notes: 1}).Diagnostics(false); got[0].Severity != check.SeverityInfo {
		t.Fatal(got)
	}
	if got := (Summary{}).Diagnostics(false); len(got) != 0 {
		t.Fatal(got)
	}
}
