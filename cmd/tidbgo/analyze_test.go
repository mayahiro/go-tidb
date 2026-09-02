package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/internal/diagnosticreport"
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

	record := runtimeCommandRecord(1, "q1:users")
	record.ServerRU = &runtimecapture.ServerRU{Known: true, Value: 1.5, AuxiliaryStatements: 1}
	input := runtimeCaptureInput(t, record)
	result := runApplicationWithInput(t, input, "analyze", "--json")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	var output struct {
		Statistics            runtimecapture.Statistics            `json:"statistics"`
		ServerRUByFingerprint []runtimecapture.FingerprintServerRU `json:"server_ru_by_fingerprint"`
		ServerRUComparison    *runtimecapture.ServerRUComparison   `json:"server_ru_comparison"`
		Diagnostics           []any                                `json:"diagnostics"`
		Suppressed            []any                                `json:"suppressed"`
		Summary               struct {
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, output = %q", err, result.Stdout())
	}
	if output.Statistics.Statements != 1 || output.Statistics.Scopes != 1 || output.Diagnostics == nil || output.Suppressed == nil || output.Summary.Warnings != 0 {
		t.Fatalf("JSON output = %#v", output)
	}
	wantServerRU := []runtimecapture.FingerprintServerRU{{
		Fingerprint: "q1:users",
		Count:       1,
		Samples:     1,
		Total:       1.5,
		Mean:        1.5,
		Minimum:     1.5,
		Maximum:     1.5,
	}}
	if !reflect.DeepEqual(output.ServerRUByFingerprint, wantServerRU) {
		t.Fatalf("server_ru_by_fingerprint = %#v, want %#v", output.ServerRUByFingerprint, wantServerRU)
	}
	if output.ServerRUComparison != nil {
		t.Fatalf("server_ru_comparison = %#v, want omitted", output.ServerRUComparison)
	}
}

func TestApplicationAnalyzeComparesServerRUBaseline(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeAnalyzeServerRUBaseline(t, directory, "baseline.json", "q1:users", 1, 1.1)
	records := runtimeServerRURecords("q1:users", 1.1, 1.2, 1.3, 1.2, 1.1)
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, records...),
		"analyze",
		"--baseline",
		"baseline.json",
	)
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"server_ru_comparison: fingerprint=q1:users status=pass baseline_count=5 baseline_samples=5 baseline_mean=1 baseline_max=1.1 current_count=5 current_samples=5 current_errors=0 current_mean=1.18 limit=1.3",
		"server_ru_comparison_summary: fingerprints=1 passed=1 regressions=0 unavailable=0 minimum_samples=5 maximum_mean_ratio=1.3",
		"summary: errors=0 warnings=0 info=0 suppressed=0",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeFailsOnServerRURegression(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeAnalyzeServerRUBaseline(t, directory, "baseline.json", "q1:users", 1, 1.1)
	records := runtimeServerRURecords("q1:users", 1.4, 1.4, 1.4, 1.4, 1.4)
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, records...),
		"analyze",
		"--baseline",
		"baseline.json",
	)
	if got, want := result.Status(), exitDiagnosticFailure; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"ERROR[RU001] ServerRU mean regressed from the baseline",
		"current mean 1.4 RU exceeds limit 1.3 RU",
		"status=regression",
		"summary: errors=1 warnings=0 info=0 suppressed=0",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeWritesStructuredServerRUComparison(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeAnalyzeServerRUBaseline(t, directory, "baseline.json", "q1:users", 1, 1.1)
	records := runtimeServerRURecords("q1:users", 1, 1, 1, 1, 1)
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, records...),
		"analyze",
		"--baseline",
		"baseline.json",
		"--json",
	)
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	var output struct {
		ServerRUComparison *runtimecapture.ServerRUComparison `json:"server_ru_comparison"`
	}
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, output = %q", err, result.Stdout())
	}
	if output.ServerRUComparison == nil ||
		output.ServerRUComparison.Summary.Passed != 1 ||
		len(output.ServerRUComparison.Entries) != 1 ||
		output.ServerRUComparison.Entries[0].Status != runtimecapture.ServerRUComparisonPass {
		t.Fatalf("server_ru_comparison = %#v", output.ServerRUComparison)
	}
}

