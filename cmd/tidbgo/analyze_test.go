package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

func TestApplicationAnalyzeReportsRuntimeNPlusOneFromStandardInput(t *testing.T) {
	t.Parallel()

	input := runtimeCaptureInput(t,
		runtimeCommandRecord(1, "q1:users"),
		runtimeCommandRecord(2, "q1:users"),
	)
	result := runApplicationWithInput(t, input, "analyze")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"WARNING[RUN002] Repeated SELECT may be an N+1 query",
		"One runtime scope executed the same User SELECT 2 times",
		"runtime: captures=1 scopes=1 statements=2 fingerprints=1 batch_groups=0 split_batches=0",
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

func TestApplicationAnalyzeWritesStructuredJSON(t *testing.T) {
	t.Parallel()

	input := runtimeCaptureInput(t, runtimeCommandRecord(1, "q1:users"))
	result := runApplicationWithInput(t, input, "analyze", "--json")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	var output struct {
		Statistics  runtimecapture.Statistics `json:"statistics"`
		Diagnostics []any                     `json:"diagnostics"`
		Suppressed  []any                     `json:"suppressed"`
		Summary     struct {
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, output = %q", err, result.Stdout())
	}
	if output.Statistics.Statements != 1 || output.Statistics.Scopes != 1 || output.Diagnostics == nil || output.Suppressed == nil || output.Summary.Warnings != 0 {
		t.Fatalf("JSON output = %#v", output)
	}
}

func TestApplicationAnalyzeReportsServerRUCollectionFailureAndSeparatedCost(t *testing.T) {
	t.Parallel()

	record := runtimeCommandRecord(1, "q1:users")
	record.ServerRU = &runtimecapture.ServerRU{
		DiagnosticDurationNS: 2_000,
		AuxiliaryStatements:  1,
		Error:                "read ServerRU failed",
	}
	result := runApplicationWithInput(t, runtimeCaptureInput(t, record), "analyze")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"WARNING[RUN003] Automatic ServerRU collection failed",
		"auxiliary_statements=1",
		"diagnostic_duration=2µs",
		"server_ru_samples=0",
		"server_ru_errors=1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeAppliesCapturedQueryDiagnostics(t *testing.T) {
	t.Parallel()

	shape := queryshape.Query{
		Model:  "Video",
		Limit:  queryshape.Bound{Set: true, Positive: true},
		Offset: queryshape.Bound{Set: true, Positive: true},
		Predicates: []queryshape.Predicate{{
			Operator: queryshape.PredicateContains,
			Field:    "Title",
		}},
		Compiler: queryshape.CompilerDecision{
			Rewrite:  queryshape.CompilerRewriteRelationTopNFallback,
			Relation: "Genres",
			Reason:   "root order is not the primary key",
		},
	}
	record := runtimeCommandRecord(1, shape.Fingerprint())
	record.Query = &shape
	result := runApplicationWithInput(t, runtimeCaptureInput(t, record), "analyze")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"WARNING[QRY002] OFFSET pagination cost grows with the offset",
		"WARNING[QRY003] Pagination has no deterministic order",
		"WARNING[QRY004] LIKE predicate starts with a wildcard",
		"WARNING[QRY005] Relation-filter TopN uses the EXISTS fallback",
		"query_shape_statements=1 schema_checked_statements=0",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeChecksCapturedIndexesAgainstSchemaSnapshot(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	const schemaSQL = `
CREATE TABLE videos (
    id BIGINT NOT NULL,
    tenant_id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`
	if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(schemaSQL), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	shape := queryshape.Query{
		Model: "Video",
		Table: "videos",
		Order: []queryshape.OrderTerm{{Column: "id", Direction: queryshape.OrderDescending}},
		Limit: queryshape.Bound{Set: true, Positive: true},
		IndexAccesses: []queryshape.IndexAccess{{
			Kind:            queryshape.IndexAccessRootOrderedLimit,
			Table:           "videos",
			EqualityColumns: []string{"tenant_id"},
			OrderColumns:    []string{"id"},
		}},
	}
	record := runtimeCommandRecord(1, shape.Fingerprint())
	record.Query = &shape
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, record),
		"analyze",
		"--schema",
		"schema.sql",
	)
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"WARNING[QRY007] Ordered limited access has no matching index prefix",
		"Candidate index prefix: videos(tenant_id, id)",
		"query_shape_statements=1 schema_checked_statements=1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeFailsWhenSchemaCannotDescribeCapturedAccess(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	const schemaSQL = `CREATE TABLE other_videos (id BIGINT NOT NULL, PRIMARY KEY (id));`
	if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(schemaSQL), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	shape := queryshape.Query{
		Model: "Video",
		Order: []queryshape.OrderTerm{{Column: "id", Direction: queryshape.OrderDescending}},
		Limit: queryshape.Bound{Set: true, Positive: true},
		IndexAccesses: []queryshape.IndexAccess{{
			Kind:         queryshape.IndexAccessRootOrderedLimit,
			Table:        "videos",
			OrderColumns: []string{"id"},
		}},
	}
	record := runtimeCommandRecord(1, shape.Fingerprint())
	record.Query = &shape
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, record),
		"analyze",
		"--schema",
		"schema.sql",
	)
	if got, want := result.Status(), exitCheckFailure; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	if !strings.Contains(stdout, "ERROR[QRY006] Query index check is unavailable") ||
		!strings.Contains(stdout, `query access table \"videos\" is absent`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestApplicationAnalyzeRejectsInvalidSchemaSnapshot(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte("CREATE TABLE videos ("), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := runApplicationAt(t, directory, nil, "analyze", "--schema", "schema.sql")
	if got, want := result.Status(), exitUsage; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if stderr := string(result.Stderr()); !strings.Contains(stderr, "parse schema snapshot") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestApplicationAnalyzeReportsMissingSchemaWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "analyze", "--schema", "missing.sql")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `read schema snapshot "missing.sql": file does not exist`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}

