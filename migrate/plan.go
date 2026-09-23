package migrate

import (
	"context"
	"fmt"
)

// Step contains the SQL for one migration file, in execution order.
type Step struct {
	Version    string   `json:"version"`
	Statements []string `json:"statements"`
}

// Plan is a read-only preview. Apply reloads files and records under the lock.
// CurrentVersion and TargetVersion identify the latest registration, not a
// high-water mark. Earlier filenames can still be pending.
type Plan struct {
	Database       string    `json:"database"`
	Direction      Direction `json:"direction"`
	CurrentVersion string    `json:"current_version"`
	TargetVersion  string    `json:"target_version"`
	CreatesHistory bool      `json:"creates_history"`
	Steps          []Step    `json:"steps"`
}

func buildPlan(migrations []Migration, state historyState, snap snapshot, direction Direction, steps int) (Plan, error) {
	p := Plan{Direction: direction, CurrentVersion: state.version(), TargetVersion: state.version()}
	if direction != Up && direction != Down {
		return p, fmt.Errorf("migrate: direction must be up or down")
	}
	if steps < 0 || direction == Down && steps == 0 {
		return p, fmt.Errorf("migrate: down requires a positive file count; up accepts zero for all pending versions")
	}
	if snap.tables > 0 && !snap.managed {
		return p, ErrUnmanaged
	}
	byVersion := make(map[string]Migration, len(migrations))
	for _, m := range migrations {
		byVersion[m.Version] = m
	}
	var versions []string
	if direction == Up {
		applied := make(map[string]bool, len(state.applied))
		for _, v := range state.applied {
			applied[v.version] = true
		}
		for _, m := range migrations {
			if !applied[m.Version] {
				versions = append(versions, m.Version)
			}
		}
	} else {
		for i := len(state.applied) - 1; i >= 0; i-- {
			versions = append(versions, state.applied[i].version)
		}
	}
	if steps > len(versions) {
		return p, fmt.Errorf("migrate: requested file count exceeds available migrations")
	}
	if steps > 0 {
		versions = versions[:steps]
	}
	for _, v := range versions {
		m, ok := byVersion[v]
		if !ok {
			return p, fmt.Errorf("migrate: missing SQL file for applied version %s", v)
		}
		if direction == Down && m.Down == "" {
			return p, fmt.Errorf("migrate: version %s is irreversible (no down section)", v)
		}
		statements, err := migrationStatements(m, direction)
		if err != nil {
			return p, err
		}
		p.Steps = append(p.Steps, Step{v, statements})
	}
	if len(p.Steps) > 0 {
		p.CreatesHistory = !snap.managed
		if direction == Up {
			p.TargetVersion = p.Steps[len(p.Steps)-1].Version
		} else {
			p.TargetVersion = ""
			if remaining := len(state.applied) - len(p.Steps); remaining > 0 {
				p.TargetVersion = state.applied[remaining-1].version
			}
		}
	}
	return p, nil
}

// Plan previews Up (zero files means all pending) or Down (positive file count).
// Selected Down sections are all checked before any migration can execute.
func (r *Runner) Plan(ctx context.Context, direction Direction, steps int) (result Plan, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		snap, state, err := inspect(ctx, s)
		if err != nil {
			return err
		}
		result, err = buildPlan(migrations, state, snap, direction, steps)
		result.Database = s.database
		return err
	})
	return result, err
}
