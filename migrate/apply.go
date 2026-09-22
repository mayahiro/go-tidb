package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Apply executes a freshly validated plan under one pinned connection and lock.
// Up with zero steps applies all pending versions; Down requires positive steps.
// It never retries statements, automatically reverses failed SQL, or runs DDL in
// a transaction. Each completed version refreshes SchemaFile from the database.
func (r *Runner) Apply(ctx context.Context, direction Direction, steps int) (result Result, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		snap, _, state, err := inspect(ctx, s, migrations)
		if err != nil {
			return err
		}
		result.Version, result.Dirty = state.version(), state.dirty != nil
		plan, err := buildPlan(migrations, state, snap, direction, steps)
		if err != nil {
			return err
		}
		if err := checkOutput(r.config.SchemaFile); err != nil {
			return err
		}
		if len(plan.Steps) == 0 {
			return r.output(snap, &result)
		}
		if err := mutationHistory(ctx, s, snap); err != nil {
			return err
		}
		byVersion := map[int64]Migration{}
		for _, m := range migrations {
			byVersion[m.Version] = m
		}
		stack := append([]int64(nil), state.stack...)
		for _, step := range plan.Steps {
			m := byVersion[step.Version]
			id, err := insertEvent(ctx, s.conn, m, string(direction), "running", len(step.Statements), snap.Hash, "", "")
			if err != nil {
				result.Dirty = true
				return err
			}
			result.Dirty = true
			result.SnapshotUpdated = false
			for i, statement := range step.Statements {
				if _, err := s.conn.ExecContext(ctx, statement); err != nil {
					failureCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					journalErr := updateEvent(failureCtx, s.conn, id, "failed", i, "")
					cancel()
					cause := errors.Join(err, journalErr)
					return &OperationError{Phase: string(direction) + " SQL", Version: m.Version, Statement: i + 1, Cause: cause}
				}
				if err := updateEvent(ctx, s.conn, id, "running", i+1, ""); err != nil {
					return &OperationError{Phase: "record statement progress", Version: m.Version, Statement: i + 1, Cause: err}
				}
			}
			next, err := capture(ctx, s.conn, s.database)
			if err != nil {
				return &OperationError{Phase: "inspect changed schema", Version: m.Version, Cause: err}
			}
			completedState := "applied"
			if direction == Down {
				completedState = "reverted"
			}
			if err := updateEvent(ctx, s.conn, id, completedState, len(step.Statements), next.Hash); err != nil {
				return &OperationError{Phase: "complete history", Version: m.Version, Cause: err}
			}
			if direction == Up {
				stack = append(stack, m.Version)
			} else {
				stack = stack[:len(stack)-1]
			}
			result.Version = 0
			if len(stack) > 0 {
				result.Version = stack[len(stack)-1]
			}
			result.Dirty = false
			result.Completed = append(result.Completed, m.Version)
			snap = next
			if err := r.output(snap, &result); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// Baseline adopts only the first local migration's up section, comparing its entire
// canonical SQL with a fresh live snapshot. It executes no application DDL/DML.
// The baseline becomes the lower bound for subsequent Down operations.
// On success it creates or refreshes SchemaFile from the current database.
func (r *Runner) Baseline(ctx context.Context) (result Result, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	if len(migrations) == 0 {
		return result, fmt.Errorf("migrate: baseline requires an initial migration; run init first")
	}
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		snap, events, _, err := inspect(ctx, s, migrations)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			return fmt.Errorf("migrate: baseline requires a database without recorded migration attempts")
		}
		if snap.tables == 0 {
			return fmt.Errorf("migrate: empty database; apply the initial migration with up")
		}
		m := migrations[0]
		expected, err := snapshotHash(m.Up)
		if err != nil {
			return err
		}
		if expected != snap.Hash {
			return fmt.Errorf("%w: initial migration does not match the current database", ErrDrift)
		}
		if err := checkOutput(r.config.SchemaFile); err != nil {
			return err
		}
		if err := mutationHistory(ctx, s, snap); err != nil {
			return err
		}
		if _, err := insertEvent(ctx, s.conn, m, "baseline", "applied", 0, snap.Hash, snap.Hash, ""); err != nil {
			return err
		}
		result.Version = m.Version
		result.Completed = []int64{m.Version}
		return r.output(snap, &result)
	})
	return result, err
}

