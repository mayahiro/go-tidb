package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const historyTable = "_tidbgo_migrations"
const historyMarker = "tidbgo:migrations:v1"
const createHistorySQL = "CREATE TABLE `_tidbgo_migrations` (" +
	"`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, " +
	"`version` BIGINT NOT NULL, `name` VARCHAR(128) NOT NULL, " +
	"`up_checksum` CHAR(64) NOT NULL, `down_checksum` CHAR(64) NOT NULL, " +
	"`direction` VARCHAR(16) NOT NULL, `state` VARCHAR(16) NOT NULL, " +
	"`statement_index` INT NOT NULL, `statement_total` INT NOT NULL, " +
	"`schema_before` CHAR(64) NOT NULL, `schema_after` CHAR(64) NOT NULL, " +
	"`reason` TEXT NOT NULL, `created_at` TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), " +
	"`finished_at` TIMESTAMP(6) NULL) COMMENT='" + historyMarker + "'"

// Event is one recorded attempt. StatementIndex counts confirmed statements;
// the following statement may have succeeded when its acknowledgement was lost.
// Resolved attempts remain in history alongside a separate repair event.
type Event struct {
	ID             int64  `json:"id"`
	Version        int64  `json:"version"`
	Name           string `json:"name"`
	UpChecksum     string `json:"up_checksum"`
	DownChecksum   string `json:"down_checksum"`
	Direction      string `json:"direction"`
	State          string `json:"state"`
	StatementIndex int    `json:"statement_index"`
	StatementTotal int    `json:"statement_total"`
	SchemaBefore   string `json:"schema_before"`
	SchemaAfter    string `json:"schema_after"`
}

type historyState struct {
	stack             []int64
	baseline, maxSeen int64
	hash              string
	dirty             *Event
}

func (s historyState) version() int64 {
	if len(s.stack) == 0 {
		return 0
	}
	return s.stack[len(s.stack)-1]
}

func readEvents(ctx context.Context, conn *sql.Conn) ([]Event, error) {
	rows, err := conn.QueryContext(ctx, "SELECT id, version, name, up_checksum, down_checksum, direction, state, statement_index, statement_total, schema_before, schema_after FROM `_tidbgo_migrations` ORDER BY id LIMIT 100001")
	if err != nil {
		return nil, &OperationError{Phase: "read history", Cause: err}
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Version, &e.Name, &e.UpChecksum, &e.DownChecksum, &e.Direction, &e.State, &e.StatementIndex, &e.StatementTotal, &e.SchemaBefore, &e.SchemaAfter); err != nil {
			return nil, &OperationError{Phase: "decode history", Cause: err}
		}
		events = append(events, e)
		if len(events) > 100000 {
			return nil, fmt.Errorf("migrate: history exceeds 100000 attempts")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, &OperationError{Phase: "read history", Cause: err}
	}
	return events, nil
}

func replay(events []Event) (historyState, error) {
	var s historyState
	var previousID int64
	for i := range events {
		e := events[i]
		if e.ID <= previousID || e.Version <= 0 || !namePattern.MatchString(e.Name) || e.StatementIndex < 0 || e.StatementTotal < e.StatementIndex || !validHash(e.UpChecksum) || !validHash(e.DownChecksum) || !validHash(e.SchemaBefore) {
			return s, fmt.Errorf("migrate: invalid migration history")
		}
		previousID = e.ID
		if e.Version > s.maxSeen {
			s.maxSeen = e.Version
		}
		if s.dirty != nil {
			return s, fmt.Errorf("migrate: history contains operations after an unresolved attempt")
		}
		if e.Direction != "up" && e.Direction != "down" && e.Direction != "baseline" && e.Direction != "repair" {
			return s, fmt.Errorf("migrate: invalid history direction")
		}
		switch e.State {
		case "running", "failed":
			if e.Direction != "up" && e.Direction != "down" {
				return s, fmt.Errorf("migrate: invalid interrupted history")
			}
			s.dirty = &events[i]
			continue
		case "resolved":
			continue
		case "applied", "reverted":
			if !validHash(e.SchemaAfter) {
				return s, fmt.Errorf("migrate: missing completed schema fingerprint")
			}
		default:
			return s, fmt.Errorf("migrate: unknown history state")
		}
		if e.Direction == "baseline" {
			if i != 0 || e.State != "applied" {
				return s, fmt.Errorf("migrate: invalid baseline history")
			}
			s.baseline = e.Version
		}
		if e.Direction != "repair" && ((e.State == "applied") != (e.Direction == "up" || e.Direction == "baseline")) {
			return s, fmt.Errorf("migrate: inconsistent history direction and state")
		}
		if e.State == "applied" {
			if e.Direction == "repair" && s.version() == e.Version { /* restored a failed down */
			} else {
				if e.Version <= s.version() {
					return s, fmt.Errorf("migrate: nonascending applied history")
				}
				s.stack = append(s.stack, e.Version)
			}
		} else {
			if e.Version <= s.baseline {
				return s, fmt.Errorf("migrate: history crosses its adoption baseline")
			}
			if s.version() == e.Version {
				s.stack = s.stack[:len(s.stack)-1]
			} else if e.Direction != "repair" || e.Version < s.version() {
				return s, fmt.Errorf("migrate: nonsequential reverse history")
			}
		}
		s.hash = e.SchemaAfter
	}
	return s, nil
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateFiles(migrations []Migration, events []Event, state historyState) error {
	byVersion := make(map[int64]Migration, len(migrations))
	seen := make(map[int64]bool)
	for _, m := range migrations {
		byVersion[m.Version] = m
	}
	for _, e := range events {
		m, ok := byVersion[e.Version]
		if !ok || m.Name != e.Name || checksum(m.Up) != e.UpChecksum || checksum(m.Down) != e.DownChecksum {
			return fmt.Errorf("%w: version %d", ErrChecksum, e.Version)
		}
		seen[e.Version] = true
	}
	for _, m := range migrations {
		if m.Version < state.maxSeen && !seen[m.Version] {
			return fmt.Errorf("migrate: newly inserted version %d precedes recorded history", m.Version)
		}
	}
	return nil
}

type journalExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, conn journalExecutor, m Migration, direction, state string, total int, before, after, reason string) (int64, error) {
	finished := "NULL"
	if state == "applied" || state == "reverted" {
		finished = "CURRENT_TIMESTAMP(6)"
	}
	result, err := conn.ExecContext(ctx, "INSERT INTO `_tidbgo_migrations` (version, name, up_checksum, down_checksum, direction, state, statement_index, statement_total, schema_before, schema_after, reason, finished_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, "+finished+")", m.Version, m.Name, checksum(m.Up), checksum(m.Down), direction, state, total, before, after, reason)
	if err != nil {
		return 0, &OperationError{Phase: "record operation", Version: m.Version, Cause: err}
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return 0, &OperationError{Phase: "read operation identity", Version: m.Version, Cause: err}
	}
	return id, nil
}

