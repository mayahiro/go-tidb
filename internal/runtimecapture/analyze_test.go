package runtimecapture

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const codeRelationTopN = querycheck.CodeRelationTopNFallback

func Analyze(records []Record, options ...AnalysisOption) Analysis {
	analyzer := newAnalyzer(options...)
	for index := range records {
		analyzer.add(records[index])
	}
	return analyzer.finish()
}

func TestAnalyzeReportsRepeatedRootSelectAndSkipsPreloadBatches(t *testing.T) {
	records := []Record{
		runtimeAnalysisRecord(1, SourceTypedSelect, "q1:users"),
		runtimeAnalysisRecord(2, SourceTypedSelect, "q1:users"),
		runtimeAnalysisRecord(3, SourcePreload, "s1:orders"),
		runtimeAnalysisRecord(4, SourcePreload, "s1:orders"),
	}
	records[0].Model = "User"
	records[1].Model = "User"
	records[2].Batch = &Batch{Group: 1, Index: 1, Count: 2, Rows: 5000, TotalRows: 6000}
	records[3].Batch = &Batch{Group: 1, Index: 2, Count: 2, Rows: 1000, TotalRows: 6000}

	analysis := Analyze(records)
	if got, want := diagnosticCodes(analysis), []string{codePossibleNPlusOne}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if analysis.Statistics.Statements != 4 || analysis.Statistics.Fingerprints != 2 || analysis.Statistics.BatchGroups != 1 || analysis.Statistics.SplitBatches != 1 {
		t.Fatalf("statistics = %#v", analysis.Statistics)
	}
}

func TestAnalyzeKeepsRuntimeScopesSeparate(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:users")
	second := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:users")
	second.ScopeID = 2
	analysis := Analyze([]Record{first, second})
	if len(analysis.Diagnostics) != 0 || analysis.Statistics.Scopes != 2 {
		t.Fatalf("Analyze() = %#v, want no cross-scope warning", analysis)
	}
}

func TestAnalyzeIncludesRepeatedFailedSelects(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedSelect, "s1:missing-user")
	first.Error = "sql: no rows in result set"
	second := runtimeAnalysisRecord(2, SourceTypedSelect, "s1:missing-user")
	second.Error = "sql: no rows in result set"
	analysis := Analyze([]Record{first, second})
	if got, want := diagnosticCodes(analysis), []string{codePossibleNPlusOne}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
}

func TestAnalyzeReportsCompilerFallbackAndMetadataFailure(t *testing.T) {
	record := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:fallback")
	record.MetadataError = "shape unavailable"
	record.Query = &queryshape.Query{
		Model: "Video",
		Compiler: queryshape.CompilerDecision{
			Rewrite:  queryshape.CompilerRewriteRelationTopNFallback,
			Relation: "VideoGenres",
			Reason:   "root order is not the primary key",
		},
	}
	analysis := Analyze([]Record{record})
	if got, want := diagnosticCodes(analysis), []string{codeIncompleteMetadata, codeRelationTopN}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
}

func TestAnalyzeAppliesCapturedQueryPatternDiagnosticsWithoutRegistration(t *testing.T) {
	record := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:patterns")
	record.Query = &queryshape.Query{
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

	analysis := Analyze([]Record{record})
	wantCodes := []string{
		querycheck.CodeOffsetPagination,
		querycheck.CodeUnorderedPagination,
		codeRelationTopN,
		querycheck.CodeLeadingWildcardFilter,
	}
	if got := diagnosticCodes(analysis); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, wantCodes)
	}
	if analysis.Statistics.QueryShapeStatements != 1 || analysis.Statistics.SchemaCheckedStatements != 0 {
		t.Fatalf("statistics = %#v", analysis.Statistics)
	}
	for _, diagnostic := range analysis.Diagnostics {
		if len(diagnostic.Evidence) == 0 || diagnostic.Evidence[0].Message != "Query fingerprint: q1:patterns" {
			t.Fatalf("diagnostic evidence = %#v", diagnostic.Evidence)
		}
	}
}

