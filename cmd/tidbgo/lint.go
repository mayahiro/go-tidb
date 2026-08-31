package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/sourcecheck"
)

const (
	lintInputID    = "path"
	lintJSONID     = "lint-json"
	lintSuppressID = "lint-suppress"
)

func lintCommand() *cli.Command {
	return cli.NewCommand("lint").
		ID("lint-command").
		About("Analyze go-tidb queries in Go source without a database connection").
		Option(
			cli.Flag(lintJSONID).
				Long("json").
				Help("Write the source analysis as JSON"),
		).
		Option(
			cli.ValueOption(lintSuppressID).
				Long("suppress").
				Repeated().
				Parser(cli.CustomParser("CODE=REASON", parseSuppression)).
				Help("Suppress one source diagnostic code with a required reason"),
		).
		Argument(
			cli.Positional(lintInputID).
				Parser(cli.StringParser()).
				Help("Go source file or directory, defaulting to the current directory"),
		).
		Handle(runLint)
}

func runLint(context *cli.Context, invocation *cli.Invocation) (cli.Outcome, error) {
	input, present := cli.ValueAs[string](invocation, lintInputID)
	if !present {
		input = "."
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(context.CurrentDirectory(), path)
	}

	analysis, err := sourcecheck.AnalyzePath(path)
	if err != nil {
		return cli.Outcome{}, lintInputDiagnostic(input, err)
	}
	report, err := check.NewReport(analysis.Diagnostics, parsedLintSuppressions(invocation)...)
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.OptionTarget(lintSuppressID))
	}

	jsonOutput, _ := invocation.Flag(lintJSONID)
	if jsonOutput {
		err = writeSourceAnalysisJSON(context.Stdout(), analysis.Statistics, report)
	} else {
		err = writeSourceAnalysisText(context.Stdout(), analysis.Statistics, report)
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write source analysis: "+err.Error())
	}
	if report.HasErrors() {
		return cli.NewOutcome(exitCheckFailure), nil
	}
	return cli.Success(), nil
}

func lintInputDiagnostic(input string, err error) *cli.Diagnostic {
	if errors.Is(err, sourcecheck.ErrNoSourceFiles) || errors.Is(err, sourcecheck.ErrInvalidSource) {
		return cli.NewDiagnostic(cli.CodeInvalidValue, fmt.Sprintf("analyze Go source %q: %s", input, err)).
			WithTarget(cli.ArgumentTarget(lintInputID))
	}
	reason := "inspection failed"
	switch {
	case errors.Is(err, os.ErrNotExist):
		reason = "path does not exist"
	case errors.Is(err, os.ErrPermission):
		reason = "permission denied"
	}
	return cli.NewDiagnostic(cli.CodeIOError, fmt.Sprintf("analyze Go source %q: %s", input, reason)).
		WithTarget(cli.ArgumentTarget(lintInputID))
}

func parsedLintSuppressions(invocation *cli.Invocation) []check.Suppression {
	values := invocation.ParsedValues(lintSuppressID)
	result := make([]check.Suppression, 0, len(values))
	for _, value := range values {
		if suppression, ok := value.Typed().(check.Suppression); ok {
			result = append(result, suppression)
		}
	}
	return result
}

type sourceAnalysisJSON struct {
	Statistics  sourcecheck.Statistics       `json:"statistics"`
	Diagnostics []check.Diagnostic           `json:"diagnostics"`
	Suppressed  []check.SuppressedDiagnostic `json:"suppressed"`
	Summary     check.Summary                `json:"summary"`
}

func writeSourceAnalysisJSON(writer io.Writer, statistics sourcecheck.Statistics, report check.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(sourceAnalysisJSON{
		Statistics:  statistics,
		Diagnostics: report.Diagnostics(),
		Suppressed:  report.Suppressed(),
		Summary:     report.Summary(),
	})
}

func writeSourceAnalysisText(writer io.Writer, statistics sourcecheck.Statistics, report check.Report) error {
	for _, diagnostic := range report.Diagnostics() {
		if err := writeTextDiagnostic(writer, stringUpper(diagnostic.Severity), diagnostic, ""); err != nil {
			return err
		}
	}
	for _, suppressed := range report.Suppressed() {
		label := "SUPPRESSED " + stringUpper(suppressed.Diagnostic.Severity)
		if err := writeTextDiagnostic(writer, label, suppressed.Diagnostic, suppressed.Reason); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(writer, sourcecheck.FormatStatistics(statistics)); err != nil {
		return err
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
