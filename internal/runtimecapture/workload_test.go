package runtimecapture

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func workloadRecords(scopes, statements int, ru float64) []Record {
	records := make([]Record, 0, scopes*statements)
	for scope := range scopes {
		for statement := range statements {
			record := runtimeAnalysisRecord(uint64(statement+1), SourceTypedMutation, "s1:update")
			record.ScopeID = uint64(scope + 1)
			record.Operation, record.Terminal = "UPDATE", "update"
			record.ServerRU = &ServerRU{Known: true, Value: ru, AuxiliaryStatements: 1}
			records = append(records, record)
		}
	}
	return records
}

func TestWorkloadAggregatesScopesNotStatementsOrCaptureIDs(t *testing.T) {
	t.Parallel()
	records := workloadRecords(2, 2, 1)
	records[1].ServerRU.Value = 3
	// Another capture can reuse a numeric scope ID. Interleaving and descending
	// completion sequence do not merge or split logical scopes.
	records[2].CaptureID, records[2].ScopeID = "another-capture", 1
	records[3].CaptureID, records[3].ScopeID = "another-capture", 1
	records[3].ServerRU.Value = 5
	slices.Reverse(records)
	analysis := Analyze(records, WithWorkload("sync-edge-2"))
	want := &WorkloadServerRU{
		WorkloadMetrics: WorkloadMetrics{
			Name: "sync-edge-2", Scopes: 2, Statements: 4,
			ServerRU:       ScopeMetric{Total: 10, Mean: 5, Minimum: 4, Maximum: 6},
			StatementCount: ScopeMetric{Total: 4, Mean: 2, Minimum: 2, Maximum: 2},
		},
		CompleteScopes: 2, Samples: 4,
	}
	if !reflect.DeepEqual(analysis.Workload, want) {
		t.Fatalf("workload = %#v, want %#v", analysis.Workload, want)
	}
	if err := analysis.Workload.validate(); err != nil {
		t.Fatal(err)
	}
	if ordinary := Analyze(records); ordinary.Workload != nil || !reflect.DeepEqual(ordinary.Statistics, analysis.Statistics) || !reflect.DeepEqual(ordinary.ServerRUByFingerprint, analysis.ServerRUByFingerprint) {
		t.Fatalf("workload option changed ordinary analysis: %#v", ordinary)
	}
	for range 5 {
		if got := Analyze(records, WithWorkload("sync-edge-2")); !reflect.DeepEqual(got, analysis) {
			t.Fatal("analysis was not deterministic")
		}
	}
}

func TestWorkloadIncludesAllDMLPathsAndExcludesTransactionControlRU(t *testing.T) {
	t.Parallel()
	records := workloadRecords(1, 7, 1)
	for index, kind := range []struct {
		operation, terminal string
		source              Source
	}{
		{"SELECT", "all", SourceTypedSelect},
		{"SELECT", "preload", SourcePreload},
		{"INSERT", "insert_many", SourceTypedMutation},
		{"UPSERT", "upsert_many", SourceTypedMutation},
		{"UPDATE", "update_where", SourceTypedMutation},
		{"DELETE", "remove_relation", SourceTypedMutation},
		{"UPDATE", "raw_exec", SourceRaw},
	} {
		records[index].Operation, records[index].Terminal, records[index].Source = kind.operation, kind.terminal, kind.source
	}
	records[2].Batch = &Batch{Group: 1, Index: 1, Count: 2, Rows: 10, TotalRows: 20}
	records[3].Batch = &Batch{Group: 1, Index: 2, Count: 2, Rows: 10, TotalRows: 20}
	for _, operation := range []string{"BEGIN", "COMMIT", "ROLLBACK"} {
		record := records[0]
		record.Sequence = uint64(len(records) + 1)
		record.Operation, record.Source, record.ServerRU = operation, SourceTransaction, nil
		records = append(records, record)
	}
	workload := Analyze(records, WithWorkload("transaction-fixture")).Workload
	if workload.CompleteScopes != 1 || workload.Statements != 7 || workload.Samples != 7 || workload.ServerRU.Total != 7 || workload.TransactionStatements != 3 || workload.StatementCount.Mean != 7 {
		t.Fatalf("scope counts = %#v", workload)
	}
}