func TestApplicationAnalyzeFailsWhenServerRUComparisonCoverageChanges(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeAnalyzeServerRUBaseline(t, directory, "baseline.json", "q1:old", 1, 1.1)
	records := runtimeServerRURecords("q1:new", 1, 1, 1, 1, 1)
	result := runApplicationAt(
		t,
		directory,
		runtimeCaptureInput(t, records...),
		"analyze",
		"--baseline",
		"baseline.json",
	)
	if got, want := result.Status(), exitDiagnosticFailure; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	stdout := string(result.Stdout())
	for _, want := range []string{
		"ERROR[RU002] ServerRU baseline comparison is incomplete",
		"Fingerprint q1:new: current measurement has no baseline entry",
		"Fingerprint q1:old: baseline entry has no current measurement",
		"fingerprints=2 passed=0 regressions=0 unavailable=2",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestApplicationAnalyzeRejectsInvalidServerRUBaseline(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "baseline.json"), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := runApplicationAt(t, directory, nil, "analyze", "--baseline", "baseline.json")
	if got, want := result.Status(), exitUsage; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if stderr := string(result.Stderr()); !strings.Contains(stderr, "ServerRU baseline version is 2, want 1") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestApplicationAnalyzeReportsMissingServerRUBaselineWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "analyze", "--baseline", "missing.json")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `open ServerRU baseline "missing.json": file does not exist`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}

func TestApplicationAnalyzeRequiresServerRUBaselineFile(t *testing.T) {
	t.Parallel()

	result := runApplicationWithInput(t, nil, "analyze", "--baseline", "-")
	if got, want := result.Status(), exitUsage; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if stderr := string(result.Stderr()); !strings.Contains(stderr, "ServerRU baseline must be a file path") {
		t.Fatalf("stderr = %q", stderr)
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
		"server_ru_fingerprint: fingerprint=q1:users count=1 samples=0 errors=1 total=0 mean=0 min=0 max=0",
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
	if got, want := result.Status(), exitDiagnosticFailure; got != want {
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

	result := runApplicationWithInput(t, []byte(`{"version":2}`), "analyze")
	if got, want := result.Status(), exitUsage; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if stdout := result.Stdout(); len(stdout) != 0 {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr := string(result.Stderr()); !strings.Contains(stderr, "runtime capture version is 2, want 1") {
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

	analysis, err := runtimecapture.AnalyzeReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("AnalyzeReader() error = %v", err)
	}
	report, err := diagnosticreport.New(analysis.Diagnostics)
	if err != nil {
		t.Fatalf("diagnosticreport.New() error = %v", err)
	}
	want := errors.New("write failed")
	writer := failingWriter{err: want}
	if err := writeRuntimeAnalysisText(writer, analysis, nil, report); !errors.Is(err, want) {
		t.Fatalf("writeRuntimeAnalysisText() error = %v, want %v", err, want)
	}
	if err := writeRuntimeAnalysisJSON(writer, analysis, nil, report); !errors.Is(err, want) {
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

func runtimeServerRURecords(fingerprint string, values ...float64) []runtimecapture.Record {
	records := make([]runtimecapture.Record, len(values))
	for index, value := range values {
		records[index] = runtimeCommandRecord(uint64(index+1), fingerprint)
		records[index].ScopeID = uint64(index + 1)
		records[index].ServerRU = &runtimecapture.ServerRU{
			Known:               true,
			Value:               value,
			AuxiliaryStatements: 1,
		}
	}
	return records
}

func writeAnalyzeServerRUBaseline(
	t testing.TB,
	directory string,
	name string,
	fingerprint string,
	mean float64,
	maximum float64,
) {
	t.Helper()
	baseline := runtimecapture.ServerRUBaseline{
		Version: runtimecapture.ServerRUBaselineVersion,
		ServerRUByFingerprint: []runtimecapture.FingerprintServerRUBaseline{{
			Fingerprint: fingerprint,
			Count:       5,
			Samples:     5,
			Total:       mean * 5,
			Mean:        mean,
			Minimum:     mean,
			Maximum:     maximum,
		}},
	}
	var output bytes.Buffer
	if err := runtimecapture.EncodeServerRUBaseline(&output, baseline); err != nil {
		t.Fatalf("EncodeServerRUBaseline() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), output.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
