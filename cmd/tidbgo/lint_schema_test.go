package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"
)

const lintSchemaSource = `package application
import "github.com/mayahiro/go-tidb/model"
type Item struct {
 model.Meta ` + "`tidbgo:\"table=items\"`" + `
 ID int64 ` + "`tidbgo:\",pk\"`" + `
 Code string ` + "`tidbgo:\",unique=code\"`" + `
 Label string
}
func init() { panic("lint must not execute this package") }
`

const lintSchemaSnapshot = `CREATE TABLE items (
 id BIGINT PRIMARY KEY,
 code VARCHAR(64) NOT NULL,
 label VARCHAR(64) NOT NULL,
 UNIQUE KEY item_code (code)
);`

func TestApplicationLintSchemaSnapshots(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "model.go"), lintSchemaSource)
	for _, tc := range []struct {
		name, snapshot, code string
		error                bool
	}{
		{name: "before", snapshot: lintSchemaSnapshot},
		{name: "removed_column", snapshot: strings.ReplaceAll(lintSchemaSnapshot, " label VARCHAR(64) NOT NULL,\n", ""), code: "CMP003", error: true},
		{name: "removed_unique", snapshot: strings.ReplaceAll(lintSchemaSnapshot, "UNIQUE KEY", "KEY"), code: "CMP015", error: true},
		{name: "required_column", snapshot: strings.ReplaceAll(lintSchemaSnapshot, " label VARCHAR(64) NOT NULL,", " label VARCHAR(64) NOT NULL,\n region VARCHAR(32) NOT NULL,"), code: "CMP010"},
		{name: "rollback_target", snapshot: lintSchemaSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.name + ".sql"
			writeLintFile(t, filepath.Join(directory, file), tc.snapshot)
			result := runApplicationAt(t, directory, nil, "lint", "--schema", file, "--json")
			wantStatus := cli.StatusSuccess
			if tc.error {
				wantStatus = exitDiagnosticFailure
			}
			if result.Status() != wantStatus || len(result.Stderr()) != 0 {
				t.Fatalf("status=%d stderr=%q", result.Status(), result.Stderr())
			}
			var output sourceAnalysisJSON
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if output.Statistics.SchemaModels != 1 || output.Statistics.AnalyzedSchemaModels != 1 || output.Statistics.UncertainSchemaModels != 0 {
				t.Fatalf("schema coverage = %+v", output.Statistics)
			}
			if tc.code == "" {
				if len(output.Diagnostics) != 0 {
					t.Fatalf("unexpected diagnostics: %+v", output.Diagnostics)
				}
				return
			}
			if len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != tc.code || output.Diagnostics[0].Location.Path != "model.go" {
				t.Fatalf("diagnostics = %+v", output.Diagnostics)
			}
			if tc.error && output.Summary.Errors != 1 || !tc.error && output.Summary.Warnings != 1 {
				t.Fatalf("summary = %+v", output.Summary)
			}
			if strings.Contains(string(result.Stdout()), directory) {
				t.Fatal("diagnostics must use input-relative source paths")
			}
		})
	}
	if result := runApplicationAt(t, directory, nil, "lint"); result.Status() != cli.StatusSuccess || !strings.Contains(string(result.Stdout()), "schema_models=0 analyzed_schema_models=0 uncertain_schema_models=0") {
		t.Fatalf("schema-free lint changed: %q %q", result.Stdout(), result.Stderr())
	}
}

func TestApplicationLintSchemaSuppressionPolicy(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "model.go"), lintSchemaSource)
	required := strings.ReplaceAll(lintSchemaSnapshot, " label VARCHAR(64) NOT NULL,", " label VARCHAR(64) NOT NULL,\n region VARCHAR(32) NOT NULL, category VARCHAR(32) NOT NULL,")
	writeLintFile(t, filepath.Join(directory, "schema.sql"), required)
	result := runApplicationAt(t, directory, nil, "lint", "--schema", "schema.sql", "--json", "--suppress", "CMP010=read-only projection")
	if result.Status() != cli.StatusSuccess {
		t.Fatalf("warning suppression: %q", result.Stderr())
	}
	var output sourceAnalysisJSON
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Summary.Suppressed != 2 || output.Summary.Warnings != 0 || len(output.Diagnostics) != 0 || len(output.Suppressed) != 2 {
		t.Fatalf("suppressed required columns = %+v", output)
	}
	for _, diagnostic := range output.Suppressed {
		if diagnostic.Reason != "read-only projection" || diagnostic.Diagnostic.Code != "CMP010" {
			t.Fatalf("suppression reason not retained: %+v", diagnostic)
		}
	}
	writeLintFile(t, filepath.Join(directory, "schema.sql"), strings.ReplaceAll(lintSchemaSnapshot, " label VARCHAR(64) NOT NULL,\n", ""))
	result = runApplicationAt(t, directory, nil, "lint", "--schema", "schema.sql", "--suppress", "CMP003=ignore missing mapping")
	if result.Status() != exitUsage || !strings.Contains(string(result.Stderr()), "CMP003 is not suppressible") {
		t.Fatalf("mapping errors must not be suppressible: status=%d stderr=%q", result.Status(), result.Stderr())
	}
}

func TestApplicationLintSchemaReportsIncompleteCoverage(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "model.go"), "package application\nimport \"github.com/mayahiro/go-tidb/model\"\ntype Base struct { ID int64 }; type Item struct { model.Meta; Base }")
	writeLintFile(t, filepath.Join(directory, "schema.sql"), "CREATE TABLE item (id BIGINT PRIMARY KEY);")
	result := runApplicationAt(t, directory, nil, "lint", "--schema", "schema.sql", "--json")
	if result.Status() != cli.StatusSuccess {
		t.Fatalf("status=%d stderr=%q", result.Status(), result.Stderr())
	}
	var output sourceAnalysisJSON
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Statistics.SchemaModels != 1 || output.Statistics.AnalyzedSchemaModels != 0 || output.Statistics.UncertainSchemaModels != 1 || output.Summary.Info != 1 || len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != "SRC002" {
		t.Fatalf("incomplete source mapping must remain explicit: %+v", output)
	}
}
