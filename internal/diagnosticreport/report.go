// Package diagnosticreport applies the CLI's diagnostic suppression and exit policy.
package diagnosticreport

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
)

// Suppression allows all suppressible diagnostics with one exact code and
// records why the application accepts them.
type Suppression struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Allow constructs a reason-carrying diagnostic suppression.
func Allow(code, reason string) Suppression {
	return Suppression{Code: code, Reason: reason}
}

// SuppressedDiagnostic records one diagnostic removed from the active result
// together with the supplied reason.
type SuppressedDiagnostic struct {
	Diagnostic check.Diagnostic `json:"diagnostic"`
	Reason     string           `json:"reason"`
}

// Summary counts active diagnostics by severity and explicitly suppressed
// diagnostics.
type Summary struct {
	Errors     int `json:"errors"`
	Warnings   int `json:"warnings"`
	Info       int `json:"info"`
	Suppressed int `json:"suppressed"`
}

// Report is an immutable aggregation of active and explicitly suppressed
// diagnostics.
type Report struct {
	diagnostics []check.Diagnostic
	suppressed  []SuppressedDiagnostic
	summary     Summary
}

// New validates diagnostics and applies exact-code suppressions.
func New(diagnostics []check.Diagnostic, suppressions ...Suppression) (Report, error) {
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
			return Report{}, fmt.Errorf("diagnostic report: suppression %d requires a diagnostic code", index)
		}
		if reason == "" {
			return Report{}, fmt.Errorf("diagnostic report: suppression for %s requires a reason", code)
		}
		if _, exists := byCode[code]; exists {
			return Report{}, fmt.Errorf("diagnostic report: suppression for %s is repeated", code)
		}
		states[index] = suppressionState{code: code, reason: reason}
		byCode[code] = index
	}

	suppressedCount := 0
	evidenceCount := 0
	for index, diagnostic := range diagnostics {
		if err := validateDiagnostic(diagnostic); err != nil {
			return Report{}, fmt.Errorf("diagnostic report: diagnostic %d: %w", index, err)
		}
		evidenceCount += len(diagnostic.Evidence)
		if stateIndex, exists := byCode[diagnostic.Code]; exists {
			if !diagnostic.Suppressible {
				return Report{}, fmt.Errorf("diagnostic report: diagnostic %s is not suppressible", diagnostic.Code)
			}
			states[stateIndex].used = true
			suppressedCount++
		}
	}
	for _, state := range states {
		if !state.used {
			return Report{}, fmt.Errorf("diagnostic report: suppression for %s does not match a diagnostic", state.code)
		}
	}

	active := make([]check.Diagnostic, 0, len(diagnostics)-suppressedCount)
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
	return Report{diagnostics: active, suppressed: suppressed, summary: summary}, nil
}

// Diagnostics returns an owned copy of active diagnostics.
func (report Report) Diagnostics() []check.Diagnostic {
	return cloneDiagnostics(report.diagnostics)
}

// Suppressed returns an owned copy of explicitly suppressed diagnostics.
func (report Report) Suppressed() []SuppressedDiagnostic {
	result := make([]SuppressedDiagnostic, len(report.suppressed))
	evidenceCount := 0
	for _, suppressed := range report.suppressed {
		evidenceCount += len(suppressed.Diagnostic.Evidence)
	}
	cloner := newDiagnosticCloner(evidenceCount)
	for index, suppressed := range report.suppressed {
		result[index] = SuppressedDiagnostic{
			Diagnostic: cloner.clone(suppressed.Diagnostic),
			Reason:     suppressed.Reason,
		}
	}
	return result
}

// Summary returns active severity counts and the suppressed count.
func (report Report) Summary() Summary {
	return report.summary
}

// HasErrors reports whether active error diagnostics remain.
func (report Report) HasErrors() bool {
	return report.summary.Errors != 0
}

func validateDiagnostic(diagnostic check.Diagnostic) error {
	code := strings.TrimSpace(diagnostic.Code)
	if code == "" {
		return fmt.Errorf("requires a code")
	}
	if code != diagnostic.Code {
		return fmt.Errorf("diagnostic code %q has surrounding whitespace", diagnostic.Code)
	}
	switch diagnostic.Severity {
	case check.SeverityError, check.SeverityWarning, check.SeverityInfo:
		return nil
	default:
		return fmt.Errorf("%s has invalid severity %q", diagnostic.Code, diagnostic.Severity)
	}
}

func incrementSummary(summary *Summary, severity check.Severity) {
	switch severity {
	case check.SeverityError:
		summary.Errors++
	case check.SeverityWarning:
		summary.Warnings++
	case check.SeverityInfo:
		summary.Info++
	}
}

func cloneDiagnostics(diagnostics []check.Diagnostic) []check.Diagnostic {
	result := make([]check.Diagnostic, len(diagnostics))
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
	evidence []check.Evidence
	offset   int
}

func newDiagnosticCloner(evidenceCount int) diagnosticCloner {
	return diagnosticCloner{evidence: make([]check.Evidence, evidenceCount)}
}

func (cloner *diagnosticCloner) clone(diagnostic check.Diagnostic) check.Diagnostic {
	count := len(diagnostic.Evidence)
	if count == 0 {
		diagnostic.Evidence = nil
		return diagnostic
	}
	end := cloner.offset + count
	copy(cloner.evidence[cloner.offset:end], diagnostic.Evidence)
	diagnostic.Evidence = cloner.evidence[cloner.offset:end:end]
	cloner.offset = end
	return diagnostic
}
