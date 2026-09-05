package runtimecapture

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

func TestAnalyzeMutationIndexCoverageAndDeduplication(t *testing.T) {
	catalog, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT PRIMARY KEY, a BIGINT, b BIGINT);")
	if err != nil {
		t.Fatal(err)
	}
	indexed := mutationIndexRecord(1, "s1:indexed", "id")
	missing := mutationIndexRecord(2, "s1:missing", "a")
	uncertain := mutationIndexRecord(3, "s1:uncertain", "b")
	uncertain.Mutation.Predicates[0].Operator = queryshape.PredicateNotEqual
	duplicate := missing
	duplicate.Sequence, duplicate.ScopeID = 4, 2
	duplicate.Error = "target failed"
	other := mutationIndexRecord(5, "s1:without-shape", "a")
	other.Mutation = nil

	analyzer := newAnalyzer(WithSchema(catalog))
	for _, record := range []Record{indexed, missing, uncertain, duplicate, other} {
		analyzer.add(record)
	}
	analysis := analyzer.finish()
	if got, want := diagnosticCodes(analysis), []string{querycheck.CodeMutationMissingIndex, querycheck.CodeMutationIndexUncertain}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %v, want %v", got, want)
	}
	statistics := analysis.Statistics
	if statistics.Statements != 5 || statistics.QueryShapeStatements != 0 || statistics.MutationShapeStatements != 4 || statistics.SchemaCheckedStatements != 4 || statistics.MutationIndexCheckedStatements != 3 || statistics.MutationIndexUncertainStatements != 1 {
		t.Fatalf("statistics = %#v", statistics)
	}
	if len(analyzer.mutationPatterns) != 3 {
		t.Fatalf("cache retained per-statement state: %#v", analyzer.mutationPatterns)
	}
	for index, fingerprint := range []string{missing.Fingerprint, uncertain.Fingerprint} {
		if analysis.Diagnostics[index].Evidence[0].Message != "Query fingerprint: "+fingerprint {
			t.Fatalf("fingerprint evidence = %#v", analysis.Diagnostics[index].Evidence)
		}
	}
	for _, want := range []string{"mutation_shape_statements=4", "mutation_index_checked_statements=3", "mutation_index_uncertain_statements=1"} {
		if !strings.Contains(FormatStatistics(statistics), want) {
			t.Fatalf("missing coverage %q", want)
		}
	}
}

func TestAnalyzeMutationShapesWithoutSchema(t *testing.T) {
	analyzer := newAnalyzer()
	analyzer.add(mutationIndexRecord(1, "s1:write", "a"))
	result := analyzer.finish()
	if len(result.Diagnostics) != 0 || result.Statistics.MutationShapeStatements != 1 || result.Statistics.SchemaCheckedStatements != 0 || result.Statistics.MutationIndexCheckedStatements != 0 || result.Statistics.MutationIndexUncertainStatements != 0 || analyzer.mutationPatterns != nil {
		t.Fatalf("analysis without schema = %#v", result)
	}
}

func TestAnalyzeMutationUnavailableSchemaIsNotCoverage(t *testing.T) {
	result := Analyze([]Record{mutationIndexRecord(1, "s1:write", "a")}, WithSchema(nil))
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != querycheck.CodeIndexCheckUnavailable || result.Diagnostics[0].Suppressible || result.Statistics.SchemaCheckedStatements != 1 || result.Statistics.MutationIndexCheckedStatements != 0 || result.Statistics.MutationIndexUncertainStatements != 0 {
		t.Fatalf("unavailable analysis = %#v", result)
	}
}

func TestAnalyzeReaderAcceptsMutationShapeAndKeepsServerRUIdentity(t *testing.T) {
	catalog, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT PRIMARY KEY, a BIGINT);")
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	for index := range 2 {
		record := mutationIndexRecord(uint64(index+1), "s1:write", "a")
		record.ServerRU = &ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := AnalyzeReader(&input, WithSchema(catalog))
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Diagnostics) != 2 || analysis.Diagnostics[0].Code != querycheck.CodeMutationMissingIndex || analysis.Diagnostics[1].Code != codeRepeatedUpdate || analysis.Statistics.MutationIndexCheckedStatements != 2 {
		t.Fatalf("streamed analysis = %#v", analysis)
	}
	if len(analysis.ServerRUByFingerprint) != 1 || analysis.ServerRUByFingerprint[0].Fingerprint != "s1:write" || analysis.ServerRUByFingerprint[0].Total != 2.5 || analysis.ServerRUByFingerprint[0].Samples != 2 {
		t.Fatalf("mutation analysis changed ServerRU identity: %#v", analysis.ServerRUByFingerprint)
	}
}

func TestMutationArtifactValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{
		{"raw", func(record *Record) { record.Source = SourceRaw }},
		{"non_write", func(record *Record) { record.Operation = "SELECT" }},
		{"primary_key_update", func(record *Record) { record.Terminal = "update" }},
		{"both_shapes", func(record *Record) { record.Query = &queryshape.Query{} }},
		{"batch", func(record *Record) { record.Batch = &Batch{Group: 1, Index: 1, Count: 1} }},
		{"missing_model", func(record *Record) { record.Mutation.Model = "" }},
		{"missing_table", func(record *Record) { record.Mutation.Table = "" }},
		{"missing_predicates", func(record *Record) { record.Mutation.Predicates = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := mutationIndexRecord(1, "s1:write", "a")
			test.change(&record)
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AnalyzeReader(bytes.NewReader(encoded)); err == nil || !strings.Contains(err.Error(), "mutation shape") {
				t.Fatalf("invalid mutation shape accepted: %s, error=%v", encoded, err)
			}
		})
	}
	for _, operation := range []string{"UPDATE", "DELETE"} {
		record := mutationIndexRecord(1, "s1:delete", "a")
		record.Terminal, record.Operation = "delete_where", operation
		if err := record.Validate(); err != nil {
			t.Fatalf("DeleteWhere operation %s rejected: %v", operation, err)
		}
	}
}

func mutationIndexRecord(sequence uint64, fingerprint, column string) Record {
	record := runtimeAnalysisRecord(sequence, SourceTypedMutation, fingerprint)
	record.Operation, record.Terminal = "UPDATE", "update_where"
	record.SQL = "UPDATE rows SET b = ? WHERE " + column + " = ?"
	record.Model = "Row"
	record.Mutation = &queryshape.Mutation{Model: "Row", Table: "rows", Predicates: []queryshape.MutationPredicate{{Operator: queryshape.PredicateEqual, Column: column}}}
	return record
}

func BenchmarkAnalyzeMutationIndexes(b *testing.B) {
	catalog, err := physicalschema.Parse("CREATE TABLE rows (id BIGINT PRIMARY KEY, a BIGINT, b BIGINT);")
	if err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{1, 1000} {
		name := "1_statement"
		if count == 1000 {
			name = "1000_statements"
		}
		b.Run(name, func(b *testing.B) {
			records := make([]Record, count)
			for index := range records {
				records[index] = mutationIndexRecord(uint64(index+1), "s1:write", "a")
			}
			b.ReportAllocs()
			for b.Loop() {
				analysis := Analyze(records, WithSchema(catalog))
				if analysis.Statistics.MutationIndexCheckedStatements != count || len(analysis.Diagnostics) != 1 {
					b.Fatalf("analysis = %#v", analysis)
				}
			}
		})
	}
}
