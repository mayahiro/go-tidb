package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/orm"
)

func TestApplicationWorkloadBaselineFromActualRuntimeCapture(t *testing.T) {
	t.Parallel()
	// No repository instrumentation or statement registration: each existing
	// example function receives only the capture context and database executor.
	baselineInput := captureWorkloadExample(t, 5, 2)
	created := runApplicationWithInput(t, baselineInput, "baseline", "--workload", "update-users-2")
	if created.Status() != cli.StatusSuccess || len(created.Stderr()) != 0 {
		t.Fatalf("baseline status=%d stderr=%q", created.Status(), created.Stderr())
	}
	baseline, err := runtimecapture.DecodeServerRUBaseline(bytes.NewReader(created.Stdout()))
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Version != 1 || baseline.Workload == nil || baseline.Workload.Scopes != 5 || baseline.Workload.ServerRU.Mean != 2.5 || baseline.Workload.StatementCount.Mean != 2 || baseline.Workload.TransactionStatements != 10 {
		t.Fatalf("baseline = %#v", baseline)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "baseline.json"), created.Stdout(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "schema.sql"), []byte("CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255));"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		calls, scopes int
		wantStatus    int
	}{
		{"same_operation_more_repetitions", 2, 7, int(cli.StatusSuccess)},
		{"repeat_growth", 20, 5, int(exitDiagnosticFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := captureWorkloadExample(t, test.scopes, test.calls)
			for _, jsonOutput := range []bool{false, true} {
				args := []string{"analyze", "--workload", "update-users-2", "--baseline", "baseline.json", "--schema", "schema.sql"}
				if jsonOutput {
					args = append(args, "--json")
				}
				result := runApplicationAt(t, directory, input, args...)
				if int(result.Status()) != test.wantStatus || len(result.Stderr()) != 0 {
					t.Fatalf("status=%d stdout=%s stderr=%s", result.Status(), result.Stdout(), result.Stderr())
				}
				if !jsonOutput {
					for _, want := range []string{"server_ru_workload: name=update-users-2", "server_ru_workload_comparison:", "complete_scopes=", "transaction_statements=", "statement_mean="} {
						if !strings.Contains(string(result.Stdout()), want) {
							t.Fatalf("missing %q in %s", want, result.Stdout())
						}
					}
					continue
				}
				var output runtimeAnalysisJSON
				if err := json.Unmarshal(result.Stdout(), &output); err != nil {
					t.Fatal(err)
				}
				if output.Workload == nil || output.Workload.CompleteScopes != test.scopes || output.Workload.Samples != test.scopes*test.calls || output.Workload.StatementCount.Mean != float64(test.calls) || output.Workload.ServerRU.Mean != float64(test.calls)*1.25 || output.ServerRUComparison.Summary.Regressions != 0 || output.ServerRUComparison.Summary.Unavailable != 0 {
					t.Fatalf("analysis = %#v", output)
				}
				if test.wantStatus == int(exitDiagnosticFailure) && (output.Summary.Errors != 1 || output.Diagnostics[0].Code != "RU003" || output.Diagnostics[0].Suppressible || !output.ServerRUComparison.Workload.StatementsRegressed || !output.ServerRUComparison.Workload.ServerRURegressed) {
					t.Fatalf("missing operation regression: %#v", output)
				}
			}
		})
	}
	// An explicit current declaration is required; scope IDs and a saved name
	// cannot tell the CLI what the current application operation means.
	for _, extra := range [][]string{nil, {"--workload", "another-operation"}} {
		args := append([]string{"analyze", "--baseline", "baseline.json"}, extra...)
		result := runApplicationAt(t, directory, baselineInput, args...)
		if result.Status() != exitDiagnosticFailure || !strings.Contains(string(result.Stdout()), "ERROR[RU004]") {
			t.Fatalf("missing identity guard: status=%d stdout=%s stderr=%s", result.Status(), result.Stdout(), result.Stderr())
		}
	}
	for _, code := range []string{"RU003", "RU004"} {
		input := captureWorkloadExample(t, 5, 20)
		args := []string{"analyze", "--baseline", "baseline.json", "--suppress", code + "=accepted"}
		if code == "RU003" {
			args = append(args, "--workload", "update-users-2")
		}
		result := runApplicationAt(t, directory, input, args...)
		if result.Status() != exitUsage || !strings.Contains(string(result.Stderr()), "not suppressible") {
			t.Fatalf("suppression status=%d stdout=%s stderr=%s", result.Status(), result.Stdout(), result.Stderr())
		}
	}
}

