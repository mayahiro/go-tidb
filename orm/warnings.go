package orm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/warningcheck"
)

// ErrWarningsWithServerRU indicates that warnings were not collected because
// ServerRU collection takes precedence. Both probes consume last-statement
// state on TiDB Cloud Starter; a second probe could describe the first probe.
var ErrWarningsWithServerRU = errors.New("orm: warning collection conflicts with ServerRU collection; collect them in separate executions")

// WarningObservation describes same-session SHOW WARNINGS collection.
// It is nil on StatementEvent when collection was not requested or applicable.
// Explicit aggregate/vector plans publish their already collected warnings too.
// Raw messages and Error can contain SQL values; the built-in logger and capture
// write only fixed diagnostic categories, counts, and failure status.
type WarningObservation struct {
	// Warnings holds raw server rows for this statement, not a prior execution.
	Warnings []PlanWarning
	// Known is true after successful collection, including an empty result.
	Known bool
	// DiagnosticDuration covers warning collection and its connection management.
	DiagnosticDuration time.Duration
	// AuxiliaryStatements counts attempted SHOW WARNINGS statements.
	AuxiliaryStatements int
	// Error reports collection or release failure without changing query results.
	Error error
}

// Diagnostics classifies collected warnings without I/O or raw message output.
// Unrecognized warnings stay generic; an empty result does not prove MPP usage.
func (w WarningObservation) Diagnostics() []check.Diagnostic {
	return summarizeWarnings(w.Warnings).Diagnostics(w.Error != nil)
}

func summarizeWarnings(rows []PlanWarning) warningcheck.Summary {
	var summary warningcheck.Summary
	for _, row := range rows {
		summary.Add(row.Level, row.Code, row.Message)
	}
	return summary
}

// WarningOption enables warning collection on observers and runtime captures.
type WarningOption interface {
	StatementObserverOption
	RuntimeCaptureOption
}

type collectWarningsOption struct{}

func (collectWarningsOption) applyStatementObserver(value *statementObserverContextValue) {
	value.options |= statementObserverCollectWarnings
}
func (collectWarningsOption) applyRuntimeCapture(value *statementObserverContextValue) {
	value.options |= statementRuntimeCollectWarnings
}

// CollectWarnings adds one same-session SHOW WARNINGS after each recognized DML
// statement. It requires *sql.DB, *sql.Conn, *sql.Tx, or their Observe wrappers.
// Rows are closed before probing; callbacks run after connection release.
// Collection failure never replaces the statement result. With CollectServerRU,
// warnings report ErrWarningsWithServerRU and no warning probe is issued.
// Some MPP warnings are only exposed by EXPLAIN; use explicit plan inspection
// for those. This option never executes EXPLAIN or replays a statement.
func CollectWarnings() WarningOption { return collectWarningsOption{} }

func statementWarningCollectionEnabled(value *statementObserverContextValue) bool {
	return value != nil && (value.observer != nil && value.options&statementObserverCollectWarnings != 0 ||
		value.runtimeCapture != nil && value.options&statementRuntimeCollectWarnings != 0)
}

func (observation *statementObservation) recordPlanWarnings(warnings []PlanWarning, err error, elapsed time.Duration, attempts int) {
	if observation != nil {
		observation.event.Warnings = &WarningObservation{Warnings: warnings, Known: warnings != nil, Error: err, DiagnosticDuration: elapsed, AuxiliaryStatements: attempts}
	}
}

func collectStatementWarnings(ctx context.Context, session QueryExecutor) ([]PlanWarning, error) {
	rows, err := session.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return nil, fmt.Errorf("orm: read statement warnings: %w", err)
	}
	if rows == nil {
		return nil, fmt.Errorf("orm: warning executor returned nil rows")
	}
	columns, err := rows.Columns()
	if err != nil {
		return nil, closeRowsAfterError("statement warnings", rows, err)
	}
	if err := validatePlanColumns("SHOW WARNINGS", columns, []string{"Level", "Code", "Message"}); err != nil {
		return nil, closeRowsAfterError("statement warnings", rows, err)
	}
	warnings := make([]PlanWarning, 0)
	for rows.Next() {
		var warning PlanWarning
		if err := rows.Scan(&warning.Level, &warning.Code, &warning.Message); err != nil {
			return nil, closeRowsAfterError("statement warnings", rows, fmt.Errorf("orm: scan statement warning: %w", err))
		}
		warnings = append(warnings, warning)
	}
	if err := finishRows("statement warnings", rows); err != nil {
		return nil, err
	}
	return warnings, nil
}
