package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

const historyTable = "_tidbgo_migrations"
const historyMarker = "tidbgo:migrations:v3"
const createHistorySQL = "CREATE TABLE `_tidbgo_migrations` (" +
	"`version` VARCHAR(251) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY, " +
	"`created_at` DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)" +
	") COMMENT='" + historyMarker + "'"
const recordTimeLayout = "2006-01-02 15:04:05.000000"

type appliedVersion struct {
	version   string
	createdAt time.Time
}

type historyState struct{ applied []appliedVersion }

func (s historyState) version() string {
	if len(s.applied) == 0 {
		return ""
	}
	return s.applied[len(s.applied)-1].version
}

func readState(ctx context.Context, conn *sql.Conn) (historyState, error) {
	var state historyState
	// A textual value works with both parseTime=true and parseTime=false drivers.
	rows, err := conn.QueryContext(ctx, "SELECT version, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f') FROM `_tidbgo_migrations` ORDER BY created_at, version LIMIT 20001")
	if err != nil {
		return state, &OperationError{Phase: "read applied versions", Cause: err}
	}
	defer rows.Close()
	for rows.Next() {
		var version, created string
		if err := rows.Scan(&version, &created); err != nil {
			return state, &OperationError{Phase: "decode applied version", Cause: err}
		}
		t, err := time.ParseInLocation(recordTimeLayout, created, time.UTC)
		if err != nil || !validVersion(version) || len(state.applied) >= 20000 {
			return state, fmt.Errorf("migrate: invalid applied migration record")
		}
		state.applied = append(state.applied, appliedVersion{version, t})
	}
	if err := rows.Err(); err != nil {
		return state, &OperationError{Phase: "read applied versions", Cause: err}
	}
	return state, nil
}

func recordApplied(ctx context.Context, conn *sql.Conn, version string) error {
	_, err := conn.ExecContext(ctx, "INSERT INTO `_tidbgo_migrations` (version) VALUES (?)", version)
	return err
}

func recordReverted(ctx context.Context, conn *sql.Conn, version string) error {
	result, err := conn.ExecContext(ctx, "DELETE FROM `_tidbgo_migrations` WHERE version = ?", version)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("migrate: expected to remove one applied version, removed %d", affected)
	}
	return nil
}

func mutationHistory(ctx context.Context, s session, snap snapshot) error {
	if snap.managed {
		return nil
	}
	if _, err := s.conn.ExecContext(ctx, createHistorySQL); err != nil {
		return &OperationError{Phase: "create version table", Cause: err}
	}
	return nil
}

// MigrationStatus describes a local or recorded version. State is pending,
// applied, or applied_missing. CreatedAt is the database's UTC registration time.
type MigrationStatus struct {
	Version    string     `json:"version"`
	State      string     `json:"state"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	Reversible bool       `json:"reversible"`
}

// Status lists applied records and local pending files. Version identifies the
// most recently registered migration, not the greatest filename.
type Status struct {
	Database   string            `json:"database"`
	Version    string            `json:"version"`
	Managed    bool              `json:"managed"`
	Migrations []MigrationStatus `json:"migrations"`
}

func inspect(ctx context.Context, s session) (snapshot, historyState, error) {
	snap, err := capture(ctx, s.conn, s.database)
	if err != nil {
		return snap, historyState{}, &OperationError{Phase: "inspect schema", Cause: err}
	}
	var state historyState
	if snap.managed {
		state, err = readState(ctx, s.conn)
	}
	return snap, state, err
}

// Status reads metadata without modifying application or version tables.
func (r *Runner) Status(ctx context.Context) (result Status, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		snap, state, err := inspect(ctx, s)
		if err != nil {
			return err
		}
		result = Status{Database: s.database, Version: state.version(), Managed: snap.managed}
		applied := make(map[string]time.Time, len(state.applied))
		for _, v := range state.applied {
			applied[v.version] = v.createdAt
		}
		for _, m := range migrations {
			item := MigrationStatus{Version: m.Version, State: "pending", Reversible: m.Down != ""}
			if t, ok := applied[m.Version]; ok {
				item.State, item.CreatedAt = "applied", &t
				delete(applied, m.Version)
			}
			result.Migrations = append(result.Migrations, item)
		}
		for v, t := range applied {
			result.Migrations = append(result.Migrations, MigrationStatus{Version: v, State: "applied_missing", CreatedAt: &t})
		}
		sort.Slice(result.Migrations, func(i, j int) bool { return result.Migrations[i].Version < result.Migrations[j].Version })
		return nil
	})
	return result, err
}
