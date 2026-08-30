package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
)

const (
	checkInputID    = "input"
	checkJSONID     = "json"
	checkSuppressID = "suppress"
)

func checkCommand() *cli.Command {
	return cli.NewCommand("check").
		ID("check-command").
		About("Report offline go-tidb diagnostics").
		Option(
			cli.Flag(checkJSONID).
				Long("json").
				Help("Write the complete report as JSON"),
		).
		Option(
			cli.ValueOption(checkSuppressID).
				Long("suppress").
				Repeated().
				Parser(cli.CustomParser("CODE=REASON", parseSuppression)).
				Help("Suppress one diagnostic code with a required reason"),
		).
		Argument(
			cli.Positional(checkInputID).
				Parser(cli.StringParser()).
				Help("JSON diagnostic array file, or - for standard input"),
		).
		Handle(runCheck)
}

func runCheck(context *cli.Context, invocation *cli.Invocation) (cli.Outcome, error) {
	reader, closeInput, diagnostic := openCheckInput(context, invocation)
	if diagnostic != nil {
		return cli.Outcome{}, diagnostic
	}
	diagnostics, err := decodeDiagnostics(reader)
	if closeErr := closeInput(); err == nil && closeErr != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "close diagnostic input: "+closeErr.Error())
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "decode diagnostic input: "+err.Error()).
			WithTarget(cli.ArgumentTarget(checkInputID))
	}

	suppressions := parsedSuppressions(invocation)
	report, err := check.NewReport(diagnostics, suppressions...)
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.OptionTarget(checkSuppressID))
	}

	jsonOutput, _ := invocation.Flag(checkJSONID)
	if jsonOutput {
		err = writeJSONReport(context.Stdout(), report)
	} else {
		err = writeTextReport(context.Stdout(), report)
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write diagnostic report: "+err.Error())
	}
	if report.HasErrors() {
		return cli.NewOutcome(exitCheckFailure), nil
	}
	return cli.Success(), nil
}

func parseSuppression(value string) (check.Suppression, error) {
	code, reason, found := strings.Cut(value, "=")
	code = strings.TrimSpace(code)
	reason = strings.TrimSpace(reason)
	if !found || code == "" || reason == "" {
		return check.Suppression{}, fmt.Errorf("value must use CODE=REASON with both parts present")
	}
	return check.Allow(code, reason), nil
}

func parsedSuppressions(invocation *cli.Invocation) []check.Suppression {
	values := invocation.ParsedValues(checkSuppressID)
	result := make([]check.Suppression, 0, len(values))
	for _, value := range values {
		if suppression, ok := value.Typed().(check.Suppression); ok {
			result = append(result, suppression)
		}
	}
	return result
}

func openCheckInput(context *cli.Context, invocation *cli.Invocation) (io.Reader, func() error, *cli.Diagnostic) {
	input, present := cli.ValueAs[string](invocation, checkInputID)
	if !present || input == "-" {
		return context.Stdin(), func() error { return nil }, nil
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(context.CurrentDirectory(), path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, cli.NewDiagnostic(cli.CodeIOError, fmt.Sprintf("open diagnostic input %q: %s", input, checkInputOpenErrorReason(err))).
			WithTarget(cli.ArgumentTarget(checkInputID))
	}
	return file, file.Close, nil
}

func checkInputOpenErrorReason(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "file does not exist"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	default:
		return "open failed"
	}
}

func decodeDiagnostics(reader io.Reader) ([]check.Diagnostic, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	diagnostics := make([]check.Diagnostic, 0)
	if err := decoder.Decode(&diagnostics); err != nil {
		return nil, err
	}
	if diagnostics == nil {
		return nil, fmt.Errorf("input must be a JSON array")
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("input contains more than one JSON value")
		}
		return nil, err
	}
	return diagnostics, nil
}

func writeJSONReport(writer io.Writer, report check.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(report)
}

func writeTextReport(writer io.Writer, report check.Report) error {
	for _, diagnostic := range report.Diagnostics() {
		if err := writeTextDiagnostic(writer, strings.ToUpper(string(diagnostic.Severity)), diagnostic, ""); err != nil {
			return err
		}
	}
	for _, suppressed := range report.Suppressed() {
		label := "SUPPRESSED " + strings.ToUpper(string(suppressed.Diagnostic.Severity))
		if err := writeTextDiagnostic(writer, label, suppressed.Diagnostic, suppressed.Reason); err != nil {
			return err
		}
	}
	summary := report.Summary()
	_, err := fmt.Fprintf(
		writer,
		"summary: errors=%d warnings=%d info=%d suppressed=%d\n",
		summary.Errors,
		summary.Warnings,
		summary.Info,
		summary.Suppressed,
	)
	return err
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
