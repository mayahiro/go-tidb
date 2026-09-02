package diagnosticreport

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestNewCountsActiveDiagnostics(t *testing.T) {
	t.Parallel()

	diagnostics := []check.Diagnostic{
		{Code: "ERR001", Severity: check.SeverityError, Title: "Error"},
		{Code: "WRN001", Severity: check.SeverityWarning, Title: "Warning", Suppressible: true},
		{Code: "INF001", Severity: check.SeverityInfo, Title: "Information", Suppressible: true},
	}
	report, err := New(diagnostics)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got, want := report.Summary(), (Summary{Errors: 1, Warnings: 1, Info: 1}); got != want {
		t.Fatalf("Summary() = %#v, want %#v", got, want)
	}
	if !report.HasErrors() {
		t.Fatal("HasErrors() = false, want true")
	}
	if got := report.Diagnostics(); !reflect.DeepEqual(got, diagnostics) {
		t.Fatalf("Diagnostics() = %#v, want %#v", got, diagnostics)
	}
	if got := report.Suppressed(); len(got) != 0 || got == nil {
		t.Fatalf("Suppressed() = %#v, want non-nil empty slice", got)
	}
}

func TestNewAppliesReasonedSuppressionByExactCode(t *testing.T) {
	t.Parallel()

	diagnostics := []check.Diagnostic{
		{Code: "WRN001", Severity: check.SeverityWarning, Title: "First", Suppressible: true},
		{Code: "WRN001", Severity: check.SeverityWarning, Title: "Second", Suppressible: true},
		{Code: "INF001", Severity: check.SeverityInfo, Title: "Active", Suppressible: true},
	}
	report, err := New(diagnostics, Allow(" WRN001 ", " intentionally accepted "))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got, want := report.Summary(), (Summary{Info: 1, Suppressed: 2}); got != want {
		t.Fatalf("Summary() = %#v, want %#v", got, want)
	}
	for _, current := range report.Suppressed() {
		if current.Diagnostic.Code != "WRN001" || current.Reason != "intentionally accepted" {
			t.Fatalf("suppressed diagnostic = %#v", current)
		}
	}
}

func TestNewRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	warning := check.Diagnostic{Code: "WRN001", Severity: check.SeverityWarning, Suppressible: true}
	errorDiagnostic := check.Diagnostic{Code: "ERR001", Severity: check.SeverityError}
	tests := []struct {
		name         string
		diagnostics  []check.Diagnostic
		suppressions []Suppression
		want         string
	}{
		{name: "missing diagnostic code", diagnostics: []check.Diagnostic{{Severity: check.SeverityWarning}}, want: "diagnostic 0: requires a code"},
		{name: "diagnostic code whitespace", diagnostics: []check.Diagnostic{{Code: " WRN001", Severity: check.SeverityWarning}}, want: "has surrounding whitespace"},
		{name: "invalid severity", diagnostics: []check.Diagnostic{{Code: "BAD001", Severity: "fatal"}}, want: `BAD001 has invalid severity "fatal"`},
		{name: "missing suppression code", diagnostics: []check.Diagnostic{warning}, suppressions: []Suppression{Allow("", "reason")}, want: "suppression 0 requires a diagnostic code"},
		{name: "missing reason", diagnostics: []check.Diagnostic{warning}, suppressions: []Suppression{Allow("WRN001", "  ")}, want: "suppression for WRN001 requires a reason"},
		{name: "repeated code", diagnostics: []check.Diagnostic{warning}, suppressions: []Suppression{Allow("WRN001", "first"), Allow("WRN001", "second")}, want: "suppression for WRN001 is repeated"},
		{name: "unused code", diagnostics: []check.Diagnostic{warning}, suppressions: []Suppression{Allow("OTHER001", "reason")}, want: "suppression for OTHER001 does not match a diagnostic"},
		{name: "non suppressible", diagnostics: []check.Diagnostic{errorDiagnostic}, suppressions: []Suppression{Allow("ERR001", "reason")}, want: "diagnostic ERR001 is not suppressible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(test.diagnostics, test.suppressions...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReportOwnsDiagnosticEvidence(t *testing.T) {
	t.Parallel()

	diagnostics := []check.Diagnostic{{
		Code:         "WRN001",
		Severity:     check.SeverityWarning,
		Evidence:     []check.Evidence{{Message: "original"}},
		Suppressible: true,
	}}
	report, err := New(diagnostics, Allow("WRN001", "accepted"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	diagnostics[0].Evidence[0].Message = "caller mutation"
	first := report.Suppressed()
	first[0].Diagnostic.Evidence[0].Message = "result mutation"
	second := report.Suppressed()
	if got, want := second[0].Diagnostic.Evidence[0].Message, "original"; got != want {
		t.Fatalf("evidence message = %q, want %q", got, want)
	}
}
