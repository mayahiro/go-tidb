package runtimecapture

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/check"
)

func TestAnalyzeRepeatedWrites(t *testing.T) {
	for _, operation := range []string{"INSERT", "UPSERT"} {
		t.Run(operation, func(t *testing.T) {
			first := repeatedWriteRecord(1, operation)
			second := repeatedWriteRecord(2, operation)
			// Result counts do not identify the number of input rows of an upsert.
			first.RowsAffectedKnown, first.RowsAffected = true, 0
			second.RowsAffectedKnown, second.RowsAffected = true, 2
			analysis := Analyze([]Record{first, second})
			diagnostic := onlyRepeatedWriteDiagnostic(t, analysis)
			if diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible {
				t.Fatalf("diagnostic policy = %#v", diagnostic)
			}
			if diagnostic.Message != "One runtime scope attempted the same typed "+operation+" statement 2 times" {
				t.Fatalf("message = %q", diagnostic.Message)
			}
			bulk := "InsertMany"
			if operation == "UPSERT" {
				bulk = "UpsertMany"
			}
			for _, want := range []string{bulk, "generated-ID use", "execution order", "transaction boundaries", "intentional retries", "measure latency and RU"} {
				if !strings.Contains(diagnostic.Suggestion, want) {
					t.Errorf("suggestion = %q, want %q", diagnostic.Suggestion, want)
				}
			}
			assertWriteEvidence(t, diagnostic,
				"Captured write attempts: 2, reported errors: 0",
				"Captured target duration: "+(2*time.Microsecond).String(),
				"Captured statement ServerRU: total=unavailable, samples=0/2, collection_errors=0",
			)
			if analysis.Statistics.Statements != 2 || analysis.Statistics.ServerRUSamples != 0 || analysis.Statistics.QueryShapeStatements != 0 {
				t.Fatalf("statistics = %#v", analysis.Statistics)
			}
		})
	}
}

func TestAnalyzeRepeatedWriteGrouping(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{
		{"capture", func(record *Record) { record.CaptureID = "other" }},
		{"scope", func(record *Record) { record.ScopeID++ }},
		{"fingerprint", func(record *Record) { record.Fingerprint = "s1:another-write" }},
		{"operation", func(record *Record) { record.Operation, record.Terminal = "UPSERT", "upsert" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := repeatedWriteRecord(1, "INSERT")
			second := repeatedWriteRecord(2, "INSERT")
			test.change(&second)
			analysis := Analyze([]Record{first, second})
			if len(analysis.Diagnostics) != 0 {
				t.Fatalf("distinct groups must not warn: %#v", analysis.Diagnostics)
			}
			// Adding a second attempt to each group must produce separate warnings
			// in first-seen order, even when the attempts are interleaved.
			third, fourth := first, second
			third.Sequence, fourth.Sequence = 3, 4
			analysis = Analyze([]Record{first, second, fourth, third})
			if got, want := diagnosticCodes(analysis), []string{codeRepeatedWrite, codeRepeatedWrite}; !reflect.DeepEqual(got, want) {
				t.Fatalf("diagnostic codes = %v, want %v", got, want)
			}
			for index, record := range []Record{first, second} {
				assertWriteEvidence(t, analysis.Diagnostics[index], "Query fingerprint: "+record.Fingerprint,
					"Capture: "+record.CaptureID+", scope: "+strconv.FormatUint(record.ScopeID, 10)+", terminal: "+record.Terminal)
			}
		})
	}
}

func TestAnalyzeRepeatedWritesDoesNotAttributeSharedSQLToOneModel(t *testing.T) {
	first := repeatedWriteRecord(1, "INSERT")
	second := repeatedWriteRecord(2, "INSERT")
	first.Model, second.Model = "FirstModel", "SecondModel"
	diagnostic := onlyRepeatedWriteDiagnostic(t, Analyze([]Record{first, second}))
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(first.Model)) || bytes.Contains(encoded, []byte(second.Model)) {
		t.Fatalf("shared SQL must not be attributed to only one model: %s", encoded)
	}
}

