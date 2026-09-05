package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cli "github.com/mayahiro/nagicli-go"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
	"github.com/mayahiro/go-tidb/orm"
)

func TestApplicationAnalyzesCapturedConditionalWritesWithoutRegistration(t *testing.T) {
	t.Parallel()

	var artifact bytes.Buffer
	capture := orm.NewRuntimeCapture(&artifact)
	ctx := orm.WithRuntimeCapture(context.Background(), capture)
	executor := &captureWriteExecutor{}
	now := time.Unix(1000, 0)
	// These are ordinary example repository operations, not diagnostic wrappers.
	if _, err := starterapp.ClaimJobLease(ctx, executor, 7, "private-owner", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := starterapp.DeleteOrdersForUser(ctx, executor, 9); err != nil {
		t.Fatal(err)
	}
	if err := capture.Err(); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 2 || strings.Contains(artifact.String(), "private-owner") {
		t.Fatalf("capture changed work or leaked values: calls=%d artifact=%s", executor.calls, artifact.String())
	}

	directory := t.TempDir()
	for _, test := range []struct {
		name        string
		orderIndex  string
		withSchema  bool
		json        bool
		suppress    bool
		wantWarning int
	}{
		{name: "without_schema"},
		{name: "missing_index_text", withSchema: true, wantWarning: 1},
		{name: "missing_index_json", withSchema: true, json: true, wantWarning: 1},
		{name: "with_index", withSchema: true, json: true, orderIndex: ", KEY by_user (user_id, id)"},
		{name: "suppressed", withSchema: true, json: true, suppress: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const leaseSchema = "CREATE TABLE job_leases (job_id BIGINT PRIMARY KEY, lock_until DATETIME);"
			orderSchema := "CREATE TABLE orders (id BIGINT PRIMARY KEY, user_id BIGINT" + test.orderIndex + ");"
			if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(leaseSchema+orderSchema), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"analyze"}
			if test.withSchema {
				args = append(args, "--schema", "schema.sql")
			}
			if test.json {
				args = append(args, "--json")
			}
			if test.suppress {
				args = append(args, "--suppress", "QRY008=bounded maintenance workload")
			}
			result := runApplicationAt(t, directory, artifact.Bytes(), args...)
			if result.Status() != cli.StatusSuccess || len(result.Stderr()) != 0 {
				t.Fatalf("analyze() status=%d stderr=%q", result.Status(), result.Stderr())
			}
			if !test.json {
				output := string(result.Stdout())
				if test.wantWarning == 1 && (!strings.Contains(output, "WARNING[QRY008] Conditional write has no matching index prefix") || !strings.Contains(output, "Order on orders") || !strings.Contains(output, "Query fingerprint: s1:")) {
					t.Fatalf("missing conditional-write diagnostic: %s", output)
				}
				if !strings.Contains(output, "mutation_shape_statements=2") || strings.Contains(output, "QRY009") || (!test.withSchema && strings.Contains(output, "QRY008")) {
					t.Fatalf("unexpected diagnostic or coverage: %s", output)
				}
				return
			}
			var output runtimeAnalysisJSON
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if output.Statistics.Statements != 2 || output.Statistics.QueryShapeStatements != 0 || output.Statistics.MutationShapeStatements != 2 || output.Statistics.SchemaCheckedStatements != 2 || output.Statistics.MutationIndexCheckedStatements != 2 || output.Statistics.MutationIndexUncertainStatements != 0 || output.Summary.Warnings != test.wantWarning {
				t.Fatalf("coverage = %#v", output)
			}
			if test.suppress && (len(output.Suppressed) != 1 || output.Suppressed[0].Reason != "bounded maintenance workload") {
				t.Fatalf("suppression = %#v", output.Suppressed)
			}
		})
	}
}

func TestApplicationConditionalWriteIndexUncertaintyAndMissingSchema(t *testing.T) {
	t.Parallel()

	var artifact bytes.Buffer
	capture := orm.NewRuntimeCapture(&artifact)
	ctx := orm.WithRuntimeCapture(context.Background(), capture)
	executor := &captureWriteExecutor{}
	if _, err := orm.DeleteWhere[starterapp.User](orm.Or(orm.Equal("ID", int64(1)), orm.Equal("Email", "private-email@example.com"))).Exec(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if err := capture.Err(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		schema string
		code   string
		status int
	}{
		{"index_merge", "CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(100), KEY by_email (email));", "QRY009", int(cli.StatusSuccess)},
		{"missing_table", "CREATE TABLE other (id BIGINT);", "QRY006", int(exitDiagnosticFailure)},
		{"missing_column", "CREATE TABLE users (id BIGINT PRIMARY KEY);", "QRY006", int(exitDiagnosticFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte(test.schema), 0o600); err != nil {
				t.Fatal(err)
			}
			result := runApplicationAt(t, directory, artifact.Bytes(), "analyze", "--schema", "schema.sql", "--json")
			if int(result.Status()) != test.status {
				t.Fatalf("status=%d stderr=%q", result.Status(), result.Stderr())
			}
			var output runtimeAnalysisJSON
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != test.code || output.Statistics.MutationIndexCheckedStatements != 0 {
				t.Fatalf("analysis = %#v", output)
			}
			if test.code == "QRY009" && (output.Statistics.MutationIndexUncertainStatements != 1 || output.Summary.Info != 1) {
				t.Fatalf("uncertainty was not explicit: %#v", output)
			}
			if test.code == "QRY006" && (output.Diagnostics[0].Suppressible || output.Summary.Errors != 1) {
				t.Fatalf("invalid schema did not block analysis: %#v", output)
			}
		})
	}
}
