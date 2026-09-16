// Package tiflash provides explicit, caller-owned TiFlash preparation and
// capability checks. It never runs DDL or changes connection settings.
package tiflash

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// QueryExecutor is the read-only database/sql boundary used by explicit checks.
// *sql.DB, *sql.Conn, and *sql.Tx implement it.
type QueryExecutor interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

var (
	// ErrTableNotFound means a base table is absent or not visible to the caller.
	ErrTableNotFound = errors.New("tiflash: base table not found or not visible")
	// ErrReplicaNotConfigured means a visible table has no enabled replica.
	ErrReplicaNotConfigured = errors.New("tiflash: replica is not configured")
)

// Replica describes initial replication readiness at the time of inspection.
// Available is sticky in TiDB and is not a continuous health or freshness check.
// Progress is in [0,1]; one does not prove that every replica is synchronized.
type Replica struct {
	Schema     string
	Table      string
	Configured bool
	Count      int64
	Available  bool
	Progress   float64
}

// BuildEnableReplica returns a quoted ALTER TABLE statement for two replicas,
// the supported TiDB Cloud Starter count. It performs no I/O; the caller decides
// when and whether to execute the DDL. Schema and table are separate identifiers.
func BuildEnableReplica(schema, table string) (string, error) {
	if err := validateTable(schema, table); err != nil {
		return "", err
	}
	return "ALTER TABLE " + quoteIdentifier(schema) + "." + quoteIdentifier(table) + " SET TIFLASH REPLICA 2", nil
}

const inspectReplicaSQL = "SELECT t.TABLE_SCHEMA, t.TABLE_NAME, r.REPLICA_COUNT, r.AVAILABLE, r.PROGRESS FROM information_schema.TABLES AS t LEFT JOIN information_schema.TIFLASH_REPLICA AS r ON r.TABLE_SCHEMA = t.TABLE_SCHEMA AND r.TABLE_NAME = t.TABLE_NAME WHERE t.TABLE_SCHEMA = ? AND t.TABLE_NAME = ? AND t.TABLE_TYPE = 'BASE TABLE'"

// InspectReplica reads metadata for one visible base table in one statement.
// A table without replicas returns Configured=false; an absent/invisible table
// returns ErrTableNotFound. Permission, unsupported metadata, and transport
// errors are returned without classifying them as missing replicas.
func InspectReplica(ctx context.Context, executor QueryExecutor, schema, table string) (result Replica, err error) {
	result.Schema, result.Table = schema, table
	if err = validateExecution(ctx, executor); err != nil {
		return result, err
	}
	if err = validateTable(schema, table); err != nil {
		return result, err
	}
	rows, err := executor.QueryContext(ctx, inspectReplicaSQL, schema, table)
	if err != nil {
		return result, fmt.Errorf("tiflash: inspect replica: %w", err)
	}
	if rows == nil {
		return result, fmt.Errorf("tiflash: executor returned nil rows")
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return result, err
		}
		return result, ErrTableNotFound
	}
	var count, available sql.NullInt64
	var progress sql.NullFloat64
	if err := rows.Scan(&result.Schema, &result.Table, &count, &available, &progress); err != nil {
		return result, fmt.Errorf("tiflash: scan replica: %w", err)
	}
	if count.Valid {
		if count.Int64 < 0 || !available.Valid || available.Int64 < 0 || available.Int64 > 1 || !progress.Valid || math.IsNaN(progress.Float64) || math.IsInf(progress.Float64, 0) || progress.Float64 < 0 || progress.Float64 > 1 {
			return result, fmt.Errorf("tiflash: invalid replica metadata")
		}
		result.Configured, result.Count = count.Int64 > 0, count.Int64
		result.Available, result.Progress = available.Int64 == 1, progress.Float64
	} else if available.Valid || progress.Valid {
		return result, fmt.Errorf("tiflash: incomplete replica metadata")
	}
	if rows.Next() {
		return result, fmt.Errorf("tiflash: replica metadata returned multiple tables")
	}
	return result, rows.Err()
}

// WaitReplicaReady polls initial availability until success or context expiry.
// A deadline is required. Zero interval uses one second; negative intervals are
// rejected. Missing configuration returns ErrReplicaNotConfigured immediately.
// The last successful inspection accompanies later errors, including cancellation.
// This is deployment/test preparation, not a periodic health monitor.
func WaitReplicaReady(ctx context.Context, executor QueryExecutor, schema, table string, interval time.Duration) (Replica, error) {
	last := Replica{Schema: schema, Table: table}
	if err := validateExecution(ctx, executor); err != nil {
		return last, err
	}
	if _, ok := ctx.Deadline(); !ok {
		return last, fmt.Errorf("tiflash: WaitReplicaReady requires a context deadline")
	}
	if interval < 0 {
		return last, fmt.Errorf("tiflash: polling interval must not be negative")
	}
	if interval == 0 {
		interval = time.Second
	}
	for {
		current, err := InspectReplica(ctx, executor, schema, table)
		if err != nil {
			return last, err
		}
		last = current
		if !last.Configured {
			return last, ErrReplicaNotConfigured
		}
		if last.Available {
			return last, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, ctx.Err()
		case <-timer.C:
		}
	}
}

func validateExecution(ctx context.Context, executor QueryExecutor) error {
	if ctx == nil {
		return fmt.Errorf("tiflash: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if executor == nil {
		return fmt.Errorf("tiflash: executor must not be nil")
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		if value.IsNil() {
			return fmt.Errorf("tiflash: executor must not be nil")
		}
	}
	return nil
}

func validateTable(schema, table string) error {
	for _, name := range []string{schema, table} {
		if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 || strings.ContainsRune(name, 0) || strings.HasSuffix(name, " ") {
			return fmt.Errorf("tiflash: schema and table must be valid nonempty identifiers of at most 64 characters")
		}
	}
	return nil
}

func quoteIdentifier(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