func TestAnalyzeRepeatedWriteExclusions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{
		{"insert_many", func(record *Record) { record.Terminal = "insert_many" }},
		{"upsert_many", func(record *Record) { record.Operation, record.Terminal = "UPSERT", "upsert_many" }},
		{"unsplit_batch", func(record *Record) { record.Batch = &Batch{Group: 1, Index: 1, Count: 1, Rows: 1, TotalRows: 1} }},
		{"split_batch", func(record *Record) {
			record.Batch = &Batch{Group: 1, Index: int(record.Sequence), Count: 2, Rows: 1, TotalRows: 2}
		}},
		{"relation_insert", func(record *Record) { record.Terminal = "relation_insert" }},
		{"raw", func(record *Record) { record.Source = SourceRaw }},
		{"unknown", func(record *Record) { record.Source = SourceUnknown }},
		{"preload", func(record *Record) { record.Source = SourcePreload }},
		{"missing_terminal", func(record *Record) { record.Terminal = "" }},
		{"mismatched_terminal", func(record *Record) { record.Terminal = "upsert" }},
		{"delete", func(record *Record) { record.Operation, record.Terminal = "DELETE", "delete" }},
		{"delete_where", func(record *Record) { record.Operation, record.Terminal = "DELETE", "delete_where" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			analyzer := newAnalyzer()
			for index := range 2 {
				record := repeatedWriteRecord(uint64(index+1), "INSERT")
				test.change(&record)
				analyzer.add(record)
			}
			analysis := analyzer.finish()
			if len(analysis.Diagnostics) != 0 {
				t.Fatalf("excluded operation produced diagnostics: %#v", analysis.Diagnostics)
			}
			if analyzer.repeatedWrites != nil || analyzer.writeGroups != nil {
				t.Fatal("excluded operations allocated repeated-write state")
			}
		})
	}
}

func TestAnalyzeRepeatedWritesServerRUCoverage(t *testing.T) {
	for _, test := range []struct {
		name     string
		samples  []*ServerRU
		evidence string
	}{
		{"missing", []*ServerRU{nil, nil}, "total=unavailable, samples=0/2, collection_errors=0"},
		{"zero", []*ServerRU{{Known: true, AuxiliaryStatements: 1}, {Known: true, AuxiliaryStatements: 1}}, "total=0, samples=2/2, collection_errors=0"},
		{"complete", []*ServerRU{{Known: true, Value: 1.25, AuxiliaryStatements: 1}, {Known: true, Value: 2.5, AuxiliaryStatements: 1}}, "total=3.75, samples=2/2, collection_errors=0"},
		{"partial", []*ServerRU{{Known: true, Value: 1.25, AuxiliaryStatements: 1}, nil}, "total=1.25, samples=1/2, collection_errors=0"},
		{"error", []*ServerRU{{Error: "RU unavailable", AuxiliaryStatements: 1}, nil}, "total=unavailable, samples=0/2, collection_errors=1"},
		{"mixed", []*ServerRU{{Known: true, Value: 1.25, AuxiliaryStatements: 1}, {Error: "RU unavailable", AuxiliaryStatements: 1}}, "total=1.25, samples=1/2, collection_errors=1"},
		{"known_and_error", []*ServerRU{{Known: true, Value: 1.25, Error: "connection release failed", AuxiliaryStatements: 1}, {Known: true, Value: 2.5, AuxiliaryStatements: 1}}, "total=3.75, samples=2/2, collection_errors=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := []Record{repeatedWriteRecord(1, "UPSERT"), repeatedWriteRecord(2, "UPSERT")}
			for index := range records {
				records[index].ServerRU = test.samples[index]
			}
			analysis := Analyze(records)
			diagnostic := onlyRepeatedWriteDiagnostic(t, analysis)
			assertWriteEvidence(t, diagnostic, "Captured statement ServerRU: "+test.evidence,
				"ServerRU covers measured attempts only, excludes BEGIN/COMMIT, and is not billed RU; attempts do not prove distinct rows or committed changes")
			if strings.Contains(test.evidence, "collection_errors=1") && diagnosticCodes(analysis)[0] != codeServerRUFailure {
				t.Fatalf("RU failure must still produce RUN003: %#v", analysis.Diagnostics)
			}
		})
	}
}

func TestAnalyzeRepeatedWritesCountsFailedAttemptsWithoutExposingValues(t *testing.T) {
	first := repeatedWriteRecord(1, "INSERT")
	second := repeatedWriteRecord(2, "INSERT")
	first.Error, first.SQL = "private-error-value", "private-sql-value"
	first.ServerRU = &ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
	second.Error = "private-retry-value"
	diagnostic := onlyRepeatedWriteDiagnostic(t, Analyze([]Record{first, second}))
	assertWriteEvidence(t, diagnostic,
		"Captured write attempts: 2, reported errors: 2",
		"Captured statement ServerRU: total=1.25, samples=1/2, collection_errors=0",
	)
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-")) {
		t.Fatalf("diagnostic exposed statement or error values: %s", encoded)
	}
}

