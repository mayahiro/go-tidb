package migrate

import (
	"context"
	"fmt"
)

// Step contains the exact trusted SQL that a migration will execute.
type Step struct {
	Version    int64    `json:"version"`
	Name       string   `json:"name"`
	Statements []string `json:"statements"`
}

// Plan is a read-only preview. Apply always reloads files and validates the
// current database under the lock; a previously printed plan is not a lease.
type Plan struct {
	Database       string    `json:"database"`
	Direction      Direction `json:"direction"`
	CurrentVersion int64     `json:"current_version"`
	TargetVersion  int64     `json:"target_version"`
	CreatesHistory bool      `json:"creates_history"`
	Steps          []Step    `json:"steps"`
}

func buildPlan(migrations []Migration, state historyState, snap snapshot, direction Direction, steps int) (Plan, error) {
	p := Plan{Direction: direction, CurrentVersion: state.version(), TargetVersion: state.version()}
	if direction != Up && direction != Down {
		return p, fmt.Errorf("migrate: direction must be up or down")
	}
	if steps < 0 || direction == Down && steps == 0 {
		return p, fmt.Errorf("migrate: down requires a positive step count; up accepts zero for all pending versions")
	}
	if state.dirty != nil {
		return p, ErrDirty
	}
	if state.hash != "" && state.hash != snap.Hash {
		return p, ErrDrift
	}
	if state.hash == "" && snap.tables > 0 {
		return p, ErrUnmanaged
	}
	var candidates []Migration
	if direction == Up {
		for _, m := range migrations {
			if m.Version > state.version() {
				candidates = append(candidates, m)
			}
		}
	} else {
		byVersion := map[int64]Migration{}
		for _, m := range migrations {
			byVersion[m.Version] = m
		}
		for i := len(state.stack) - 1; i >= 0; i-- {
			v := state.stack[i]
			if v <= state.baseline {
				break
			}
			candidates = append(candidates, byVersion[v])
		}
	}
	if steps > len(candidates) {
		return p, fmt.Errorf("migrate: requested steps exceed available migrations or cross the adoption baseline")
	}
	if steps > 0 {
		candidates = candidates[:steps]
	}
	for _, m := range candidates {
		source := m.Up
		if direction == Down {
			source = m.Down
			if source == "" {
				return p, fmt.Errorf("migrate: version %d is irreversible (no down section)", m.Version)
			}
		}
		statements, err := splitSQL(source)
		if err != nil {
			return p, err
		}
		p.Steps = append(p.Steps, Step{m.Version, m.Name, statements})
	}
	if len(p.Steps) > 0 {
		p.CreatesHistory = !snap.managed
		if direction == Up {
			p.TargetVersion = p.Steps[len(p.Steps)-1].Version
		} else {
			remaining := len(state.stack) - len(p.Steps)
			p.TargetVersion = 0
			if remaining > 0 {
				p.TargetVersion = state.stack[remaining-1]
			}
		}
	}
	return p, nil
}

// Plan previews up (steps=0 means all pending) or down (steps must be positive).
// All selected down sections are checked before any operation can start.
func (r *Runner) Plan(ctx context.Context, direction Direction, steps int) (result Plan, err error) {
	migrations, err := r.load()
	if err != nil {
		return result, err
	}
	err = r.session(ctx, func(s session) error {
		snap, _, state, err := inspect(ctx, s, migrations)
		if err != nil {
			return err
		}
		result, err = buildPlan(migrations, state, snap, direction, steps)
		result.Database = s.database
		return err
	})
	return result, err
}
