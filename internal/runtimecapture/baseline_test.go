package runtimecapture

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestNewServerRUBaselineCopiesValidatedStatistics(t *testing.T) {
	t.Parallel()

	analysis := Analysis{ServerRUByFingerprint: []FingerprintServerRU{
		{Fingerprint: "q1:first", Count: 3, Samples: 2, Total: 4, Mean: 2, Minimum: 1, Maximum: 3},
		{Fingerprint: "s1:second", Count: 1, Samples: 1, Total: 1.25, Mean: 1.25, Minimum: 1.25, Maximum: 1.25},
	}}
	baseline, err := NewServerRUBaseline(analysis)
	if err != nil {
		t.Fatalf("NewServerRUBaseline() error = %v", err)
	}
	want := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{
			{Fingerprint: "q1:first", Count: 3, Samples: 2, Total: 4, Mean: 2, Minimum: 1, Maximum: 3},
			{Fingerprint: "s1:second", Count: 1, Samples: 1, Total: 1.25, Mean: 1.25, Minimum: 1.25, Maximum: 1.25},
		},
	}
	if !reflect.DeepEqual(baseline, want) {
		t.Fatalf("NewServerRUBaseline() = %#v, want %#v", baseline, want)
	}
	analysis.ServerRUByFingerprint[0].Mean = 99
	if baseline.ServerRUByFingerprint[0].Mean != 2 {
		t.Fatalf("baseline aliases analysis storage: %#v", baseline)
	}
}

func TestNewServerRUBaselineRejectsIncompleteStatistics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statistics []FingerprintServerRU
		want       string
	}{
		{name: "no collection", want: "at least one successful sample"},
		{
			name:       "error-only collection",
			statistics: []FingerprintServerRU{{Fingerprint: "q1:first", Count: 1, Errors: 1}},
			want:       "ServerRU collection errors: 1",
		},
		{
			name:       "successful sample with error",
			statistics: []FingerprintServerRU{{Fingerprint: "q1:first", Count: 1, Samples: 1, Errors: 1, Total: 1, Mean: 1, Minimum: 1, Maximum: 1}},
			want:       "ServerRU collection errors: 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewServerRUBaseline(Analysis{ServerRUByFingerprint: test.statistics})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewServerRUBaseline() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServerRUBaselineValidateRejectsInvalidData(t *testing.T) {
	t.Parallel()

	valid := FingerprintServerRUBaseline{
		Fingerprint: "q1:first",
		Count:       2,
		Samples:     2,
		Total:       4,
		Mean:        2,
		Minimum:     1,
		Maximum:     3,
	}
	tests := []struct {
		name     string
		baseline ServerRUBaseline
		want     string
	}{
		{name: "version", baseline: ServerRUBaseline{Version: 2, ServerRUByFingerprint: []FingerprintServerRUBaseline{valid}}, want: "version is 2, want 1"},
		{name: "fingerprint", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Count: 1, Samples: 1}}}, want: "requires fingerprint"},
		{name: "unsorted", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "s1:z", Count: 1, Samples: 1}, {Fingerprint: "q1:a", Count: 1, Samples: 1}}}, want: "unique and sorted"},
		{name: "duplicate", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{valid, valid}}, want: "unique and sorted"},
		{name: "count", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Samples: 1}}}, want: "positive count"},
		{name: "samples", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 1, Samples: 2}}}, want: "invalid sample count"},
		{name: "negative total", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 1, Samples: 1, Total: -1}}}, want: "invalid total"},
		{name: "statistics order", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 1, Samples: 1, Total: 2, Mean: 2, Minimum: 3, Maximum: 4}}}, want: "inconsistent min, mean, and max"},
		{name: "mean", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 2, Samples: 2, Total: 4, Mean: 1, Maximum: 1}}}, want: "inconsistent total, sample count, and mean"},
		{name: "saturated total mean", baseline: ServerRUBaseline{Version: 1, ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 2, Samples: 2, Total: math.MaxFloat64}}}, want: "inconsistent total, sample count, and mean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.baseline.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServerRUBaselineEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	baseline := ServerRUBaseline{
		Version: ServerRUBaselineVersion,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{{
			Fingerprint: "q1:<users>",
			Count:       1,
			Samples:     1,
			Total:       1.5,
			Mean:        1.5,
			Minimum:     1.5,
			Maximum:     1.5,
		}},
	}
	var output bytes.Buffer
	if err := EncodeServerRUBaseline(&output, baseline); err != nil {
		t.Fatalf("EncodeServerRUBaseline() error = %v", err)
	}
	const want = `{"version":1,"server_ru_by_fingerprint":[{"fingerprint":"q1:<users>","count":1,"samples":1,"total":1.5,"mean":1.5,"min":1.5,"max":1.5}]}` + "\n"
	if got := output.String(); got != want {
		t.Fatalf("encoded baseline = %q, want %q", got, want)
	}
	decoded, err := DecodeServerRUBaseline(&output)
	if err != nil {
		t.Fatalf("DecodeServerRUBaseline() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, baseline) {
		t.Fatalf("decoded baseline = %#v, want %#v", decoded, baseline)
	}
}

