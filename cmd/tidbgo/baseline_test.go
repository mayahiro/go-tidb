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

	second := runtimeCommandRecord(1, "s1:second")
	second.ServerRU = &runtimecapture.ServerRU{Known: true, Value: 2, AuxiliaryStatements: 1}
	first := runtimeCommandRecord(2, "q1:first")
	first.ServerRU = &runtimecapture.ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
	result := runApplicationWithInput(t, runtimeCaptureInput(t, second, first), "baseline")
	if got, want := result.Status(), cli.StatusSuccess; got != want {
		t.Fatalf("status = %d, want %d, stderr = %q", got, want, result.Stderr())
	}
	const want = `{"version":1,"server_ru_by_fingerprint":[{"fingerprint":"q1:first","count":1,"samples":1,"total":1.25,"mean":1.25,"min":1.25,"max":1.25},{"fingerprint":"s1:second","count":1,"samples":1,"total":2,"mean":2,"min":2,"max":2}]}` + "\n"
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
	record := runtimeCommandRecord(1, "q1:first")
	record.ServerRU = &runtimecapture.ServerRU{Known: true, Value: 1, AuxiliaryStatements: 1}
	if err := os.WriteFile(filepath.Join(directory, "runtime.jsonl"), runtimeCaptureInput(t, record), 0o600); err != nil {
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
			want:  "at least one successful sample",
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