// RepairOptions acknowledges an inspected, interrupted attempt. ExpectedSchema
// must name a separately reviewed SQL snapshot matching the live structure.
// Applied chooses the resulting state of Version, not the attempted direction.
// Reason is a required audit explanation, never an automatic retry instruction.
type RepairOptions struct {
	Version        int64
	Applied        bool
	ExpectedSchema string
	Reason         string
}

// Repair records an operator-confirmed result without executing migration SQL.
// The failed attempt is retained as resolved and a separate repair event is
// appended atomically. Schema equality does not prove a data backfill succeeded;
// the operator must independently check data and in-flight server DDL jobs.
func (r *Runner) Repair(ctx context.Context, options RepairOptions) (result Result, err error) {
	if options.Version <= 0 || options.ExpectedSchema == "" || !reasonValid(options.Reason) {
		return result, fmt.Errorf("migrate: repair requires a positive version, expected schema file, and a reason of 1..1024 bytes")
	}
	expectedSQL, err := readSQL(options.ExpectedSchema)
	if err != nil {
		return result, err
	}
	expected, err := snapshotHash(expectedSQL)
	if err != nil {
		return result, err
	}
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		snap, _, state, err := inspect(ctx, s, migrations)
		if err != nil {
			return err
		}
		result.Version, result.Dirty = state.version(), state.dirty != nil
		if state.dirty == nil || state.dirty.Version != options.Version {
			return fmt.Errorf("migrate: repair must name the unresolved migration version")
		}
		if expected != snap.Hash {
			return fmt.Errorf("%w: reviewed repair snapshot does not match the database", ErrDrift)
		}
		restoresBefore := state.dirty.Direction == "up" && !options.Applied || state.dirty.Direction == "down" && options.Applied
		if restoresBefore && snap.Hash != state.dirty.SchemaBefore {
			return fmt.Errorf("%w: repaired structure does not match the state before the attempt", ErrDrift)
		}
		if err := checkOutput(r.config.SchemaFile); err != nil {
			return err
		}
		var m Migration
		for _, candidate := range migrations {
			if candidate.Version == options.Version {
				m = candidate
				break
			}
		}
		tx, err := s.conn.BeginTx(ctx, nil)
		if err != nil {
			return &OperationError{Phase: "begin repair record", Cause: err}
		}
		defer tx.Rollback()
		updated, err := tx.ExecContext(ctx, "UPDATE `_tidbgo_migrations` SET state = 'resolved', finished_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND state IN ('running','failed')", state.dirty.ID)
		if err != nil {
			return &OperationError{Phase: "resolve attempt", Cause: err}
		}
		n, err := updated.RowsAffected()
		if err != nil || n != 1 {
			return fmt.Errorf("migrate: unresolved attempt changed during repair")
		}
		completedState := "reverted"
		if options.Applied {
			completedState = "applied"
		}
		if _, err := insertEvent(ctx, tx, m, "repair", completedState, 0, state.dirty.SchemaBefore, snap.Hash, options.Reason); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return &OperationError{Phase: "commit repair record", Version: m.Version, Cause: err}
		}
		if options.Applied {
			result.Version = m.Version
		} else if state.version() == m.Version {
			result.Version = 0
			if len(state.stack) > 1 {
				result.Version = state.stack[len(state.stack)-2]
			}
		}
		result.Dirty = false
		result.Completed = []int64{m.Version}
		return r.output(snap, &result)
	})
	return result, err
}

func checkOutput(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("migrate: snapshot output must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tidbgo-schema-check-*")
	if err != nil {
		return &OperationError{Phase: "prepare snapshot output", Cause: err}
	}
	name := f.Name()
	err = f.Close()
	removeErr := os.Remove(name)
	return errors.Join(err, removeErr)
}
