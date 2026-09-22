package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Apply executes each file's statements sequentially. Up selects unregistered
// filenames in ascending order; Down selects records by descending created_at,
// then filename for equal timestamps. Zero files means all pending for Up.
// A file is registered or removed only after all its SQL succeeds. DDL and
// record writes are not atomic. Errors stop execution and require human recovery.
func (r *Runner) Apply(ctx context.Context, direction Direction, steps int) (result Result, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) (runErr error) {
		result.Database = s.database
		snap, state, err := inspect(ctx, s)
		if err != nil {
			return err
		}
		result.Version = state.version()
		plan, err := buildPlan(migrations, state, snap, direction, steps)
		if err != nil {
			return err
		}
		var events []Event
		for _, step := range plan.Steps {
			for i, statement := range step.Statements {
				events = append(events, Event{Version: step.Version, Direction: direction, Phase: "SQL", Statement: i + 1, SQL: statement})
			}
		}
		sent := 0
		defer func() {
			if runErr != nil {
				for _, event := range events[sent:] {
					event.State = "unexecuted"
					runErr = errors.Join(runErr, r.emit(event))
				}
			}
		}()
		if err := checkOutput(r.config.SchemaFile); err != nil {
			return err
		}
		if len(plan.Steps) == 0 {
			return r.output(snap, &result)
		}
		if !snap.managed {
			if err := r.operation(Event{Direction: direction, Phase: "create version table"}, func() error { return mutationHistory(ctx, s, snap) }); err != nil {
				return err
			}
		}
		stack := append([]appliedVersion(nil), state.applied...)
		for _, step := range plan.Steps {
			result.SnapshotUpdated = false
			for range step.Statements {
				event := events[sent]
				if err := r.operation(event, func() error {
					sent++
					_, err := s.conn.ExecContext(ctx, event.SQL)
					return err
				}); err != nil {
					return err
				}
			}
			phase := "record applied version"
			if direction == Down {
				phase = "remove applied version"
			}
			recordErr := r.operation(Event{Version: step.Version, Direction: direction, Phase: phase}, func() error {
				var err error
				if direction == Up {
					err = recordApplied(ctx, s.conn, step.Version)
				} else {
					err = recordReverted(ctx, s.conn, step.Version)
				}
				if err != nil {
					return err
				}
				if direction == Up {
					stack = append(stack, appliedVersion{version: step.Version})
				} else {
					stack = stack[:len(stack)-1]
				}
				result.Version = (historyState{applied: stack}).version()
				result.Completed = append(result.Completed, step.Version)
				return nil
			})
			if recordErr != nil {
				return recordErr
			}
			next, err := capture(ctx, s.conn, s.database)
			if err != nil {
				return errors.Join(ErrSnapshot, &OperationError{Phase: "read changed schema", Version: step.Version, Cause: err})
			}
			if err := r.output(next, &result); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// Baseline adopts one reviewed initial file without executing its SQL. Its Up
// must match the current database snapshot and it must have no Down section.
// Run after Init, before adding later files. The initial version is recorded
// with a database-generated created_at, and schema.sql is refreshed.
func (r *Runner) Baseline(ctx context.Context) (result Result, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	if len(migrations) != 1 || migrations[0].Down != "" {
		return result, fmt.Errorf("migrate: baseline requires one initial migration without a down section; run init first")
	}
	m := migrations[0]
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		snap, state, err := inspect(ctx, s)
		if err != nil {
			return err
		}
		if len(state.applied) != 0 {
			return fmt.Errorf("migrate: baseline requires a database without applied versions")
		}
		if snap.tables == 0 {
			return fmt.Errorf("migrate: empty database; apply the initial migration with up")
		}
		expected, err := snapshotHash(m.Up)
		if err != nil {
			return err
		}
		actual, err := snapshotHash(snap.SQL)
		if err != nil {
			return err
		}
		if expected != actual {
			return fmt.Errorf("migrate: initial migration does not match the current database")
		}
		if err := checkOutput(r.config.SchemaFile); err != nil {
			return err
		}
		if !snap.managed {
			if err := r.operation(Event{Phase: "create version table"}, func() error { return mutationHistory(ctx, s, snap) }); err != nil {
				return err
			}
		}
		if err := r.operation(Event{Version: m.Version, Phase: "record baseline version"}, func() error {
			if err := recordApplied(ctx, s.conn, m.Version); err != nil {
				return err
			}
			result.Completed = []string{m.Version}
			result.Version = m.Version
			return nil
		}); err != nil {
			return err
		}
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
