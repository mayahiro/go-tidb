package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/referencecheck"
	cli "github.com/mayahiro/nagicli-go"
	"github.com/mayahiro/nagicli-go/clitest"
)

type auditTestConnector struct {
	values []int64
	failAt int
	calls  int
}

func (c *auditTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &auditTestConnection{c}, nil
}
func (*auditTestConnector) Driver() driver.Driver { return auditTestDriver{} }

type auditTestDriver struct{}

func (auditTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unexpected Open") }

type auditTestConnection struct{ c *auditTestConnector }

func (*auditTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*auditTestConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (*auditTestConnection) Close() error { return nil }
func (c *auditTestConnection) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := c.c.calls
	c.c.calls++
	if i == c.c.failAt {
		return nil, errors.New("secret credentials and row values")
	}
	return &auditTestRows{value: c.c.values[i]}, nil
}

type auditTestRows struct {
	value int64
	done  bool
}

func (*auditTestRows) Columns() []string { return []string{"orphan"} }
func (*auditTestRows) Close() error      { return nil }
func (r *auditTestRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

func TestAuditExecutionStatusAndPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   []int64
		failAt   int
		status   cli.ExitStatus
		complete bool
		codes    string
	}{
		{"clean", []int64{0, 0}, -1, cli.StatusSuccess, true, ""},
		{"orphan", []int64{1, 0}, -1, exitDiagnosticFailure, true, "REF005"},
		{"partial", []int64{1}, 1, exitInternalError, false, "REF006 REF005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector := &auditTestConnector{values: tc.values, failAt: tc.failAt}
			db := sql.OpenDB(connector)
			defer db.Close()
			ref := referencecheck.Reference{Name: "Child.Parent", ChildTable: "child", ChildColumns: []string{"parent_id"}, ParentTable: "parent", ParentColumns: []string{"id"}}
			output := auditOutput{Plan: []auditProbe{{Reference: ref}, {Reference: ref}}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			status := executeAudit(ctx, db, &output)
			if status != tc.status || output.Complete != tc.complete || len(output.Results) != 2 || !output.Results[0].Checked || output.Results[1].Checked != tc.complete {
				t.Fatalf("status=%d output=%+v", status, output)
			}
			var codes []string
			for _, d := range output.Diagnostics {
				codes = append(codes, d.Code)
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(codes, " ") != tc.codes || strings.Contains(string(encoded), "secret") {
				t.Fatalf("diagnostics=%s", encoded)
			}
		})
	}
}

func runAuditAt(t *testing.T, directory string, environment map[string]string, args ...string) clitest.Result {
	t.Helper()
	driver := clitest.New(application("dev")).Policy(runtimePolicy()).CurrentDirectory(directory).Arguments(args...)
	for name, value := range environment {
		driver.Environment(name, value)
	}
	result, err := driver.Run()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

const auditSource = "package application\nimport \"github.com/mayahiro/go-tidb/model\"\n" +
	"type Parent struct { model.Meta; ID int64 `tidbgo:\",pk\"`; Children []Child `tidbgo:\"has_many,join=ID:ParentID\"` }\n" +
	"type Child struct { ID int64 `tidbgo:\",pk\"`; ParentID *int64; Parent *Parent `tidbgo:\"belongs_to,join=ParentID:ID\"` }\n" +
	"func init(){panic(\"must not execute user code\")}\n"
const auditSQL = "CREATE TABLE parent(id BIGINT PRIMARY KEY);CREATE TABLE child(id BIGINT PRIMARY KEY,parent_id BIGINT,KEY parent_lookup(parent_id));"

func TestAuditDryRunAndSelection(t *testing.T) {
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "models.go"), auditSource)
	writeLintFile(t, filepath.Join(directory, "schema.sql"), auditSQL)
	for _, selection := range [][]string{nil, {"--relation", "Parent.Children"}, {"--relation", "Parent.Children", "--relation", "Child.Parent"}} {
		args := append([]string{"audit", "--schema", "schema.sql", "--dry-run", "--json"}, selection...)
		result := runAuditAt(t, directory, map[string]string{"TIDBGO_DSN": "invalid secret DSN"}, args...)
		if result.Status() != cli.StatusSuccess || len(result.Stderr()) != 0 {
			t.Fatalf("status=%d stderr=%s", result.Status(), result.Stderr())
		}
		var output auditOutput
		if err := json.Unmarshal(result.Stdout(), &output); err != nil {
			t.Fatal(err)
		}
		if !output.DryRun || output.Complete || len(output.Results) != 0 || len(output.Plan) != 1 || len(output.Diagnostics) != 0 || output.Statistics.AnalyzedSchemaRelations != 2 {
			t.Fatalf("output=%+v", output)
		}
		if len(selection) == 0 && len(output.Plan[0].AlsoDeclaredBy) != 1 {
			t.Fatal("inverse references must share one probe")
		}
		if !strings.HasPrefix(output.Plan[0].SQL, "SELECT EXISTS") || strings.Contains(string(result.Stdout()), directory) || strings.Contains(string(result.Stdout()), "secret") {
			t.Fatalf("unsafe plan output: %s", result.Stdout())
		}
	}
	result := runApplicationAt(t, directory, nil, "audit", "--schema", "schema.sql", "--dry-run")
	if result.Status() != cli.StatusSuccess || !strings.Contains(string(result.Stdout()), "complete=false references=1 checked=0") {
		t.Fatalf("text=%s stderr=%s", result.Stdout(), result.Stderr())
	}
}

