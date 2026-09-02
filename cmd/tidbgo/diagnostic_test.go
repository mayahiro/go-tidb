package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestWriteTextDiagnosticIncludesDetails(t *testing.T) {
	t.Parallel()

	diagnostic := check.Diagnostic{
		Code:       "ERR001",
		Severity:   check.SeverityError,
		Title:      "Invalid model",
		Message:    "mapping failed",
		Location:   check.Location{Path: "models/user.go", Line: 10, Column: 2},
		Evidence:   []check.Evidence{{Message: "field User.ID", Location: check.Location{Line: 11}}},
		Suggestion: "fix the mapping",
		Reference:  "https://example.test/reference",
	}
	var output strings.Builder
	if err := writeTextDiagnostic(&output, "ERROR", diagnostic, "accepted for this test"); err != nil {
		t.Fatalf("writeTextDiagnostic() error = %v", err)
	}
	const want = "ERROR[ERR001] Invalid model\n" +
		"  mapping failed\n" +
		"  at: models/user.go:10:2\n" +
		"  evidence: field User.ID at line 11\n" +
		"  suggestion: fix the mapping\n" +
		"  reference: https://example.test/reference\n" +
		"  reason: accepted for this test\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWriteTextDiagnosticEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	diagnostic := check.Diagnostic{Code: "WRN001", Title: "line\nbreak\x1b[31m"}
	var output strings.Builder
	if err := writeTextDiagnostic(&output, "WARNING", diagnostic, ""); err != nil {
		t.Fatalf("writeTextDiagnostic() error = %v", err)
	}
	if got := output.String(); strings.Contains(got, "\x1b") || !strings.Contains(got, `line\nbreak\x1b[31m`) {
		t.Fatalf("output = %q, want escaped control characters", got)
	}
}

func TestWriteTextDiagnosticPropagatesWriterError(t *testing.T) {
	t.Parallel()

	want := errors.New("write failed")
	err := writeTextDiagnostic(failingWriter{err: want}, "INFO", check.Diagnostic{Code: "INF001"}, "")
	if !errors.Is(err, want) {
		t.Fatalf("writeTextDiagnostic() error = %v, want %v", err, want)
	}
}
