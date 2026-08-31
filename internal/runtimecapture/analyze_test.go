package runtimecapture

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/queryshape"
)

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