func TestApplicationAnalyzeRecordsReasonedSuppression(t *testing.T) {
	t.Parallel()

	input := runtimeCaptureInput(t,
		runtimeCommandRecord(1, "q1:users"),
		runtimeCommandRecord(2, "q1:users"),
	)
	result := runApplicationWithInput(t, input, "analyze", "--suppress", "RUN002=intentional polling")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"SUPPRESSED WARNING[RUN002] Repeated SELECT may be an N+1 query",
		"reason: intentional polling",
		"summary: errors=0 warnings=0 info=0 suppressed=1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeReadsExplicitFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := runtimeCaptureInput(t, runtimeCommandRecord(1, "q1:users"))
	if err := os.WriteFile(filepath.Join(directory, "runtime.jsonl"), input, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := runApplicationAt(t, directory, nil, "analyze", "runtime.jsonl")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
}

func TestApplicationAnalyzeRejectsInvalidArtifact(t *testing.T) {
	t.Parallel()

	result := runApplicationWithInput(t, []byte(`{"version":1}`), "analyze")
	if got, want := result.Status(), exitUsage; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if stdout := result.Stdout(); len(stdout) != 0 {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr := string(result.Stderr()); !strings.Contains(stderr, "runtime capture version is 1, want 2") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestApplicationAnalyzeReportsMissingFileWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "analyze", "missing.jsonl")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `open runtime capture input "missing.jsonl"`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}

func TestRuntimeAnalysisWritersPropagateErrors(t *testing.T) {
	t.Parallel()

	analysis := runtimecapture.Analyze(nil)
	report, err := check.NewReport(analysis.Diagnostics)
	if err != nil {
		t.Fatalf("check.NewReport() error = %v", err)
	}
	want := errors.New("write failed")
	writer := failingWriter{err: want}
	if err := writeRuntimeAnalysisText(writer, analysis.Statistics, report); !errors.Is(err, want) {
		t.Fatalf("writeRuntimeAnalysisText() error = %v, want %v", err, want)
	}
	if err := writeRuntimeAnalysisJSON(writer, analysis.Statistics, report); !errors.Is(err, want) {
		t.Fatalf("writeRuntimeAnalysisJSON() error = %v, want %v", err, want)
	}
}

func runtimeCaptureInput(t testing.TB, records ...runtimecapture.Record) []byte {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}
	return output.Bytes()
}

func runtimeCommandRecord(sequence uint64, fingerprint string) runtimecapture.Record {
	return runtimecapture.Record{
		Version:       runtimecapture.Version,
		CaptureID:     "capture",
		ScopeID:       1,
		Sequence:      sequence,
		Operation:     "SELECT",
		Source:        runtimecapture.SourceTypedSelect,
		Terminal:      "all",
		Model:         "User",
		Fingerprint:   fingerprint,
		SQL:           "SELECT `id` FROM `users` WHERE `id` = ?",
		ArgumentCount: 1,
		DurationNS:    1000,
	}
}
