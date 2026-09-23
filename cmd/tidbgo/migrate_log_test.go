package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/mayahiro/go-tidb/migrate"
)

func TestMigrationRecoveryLogPersistsEachResult(t *testing.T) {
	var console bytes.Buffer
	l, err := newMigrationLog(filepath.Join(t.TempDir(), "log", "tidbgo"), &console, "user:test-password@tcp(localhost:4000)/app?tls=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.close() })
	event := migrate.Event{Time: time.Now().UTC(), Version: "001_add", Direction: migrate.Up, Phase: "SQL", Statement: 1, SQL: "ALTER TABLE items ADD COLUMN label INT", State: "started"}
	if err := l.event(event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(l.path)
	if err != nil || !strings.Contains(string(data), event.SQL) || !strings.Contains(string(data), "state=started") {
		t.Fatalf("start not persisted: %s %v", data, err)
	}
	event.State = "succeeded"
	if err := l.event(event); err != nil {
		t.Fatal(err)
	}
	event.Statement = 2
	event.State = "error"
	event.Err = &mysql.MySQLError{Number: 1060, SQLState: [5]byte{'4', '2', 'S', '2', '1'}, Message: "Duplicate column name 'label'; password=test-password"}
	if err := l.event(event); err != nil {
		t.Fatal(err)
	}
	event.Statement = 3
	event.State = "unexecuted"
	event.Err = nil
	event.SQL = "ALTER TABLE items ADD COLUMN other INT"
	if err := l.event(event); err != nil {
		t.Fatal(err)
	}
	if err := l.finish(errors.New("operation failed")); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=started", "state=succeeded", "state=failed", "state=unexecuted", "statement=2", "1060", "42S21", "Duplicate column name 'label'", event.SQL} {
		if !strings.Contains(string(data), want) || !strings.Contains(console.String(), want) {
			t.Fatalf("missing %q in recovery output", want)
		}
	}
	if strings.Contains(string(data), "test-password") || strings.Contains(console.String(), "test-password") {
		t.Fatal("credential leaked")
	}
	info, err := os.Stat(l.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("log permissions: %v %v", info, err)
	}
}

func TestMigrationLogUnknownOutcomeAndRecordFailure(t *testing.T) {
	var console bytes.Buffer
	l, err := newMigrationLog(t.TempDir(), &console, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{io.EOF, context.DeadlineExceeded} {
		if err := l.event(migrate.Event{Time: time.Now().UTC(), Version: "001", Phase: "SQL", Statement: 2, State: "error", Err: cause}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.event(migrate.Event{Time: time.Now().UTC(), Version: "001", Phase: "record applied version", State: "error", Err: io.EOF}); err != nil {
		t.Fatal(err)
	}
	if err := l.finish(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Count(console.String(), "state=unknown") != 3 || !strings.Contains(console.String(), `phase="record applied version"`) {
		t.Fatal(console.String())
	}
}

type failingMigrationWriter struct{}

func (failingMigrationWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestMigrationLogReportsFilesystemAndConsoleFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newMigrationLog(path, io.Discard, ""); err == nil {
		t.Fatal("unwritable log location accepted")
	}
	l, err := newMigrationLog(dir, io.Discard, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.close() })
	l.console = failingMigrationWriter{}
	if err := l.event(migrate.Event{Time: time.Now().UTC(), State: "started"}); err == nil {
		t.Fatal("console write failure ignored")
	}
	l.console = io.Discard
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	if err := l.event(migrate.Event{Time: time.Now().UTC(), State: "started"}); err == nil {
		t.Fatal("log write failure ignored")
	}
}
