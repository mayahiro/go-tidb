package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/diagnosticreport"
)

func parseSuppression(value string) (diagnosticreport.Suppression, error) {
	code, reason, found := strings.Cut(value, "=")
	code = strings.TrimSpace(code)
	reason = strings.TrimSpace(reason)
	if !found || code == "" || reason == "" {
		return diagnosticreport.Suppression{}, fmt.Errorf("value must use CODE=REASON with both parts present")
	}
	return diagnosticreport.Allow(code, reason), nil
}

func writeTextDiagnostic(writer io.Writer, label string, diagnostic check.Diagnostic, reason string) error {
	if _, err := fmt.Fprintf(writer, "%s[%s] %s\n", label, safeText(diagnostic.Code), safeText(diagnostic.Title)); err != nil {
		return err
	}
	if diagnostic.Message != "" {
		if _, err := fmt.Fprintf(writer, "  %s\n", safeText(diagnostic.Message)); err != nil {
			return err
		}
	}
	if location := textLocation(diagnostic.Location); location != "" {
		if _, err := fmt.Fprintf(writer, "  at: %s\n", location); err != nil {
			return err
		}
	}
	for _, evidence := range diagnostic.Evidence {
		if _, err := fmt.Fprintf(writer, "  evidence: %s", safeText(evidence.Message)); err != nil {
			return err
		}
		if location := textLocation(evidence.Location); location != "" {
			if _, err := fmt.Fprintf(writer, " at %s", location); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
	}
	if diagnostic.Suggestion != "" {
		if _, err := fmt.Fprintf(writer, "  suggestion: %s\n", safeText(diagnostic.Suggestion)); err != nil {
			return err
		}
	}
	if diagnostic.Reference != "" {
		if _, err := fmt.Fprintf(writer, "  reference: %s\n", safeText(diagnostic.Reference)); err != nil {
			return err
		}
	}
	if reason != "" {
		if _, err := fmt.Fprintf(writer, "  reason: %s\n", safeText(reason)); err != nil {
			return err
		}
	}
	return nil
}

func textLocation(location check.Location) string {
	path := safeText(location.Path)
	if path == "" {
		switch {
		case location.Line != 0 && location.Column != 0:
			return fmt.Sprintf("line %d, column %d", location.Line, location.Column)
		case location.Line != 0:
			return fmt.Sprintf("line %d", location.Line)
		default:
			return ""
		}
	}
	if location.Line == 0 {
		return path
	}
	if location.Column == 0 {
		return fmt.Sprintf("%s:%d", path, location.Line)
	}
	return fmt.Sprintf("%s:%d:%d", path, location.Line, location.Column)
}

func safeText(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return quoted[1 : len(quoted)-1]
}
