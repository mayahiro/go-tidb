package runtimecapture

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestCompareServerRUAppliesRatioAndObservedMaximumFloor(t *testing.T) {
	baseline := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{
			serverRUBaselineComparisonEntry("q1:floor", 5, 2, 1.5, 3),
			serverRUBaselineComparisonEntry("q1:ratio", 5, 1, 0.9, 1.1),
			serverRUBaselineComparisonEntry("q1:regression", 5, 1, 0.9, 1.2),
		},
	}
	analysis := Analysis{ServerRUByFingerprint: []FingerprintServerRU{
		serverRUCurrentComparisonEntry("q1:floor", 5, 2.9, 2.8, 3),
		serverRUCurrentComparisonEntry("q1:ratio", 5, 1.3, 1.2, 1.4),
		serverRUCurrentComparisonEntry("q1:regression", 5, 1.31, 1.2, 1.4),
	}}

	comparison, err := CompareServerRU(analysis, baseline)
	if err != nil {
		t.Fatalf("CompareServerRU() error = %v", err)
	}
	if got, want := comparison.Policy, (ServerRUComparisonPolicy{MinimumSamples: 5, MaximumMeanRatio: 1.3}); got != want {
		t.Fatalf("policy = %#v, want %#v", got, want)
	}
	if got, want := comparison.Summary, (ServerRUComparisonSummary{Fingerprints: 3, Passed: 2, Regressions: 1}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	wantStatuses := []ServerRUComparisonStatus{
		ServerRUComparisonPass,
		ServerRUComparisonPass,
		ServerRUComparisonRegression,
	}
	for index, want := range wantStatuses {
		if got := comparison.Entries[index].Status; got != want {
			t.Fatalf("entries[%d].status = %q, want %q", index, got, want)
		}
	}
	if comparison.Entries[0].Limit != 3 {
		t.Fatalf("observed maximum limit = %v, want 3", comparison.Entries[0].Limit)
	}
	if comparison.Entries[1].Limit != 1.3 {
		t.Fatalf("ratio limit = %v, want 1.3", comparison.Entries[1].Limit)
	}
}

func TestCompareServerRUReportsEveryUnavailableStatusInFingerprintOrder(t *testing.T) {
	baseline := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{
			serverRUBaselineComparisonEntry("q1:collection", 5, 1, 1, 1),
			serverRUBaselineComparisonEntry("q1:incomplete", 5, 1, 1, 1),
			serverRUBaselineComparisonEntry("q1:insufficient", 5, 1, 1, 1),
			serverRUBaselineComparisonEntry("q1:missing-current", 5, 1, 1, 1),
		},
	}
	analysis := Analysis{ServerRUByFingerprint: []FingerprintServerRU{
		serverRUCurrentComparisonEntry("q1:collection", 5, 1, 1, 1),
		serverRUCurrentComparisonEntry("q1:incomplete", 6, 1, 1, 1),
		serverRUCurrentComparisonEntry("q1:insufficient", 4, 1, 1, 1),
		serverRUCurrentComparisonEntry("q1:missing-baseline", 5, 1, 1, 1),
	}}
	analysis.ServerRUByFingerprint[0].Errors = 1
	analysis.ServerRUByFingerprint[1].Samples = 5
	analysis.ServerRUByFingerprint[1].Total = 5

	comparison, err := CompareServerRU(analysis, baseline)
	if err != nil {
		t.Fatalf("CompareServerRU() error = %v", err)
	}
	want := []FingerprintServerRUComparison{
		{Fingerprint: "q1:collection", Status: ServerRUComparisonCollectionError, BaselineCount: 5, BaselineSamples: 5, BaselineMean: 1, BaselineMaximum: 1, CurrentCount: 5, CurrentSamples: 5, CurrentErrors: 1, CurrentMean: 1, Limit: 1.3},
		{Fingerprint: "q1:incomplete", Status: ServerRUComparisonIncompleteCoverage, BaselineCount: 5, BaselineSamples: 5, BaselineMean: 1, BaselineMaximum: 1, CurrentCount: 6, CurrentSamples: 5, CurrentMean: 1, Limit: 1.3},
		{Fingerprint: "q1:insufficient", Status: ServerRUComparisonInsufficientSamples, BaselineCount: 5, BaselineSamples: 5, BaselineMean: 1, BaselineMaximum: 1, CurrentCount: 4, CurrentSamples: 4, CurrentMean: 1, Limit: 1.3},
		{Fingerprint: "q1:missing-baseline", Status: ServerRUComparisonMissingBaseline, CurrentCount: 5, CurrentSamples: 5, CurrentMean: 1},
		{Fingerprint: "q1:missing-current", Status: ServerRUComparisonMissingCurrent, BaselineCount: 5, BaselineSamples: 5, BaselineMean: 1, BaselineMaximum: 1, Limit: 1.3},
	}
	if !reflect.DeepEqual(comparison.Entries, want) {
		t.Fatalf("entries = %#v, want %#v", comparison.Entries, want)
	}
	if got, want := comparison.Summary, (ServerRUComparisonSummary{Fingerprints: 5, Unavailable: 5}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
}

