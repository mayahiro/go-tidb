package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/migrate"
)

type migrationLog struct {
	file    *os.File
	path    string
	console io.Writer
	dsn     string
	closed  bool
}

func newMigrationLog(directory string, console io.Writer, dsn string) (*migrationLog, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(directory, "run-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-*.log")
	if err != nil {
		return nil, err
	}
	l := &migrationLog{file: f, path: f.Name(), console: console, dsn: dsn}
	if _, err := fmt.Fprintf(console, "migration log: %s\n", l.path); err != nil {
		_ = l.close()
		return nil, err
	}
	return l, nil
}

func (l *migrationLog) event(event migrate.Event) error {
	state := event.State
	if state == "error" {
		state = "unknown"
		var server *mysql.MySQLError
		if errors.As(event.Err, &server) {
			state = "failed"
		}
	}
	var b strings.Builder
	file := ""
	if event.Version != "" {
		file = event.Version + ".sql"
	}
	fmt.Fprintf(&b, "%s state=%s phase=%q version=%q direction=%s file=%q statement=%d\n", event.Time.Format(time.RFC3339Nano), state, event.Phase, event.Version, event.Direction, file, event.Statement)
	if event.SQL != "" && (state == "started" || state == "unexecuted") {
		fmt.Fprintf(&b, "%s;\n", redact.String(event.SQL, migrationSecrets(l.dsn)...))
	}
	if event.Err != nil {
		fmt.Fprintln(&b, safeMigrationError(event.Err, l.dsn))
	}
	text := b.String()
	_, writeErr := io.WriteString(l.file, text)
	if writeErr == nil {
		writeErr = l.file.Sync()
	}
	_, consoleErr := io.WriteString(l.console, text)
	return errors.Join(writeErr, consoleErr)
}

func (l *migrationLog) finish(operationErr error) error {
	status := "succeeded"
	if operationErr != nil {
		status = "error"
	}
	_, err := fmt.Fprintf(l.file, "%s operation=%s\n", time.Now().UTC().Format(time.RFC3339Nano), status)
	if operationErr != nil {
		_, writeErr := fmt.Fprintln(l.file, safeMigrationError(operationErr, l.dsn))
		err = errors.Join(err, writeErr)
	}
	return errors.Join(err, l.close())
}

func (l *migrationLog) close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return errors.Join(l.file.Sync(), l.file.Close())
}