func TestApplicationWorkloadRequiresCoverageAndExplicitValidName(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"analyze", "baseline"} {
		for _, name := range []string{"", "line\nbreak", "-prefix", strings.Repeat("x", 129)} {
			result := runApplicationWithInput(t, nil, command, "--workload", name)
			if result.Status() != exitUsage || len(result.Stdout()) != 0 {
				t.Fatalf("%s accepted %q: status=%d stderr=%s", command, name, result.Status(), result.Stderr())
			}
		}
	}
	// The unmodified capture API still writes v1 records with no workload label.
	// Declaring a workload is an offline input contract, not a record filter.
	records := runtimeServerRURecords("s1:query", 1, 1, 1, 1, 1)
	for index := range records {
		records[index].ScopeID = 1
	}
	result := runApplicationWithInput(t, runtimeCaptureInput(t, records...), "baseline", "--workload", "single-operation")
	if result.Status() != exitUsage || !strings.Contains(string(result.Stderr()), "five complete scopes") {
		t.Fatalf("statement samples counted as scopes: %s", result.Stderr())
	}
	for index := range records {
		records[index].ScopeID = uint64(index + 1)
	}
	records[0].ServerRU = nil
	result = runApplicationWithInput(t, runtimeCaptureInput(t, records...), "baseline", "--workload", "incomplete")
	if result.Status() != exitUsage || !strings.Contains(string(result.Stderr()), "complete ServerRU coverage") {
		t.Fatalf("missing coverage guard: %s", result.Stderr())
	}
	result = runApplicationWithInput(t, runtimeCaptureInput(t, records...), "analyze", "--workload", "incomplete", "--json")
	if result.Status() != cli.StatusSuccess {
		t.Fatalf("descriptive analysis failed: %s", result.Stderr())
	}
	var output runtimeAnalysisJSON
	if err := json.Unmarshal(result.Stdout(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Workload.Scopes != 5 || output.Workload.CompleteScopes != 4 || output.Workload.Samples != 4 || output.Workload.ServerRU.Total != 4 || output.ServerRUComparison != nil {
		t.Fatalf("coverage = %#v", output)
	}
}

func captureWorkloadExample(t *testing.T, scopes, calls int) []byte {
	t.Helper()
	connector := &workloadTestConnector{}
	database := sql.OpenDB(connector)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	var artifact bytes.Buffer
	capture := orm.NewRuntimeCapture(&artifact)
	for range scopes {
		ctx := orm.WithRuntimeCapture(context.Background(), capture, orm.CollectServerRU())
		if err := orm.Transaction(ctx, database, func(tx *sql.Tx) error {
			for index := range calls {
				value := starterapp.User{ID: int64(index + 1), Email: "private-workload-value@example.com"}
				rows, err := starterapp.UpdateUserEmail(ctx, tx, &value)
				if err != nil {
					return err
				}
				if rows != 1 {
					return fmt.Errorf("rows = %d, want 1", rows)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := capture.Err(); err != nil {
		t.Fatal(err)
	}
	if connector.targets != scopes*calls || connector.probes != scopes*calls || connector.begins != scopes || connector.commits != scopes {
		t.Fatalf("unexpected DB work: %#v", connector)
	}
	if strings.Contains(artifact.String(), "private-workload-value") || strings.Contains(artifact.String(), `"workload"`) {
		t.Fatalf("artifact leaked values or changed runtime format: %s", artifact.String())
	}
	return artifact.Bytes()
}

// A database/sql connector exercises real ORM transaction and automatic RU
// observation paths, without an actual database or driver dependency.
type workloadTestConnector struct{ targets, probes, begins, commits int }

func (c *workloadTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &workloadTestConn{state: c}, nil
}
func (*workloadTestConnector) Driver() driver.Driver { return workloadTestDriver{} }

type workloadTestDriver struct{}

func (workloadTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type workloadTestConn struct{ state *workloadTestConnector }

func (*workloadTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*workloadTestConn) Close() error { return nil }
func (c *workloadTestConn) Begin() (driver.Tx, error) {
	c.state.begins++
	return workloadTestTx{state: c.state}, nil
}
func (c *workloadTestConn) ExecContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(statement, "UPDATE ") {
		return nil, fmt.Errorf("unexpected target: %s", statement)
	}
	c.state.targets++
	return driver.RowsAffected(1), nil
}
func (c *workloadTestConn) QueryContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	if statement != "SELECT @@tidb_last_query_info" {
		return nil, fmt.Errorf("unexpected query: %s", statement)
	}
	c.state.probes++
	return &workloadTestRows{}, nil
}

type workloadTestTx struct{ state *workloadTestConnector }

func (tx workloadTestTx) Commit() error { tx.state.commits++; return nil }
func (workloadTestTx) Rollback() error  { return nil }

type workloadTestRows struct{ read bool }

func (*workloadTestRows) Columns() []string { return []string{"@@tidb_last_query_info"} }
func (*workloadTestRows) Close() error      { return nil }
func (rows *workloadTestRows) Next(values []driver.Value) error {
	if rows.read {
		return io.EOF
	}
	rows.read = true
	values[0] = `{"ru_consumption":1.25}`
	return nil
}
