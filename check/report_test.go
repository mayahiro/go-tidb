package check

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNewReportCountsActiveDiagnostics(t *testing.T) {
	t.Parallel()

	diagnostics := []Diagnostic{
		{Code: "ERR001", Severity: SeverityError, Title: "Error"},
		{Code: "WRN001", Severity: SeverityWarning, Title: "Warning", Suppressible: true},
		{Code: "INF001", Severity: SeverityInfo, Title: "Information", Suppressible: true},
	}
	report, err := NewReport(diagnostics)
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
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

func TestNewReportAppliesReasonedSuppressionByExactCode(t *testing.T) {
	t.Parallel()

	diagnostics := []Diagnostic{
		{Code: "WRN001", Severity: SeverityWarning, Title: "First", Suppressible: true},
		{Code: "WRN001", Severity: SeverityWarning, Title: "Second", Suppressible: true},
		{Code: "INF001", Severity: SeverityInfo, Title: "Active", Suppressible: true},
	}
	report, err := NewReport(diagnostics, Allow(" WRN001 ", " intentionally accepted "))
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	if got, want := report.Summary(), (Summary{Info: 1, Suppressed: 2}); got != want {
		t.Fatalf("Summary() = %#v, want %#v", got, want)
	}
	if report.HasErrors() {
		t.Fatal("HasErrors() = true, want false")
	}
	suppressed := report.Suppressed()
	if len(suppressed) != 2 {
		t.Fatalf("Suppressed() = %#v, want 2 entries", suppressed)
	}
	for _, current := range suppressed {
		if current.Diagnostic.Code != "WRN001" || current.Reason != "intentionally accepted" {
			t.Fatalf("suppressed diagnostic = %#v", current)
		}
	}
}

func TestNewReportRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	warning := Diagnostic{Code: "WRN001", Severity: SeverityWarning, Suppressible: true}
	errorDiagnostic := Diagnostic{Code: "ERR001", Severity: SeverityError}
	tests := []struct {
		name         string
		diagnostics  []Diagnostic
		suppressions []Suppression
		want         string
	}{
		{name: "missing diagnostic code", diagnostics: []Diagnostic{{Severity: SeverityWarning}}, want: "diagnostic 0: requires a code"},
		{name: "diagnostic code whitespace", diagnostics: []Diagnostic{{Code: " WRN001", Severity: SeverityWarning}}, want: "has surrounding whitespace"},
		{name: "invalid severity", diagnostics: []Diagnostic{{Code: "BAD001", Severity: "fatal"}}, want: `BAD001 has invalid severity "fatal"`},
		{name: "missing suppression code", diagnostics: []Diagnostic{warning}, suppressions: []Suppression{Allow("", "reason")}, want: "suppression 0 requires a diagnostic code"},
		{name: "missing reason", diagnostics: []Diagnostic{warning}, suppressions: []Suppression{Allow("WRN001", "  ")}, want: "suppression for WRN001 requires a reason"},
		{name: "repeated code", diagnostics: []Diagnostic{warning}, suppressions: []Suppression{Allow("WRN001", "first"), Allow("WRN001", "second")}, want: "suppression for WRN001 is repeated"},
		{name: "unused code", diagnostics: []Diagnostic{warning}, suppressions: []Suppression{Allow("OTHER001", "reason")}, want: "suppression for OTHER001 does not match a diagnostic"},
		{name: "non suppressible", diagnostics: []Diagnostic{errorDiagnostic}, suppressions: []Suppression{Allow("ERR001", "reason")}, want: "diagnostic ERR001 is not suppressible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewReport(test.diagnostics, test.suppressions...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewReport() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReportOwnsDiagnosticEvidence(t *testing.T) {
	t.Parallel()

	diagnostics := []Diagnostic{{
		Code:         "WRN001",
		Severity:     SeverityWarning,
		Evidence:     []Evidence{{Message: "original"}},
		Suppressible: true,
	}}
	report, err := NewReport(diagnostics)
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	diagnostics[0].Evidence[0].Message = "caller mutation"
	first := report.Diagnostics()
	first[0].Evidence[0].Message = "result mutation"
	second := report.Diagnostics()
	if got, want := second[0].Evidence[0].Message, "original"; got != want {
		t.Fatalf("evidence message = %q, want %q", got, want)
	}
}

func TestReportOwnsSuppressedDiagnosticEvidence(t *testing.T) {
	t.Parallel()

	diagnostics := []Diagnostic{{
		Code:         "WRN001",
		Severity:     SeverityWarning,
		Evidence:     []Evidence{{Message: "original"}},
		Suppressible: true,
	}}
	report, err := NewReport(diagnostics, Allow("WRN001", "accepted"))
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	diagnostics[0].Evidence[0].Message = "caller mutation"
	first := report.Suppressed()
	first[0].Diagnostic.Evidence[0].Message = "result mutation"
	second := report.Suppressed()
	if got, want := second[0].Diagnostic.Evidence[0].Message, "original"; got != want {
		t.Fatalf("evidence message = %q, want %q", got, want)
	}
}

func TestReportJSONUsesStableNonNullCollections(t *testing.T) {
	t.Parallel()

	report, err := NewReport(nil)
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const want = `{"diagnostics":[],"suppressed":[],"summary":{"errors":0,"warnings":0,"info":0,"suppressed":0}}`
	if string(data) != want {
		t.Fatalf("json = %s, want %s", data, want)
	}
}
