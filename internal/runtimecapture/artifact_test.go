package runtimecapture

import (
	"strings"
	"testing"
)

func TestDecodeAcceptsJSONLinesAndReturnsNonNilEmpty(t *testing.T) {
	record := `{"version":1,"capture_id":"capture","scope_id":1,"sequence":1,"operation":"SELECT","source":"raw","fingerprint":"s1:test","sql":"SELECT 1","argument_count":0,"started_at":"2026-08-31T00:00:00Z","duration_ns":1,"rows_returned_known":true,"rows_affected_known":false}`
	records, err := Decode(strings.NewReader(record + "\n" + record))
	if err != nil || len(records) != 2 {
		t.Fatalf("Decode() = %#v, %v, want two records", records, err)
	}
	empty, err := Decode(strings.NewReader(""))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("Decode(empty) = %#v, %v, want non-nil empty", empty, err)
	}
}

func TestDecodeRejectsInvalidArtifact(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "malformed", input: `{`, want: "decode runtime capture record 1"},
		{name: "unknown field", input: `{"version":1,"unknown":true}`, want: "unknown field"},
		{name: "version", input: `{"version":2}`, want: "version is 2, want 1"},
		{name: "scope", input: `{"version":1,"capture_id":"capture","operation":"SELECT","fingerprint":"s1:test"}`, want: "positive scope_id"},
		{name: "batch", input: `{"version":1,"capture_id":"capture","scope_id":1,"sequence":1,"operation":"INSERT","fingerprint":"s1:test","batch":{"group":1,"index":2,"count":1,"rows":1,"total_rows":1}}`, want: "invalid batch position"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if records, err := Decode(strings.NewReader(test.input)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Decode() = %#v, %v, want substring %q", records, err, test.want)
			}
		})
	}
}

func TestStatementFingerprintIsStableAndOperationSensitive(t *testing.T) {
	first := StatementFingerprint("SELECT", "SELECT * FROM users WHERE id = ?")
	second := StatementFingerprint("SELECT", "SELECT * FROM users WHERE id = ?")
	if first != second || !strings.HasPrefix(first, "s1:") {
		t.Fatalf("StatementFingerprint() = %q, %q", first, second)
	}
	if first == StatementFingerprint("EXPLAIN", "SELECT * FROM users WHERE id = ?") {
		t.Fatal("StatementFingerprint() ignored operation")
	}
}