func TestWorkloadRequiresCompleteRepeatedScopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func([]Record) []Record
		want   string
	}{
		{"one_scope_many_samples", func(records []Record) []Record { return workloadRecords(1, 100, 1) }, "five complete scopes"},
		{"empty", func([]Record) []Record { return nil }, "five complete scopes"},
		{"one_unsampled", func(records []Record) []Record { records[0].ServerRU = nil; return records }, "complete ServerRU coverage"},
		{"entire_scope_unsampled", func(records []Record) []Record {
			for index := range 2 {
				records[index].ServerRU = nil
			}
			return records
		}, "complete ServerRU coverage"},
		{"entire_fingerprint_unsampled", func(records []Record) []Record {
			records[0].Fingerprint, records[0].ServerRU = "new-unsampled", nil
			return records
		}, "complete ServerRU coverage"},
		{"collection_error", func(records []Record) []Record {
			records[0].ServerRU = &ServerRU{Error: "query failed"}
			return records
		}, "collection or connection release"},
		{"release_error", func(records []Record) []Record { records[0].ServerRU.Error = "release failed"; return records }, "collection or connection release"},
		{"statement_error", func(records []Record) []Record { records[0].Error = "write failed"; return records }, "execution or result-processing"},
		{"unknown_raw", func(records []Record) []Record {
			records[0].Operation, records[0].Source, records[0].ServerRU = "EXEC", SourceRaw, nil
			return records
		}, "unsupported operations"},
		{"plan", func(records []Record) []Record {
			records[0].Operation, records[0].Source, records[0].ServerRU = "EXPLAIN ANALYZE", SourcePlan, nil
			return records
		}, "unsupported operations"},
		{"commit_error", func(records []Record) []Record {
			records[0].Operation, records[0].Source, records[0].ServerRU, records[0].Error = "COMMIT", SourceTransaction, nil, "commit failed"
			return records
		}, "execution or result-processing"},
		{"transaction_only_scope", func(records []Record) []Record {
			for index := range 2 {
				records[index].Operation, records[index].Source, records[index].ServerRU = "BEGIN", SourceTransaction, nil
			}
			return records
		}, "at least one DML"},
		{"scope_overflow", func(records []Record) []Record {
			for index := range 2 {
				records[index].ServerRU.Value = math.MaxFloat64
			}
			return records
		}, "overflowed"},
		{"aggregate_overflow", func(records []Record) []Record {
			for index := range records {
				records[index].ServerRU.Value = math.MaxFloat64 / 4
			}
			return records
		}, "overflowed"},
	}
	baseline, err := NewServerRUBaseline(Analyze(workloadRecords(5, 2, 1), WithWorkload("update-2")))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := Analyze(test.change(workloadRecords(5, 2, 1)), WithWorkload("update-2"))
			if _, err := NewServerRUBaseline(analysis); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("baseline error = %v, want %q", err, test.want)
			}
			comparison, err := CompareServerRU(analysis, baseline)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Workload.Status != "unavailable" || !strings.Contains(comparison.Workload.Reason, test.want) {
				t.Fatalf("comparison = %#v", comparison.Workload)
			}
			found := false
			for _, diagnostic := range comparison.Diagnostics() {
				if diagnostic.Code == codeWorkloadUnavailable {
					found = true
					if diagnostic.Suppressible || diagnostic.Severity != "error" {
						t.Fatalf("diagnostic = %#v", diagnostic)
					}
				}
			}
			if !found {
				t.Fatal("missing unavailable diagnostic")
			}
		})
	}
}

func TestWorkloadComparisonDetectsRepeatGrowthWithUnchangedStatementMean(t *testing.T) {
	t.Parallel()
	baseline, err := NewServerRUBaseline(Analyze(workloadRecords(5, 10, 1), WithWorkload("update-10")))
	if err != nil {
		t.Fatal(err)
	}
	current := Analyze(workloadRecords(8, 100, 1), WithWorkload("update-10"))
	comparison, err := CompareServerRU(current, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Summary.Passed != 1 || comparison.Summary.Regressions != 0 || comparison.Workload.Status != "regression" || !comparison.Workload.ServerRURegressed || !comparison.Workload.StatementsRegressed {
		t.Fatalf("comparison = %#v, workload = %#v", comparison, comparison.Workload)
	}
	if comparison.Workload.ServerRULimit != 13 || comparison.Workload.StatementCountLimit != 13 || len(comparison.Diagnostics()) != 1 || comparison.Diagnostics()[0].Code != codeWorkloadRegression {
		t.Fatalf("diagnostics = %#v", comparison.Diagnostics())
	}
	current.Workload.Name = "changed-after-comparison"
	baseline.Workload.Name = "changed-after-comparison"
	if comparison.Workload.Current.Name != "update-10" || comparison.Workload.Baseline.Name != "update-10" {
		t.Fatal("comparison aliases mutable input")
	}
}

func TestWorkloadComparisonRatioNoiseFloorAndIndependentMetrics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                                         string
		statements                                   int
		ru                                           float64
		regression, ruRegressed, statementsRegressed bool
	}{
		{"same", 10, 1, false, false, false},
		{"threshold_equality", 13, 1, false, false, false},
		{"more_calls_only", 20, 0.25, true, false, true},
		{"more_ru_only", 10, 2, true, true, false},
		{"less_work", 5, 1, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline, err := NewServerRUBaseline(Analyze(workloadRecords(5, 10, 1), WithWorkload("test")))
			if err != nil {
				t.Fatal(err)
			}
			comparison, err := CompareServerRU(Analyze(workloadRecords(9, test.statements, test.ru), WithWorkload("test")), baseline)
			if err != nil {
				t.Fatal(err)
			}
			result := comparison.Workload
			if (result.Status == "regression") != test.regression || result.ServerRURegressed != test.ruRegressed || result.StatementsRegressed != test.statementsRegressed {
				t.Fatalf("comparison = %#v", result)
			}
		})
	}
	// One larger baseline operation raises the observed maximum above 130% of
	// the mean. Repeating that maximum is not a regression.
	records := append(workloadRecords(4, 1, 1), workloadRecords(1, 10, 1)...)
	for index := 4; index < len(records); index++ {
		records[index].ScopeID = 5
	}
	baseline, err := NewServerRUBaseline(Analyze(records, WithWorkload("variable-cost")))
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareServerRU(Analyze(workloadRecords(5, 10, 1), WithWorkload("variable-cost")), baseline)
	if err != nil || comparison.Workload.Status != "pass" || comparison.Workload.ServerRULimit != 10 || comparison.Workload.StatementCountLimit != 10 {
		t.Fatalf("comparison = %#v, error = %v", comparison.Workload, err)
	}
}

