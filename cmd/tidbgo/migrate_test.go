package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	cli "github.com/mayahiro/nagicli-go"
	"github.com/mayahiro/nagicli-go/clitest"

	"github.com/mayahiro/go-tidb/migrate"
)

func TestMigrationCLICommands(t *testing.T) {
	for _, command := range []string{"new", "lint", "init", "baseline", "status", "plan", "up", "down", "dump"} {
		r := runApplication(t, "migrate", command, "--help")
		if r.Status() != cli.StatusSuccess || !strings.Contains(string(r.Stdout()), "--dir") {
			t.Fatalf("%s: %s", command, r.Stderr())
		}
	}
	dir := t.TempDir()
	r := runApplicationAt(t, dir, nil, "migrate", "new", "create_users")
	if r.Status() != cli.StatusSuccess {
		t.Fatal(string(r.Stderr()))
	}
	entries, err := os.ReadDir(filepath.Join(dir, "migrations"))
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "_create_users.sql") || len(entries[0].Name()) != 17+len("_create_users.sql") {
		t.Fatalf("created files: %v, %v", entries, err)
	}
	if !strings.Contains(string(r.Stdout()), entries[0].Name()) {
		t.Fatal("output does not identify the generated file")
	}
	path := filepath.Join(dir, "migrations", entries[0].Name())
	if err := os.WriteFile(path, []byte("-- tidbgo:up\nCREATE TABLE users (id INT);\n\n-- tidbgo:down\nDROP TABLE users;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r = runApplicationAt(t, dir, nil, "migrate", "lint", "--json")
	if r.Status() != cli.StatusSuccess || !strings.Contains(string(r.Stdout()), `"versions":1`) {
		t.Fatalf("lint: %s %s", r.Stdout(), r.Stderr())
	}
	r = runApplicationAt(t, dir, nil, "migrate", "new", "../escape")
	if r.Status() != exitDiagnosticFailure || len(r.Stdout()) != 0 {
		t.Fatalf("invalid new: %s %s", r.Stdout(), r.Stderr())
	}
}

func TestMigrationCLIReportsLintScopeAndSchemaConflicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "migrations")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "001.sql"), []byte("-- tidbgo:up\nALTER TABLE items ADD COLUMN label INT; UPDATE items SET label=1;"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, jsonOutput := range []bool{false, true} {
		args := []string{"migrate", "lint"}
		want := "execution_checked=false"
		if jsonOutput {
			args = append(args, "--json")
			want = `"execution_checked":false`
		}
		r := runApplicationAt(t, dir, nil, args...)
		if r.Status() != cli.StatusSuccess || !strings.Contains(string(r.Stdout()), want) || !strings.Contains(string(r.Stdout()), "unverified") || strings.Contains(string(r.Stdout()), "checks passed") {
			t.Fatalf("scope: %s %s", r.Stdout(), r.Stderr())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("CREATE TABLE items (id INT, label INT);"), 0644); err != nil {
		t.Fatal(err)
	}
	r := runApplicationAt(t, dir, nil, "migrate", "lint", "--schema", "schema.sql", "--file", "001.sql", "--direction", "up", "--json")
	if r.Status() != exitDiagnosticFailure || !strings.Contains(string(r.Stdout()), "already exists") || !strings.Contains(string(r.Stdout()), `"schema_checked":true`) {
		t.Fatalf("conflict: %s %s", r.Stdout(), r.Stderr())
	}
}

func TestMigrationCLIReportsWrappedDatabaseErrors(t *testing.T) {
	server := &mysql.MySQLError{Number: 1054, SQLState: [5]byte{'4', '2', 'S', '2', '2'}, Message: "Unknown column 'missing_field'\npassword=private-password"}
	operation := &migrate.OperationError{Phase: "SQL", Version: "001_add", Statement: 4, Cause: errors.Join(server, errors.New("other failure"))}
	for _, err := range []error{server, operation, errors.Join(operation, context.Canceled)} {
		message := safeMigrationError(err, "user:private-password@tcp(localhost:4000)/app?tls=true")
		for _, want := range []string{"1054", "42S22", "Unknown column 'missing_field'"} {
			if !strings.Contains(message, want) {
				t.Fatalf("missing %q: %s", want, message)
			}
		}
		if strings.Contains(message, "private-password") || strings.Contains(message, "\n") {
			t.Fatalf("unsafe message: %s", message)
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		message := safeMigrationError(&migrate.OperationError{Phase: "SQL", Cause: cause}, "")
		if !strings.Contains(message, cause.Error()) {
			t.Fatal(message)
		}
	}
	logFailure := &migrate.OperationError{Phase: "write execution progress", Cause: errors.New("log storage full")}
	message := safeMigrationError(errors.Join(operation, logFailure), "private-password")
	for _, want := range []string{"1054", "other failure", "write execution progress", "log storage full"} {
		if !strings.Contains(message, want) {
			t.Fatalf("simultaneous failures lost %q: %s", want, message)
		}
	}
}

func TestMigrationCLIRequiresExplicitConnection(t *testing.T) {
	for _, args := range [][]string{
		{"migrate", "up"},
		{"migrate", "plan", "--direction", "sideways"},
		{"migrate", "down", "--steps", "0"},
		{"migrate", "up", "--timeout", "0s"},
		{"migrate", "up", "--lock-timeout", "0s"},
	} {
		r := runApplication(t, args...)
		if r.Status() != exitUsage {
			t.Fatalf("%v: %d %s", args, r.Status(), r.Stderr())
		}
	}
	r, err := clitest.New(application("dev")).Policy(runtimePolicy()).Arguments("migrate", "up").Environment("TIDBGO_DSN", "someone:secret-value@tcp(localhost:4000)/app?tls=false").Run()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status() != exitUsage || strings.Contains(string(r.Stderr()), "secret-value") {
		t.Fatalf("insecure DSN: %s", r.Stderr())
	}
}

func TestMigrationDSNSafety(t *testing.T) {
	for _, dsn := range []string{
		"user:secret@tcp(localhost:4000)/app", "user:secret@tcp(localhost:4000)/app?tls=skip-verify",
		"user:secret@tcp(localhost:4000)/app?tls=preferred", "user:secret@tcp(localhost:4000)/app?tls=true&multiStatements=true",
		"user:secret@tcp(localhost:4000)/app?tls=true&autocommit=false", "user:secret@tcp(localhost:4000)/?tls=true",
	} {
		db, err := openMigrationDatabase(dsn)
		if db != nil {
			db.Close()
		}
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe DSN result: %v", err)
		}
	}
	db, err := openMigrationDatabase("user:secret@tcp(localhost:4000)/app?tls=true&parseTime=true&interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	err = &mysql.MySQLError{Number: 1064, Message: "private SQL value"}
	if message := safeMigrationError(err, ""); !strings.Contains(message, "private SQL") || !strings.Contains(message, "1064") {
		t.Fatal(message)
	}
}
