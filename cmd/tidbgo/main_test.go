package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"
	"github.com/mayahiro/nagicli-go/clitest"

	"github.com/mayahiro/go-tidb/check"
)

func TestApplicationValid(t *testing.T) {
	t.Parallel()

	if err := application("dev").Validate(); err != nil {
		t.Fatalf("application().Validate() error = %v", err)
	}
}

func TestApplicationVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
	}{
		{name: "command", arguments: []string{"version"}},
		{name: "long option", arguments: []string{"--version"}},
		{name: "short option", arguments: []string{"-V"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, tt.arguments...)
			if got, want := result.Status(), cli.StatusSuccess; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got, want := string(result.Stdout()), "tidbgo dev\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if got := result.Stderr(); len(got) != 0 {
				t.Fatalf("stderr = %q, want empty", got)
			}
		})
	}
}

func TestApplicationHelp(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		result := runApplication(t, arguments...)
		if got, want := result.Status(), cli.StatusSuccess; got != want {
			t.Fatalf("arguments %q: status = %d, want %d", arguments, got, want)
		}
		stdout := string(result.Stdout())
		for _, want := range []string{
			"Usage:\n  tidbgo [OPTIONS] <COMMAND>",
			"check",
			"Additional commands:\n  version",
			"Options:",
			"-V, --version",
		} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("arguments %q: stdout = %q, want substring %q", arguments, stdout, want)
			}
		}
		if got := result.Stderr(); len(got) != 0 {
			t.Fatalf("arguments %q: stderr = %q, want empty", arguments, got)
		}
	}
}

func TestApplicationUsageError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
		code      cli.DiagnosticCode
	}{
		{name: "no command", code: cli.CodeMissingSubcommand},
		{name: "unknown command", arguments: []string{"unknown"}, code: cli.CodeUnknownCommand},
		{name: "removed generate command", arguments: []string{"generate"}, code: cli.CodeUnknownCommand},
		{name: "extra version argument", arguments: []string{"version", "extra"}, code: cli.CodeUnknownCommand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, tt.arguments...)
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got := result.Stdout(); len(got) != 0 {
				t.Fatalf("stdout = %q, want empty", got)
			}
			stderr := string(result.Stderr())
			if want := "error[" + string(tt.code) + "]"; !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, want)
			}
			if want := "usage: tidbgo [OPTIONS] <COMMAND>"; !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, want)
			}
		})
	}
}

func TestExitStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status cli.ExitStatus
		want   cli.ExitStatus
	}{
		{name: "success", status: cli.StatusSuccess, want: 0},
		{name: "check failure", status: exitCheckFailure, want: 1},
		{name: "usage", status: exitUsage, want: 2},
		{name: "internal error", status: exitInternalError, want: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.status != tt.want {
				t.Fatalf("status = %d, want %d", tt.status, tt.want)
			}
		})
	}

	policy := runtimePolicy()
	for _, code := range []cli.DiagnosticCode{
		cli.CodeInvalidSpecification,
		cli.CodeHandlerError,
		cli.CodeIOError,
	} {
		diagnostic := cli.NewDiagnostic(code, "test failure")
		if got := policy.StatusForDiagnostic(diagnostic); got != exitInternalError {
			t.Fatalf("diagnostic %q status = %d, want %d", code, got, exitInternalError)
		}
	}
}

