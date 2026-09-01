package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

func TestApplicationBaselineWritesVersionedServerRUStatistics(t *testing.T) {
	t.Parallel()

	records := append(
		runtimeServerRURecords("s1:second", 2, 2, 2, 2, 2),
		runtimeServerRURecords("q1:first", 1.25, 1.25, 1.25, 1.25, 1.25)...,
	)
	for index := range records {
		records[index].Sequence = uint64(index + 1)
		records[index].ScopeID = uint64(index + 1)
	}
	result := runApplicationWithInput(t, runtimeCaptureInput(t, records...), "baseline")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	const want = `{"version":1,"server_ru_by_fingerprint":[{"fingerprint":"q1:first","count":5,"samples":5,"total":6.25,"mean":1.25,"min":1.25,"max":1.25},{"fingerprint":"s1:second","count":5,"samples":5,"total":10,"mean":2,"min":2,"max":2}]}` + "\n"
	if got := string(result.Stdout()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := result.Stderr(); len(got) != 0 {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestApplicationBaselineReadsExplicitFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	records := runtimeServerRURecords("q1:first", 1, 1, 1, 1, 1)
	if err := os.WriteFile(filepath.Join(directory, "runtime.jsonl"), runtimeCaptureInput(t, records...), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result := runApplicationAt(t, directory, nil, "baseline", "runtime.jsonl")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
}

func TestApplicationBaselineRejectsIncompleteCapture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "no samples",
			input: runtimeCaptureInput(t, runtimeCommandRecord(1, "q1:first")),
			want:  "at least 5 successful samples",
		},
		{
			name: "collection error",
			input: func() []byte {
				record := runtimeCommandRecord(1, "q1:first")
				record.ServerRU = &runtimecapture.ServerRU{AuxiliaryStatements: 1, Error: "read failed"}
				return runtimeCaptureInput(t, record)
			}(),
			want: "ServerRU collection errors: 1",
		},
		{
			name:  "insufficient samples",
			input: runtimeCaptureInput(t, runtimeServerRURecords("q1:first", 1, 1, 1, 1)...),
			want:  "at least 5 samples",
		},
		{
			name: "incomplete measurement coverage",
			input: func() []byte {
				records := runtimeServerRURecords("q1:first", 1, 1, 1, 1)
				unsampled := runtimeCommandRecord(5, "q1:first")
				unsampled.ScopeID = 5
				records = append(records, unsampled)
				return runtimeCaptureInput(t, records...)
			}(),
			want: "complete measurement coverage",
		},
		{
			name:  "invalid artifact",
			input: []byte(`{"version":2}`),
			want:  "runtime capture version is 2, want 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := runApplicationWithInput(t, test.input, "baseline")
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got := result.Stdout(); len(got) != 0 {
				t.Fatalf("stdout = %q, want empty", got)
			}
			if stderr := string(result.Stderr()); !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, test.want)
			}
		})
	}
}

func TestApplicationBaselineReportsMissingFileWithoutResolvedPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	result := runApplicationAt(t, directory, nil, "baseline", "missing.jsonl")
	if got, want := result.Status(), exitInternalError; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	stderr := string(result.Stderr())
	if !strings.Contains(stderr, `open runtime capture input "missing.jsonl"`) || strings.Contains(stderr, directory) {
		t.Fatalf("stderr = %q, want user-supplied path without resolved directory", stderr)
	}
}
