package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	cli "github.com/mayahiro/nagicli-go"
	"github.com/mayahiro/nagicli-go/clitest"
)

func TestMigrationCLICommands(t *testing.T) {
	for _, command := range []string{"new", "lint", "init", "baseline", "status", "plan", "up", "down", "dump", "repair"} {
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

func TestMigrationCLIRequiresExplicitConnectionAndRepair(t *testing.T) {
	for _, args := range [][]string{
		{"migrate", "up"},
		{"migrate", "plan", "--direction", "sideways"},
		{"migrate", "down", "--steps", "0"},
		{"migrate", "up", "--timeout", "0s"},
		{"migrate", "up", "--lock-timeout", "0s"},
		{"migrate", "repair", "1"},
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
	if message := safeMigrationError(err); strings.Contains(message, "private SQL") || !strings.Contains(message, "1064") {
		t.Fatal(message)
	}
}