func updateEvent(ctx context.Context, conn *sql.Conn, id int64, state string, confirmed int, hash string) error {
	result, err := conn.ExecContext(ctx, "UPDATE `_tidbgo_migrations` SET state = ?, statement_index = ?, schema_after = ?, finished_at = CASE WHEN ? IN ('applied','reverted','failed') THEN CURRENT_TIMESTAMP(6) ELSE NULL END WHERE id = ? AND state = 'running'", state, confirmed, hash, state, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("migrate: operation journal did not update exactly one running attempt")
	}
	return nil
}

func mutationHistory(ctx context.Context, s session, snap snapshot) error {
	if snap.managed {
		return nil
	}
	if _, err := s.conn.ExecContext(ctx, createHistorySQL); err != nil {
		return &OperationError{Phase: "create history", Cause: err}
	}
	return nil
}

// MigrationStatus is the current status of one local migration file.
type MigrationStatus struct {
	Version    int64  `json:"version"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Reversible bool   `json:"reversible"`
}

// Status is a read-only view of current history, pending files, and live drift.
// An empty unmanaged database is ready for Up; a nonempty one needs Baseline.
type Status struct {
	Database   string            `json:"database"`
	Version    int64             `json:"version"`
	Baseline   int64             `json:"baseline"`
	Managed    bool              `json:"managed"`
	Dirty      bool              `json:"dirty"`
	Drift      bool              `json:"drift"`
	Migrations []MigrationStatus `json:"migrations"`
	Events     []Event           `json:"events"`
}

func inspect(ctx context.Context, s session, migrations []Migration) (snapshot, []Event, historyState, error) {
	snap, err := capture(ctx, s.conn, s.database)
	if err != nil {
		return snap, nil, historyState{}, &OperationError{Phase: "inspect schema", Cause: err}
	}
	var events []Event
	if snap.managed {
		events, err = readEvents(ctx, s.conn)
		if err != nil {
			return snap, nil, historyState{}, err
		}
	}
	state, err := replay(events)
	if err != nil {
		return snap, events, state, err
	}
	if err = validateFiles(migrations, events, state); err != nil {
		return snap, events, state, err
	}
	return snap, events, state, nil
}

// Status reads metadata without creating or modifying application/history tables.
func (r *Runner) Status(ctx context.Context) (result Status, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		snap, events, state, err := inspect(ctx, s, migrations)
		if err != nil {
			return err
		}
		result = Status{Database: s.database, Version: state.version(), Baseline: state.baseline, Managed: snap.managed, Dirty: state.dirty != nil, Drift: state.hash != "" && state.hash != snap.Hash, Events: events}
		applied := map[int64]bool{}
		for _, v := range state.stack {
			applied[v] = true
		}
		for _, m := range migrations {
			status := "pending"
			if applied[m.Version] {
				status = "applied"
			}
			if m.Version == state.baseline {
				status = "baseline"
			}
			if state.dirty != nil && m.Version == state.dirty.Version {
				status = state.dirty.State + "_" + state.dirty.Direction
			}
			result.Migrations = append(result.Migrations, MigrationStatus{m.Version, m.Name, status, m.Down != ""})
		}
		return nil
	})
	return result, err
}

func reasonValid(reason string) bool { return strings.TrimSpace(reason) != "" && len(reason) <= 1024 }