func TestNewServerRUBaselineAcceptsSaturatedTotal(t *testing.T) {
	t.Parallel()

	baseline, err := NewServerRUBaseline(Analysis{ServerRUByFingerprint: []FingerprintServerRU{{
		Fingerprint: "q1:first",
		Count:       2,
		Samples:     2,
		Total:       math.MaxFloat64,
		Mean:        math.MaxFloat64,
		Minimum:     math.MaxFloat64,
		Maximum:     math.MaxFloat64,
	}}})
	if err != nil {
		t.Fatalf("NewServerRUBaseline() error = %v", err)
	}
	if baseline.ServerRUByFingerprint[0].Total != math.MaxFloat64 {
		t.Fatalf("baseline = %#v", baseline)
	}
}

func TestDecodeServerRUBaselineRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	valid := `{"version":1,"server_ru_by_fingerprint":[{"fingerprint":"q1:first","count":1,"samples":1,"total":1,"mean":1,"min":1,"max":1}]}`
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "malformed", input: `{`, want: "decode ServerRU baseline"},
		{name: "unknown field", input: strings.Replace(valid, `"version":1`, `"version":1,"unknown":true`, 1), want: "unknown field"},
		{name: "analysis-only error field", input: strings.Replace(valid, `"samples":1`, `"samples":1,"errors":0`, 1), want: "unknown field"},
		{name: "trailing value", input: valid + ` {}`, want: "more than one JSON value"},
		{name: "new version", input: strings.Replace(valid, `"version":1`, `"version":2`, 1), want: "version is 2, want 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeServerRUBaseline(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeServerRUBaseline() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestServerRUBaselineIORejectsNilAndPropagatesWriteError(t *testing.T) {
	t.Parallel()

	if _, err := DecodeServerRUBaseline(nil); err == nil || !strings.Contains(err.Error(), "input is nil") {
		t.Fatalf("DecodeServerRUBaseline(nil) error = %v", err)
	}
	baseline := ServerRUBaseline{
		Version:               1,
		ServerRUByFingerprint: []FingerprintServerRUBaseline{{Fingerprint: "q1:first", Count: 1, Samples: 1}},
	}
	if err := EncodeServerRUBaseline(nil, baseline); err == nil || !strings.Contains(err.Error(), "output is nil") {
		t.Fatalf("EncodeServerRUBaseline(nil) error = %v", err)
	}
	want := errors.New("write failed")
	if err := EncodeServerRUBaseline(baselineFailingWriter{err: want}, baseline); !errors.Is(err, want) {
		t.Fatalf("EncodeServerRUBaseline() error = %v, want %v", err, want)
	}
}

func BenchmarkNewServerRUBaseline(b *testing.B) {
	b.Run("1_fingerprint", func(b *testing.B) {
		benchmarkNewServerRUBaseline(b, 1)
	})
	b.Run("10000_fingerprints", func(b *testing.B) {
		benchmarkNewServerRUBaseline(b, 10_000)
	})
}

func benchmarkNewServerRUBaseline(b *testing.B, fingerprintCount int) {
	statistics := make([]FingerprintServerRU, fingerprintCount)
	for index := range statistics {
		statistics[index] = FingerprintServerRU{
			Fingerprint: fmt.Sprintf("q1:%08d", index),
			Count:       1,
			Samples:     1,
			Total:       1,
			Mean:        1,
			Minimum:     1,
			Maximum:     1,
		}
	}
	var baseline ServerRUBaseline
	var err error
	b.ReportAllocs()
	b.ReportMetric(float64(fingerprintCount), "fingerprints/baseline")
	for b.Loop() {
		baseline, err = NewServerRUBaseline(Analysis{ServerRUByFingerprint: statistics})
	}
	if err != nil {
		b.Fatalf("NewServerRUBaseline() error = %v", err)
	}
	runtimeBaselineSink = baseline
}

var runtimeBaselineSink ServerRUBaseline

type baselineFailingWriter struct {
	err error
}

func (writer baselineFailingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