func TestAnalyzeDeduplicatesCapturedQueryDiagnosticsByFingerprint(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:unordered")
	first.Query = &queryshape.Query{
		Model: "Video",
		Limit: queryshape.Bound{Set: true, Positive: true},
	}
	second := first
	second.Sequence = 2
	second.ScopeID = 2

	analysis := Analyze([]Record{first, second})
	if got, want := diagnosticCodes(analysis), []string{querycheck.CodeUnorderedPagination}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if analysis.Statistics.QueryShapeStatements != 2 {
		t.Fatalf("statistics = %#v", analysis.Statistics)
	}
}

func TestAnalyzePatternCacheSeparatesPositiveBoundClassification(t *testing.T) {
	zero := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:limited")
	zero.Query = &queryshape.Query{
		Model: "Video",
		Limit: queryshape.Bound{Set: true},
	}
	positive := zero
	positive.Sequence = 2
	positive.ScopeID = 2
	positiveShape := *zero.Query
	positiveShape.Limit.Positive = true
	positive.Query = &positiveShape

	analysis := Analyze([]Record{zero, positive})
	if got, want := diagnosticCodes(analysis), []string{querycheck.CodeUnorderedPagination}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
}

func TestAnalyzeAppliesSchemaDiagnosticsToCapturedQueryShapes(t *testing.T) {
	catalog, err := physicalschema.Parse(`
CREATE TABLE videos (
    id BIGINT NOT NULL,
    tenant_id BIGINT NOT NULL,
    PRIMARY KEY (id)
);`)
	if err != nil {
		t.Fatalf("schema.Parse() error = %v", err)
	}
	record := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:index")
	record.Query = &queryshape.Query{
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

	withoutSchema := Analyze([]Record{record})
	if len(withoutSchema.Diagnostics) != 0 || withoutSchema.Statistics.SchemaCheckedStatements != 0 {
		t.Fatalf("Analyze() without schema = %#v", withoutSchema)
	}
	withSchema := Analyze([]Record{record}, WithSchema(catalog))
	if got, want := diagnosticCodes(withSchema), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if withSchema.Statistics.QueryShapeStatements != 1 || withSchema.Statistics.SchemaCheckedStatements != 1 {
		t.Fatalf("statistics = %#v", withSchema.Statistics)
	}
}

func TestAnalyzeReaderStreamsValidatedRecords(t *testing.T) {
	first, err := json.Marshal(runtimeAnalysisRecord(1, SourceTypedSelect, "q1:users"))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	second, err := json.Marshal(runtimeAnalysisRecord(2, SourceTypedSelect, "q1:users"))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	analysis, err := AnalyzeReader(strings.NewReader(string(first) + "\n" + string(second) + "\n"))
	if err != nil {
		t.Fatalf("AnalyzeReader() error = %v", err)
	}
	if analysis.Statistics.Statements != 2 || len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != codePossibleNPlusOne {
		t.Fatalf("AnalyzeReader() = %#v", analysis)
	}
}

func TestAnalyzeSaturatesAccumulatedDuration(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedSelect, "q1:first")
	first.DurationNS = math.MaxInt64
	second := runtimeAnalysisRecord(2, SourceTypedSelect, "q1:second")
	second.DurationNS = 1
	analysis := Analyze([]Record{first, second})
	if analysis.Statistics.TargetDuration != math.MaxInt64 {
		t.Fatalf("target duration = %d, want %d", analysis.Statistics.TargetDuration, int64(math.MaxInt64))
	}
}