func TestApplicationCheckReportsDiagnosticsFromStandardInput(t *testing.T) {
	t.Parallel()

	input := `[
  {"code":"WRN001","severity":"warning","title":"Potential issue","message":"review this","suppressible":true},
  {"code":"ERR001","severity":"error","title":"Invalid mapping","message":"fix this","suppressible":false}
]`
	result := runApplicationWithInput(t, []byte(input), "check")
	if got, want := result.Status(), exitCheckFailure; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	const want = "WARNING[WRN001] Potential issue\n" +
		"  review this\n" +
		"ERROR[ERR001] Invalid mapping\n" +
		"  fix this\n" +
		"summary: errors=1 warnings=1 info=0 suppressed=0\n"
	if got := string(result.Stdout()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := result.Stderr(); len(got) != 0 {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestApplicationCheckRecordsReasonedSuppression(t *testing.T) {
	t.Parallel()

	input := `[{"code":"WRN001","severity":"warning","title":"Expected warning","suppressible":true}]`
	result := runApplicationWithInput(t, []byte(input), "check", "--suppress", "WRN001=read-only model")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	const want = "SUPPRESSED WARNING[WRN001] Expected warning\n" +
		"  reason: read-only model\n" +
		"summary: errors=0 warnings=0 info=0 suppressed=1\n"
	if got := string(result.Stdout()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestApplicationCheckWritesJSONReport(t *testing.T) {
	t.Parallel()

	input := `[{"code":"INF001","severity":"info","title":"Information","suppressible":true}]`
	result := runApplicationWithInput(t, []byte(input), "check", "--json")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	const want = `{"diagnostics":[{"code":"INF001","severity":"info","title":"Information","message":"","suppressible":true}],"suppressed":[],"summary":{"errors":0,"warnings":0,"info":1,"suppressed":0}}` + "\n"
	if got := string(result.Stdout()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestApplicationCheckReadsExplicitFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "diagnostics.json"), []byte("[]"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := runApplicationAt(t, directory, nil, "check", "diagnostics.json")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	if got, want := string(result.Stdout()), "summary: errors=0 warnings=0 info=0 suppressed=0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestApplicationCheckRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		arguments []string
		want      string
	}{
		{name: "malformed JSON", input: `[`, arguments: []string{"check"}, want: "decode diagnostic input"},
		{name: "null instead of array", input: `null`, arguments: []string{"check"}, want: "must be a JSON array"},
		{name: "trailing JSON value", input: `[] {}`, arguments: []string{"check"}, want: "more than one JSON value"},
		{name: "unknown JSON field", input: `[{"code":"WRN001","severity":"warning","title":"Warning","suppressible":true,"unknown":true}]`, arguments: []string{"check"}, want: "unknown field"},
		{name: "invalid suppression syntax", input: `[]`, arguments: []string{"check", "--suppress", "WRN001"}, want: "CODE=REASON"},
		{name: "unused suppression", input: `[]`, arguments: []string{"check", "--suppress", "WRN001=reason"}, want: "does not match a diagnostic"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := runApplicationWithInput(t, []byte(test.input), test.arguments...)
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got := result.Stdout(); len(got) != 0 {
				t.Fatalf("stdout = %q, want empty", got)
			}
			stderr := string(result.Stderr())
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, test.want)
			}
		})
	}
}

func TestApplicationCheckReportsMissingFileWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "check", "missing.json")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `open diagnostic input "missing.json"`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}

func TestWriteTextReportIncludesDiagnosticDetails(t *testing.T) {
	t.Parallel()

	report, err := check.NewReport([]check.Diagnostic{{
		Code:       "ERR001",
		Severity:   check.SeverityError,
		Title:      "Invalid model",
		Message:    "mapping failed",
		Location:   check.Location{Path: "models/user.go", Line: 10, Column: 2},
		Evidence:   []check.Evidence{{Message: "field User.ID", Location: check.Location{Line: 11}}},
		Suggestion: "fix the mapping",
		Reference:  "https://example.test/reference",
	}})
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	var output strings.Builder
	if err := writeTextReport(&output, report); err != nil {
		t.Fatalf("writeTextReport() error = %v", err)
	}
	const want = "ERROR[ERR001] Invalid model\n" +
		"  mapping failed\n" +
		"  at: models/user.go:10:2\n" +
		"  evidence: field User.ID at line 11\n" +
		"  suggestion: fix the mapping\n" +
		"  reference: https://example.test/reference\n" +
		"summary: errors=1 warnings=0 info=0 suppressed=0\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReportWritersPropagateErrors(t *testing.T) {
	t.Parallel()

	report, err := check.NewReport([]check.Diagnostic{{Code: "INF001", Severity: check.SeverityInfo}})
	if err != nil {
		t.Fatalf("NewReport() error = %v", err)
	}
	want := errors.New("write failed")
	writer := failingWriter{err: want}
	if err := writeTextReport(writer, report); !errors.Is(err, want) {
		t.Fatalf("writeTextReport() error = %v, want %v", err, want)
	}
	if err := writeJSONReport(writer, report); !errors.Is(err, want) {
		t.Fatalf("writeJSONReport() error = %v, want %v", err, want)
	}
}

func TestApplicationCheckEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	input := `[{"code":"WRN001","severity":"warning","title":"line\nbreak\u001b[31m","suppressible":true}]`
	result := runApplicationWithInput(t, []byte(input), "check")
	stdout := string(result.Stdout())
	if strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, `line\nbreak\x1b[31m`) {
		t.Fatalf("stdout = %q, want escaped control characters", stdout)
	}
}

func runApplication(t *testing.T, arguments ...string) clitest.Result {
	t.Helper()
	return runApplicationAt(t, "/", nil, arguments...)
}

func runApplicationWithInput(t *testing.T, input []byte, arguments ...string) clitest.Result {
	t.Helper()
	return runApplicationAt(t, "/", input, arguments...)
}

func runApplicationAt(t *testing.T, directory string, input []byte, arguments ...string) clitest.Result {
	t.Helper()

	driver := clitest.New(application("dev")).
		Arguments(normalizeArguments(arguments)...).
		Policy(runtimePolicy()).
		CurrentDirectory(directory)
	if input != nil {
		driver.Stdin(input)
	}
	result, err := driver.Run()
	if err != nil {
		t.Fatalf("run application: %v", err)
	}
	return result
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