func TestAuditPreflightRejectsErrorsAndIncompleteMappings(t *testing.T) {
	for _, tc := range []struct{ name, source, sql string }{
		{"no relations", "package app\ntype DTO struct{ID int64}", auditSQL},
		{"missing column", auditSource, strings.ReplaceAll(auditSQL, "parent_id", "other_id")},
		{"unknown relation", strings.Replace(auditSource, "join=ID:ParentID", "join=ID:Unknown", 1), auditSQL},
		{"missing model", strings.Replace(auditSource, "type Child struct", "type Hidden struct", 1), auditSQL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			writeLintFile(t, filepath.Join(directory, "model.go"), tc.source)
			writeLintFile(t, filepath.Join(directory, "schema.sql"), tc.sql)
			result := runAuditAt(t, directory, map[string]string{"TIDBGO_DSN": "must not parse or connect"}, "audit", "--schema", "schema.sql", "--json")
			if result.Status() != exitDiagnosticFailure || len(result.Stderr()) != 0 {
				t.Fatalf("status=%d stderr=%s", result.Status(), result.Stderr())
			}
			var output auditOutput
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if output.Complete || len(output.Results) != 0 || !strings.Contains(string(result.Stdout()), "REF006") {
				t.Fatalf("incomplete output=%+v", output)
			}
		})
	}
}

func TestAuditOptionsAndConnectionBoundary(t *testing.T) {
	directory := t.TempDir()
	writeLintFile(t, filepath.Join(directory, "models.go"), auditSource)
	writeLintFile(t, filepath.Join(directory, "schema.sql"), auditSQL)
	for _, args := range [][]string{
		{"audit", "--dry-run"},
		{"audit", "--schema", "schema.sql"},
		{"audit", "--schema", "schema.sql", "--dry-run", "--timeout", "0s"},
		{"audit", "--schema", "schema.sql", "--dry-run", "--timeout", "2h"},
		{"audit", "--schema", "schema.sql", "--dry-run", "--relation", "Missing.Parent"},
	} {
		result := runApplicationAt(t, directory, nil, args...)
		if result.Status() != exitUsage {
			t.Fatalf("%v: %d %s", args, result.Status(), result.Stderr())
		}
	}
	result := runAuditAt(t, directory, map[string]string{"AUDIT_DSN": "user:secret@tcp(localhost:4000)/application?tls=false"}, "audit", "--schema", "schema.sql", "--dsn-env", "AUDIT_DSN")
	if result.Status() != exitUsage || !strings.Contains(string(result.Stderr()), "audit DSN requires") || strings.Contains(string(result.Stderr()), "secret") {
		t.Fatalf("TLS policy=%s", result.Stderr())
	}
}