func TestAnalyzeAggregatesServerRUCostAndReportsFailures(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedMutation, "s1:first")
	first.Operation = "UPDATE"
	first.Terminal = "update_where"
	first.SQL = "UPDATE `users` SET `active` = ? WHERE `id` = ?"
	first.ServerRU = &ServerRU{Known: true, Value: 1.25, DiagnosticDurationNS: 100, AuxiliaryStatements: 1}
	second := first
	second.Sequence = 2
	second.ServerRU = &ServerRU{Known: true, Value: 2.5, DiagnosticDurationNS: 200, AuxiliaryStatements: 1}
	third := first
	third.Sequence = 3
	third.Fingerprint = "s1:third"
	third.ServerRU = &ServerRU{DiagnosticDurationNS: 50, AuxiliaryStatements: 1, Error: "read failed"}
	fourth := third
	fourth.Sequence = 4
	fourth.ServerRU = &ServerRU{DiagnosticDurationNS: 50, AuxiliaryStatements: 1, Error: "later read failed"}
	fifth := first
	fifth.Sequence = 5
	fifth.ServerRU = nil

	analysis := Analyze([]Record{fifth, first, second, third, fourth})
	statistics := analysis.Statistics
	if statistics.Statements != 5 || statistics.AuxiliaryStatements != 4 || statistics.ServerRUSamples != 2 || statistics.ServerRUErrors != 2 || statistics.ServerRUTotal != 3.75 || statistics.DiagnosticDuration != 400 {
		t.Fatalf("statistics = %#v", statistics)
	}
	wantByFingerprint := []FingerprintServerRU{
		{Fingerprint: "s1:first", Count: 3, Samples: 2, Total: 3.75, Mean: 1.875, Minimum: 1.25, Maximum: 2.5},
		{Fingerprint: "s1:third", Count: 2, Errors: 2},
	}
	if got := analysis.ServerRUByFingerprint; !reflect.DeepEqual(got, wantByFingerprint) {
		t.Fatalf("ServerRUByFingerprint = %#v, want %#v", got, wantByFingerprint)
	}
	if got, want := diagnosticCodes(analysis), []string{codeServerRUFailure}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	formatted := FormatStatistics(statistics)
	for _, want := range []string{"statements=5", "auxiliary_statements=4", "diagnostic_duration=400ns", "server_ru_samples=2", "server_ru_errors=2", "server_ru_total=3.75"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("FormatStatistics() = %q, want substring %q", formatted, want)
		}
	}
	if got, want := FormatFingerprintServerRU(analysis.ServerRUByFingerprint[0]), "server_ru_fingerprint: fingerprint=s1:first count=3 samples=2 errors=0 total=3.75 mean=1.875 min=1.25 max=2.5"; got != want {
		t.Fatalf("FormatFingerprintServerRU() = %q, want %q", got, want)
	}
}

func TestAnalyzeSaturatesFingerprintServerRUTotalWithoutOverflowingMean(t *testing.T) {
	first := runtimeAnalysisRecord(1, SourceTypedMutation, "s1:update")
	first.Operation = "UPDATE"
	first.ServerRU = &ServerRU{Known: true, Value: math.MaxFloat64, AuxiliaryStatements: 1}
	second := first
	second.Sequence = 2

	analysis := Analyze([]Record{first, second})
	if len(analysis.ServerRUByFingerprint) != 1 {
		t.Fatalf("ServerRUByFingerprint = %#v, want one aggregate", analysis.ServerRUByFingerprint)
	}
	statistics := analysis.ServerRUByFingerprint[0]
	if statistics.Total != math.MaxFloat64 || statistics.Mean != math.MaxFloat64 || statistics.Minimum != math.MaxFloat64 || statistics.Maximum != math.MaxFloat64 {
		t.Fatalf("fingerprint ServerRU = %#v", statistics)
	}
}