func TestCompareServerRUSaturatesRatioLimit(t *testing.T) {
	baseline := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{{
			Fingerprint: "q1:max",
			Count:       5,
			Samples:     5,
			Total:       math.MaxFloat64,
			Mean:        math.MaxFloat64,
			Minimum:     math.MaxFloat64,
			Maximum:     math.MaxFloat64,
		}},
	}
	analysis := Analysis{ServerRUByFingerprint: []FingerprintServerRU{{
		Fingerprint: "q1:max",
		Count:       5,
		Samples:     5,
		Total:       math.MaxFloat64,
		Mean:        math.MaxFloat64,
		Minimum:     math.MaxFloat64,
		Maximum:     math.MaxFloat64,
	}}}

	comparison, err := CompareServerRU(analysis, baseline)
	if err != nil {
		t.Fatalf("CompareServerRU() error = %v", err)
	}
	if comparison.Entries[0].Limit != math.MaxFloat64 || comparison.Entries[0].Status != ServerRUComparisonPass {
		t.Fatalf("entry = %#v", comparison.Entries[0])
	}
}

func TestCompareServerRURejectsInvalidInputs(t *testing.T) {
	validBaseline := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{
			serverRUBaselineComparisonEntry("q1:valid", 5, 1, 1, 1),
		},
	}
	validCurrent := serverRUCurrentComparisonEntry("q1:valid", 5, 1, 1, 1)
	tests := []struct {
		name     string
		analysis Analysis
		baseline ServerRUBaseline
		want     string
	}{
		{name: "baseline", baseline: ServerRUBaseline{Version: 2}, want: "baseline version is 2"},
		{name: "fingerprint", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Count: 1}}}, want: "requires fingerprint"},
		{name: "unsorted", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{validCurrent, validCurrent}}, want: "unique and sorted"},
		{name: "count", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid"}}}, want: "positive count"},
		{name: "samples", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 1, Samples: 2}}}, want: "invalid sample count"},
		{name: "errors", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 1, Errors: 2}}}, want: "invalid error count"},
		{name: "statistics without samples", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 1, Mean: 1}}}, want: "statistics without samples"},
		{name: "numeric", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 1, Samples: 1, Total: math.NaN()}}}, want: "invalid total"},
		{name: "order", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 1, Samples: 1, Total: 2, Mean: 2, Minimum: 3, Maximum: 2}}}, want: "inconsistent min"},
		{name: "mean", baseline: validBaseline, analysis: Analysis{ServerRUByFingerprint: []FingerprintServerRU{{Fingerprint: "q1:valid", Count: 2, Samples: 2, Total: 4, Mean: 1, Maximum: 1}}}, want: "inconsistent total"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompareServerRU(test.analysis, test.baseline)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompareServerRU() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServerRUComparisonDiagnosticsAreAggregatedAndNotSuppressible(t *testing.T) {
	comparison := ServerRUComparison{
		Summary: ServerRUComparisonSummary{Fingerprints: 3, Passed: 1, Regressions: 1, Unavailable: 1},
		Entries: []FingerprintServerRUComparison{
			{Fingerprint: "q1:pass", Status: ServerRUComparisonPass},
			{Fingerprint: "q1:regression", Status: ServerRUComparisonRegression, BaselineMean: 1, BaselineMaximum: 1.1, CurrentMean: 1.5, Limit: 1.3},
			{Fingerprint: "q1:missing", Status: ServerRUComparisonMissingCurrent},
		},
	}

	diagnostics := comparison.Diagnostics()
	if len(diagnostics) != 2 {
		t.Fatalf("Diagnostics() = %#v, want two diagnostics", diagnostics)
	}
	if diagnostics[0].Code != codeServerRURegression || diagnostics[0].Severity != check.SeverityError || diagnostics[0].Suppressible || len(diagnostics[0].Evidence) != 1 {
		t.Fatalf("regression diagnostic = %#v", diagnostics[0])
	}
	if diagnostics[1].Code != codeServerRUComparisonUnavailable || diagnostics[1].Severity != check.SeverityError || diagnostics[1].Suppressible || len(diagnostics[1].Evidence) != 1 {
		t.Fatalf("unavailable diagnostic = %#v", diagnostics[1])
	}
	if !strings.Contains(diagnostics[0].Evidence[0].Message, "current mean 1.5 RU exceeds limit 1.3 RU") ||
		!strings.Contains(diagnostics[1].Evidence[0].Message, "no current measurement") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestFormatServerRUComparison(t *testing.T) {
	entry := FingerprintServerRUComparison{
		Fingerprint:     "q1:users",
		Status:          ServerRUComparisonRegression,
		BaselineCount:   5,
		BaselineSamples: 5,
		BaselineMean:    1,
		BaselineMaximum: 1.2,
		CurrentCount:    5,
		CurrentSamples:  5,
		CurrentMean:     1.4,
		Limit:           1.3,
	}
	if got, want := FormatFingerprintServerRUComparison(entry), "server_ru_comparison: fingerprint=q1:users status=regression baseline_count=5 baseline_samples=5 baseline_mean=1 baseline_max=1.2 current_count=5 current_samples=5 current_errors=0 current_mean=1.4 limit=1.3"; got != want {
		t.Fatalf("FormatFingerprintServerRUComparison() = %q, want %q", got, want)
	}
	comparison := ServerRUComparison{
		Policy:  ServerRUComparisonPolicy{MinimumSamples: 5, MaximumMeanRatio: 1.3},
		Summary: ServerRUComparisonSummary{Fingerprints: 2, Passed: 1, Regressions: 1},
	}
	if got, want := FormatServerRUComparisonSummary(comparison), "server_ru_comparison_summary: fingerprints=2 passed=1 regressions=1 unavailable=0 minimum_samples=5 maximum_mean_ratio=1.3"; got != want {
		t.Fatalf("FormatServerRUComparisonSummary() = %q, want %q", got, want)
	}
}

func BenchmarkCompareServerRU(b *testing.B) {
	b.Run("1_fingerprint", func(b *testing.B) {
		benchmarkCompareServerRU(b, 1)
	})
	b.Run("10000_fingerprints", func(b *testing.B) {
		benchmarkCompareServerRU(b, 10_000)
	})
}

func benchmarkCompareServerRU(b *testing.B, fingerprintCount int) {
	baselineEntries := make([]FingerprintServerRUBaseline, fingerprintCount)
	currentEntries := make([]FingerprintServerRU, fingerprintCount)
	for index := range fingerprintCount {
		fingerprint := fmt.Sprintf("q1:%08d", index)
		baselineEntries[index] = serverRUBaselineComparisonEntry(fingerprint, 5, 1, 0.9, 1.1)
		currentEntries[index] = serverRUCurrentComparisonEntry(fingerprint, 5, 1.1, 1, 1.2)
	}
	baseline := ServerRUBaseline{Version: ServerRUBaselineVersion, ServerRUByFingerprint: baselineEntries}
	analysis := Analysis{ServerRUByFingerprint: currentEntries}
	var comparison ServerRUComparison
	b.ReportAllocs()
	b.ReportMetric(float64(fingerprintCount), "fingerprints/compare")
	for b.Loop() {
		var err error
		comparison, err = CompareServerRU(analysis, baseline)
		if err != nil {
			b.Fatalf("CompareServerRU() error = %v", err)
		}
	}
	runtimeServerRUComparisonSink = comparison
}

func serverRUBaselineComparisonEntry(fingerprint string, count int, mean, minimum, maximum float64) FingerprintServerRUBaseline {
	return FingerprintServerRUBaseline{
		Fingerprint: fingerprint,
		Count:       count,
		Samples:     count,
		Total:       mean * float64(count),
		Mean:        mean,
		Minimum:     minimum,
		Maximum:     maximum,
	}
}

func serverRUCurrentComparisonEntry(fingerprint string, count int, mean, minimum, maximum float64) FingerprintServerRU {
	return FingerprintServerRU{
		Fingerprint: fingerprint,
		Count:       count,
		Samples:     count,
		Total:       mean * float64(count),
		Mean:        mean,
		Minimum:     minimum,
		Maximum:     maximum,
	}
}

var runtimeServerRUComparisonSink ServerRUComparison