func TestAnalyzeRepeatedWritesRUIsScopeLocal(t *testing.T) {
	first := repeatedWriteRecord(1, "INSERT")
	second := repeatedWriteRecord(2, "INSERT")
	other := repeatedWriteRecord(3, "INSERT")
	first.ServerRU = &ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
	other.ScopeID = 2
	other.ServerRU = &ServerRU{Known: true, Value: 10, AuxiliaryStatements: 1}
	analysis := Analyze([]Record{first, other, second})
	diagnostic := onlyRepeatedWriteDiagnostic(t, analysis)
	assertWriteEvidence(t, diagnostic, "Captured statement ServerRU: total=1.25, samples=1/2, collection_errors=0")
	if analysis.ServerRUByFingerprint[0].Total != 11.25 || analysis.ServerRUByFingerprint[0].Count != 3 {
		t.Fatalf("global RU totals = %#v", analysis.ServerRUByFingerprint)
	}
}

func TestAnalyzeRepeatedWritesUsesConstantStatePerGroup(t *testing.T) {
	analyzer := newAnalyzer()
	for index := range 1000 {
		record := repeatedWriteRecord(uint64(index+1), "INSERT")
		record.ServerRU = &ServerRU{Known: true, Value: 0.5, AuxiliaryStatements: 1}
		analyzer.add(record)
	}
	if len(analyzer.repeatedWrites) != 1 || len(analyzer.writeGroups) != 1 || cap(analyzer.writeGroups) != 1 {
		t.Fatalf("aggregation retained more than one group: keys=%d groups=%d capacity=%d", len(analyzer.repeatedWrites), len(analyzer.writeGroups), cap(analyzer.writeGroups))
	}
	if analyzer.writeGroups[0].count != 1000 || analyzer.writeGroups[0].ruTotal != 500 {
		t.Fatalf("group = %#v", analyzer.writeGroups[0])
	}
	onlyRepeatedWriteDiagnostic(t, analyzer.finish())
}

func TestAnalyzeReaderRepeatedWritesSaturatesTotals(t *testing.T) {
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for index := range 2 {
		record := repeatedWriteRecord(uint64(index+1), "UPSERT")
		record.DurationNS = math.MaxInt64
		record.ServerRU = &ServerRU{Known: true, Value: math.MaxFloat64, AuxiliaryStatements: 1}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := AnalyzeReader(&input)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := onlyRepeatedWriteDiagnostic(t, analysis)
	assertWriteEvidence(t, diagnostic,
		"Captured target duration: "+time.Duration(math.MaxInt64).String(),
		"Captured statement ServerRU: total="+strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64)+", samples=2/2, collection_errors=0",
	)
	if _, err := json.Marshal(analysis); err != nil {
		t.Fatalf("analysis contains non-finite aggregates: %v", err)
	}
}

func repeatedWriteRecord(sequence uint64, operation string) Record {
	record := runtimeAnalysisRecord(sequence, SourceTypedMutation, "s1:write")
	record.Operation = operation
	record.Terminal = strings.ToLower(operation)
	record.SQL = "INSERT INTO `users` (`email`) VALUES (?)"
	return record
}

func onlyRepeatedWriteDiagnostic(t *testing.T, analysis Analysis) check.Diagnostic {
	t.Helper()
	return onlyRuntimeWriteDiagnostic(t, analysis, codeRepeatedWrite)
}

func onlyRuntimeWriteDiagnostic(t *testing.T, analysis Analysis, code string) check.Diagnostic {
	t.Helper()
	var result *check.Diagnostic
	for _, diagnostic := range analysis.Diagnostics {
		if diagnostic.Code == code {
			if result != nil {
				t.Fatalf("more than one %s: %#v", code, analysis.Diagnostics)
			}
			result = &diagnostic
		} else if diagnostic.Code != codeServerRUFailure {
			t.Errorf("unexpected diagnostic: %#v", diagnostic)
		}
	}
	if result == nil {
		t.Fatalf("missing %s: %#v", code, analysis.Diagnostics)
	}
	return *result
}

func assertWriteEvidence(t *testing.T, diagnostic check.Diagnostic, messages ...string) {
	t.Helper()
	for _, message := range messages {
		found := false
		for _, evidence := range diagnostic.Evidence {
			if evidence.Message == message {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing evidence %q in %#v", message, diagnostic.Evidence)
		}
	}
}