func TestWorkloadIdentityAndFingerprintComparisonsRemainIndependent(t *testing.T) {
	t.Parallel()
	baselineAnalysis := Analyze(workloadRecords(5, 2, 1), WithWorkload("test"))
	baseline, err := NewServerRUBaseline(baselineAnalysis)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []AnalysisOption{nil, WithWorkload("different")} {
		comparison, err := CompareServerRU(Analyze(workloadRecords(5, 2, 1), option), baseline)
		if err != nil || comparison.Workload.Status != "unavailable" {
			t.Fatalf("comparison = %#v, error = %v", comparison, err)
		}
	}
	withoutWorkload := baseline
	withoutWorkload.Workload = nil
	comparison, err := CompareServerRU(baselineAnalysis, withoutWorkload)
	if err != nil || comparison.Workload.Status != "unavailable" {
		t.Fatalf("comparison = %#v, error = %v", comparison, err)
	}
	records := workloadRecords(5, 1, 0.5)
	for index := range records {
		records[index].Fingerprint = "s1:new-sql-shape"
	}
	comparison, err = CompareServerRU(Analyze(records, WithWorkload("test")), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Workload.Status != "pass" || comparison.Summary.Unavailable != 2 || len(comparison.Diagnostics()) != 1 || comparison.Diagnostics()[0].Code != codeServerRUComparisonUnavailable {
		t.Fatalf("fingerprint changes were hidden by passing scope totals: %#v", comparison)
	}
}

func TestWorkloadBaselineRoundTripValidationAndReader(t *testing.T) {
	t.Parallel()
	var artifact bytes.Buffer
	for _, record := range workloadRecords(5, 2, 0) {
		if err := json.NewEncoder(&artifact).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	analysis, err := AnalyzeReader(&artifact, WithWorkload("zero-cost"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := NewServerRUBaseline(analysis)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := EncodeServerRUBaseline(&encoded, baseline); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), "capture_id") || strings.Contains(encoded.String(), "complete_scopes") || strings.Contains(encoded.String(), "collection_errors") || !strings.Contains(encoded.String(), `"version":1`) {
		t.Fatalf("baseline leaked per-run identity or redundant coverage: %s", encoded.String())
	}
	decoded, err := DecodeServerRUBaseline(&encoded)
	if err != nil || !reflect.DeepEqual(decoded, baseline) {
		t.Fatalf("round trip = %#v, %v", decoded, err)
	}
	analysis.Workload.Name = "mutated"
	if baseline.Workload.Name != "zero-cost" {
		t.Fatal("baseline aliases input")
	}
	for _, change := range []func(*WorkloadMetrics){
		func(w *WorkloadMetrics) { w.Name = "invalid\nlabel" },
		func(w *WorkloadMetrics) { w.Scopes = 0 },
		func(w *WorkloadMetrics) { w.Statements++ },
		func(w *WorkloadMetrics) { w.ServerRU.Mean = math.NaN() },
		func(w *WorkloadMetrics) { w.StatementCount.Minimum = -1 },
		func(w *WorkloadMetrics) { w.StatementCount.Maximum = 2.5 },
		func(w *WorkloadMetrics) { w.ServerRU = ScopeMetric{Total: 5, Mean: 1, Minimum: 1, Maximum: 1} },
	} {
		invalid := baseline
		copy := *baseline.Workload
		invalid.Workload = &copy
		change(invalid.Workload)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted invalid workload baseline: %#v", copy)
		}
	}
	for _, name := range []string{"", "a b", "../private", "-leading", "control\x1b", strings.Repeat("x", 129), "日本語"} {
		if _, err := AnalyzeReader(strings.NewReader(""), WithWorkload(name)); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
	for _, name := range []string{"job", "Job.sync-10_edges", "1", strings.Repeat("x", 128)} {
		if err := ValidateWorkloadName(name); err != nil {
			t.Fatalf("rejected name %q: %v", name, err)
		}
	}
}
