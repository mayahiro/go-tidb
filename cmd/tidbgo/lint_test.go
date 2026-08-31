package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/sourcecheck"
)

func TestApplicationLintReportsNarrowProjectionFromCurrentDirectory(t *testing.T) {
	t.Parallel()

	directory := writeLintFixture(t)
	result := runApplicationAt(t, directory, nil, "lint")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"WARNING[SRC001] Query can use a narrower projection",
		`Add Select(\"ID\") before the terminal`,
		"at: repository.go:",
		"source: files=1 model_types=1 result_queries=1 explicit_projections=0 analyzed=1 uncertain=0",
		"summary: errors=0 warnings=1 info=0 suppressed=0",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
	if got := result.Stderr(); len(got) != 0 {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestApplicationLintWritesStructuredJSON(t *testing.T) {
	t.Parallel()

	directory := writeLintFixture(t)
	result := runApplicationAt(t, directory, nil, "lint", "--json")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	var output struct {
		Statistics  sourcecheck.Statistics `json:"statistics"`
		Diagnostics []check.Diagnostic     `json:"diagnostics"`
		Suppressed  []any                  `json:"suppressed"`
		Summary     check.Summary          `json:"summary"`
	}
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, output = %q", err, result.Stdout())
	}
	if output.Statistics.Analyzed != 1 || len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != "SRC001" || output.Suppressed == nil || output.Summary.Warnings != 1 {
		t.Fatalf("JSON output = %#v", output)
	}
}

func TestApplicationLintRecordsReasonedSuppression(t *testing.T) {
	t.Parallel()

	directory := writeLintFixture(t)
	result := runApplicationAt(t, directory, nil, "lint", "--suppress", "SRC001=endpoint intentionally returns a full model")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"SUPPRESSED WARNING[SRC001] Query can use a narrower projection",
		"reason: endpoint intentionally returns a full model",
		"summary: errors=0 warnings=0 info=0 suppressed=1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationLintReadsExplicitFile(t *testing.T) {
	t.Parallel()

	directory := writeLintFixture(t)
	result := runApplicationAt(t, directory, nil, "lint", "repository.go")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	if got := string(result.Stdout()); !strings.Contains(got, "source: files=1") {
		t.Fatalf("stdout = %q, want source statistics", got)
	}
}

func TestApplicationLintRejectsInvalidSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{name: "no source", prepare: func(*testing.T, string) {}, want: "no production Go files"},
		{name: "syntax", prepare: func(t *testing.T, directory string) {
			writeLintFile(t, filepath.Join(directory, "bad.go"), "package broken\nfunc {\n")
		}, want: "parse Go source"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			test.prepare(t, directory)
			result := runApplicationAt(t, directory, nil, "lint")
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if stdout := result.Stdout(); len(stdout) != 0 {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if stderr := string(result.Stderr()); !strings.Contains(stderr, test.want) || strings.Contains(stderr, directory) {
				t.Fatalf("stderr = %q, want %q without resolved directory", stderr, test.want)
			}
		})
	}
}

func TestApplicationLintReportsMissingPathWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "lint", "missing")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `analyze Go source "missing": path does not exist`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}

func TestSourceAnalysisWritersPropagateErrors(t *testing.T) {
	t.Parallel()

	report, err := check.NewReport([]check.Diagnostic{{Code: "SRC001", Severity: check.SeverityWarning}})
	if err != nil {
		t.Fatalf("check.NewReport() error = %v", err)
	}
	want := errors.New("write failed")
	writer := failingWriter{err: want}
	if err := writeSourceAnalysisText(writer, sourcecheck.Statistics{}, report); !errors.Is(err, want) {
		t.Fatalf("writeSourceAnalysisText() error = %v, want %v", err, want)
	}
	if err := writeSourceAnalysisJSON(writer, sourcecheck.Statistics{}, report); !errors.Is(err, want) {
		t.Fatalf("writeSourceAnalysisJSON() error = %v, want %v", err, want)
	}
}

func writeLintFixture(t testing.TB) string {
	t.Helper()
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "repository.go"), `package application

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Email string }

func load() {
	users, _ := orm.Query[User]().All(ctx, db)
	_ = users[0].ID
}
`)
	return directory
}

func writeLintFile(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
}