func TestAnalyzeCountsUsableServerRUAndReleaseErrorFromOneStatement(t *testing.T) {
	record := runtimeAnalysisRecord(1, SourceTypedMutation, "s1:update")
	record.Operation = "UPDATE"
	record.ServerRU = &ServerRU{
		Known:               true,
		Value:               2.25,
		AuxiliaryStatements: 1,
		Error:               "release failed",
	}

	analysis := Analyze([]Record{record})
	want := []FingerprintServerRU{{
		Fingerprint: "s1:update",
		Count:       1,
		Samples:     1,
		Errors:      1,
		Total:       2.25,
		Mean:        2.25,
		Minimum:     2.25,
		Maximum:     2.25,
	}}
	if !reflect.DeepEqual(analysis.ServerRUByFingerprint, want) {
		t.Fatalf("ServerRUByFingerprint = %#v, want %#v", analysis.ServerRUByFingerprint, want)
	}
	if analysis.Statistics.ServerRUSamples != 1 || analysis.Statistics.ServerRUErrors != 1 {
		t.Fatalf("statistics = %#v", analysis.Statistics)
	}
	if got := diagnosticCodes(analysis); !reflect.DeepEqual(got, []string{codeServerRUFailure}) {
		t.Fatalf("diagnostic codes = %#v", got)
	}
}

func TestAnalyzeReturnsNonNilEmptyServerRUStatistics(t *testing.T) {
	analysis := Analyze(nil)
	if analysis.ServerRUByFingerprint == nil || len(analysis.ServerRUByFingerprint) != 0 {
		t.Fatalf("ServerRUByFingerprint = %#v, want non-nil empty", analysis.ServerRUByFingerprint)
	}
}

func BenchmarkAnalyzeCapturedQueryShapes(b *testing.B) {
	shape := &queryshape.Query{
		Model:  "Video",
		Limit:  queryshape.Bound{Set: true, Positive: true},
		Offset: queryshape.Bound{Set: true, Positive: true},
		Predicates: []queryshape.Predicate{{
			Operator: queryshape.PredicateContains,
			Field:    "Title",
		}},
	}
	records := make([]Record, 100)
	for index := range records {
		records[index] = runtimeAnalysisRecord(uint64(index+1), SourceTypedSelect, "q1:video-list")
		records[index].Query = shape
	}
	var analysis Analysis
	b.ReportAllocs()
	for b.Loop() {
		analysis = Analyze(records)
	}
	runtimeAnalysisSink = analysis
}

func BenchmarkAnalyzeServerRUOneFingerprint(b *testing.B) {
	b.Run("1_sample", func(b *testing.B) {
		benchmarkAnalyzeServerRUOneFingerprint(b, 1)
	})
	b.Run("10000_samples", func(b *testing.B) {
		benchmarkAnalyzeServerRUOneFingerprint(b, 10_000)
	})
}

func benchmarkAnalyzeServerRUOneFingerprint(b *testing.B, sampleCount int) {
	sample := &ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
	records := make([]Record, sampleCount)
	for index := range records {
		records[index] = runtimeAnalysisRecord(uint64(index+1), SourceTypedMutation, "s1:update")
		records[index].Operation = "UPDATE"
		records[index].Terminal = "update_where"
		records[index].ServerRU = sample
	}
	var analysis Analysis
	b.ReportAllocs()
	b.ReportMetric(float64(sampleCount), "samples/analyze")
	for b.Loop() {
		analysis = Analyze(records)
	}
	runtimeAnalysisSink = analysis
}

func runtimeAnalysisRecord(sequence uint64, source Source, fingerprint string) Record {
	return Record{
		Version:       Version,
		CaptureID:     "capture",
		ScopeID:       1,
		Sequence:      sequence,
		Operation:     "SELECT",
		Source:        source,
		Terminal:      "all",
		Fingerprint:   fingerprint,
		SQL:           "SELECT `id` FROM `users` WHERE `id` = ?",
		ArgumentCount: 1,
		DurationNS:    1000,
	}
}

func diagnosticCodes(analysis Analysis) []string {
	result := make([]string, len(analysis.Diagnostics))
	for index := range analysis.Diagnostics {
		result[index] = analysis.Diagnostics[index].Code
	}
	return result
}

var runtimeAnalysisSink Analysis
