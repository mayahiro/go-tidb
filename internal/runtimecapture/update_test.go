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

func TestAnalyzeRepeatedUpdates(t *testing.T) {
	for _, terminal := range []string{"update", "update_where"} {
		t.Run(terminal, func(t *testing.T) {
			first, second := repeatedUpdateRecord(1, terminal), repeatedUpdateRecord(2, terminal)
			// UpdateWhere may affect many rows or no rows. Neither count nor
			// absent bind values can prove batchability or distinct targets.
			first.RowsAffectedKnown, first.RowsAffected = true, 0
			second.RowsAffectedKnown, second.RowsAffected = true, 100
			diagnostic := onlyRuntimeWriteDiagnostic(t, Analyze([]Record{first, second}), codeRepeatedUpdate)
			if diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible || diagnostic.Title != "Repeated UPDATE warrants application review" {
				t.Fatalf("diagnostic policy = %#v", diagnostic)
			}
			if diagnostic.Message != "One runtime scope attempted the same typed UPDATE statement 2 times; repetition does not prove that the calls can be combined" {
				t.Fatalf("message = %q", diagnostic.Message)
			}
			for _, want := range []string{"assignments and predicates", "row-specific values", "lease conditions", "atomic increments", "execution order", "transaction boundaries", "intentional retries", "measure latency and RU"} {
				if !strings.Contains(diagnostic.Suggestion, want) {
					t.Errorf("suggestion = %q, want %q", diagnostic.Suggestion, want)
				}
			}
			assertWriteEvidence(t, diagnostic,
				"Captured write attempts: 2, reported errors: 0",
				"Captured target duration: "+(2*time.Microsecond).String(),
				"Captured statement ServerRU: total=unavailable, samples=0/2, collection_errors=0",
				"ServerRU covers measured attempts only, excludes BEGIN/COMMIT, and is not billed RU; attempts do not prove distinct rows or committed changes",
			)
		})
	}
}

func TestAnalyzeRepeatedUpdateGrouping(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
		code   string
	}{
		{"capture", func(record *Record) { record.CaptureID = "another" }, codeRepeatedUpdate},
		{"scope", func(record *Record) { record.ScopeID++ }, codeRepeatedUpdate},
		{"fingerprint", func(record *Record) { record.Fingerprint = "s1:another-update" }, codeRepeatedUpdate},
		{"terminal", func(record *Record) { record.Terminal = "update_where" }, codeRepeatedUpdate},
		{"insert", func(record *Record) { record.Operation, record.Terminal = "INSERT", "insert" }, codeRepeatedWrite},
		{"upsert", func(record *Record) { record.Operation, record.Terminal = "UPSERT", "upsert" }, codeRepeatedWrite},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, second := repeatedUpdateRecord(1, "update"), repeatedUpdateRecord(2, "update")
			test.change(&second)
			if analysis := Analyze([]Record{first, second}); len(analysis.Diagnostics) != 0 {
				t.Fatalf("separate groups must not warn: %#v", analysis.Diagnostics)
			}
			third, fourth := first, second
			third.Sequence, fourth.Sequence = 3, 4
			analysis := Analyze([]Record{first, second, fourth, third})
			if got, want := diagnosticCodes(analysis), []string{codeRepeatedUpdate, test.code}; !reflect.DeepEqual(got, want) {
				t.Fatalf("diagnostic order = %v, want %v", got, want)
			}
			for index, record := range []Record{first, second} {
				assertWriteEvidence(t, analysis.Diagnostics[index], "Query fingerprint: "+record.Fingerprint,
					"Capture: "+record.CaptureID+", scope: "+strconv.FormatUint(record.ScopeID, 10)+", terminal: "+record.Terminal)
			}
		})
	}
}

func TestAnalyzeRepeatedUpdateExclusions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{
		{"soft_delete", func(record *Record) { record.Terminal = "delete" }},
		{"soft_delete_where", func(record *Record) { record.Terminal = "delete_where" }},
		{"relation", func(record *Record) { record.Terminal = "relation_update" }},
		{"many", func(record *Record) { record.Terminal = "update_many" }},
		{"batch", func(record *Record) { record.Batch = &Batch{Group: 1, Index: 1, Count: 1, Rows: 1, TotalRows: 1} }},
		{"raw", func(record *Record) { record.Source = SourceRaw }},
		{"unknown", func(record *Record) { record.Source = SourceUnknown }},
		{"preload", func(record *Record) { record.Source = SourcePreload }},
		{"query", func(record *Record) { record.Source = SourceTypedSelect }},
		{"missing_terminal", func(record *Record) { record.Terminal = "" }},
		{"mismatched_terminal", func(record *Record) { record.Terminal = "insert" }},
		{"mismatched_operation", func(record *Record) { record.Operation = "UPSERT" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			analyzer := newAnalyzer()
			for index := range 2 {
				record := repeatedUpdateRecord(uint64(index+1), "update")
				test.change(&record)
				analyzer.add(record)
			}
			if codes := diagnosticCodes(analyzer.finish()); len(codes) != 0 {
				t.Fatalf("excluded update produced diagnostics: %v", codes)
			}
			if analyzer.repeatedWrites != nil || analyzer.writeGroups != nil {
				t.Fatal("excluded updates allocated repeated-write state")
			}
		})
	}
}

func TestAnalyzeRepeatedUpdateEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		samples  []*ServerRU
		evidence string
	}{
		{"missing", []*ServerRU{nil, nil}, "total=unavailable, samples=0/2, collection_errors=0"},
		{"zero", []*ServerRU{{Known: true}, {Known: true}}, "total=0, samples=2/2, collection_errors=0"},
		{"complete", []*ServerRU{{Known: true, Value: 1.25}, {Known: true, Value: 2.5}}, "total=3.75, samples=2/2, collection_errors=0"},
		{"partial", []*ServerRU{{Known: true, Value: 1.25}, nil}, "total=1.25, samples=1/2, collection_errors=0"},
		{"failed", []*ServerRU{{Error: "private-probe-error"}, nil}, "total=unavailable, samples=0/2, collection_errors=1"},
		{"mixed", []*ServerRU{{Known: true, Value: 1.25}, {Error: "private-probe-error"}}, "total=1.25, samples=1/2, collection_errors=1"},
		{"known_and_error", []*ServerRU{{Known: true, Value: 1.25, Error: "private-release-error"}, {Known: true, Value: 2.5}}, "total=3.75, samples=2/2, collection_errors=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, second := repeatedUpdateRecord(1, "update_where"), repeatedUpdateRecord(2, "update_where")
			first.Model, second.Model = "private-first-model", "private-second-model"
			first.Error, second.Error = "private-error", "private-retry-error"
			first.SQL = "private-sql-template"
			first.ServerRU, second.ServerRU = test.samples[0], test.samples[1]
			for _, sample := range test.samples {
				if sample != nil {
					sample.AuxiliaryStatements = 1
				}
			}
			// A third attempt in another scope is part of global RU but must
			// not contribute to this group's count, duration, or RU coverage.
			other := repeatedUpdateRecord(3, "update_where")
			other.ScopeID++
			other.ServerRU = &ServerRU{Known: true, Value: 100, AuxiliaryStatements: 1}
			analysis := Analyze([]Record{first, other, second})
			diagnostic := onlyRuntimeWriteDiagnostic(t, analysis, codeRepeatedUpdate)
			assertWriteEvidence(t, diagnostic,
				"Captured write attempts: 2, reported errors: 2",
				"Captured target duration: "+(2*time.Microsecond).String(),
				"Captured statement ServerRU: "+test.evidence)
			encoded, err := json.Marshal(diagnostic)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte("private-")) {
				t.Fatalf("diagnostic exposed raw SQL, errors, or an ambiguous model: %s", encoded)
			}
			if strings.Contains(test.evidence, "collection_errors=1") && diagnosticCodes(analysis)[0] != codeServerRUFailure {
				t.Fatalf("missing RUN003: %#v", analysis.Diagnostics)
			}
		})
	}
}

func TestAnalyzeRepeatedUpdatesRetainsCountersOnly(t *testing.T) {
	analyzer := newAnalyzer()
	for index := range 10000 {
		record := repeatedUpdateRecord(uint64(index+1), "update")
		record.ServerRU = &ServerRU{Known: true, Value: 0.5, AuxiliaryStatements: 1}
		analyzer.add(record)
	}
	if len(analyzer.repeatedWrites) != 1 || len(analyzer.writeGroups) != 1 || cap(analyzer.writeGroups) != 1 {
		t.Fatalf("aggregation retained more than one group: keys=%d groups=%d capacity=%d", len(analyzer.repeatedWrites), len(analyzer.writeGroups), cap(analyzer.writeGroups))
	}
	if group := analyzer.writeGroups[0]; group.count != 10000 || group.ruTotal != 5000 {
		t.Fatalf("group = %#v", group)
	}
	onlyRuntimeWriteDiagnostic(t, analyzer.finish(), codeRepeatedUpdate)
}

func TestAnalyzeReaderRepeatedUpdatesSaturatesTotals(t *testing.T) {
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for index := range 2 {
		record := repeatedUpdateRecord(uint64(index+1), "update_where")
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
	diagnostic := onlyRuntimeWriteDiagnostic(t, analysis, codeRepeatedUpdate)
	assertWriteEvidence(t, diagnostic,
		"Captured target duration: "+time.Duration(math.MaxInt64).String(),
		"Captured statement ServerRU: total="+strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64)+", samples=2/2, collection_errors=0")
	if _, err := json.Marshal(analysis); err != nil {
		t.Fatalf("analysis contains non-finite aggregates: %v", err)
	}
}

func repeatedUpdateRecord(sequence uint64, terminal string) Record {
	record := runtimeAnalysisRecord(sequence, SourceTypedMutation, "s1:update")
	record.Operation, record.Terminal = "UPDATE", terminal
	record.SQL = "UPDATE `users` SET `email` = ? WHERE `id` = ?"
	return record
}
