package check

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Suppression allows all suppressible diagnostics with one exact code and
// records why the application accepts them.
type Suppression struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Allow constructs a reason-carrying diagnostic suppression.
// NewReport validates the code, reason, and matching diagnostics.
func Allow(code, reason string) Suppression {
	return Suppression{Code: code, Reason: reason}
}

// SuppressedDiagnostic records one diagnostic removed from the active result
// together with the application-provided reason.
type SuppressedDiagnostic struct {
	Diagnostic Diagnostic `json:"diagnostic"`
	Reason     string     `json:"reason"`
}

// Summary counts active diagnostics by severity and diagnostics suppressed
// with an explicit reason.
type Summary struct {
	Errors     int `json:"errors"`
	Warnings   int `json:"warnings"`
	Info       int `json:"info"`
	Suppressed int `json:"suppressed"`
}

// Report is an immutable aggregation of active and explicitly suppressed
// diagnostics.
//
// The default policy fails only when active error diagnostics remain.
type Report struct {
	diagnostics []Diagnostic
	suppressed  []SuppressedDiagnostic
	summary     Summary
}

// NewReport validates diagnostics and applies exact-code suppressions.
//
// Every suppression must have a non-empty reason, match at least one
// diagnostic, and target only diagnostics that declare themselves
// suppressible. Repeating a suppression code is rejected.
func NewReport(diagnostics []Diagnostic, suppressions ...Suppression) (Report, error) {
	type suppressionState struct {
		code   string
		reason string
		used   bool
	}
	states := make([]suppressionState, len(suppressions))
	byCode := make(map[string]int, len(suppressions))

	for index, suppression := range suppressions {
		code := strings.TrimSpace(suppression.Code)
		reason := strings.TrimSpace(suppression.Reason)
		if code == "" {
			return Report{}, fmt.Errorf("check: suppression %d requires a diagnostic code", index)
		}
		if reason == "" {
			return Report{}, fmt.Errorf("check: suppression for %s requires a reason", code)
		}
		if _, exists := byCode[code]; exists {
			return Report{}, fmt.Errorf("check: suppression for %s is repeated", code)
		}
		states[index] = suppressionState{code: code, reason: reason}
		byCode[code] = index
	}

	suppressedCount := 0
	evidenceCount := 0
	for index, diagnostic := range diagnostics {
		if err := validateReportDiagnostic(diagnostic); err != nil {
			return Report{}, fmt.Errorf("check: diagnostic %d: %w", index, err)
		}
		evidenceCount += len(diagnostic.Evidence)
		if stateIndex, exists := byCode[diagnostic.Code]; exists {
			if !diagnostic.Suppressible {
				return Report{}, fmt.Errorf("check: diagnostic %s is not suppressible", diagnostic.Code)
			}
			states[stateIndex].used = true
			suppressedCount++
		}
	}
	for _, state := range states {
		if !state.used {
			return Report{}, fmt.Errorf("check: suppression for %s does not match a diagnostic", state.code)
		}
	}

	active := make([]Diagnostic, 0, len(diagnostics)-suppressedCount)
	suppressed := make([]SuppressedDiagnostic, 0, suppressedCount)
	cloner := newDiagnosticCloner(evidenceCount)
	summary := Summary{Suppressed: suppressedCount}
	for _, diagnostic := range diagnostics {
		cloned := cloner.clone(diagnostic)
		if stateIndex, exists := byCode[diagnostic.Code]; exists {
			suppressed = append(suppressed, SuppressedDiagnostic{
				Diagnostic: cloned,
				Reason:     states[stateIndex].reason,
			})
			continue
		}
		active = append(active, cloned)
		incrementSummary(&summary, diagnostic.Severity)
	}
	return Report{
		diagnostics: active,
		suppressed:  suppressed,
		summary:     summary,
	}, nil
}

// Diagnostics returns an owned copy of active diagnostics.
func (r Report) Diagnostics() []Diagnostic {
	return cloneDiagnostics(r.diagnostics)
}

// Suppressed returns an owned copy of explicitly suppressed diagnostics.
func (r Report) Suppressed() []SuppressedDiagnostic {
	result := make([]SuppressedDiagnostic, len(r.suppressed))
	evidenceCount := 0
	for _, suppressed := range r.suppressed {
		evidenceCount += len(suppressed.Diagnostic.Evidence)
	}
	cloner := newDiagnosticCloner(evidenceCount)
	for index, suppressed := range r.suppressed {
		result[index] = SuppressedDiagnostic{
			Diagnostic: cloner.clone(suppressed.Diagnostic),
			Reason:     suppressed.Reason,
		}
	}
	return result
}

// Summary returns active severity counts and the suppressed count.
func (r Report) Summary() Summary {
	return r.summary
}

// HasErrors reports whether the default policy should fail.
func (r Report) HasErrors() bool {
	return r.summary.Errors != 0
}

// MarshalJSON emits stable non-null arrays for active and suppressed
// diagnostics.
func (r Report) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Diagnostics []Diagnostic           `json:"diagnostics"`
		Suppressed  []SuppressedDiagnostic `json:"suppressed"`
		Summary     Summary                `json:"summary"`
	}{
		Diagnostics: nonNilDiagnostics(r.diagnostics),
		Suppressed:  nonNilSuppressed(r.suppressed),
		Summary:     r.summary,
	})
}

func validateReportDiagnostic(diagnostic Diagnostic) error {
	code := strings.TrimSpace(diagnostic.Code)
	if code == "" {
		return fmt.Errorf("requires a code")
	}
	if code != diagnostic.Code {
		return fmt.Errorf("diagnostic code %q has surrounding whitespace", diagnostic.Code)
	}
	switch diagnostic.Severity {
	case SeverityError, SeverityWarning, SeverityInfo:
		return nil
	default:
		return fmt.Errorf("%s has invalid severity %q", diagnostic.Code, diagnostic.Severity)
	}
}

func incrementSummary(summary *Summary, severity Severity) {
	switch severity {
	case SeverityError:
		summary.Errors++
	case SeverityWarning:
		summary.Warnings++
	case SeverityInfo:
		summary.Info++
	}
}

func cloneDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	result := make([]Diagnostic, len(diagnostics))
	evidenceCount := 0
	for _, diagnostic := range diagnostics {
		evidenceCount += len(diagnostic.Evidence)
	}
	cloner := newDiagnosticCloner(evidenceCount)
	for index, diagnostic := range diagnostics {
		result[index] = cloner.clone(diagnostic)
	}
	return result
}

type diagnosticCloner struct {
	evidence []Evidence
	offset   int
}

func newDiagnosticCloner(evidenceCount int) diagnosticCloner {
	return diagnosticCloner{evidence: make([]Evidence, evidenceCount)}
}

func (c *diagnosticCloner) clone(diagnostic Diagnostic) Diagnostic {
	count := len(diagnostic.Evidence)
	if count == 0 {
		diagnostic.Evidence = nil
		return diagnostic
	}
	end := c.offset + count
	copy(c.evidence[c.offset:end], diagnostic.Evidence)
	diagnostic.Evidence = c.evidence[c.offset:end:end]
	c.offset = end
	return diagnostic
}

func nonNilDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	if diagnostics == nil {
		return make([]Diagnostic, 0)
	}
	return diagnostics
}

func nonNilSuppressed(suppressed []SuppressedDiagnostic) []SuppressedDiagnostic {
	if suppressed == nil {
		return make([]SuppressedDiagnostic, 0)
	}
	return suppressed
}
