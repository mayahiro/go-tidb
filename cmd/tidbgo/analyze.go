package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

const (
	analyzeInputID    = "runtime-input"
	analyzeJSONID     = "runtime-json"
	analyzeSuppressID = "runtime-suppress"
)

func analyzeCommand() *cli.Command {
	return cli.NewCommand("analyze").
		ID("analyze-command").
		About("Analyze a go-tidb runtime capture without a database connection").
		Option(
			cli.Flag(analyzeJSONID).
				Long("json").
				Help("Write the runtime analysis as JSON"),
		).
		Option(
			cli.ValueOption(analyzeSuppressID).
				Long("suppress").
				Repeated().
				Parser(cli.CustomParser("CODE=REASON", parseSuppression)).
				Help("Suppress one runtime diagnostic code with a required reason"),
		).
		Argument(
			cli.Positional(analyzeInputID).
				Parser(cli.StringParser()).
				Help("Runtime capture JSON Lines file, or - for standard input"),
		).
		Handle(runAnalyze)
}

func runAnalyze(context *cli.Context, invocation *cli.Invocation) (cli.Outcome, error) {
	reader, closeInput, diagnostic := openAnalyzeInput(context, invocation)
	if diagnostic != nil {
		return cli.Outcome{}, diagnostic
	}
	analysis, err := runtimecapture.AnalyzeReader(reader)
	if closeErr := closeInput(); err == nil && closeErr != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "close runtime capture input: "+closeErr.Error())
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.ArgumentTarget(analyzeInputID))
	}
	report, err := check.NewReport(analysis.Diagnostics, parsedAnalyzeSuppressions(invocation)...)
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.OptionTarget(analyzeSuppressID))
	}

	jsonOutput, _ := invocation.Flag(analyzeJSONID)
	if jsonOutput {
		err = writeRuntimeAnalysisJSON(context.Stdout(), analysis.Statistics, report)
	} else {
		err = writeRuntimeAnalysisText(context.Stdout(), analysis.Statistics, report)
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write runtime analysis: "+err.Error())
	}
	if report.HasErrors() {
		return cli.NewOutcome(exitCheckFailure), nil
	}
	return cli.Success(), nil
}

func parsedAnalyzeSuppressions(invocation *cli.Invocation) []check.Suppression {
	values := invocation.ParsedValues(analyzeSuppressID)
	result := make([]check.Suppression, 0, len(values))
	for _, value := range values {
		if suppression, ok := value.Typed().(check.Suppression); ok {
			result = append(result, suppression)
		}
	}
	return result
}

func openAnalyzeInput(context *cli.Context, invocation *cli.Invocation) (io.Reader, func() error, *cli.Diagnostic) {
	input, present := cli.ValueAs[string](invocation, analyzeInputID)
	if !present || input == "-" {
		return context.Stdin(), func() error { return nil }, nil
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(context.CurrentDirectory(), path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, cli.NewDiagnostic(cli.CodeIOError, fmt.Sprintf("open runtime capture input %q: %s", input, checkInputOpenErrorReason(err))).
			WithTarget(cli.ArgumentTarget(analyzeInputID))
	}
	return file, file.Close, nil
}

type runtimeAnalysisJSON struct {
	Statistics  runtimecapture.Statistics    `json:"statistics"`
	Diagnostics []check.Diagnostic           `json:"diagnostics"`
	Suppressed  []check.SuppressedDiagnostic `json:"suppressed"`
	Summary     check.Summary                `json:"summary"`
}

func writeRuntimeAnalysisJSON(writer io.Writer, statistics runtimecapture.Statistics, report check.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(runtimeAnalysisJSON{
		Statistics:  statistics,
		Diagnostics: report.Diagnostics(),
		Suppressed:  report.Suppressed(),
		Summary:     report.Summary(),
	})
}

func writeRuntimeAnalysisText(writer io.Writer, statistics runtimecapture.Statistics, report check.Report) error {
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
	if _, err := fmt.Fprintln(writer, runtimecapture.FormatStatistics(statistics)); err != nil {
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

func stringUpper(severity check.Severity) string {
	switch severity {
	case check.SeverityError:
		return "ERROR"
	case check.SeverityWarning:
		return "WARNING"
	case check.SeverityInfo:
		return "INFO"
	default:
		return string(severity)
	}
}
