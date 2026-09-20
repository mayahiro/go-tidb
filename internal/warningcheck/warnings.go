// Package warningcheck classifies server warnings without retaining server text.
package warningcheck

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
)

// Summary contains counts only; messages, identifiers, and values are omitted.
type Summary struct {
	MPPBlocked int `json:"mpp_blocked"`
	Other      int `json:"other"`
	Notes      int `json:"notes"`
}

// Add recognizes TiDB's MPP warning prefix, not error code 1105 alone.
func (s *Summary) Add(level string, code int, message string) {
	if strings.EqualFold(level, "Note") {
		s.Notes++
	} else if strings.EqualFold(level, "Warning") && code == 1105 && strings.HasPrefix(message, "MPP mode may be blocked because ") {
		s.MPPBlocked++
	} else {
		s.Other++
	}
}

// Diagnostics returns deterministic, value-free diagnostics, even for partial MPP.
// A warning is not evidence that the complete statement fell back from MPP.
func (s Summary) Diagnostics(failed bool) []check.Diagnostic {
	var result []check.Diagnostic
	if s.MPPBlocked > 0 {
		result = append(result, check.Diagnostic{
			Code: "WRN001", Severity: check.SeverityWarning, Title: "Server reported an MPP limitation",
			Message:      "TiDB reported a possible MPP limitation; this does not establish whole-query fallback or a performance regression",
			Evidence:     []check.Evidence{{Message: fmt.Sprintf("MPP limitation warnings: %d", s.MPPBlocked)}},
			Suggestion:   "Inspect the explicit plan and compare latency and ServerRU with representative inputs",
			Suppressible: true, Reference: "https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/",
		})
	}
	if s.Other > 0 || s.Notes > 0 {
		severity := check.SeverityWarning
		if s.Other == 0 {
			severity = check.SeverityInfo
		}
		result = append(result, check.Diagnostic{
			Code: "WRN002", Severity: severity, Title: "Server returned additional diagnostics",
			Message:      "Server messages are omitted because they can contain SQL values",
			Evidence:     []check.Evidence{{Message: fmt.Sprintf("Other warnings/errors: %d; notes: %d", s.Other, s.Notes)}},
			Suggestion:   "Review the raw warnings through the caller-owned observation or explicit plan result",
			Suppressible: true, Reference: "https://docs.pingcap.com/tidb/stable/sql-statement-show-warnings/",
		})
	}
	if failed {
		result = append(result, check.Diagnostic{
			Code: "WRN003", Severity: check.SeverityWarning, Title: "Warning collection was not completed successfully",
			Message:      "Warning coverage is incomplete; absence of warnings is not proof that none occurred",
			Suggestion:   "Inspect the warning observation error; collect warnings separately from ServerRU",
			Suppressible: true,
		})
	}
	return result
}
