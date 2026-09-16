package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

// PlanWarning is one same-session SHOW WARNINGS row for an explicit plan query.
// Message is unredacted server text and can include SQL values. It is returned
// to the caller only and is not automatically written to RuntimeCapture.
type PlanWarning struct {
	Level   string
	Code    int
	Message string
}

// AggregatePlan separates requested hints from a planned or executed statement.
// Explain populates Planned; ExplainAnalyze populates Executed. Neither observes
// another SELECT. A non-nil empty Warnings means successful warning collection;
// WarningsError reports an auxiliary failure without discarding a valid plan.
type AggregatePlan struct {
	Requested     ReadPolicy
	Planned       []ExplainRow
	Executed      ExplainAnalyzePlan
	Warnings      []PlanWarning
	WarningsError error
}

// Explain inspects the aggregate SELECT and then collects SHOW WARNINGS on the
// same connection. It requires *sql.DB, *sql.Conn, *sql.Tx, or Observe wrappers.
// A pooled DB is pinned only for these statements. No session SET is executed.
// It does not run the SELECT, although TiDB may evaluate expressions while
// optimizing. Observer callbacks run after warnings and connection release.
func (q *AggregateQuery[T]) Explain(ctx context.Context, executor QueryExecutor) (AggregatePlan, error) {
	return q.inspectPlan(ctx, executor, false)
}

// ExplainAnalyze explicitly executes the complete aggregate SELECT and returns
// its runtime plan plus same-session warnings. It consumes database resources
// and does not return the aggregate values. It never replays a prior ScanAll.
// CollectServerRU does not probe plan statements; runtime operator details remain
// in Executed. Measure ordinary SELECT ServerRU separately using ScanAll.
func (q *AggregateQuery[T]) ExplainAnalyze(ctx context.Context, executor QueryExecutor) (AggregatePlan, error) {
	return q.inspectPlan(ctx, executor, true)
}

func (q *AggregateQuery[T]) inspectPlan(ctx context.Context, executor QueryExecutor, analyze bool) (result AggregatePlan, err error) {
	if err = validateQueryExecution(ctx, executor); err != nil {
		return result, err
	}
	c, err := q.compile()
	if err != nil {
		return result, err
	}
	resolver, err := q.planAccessResolver(c)
	if err != nil {
		return result, err
	}
	result.Requested = ReadPolicy{Engine: q.policy.Engine, MPP: q.policy.MPP}
	ctx = executorStatementContext(ctx, executor)
	var session QueryExecutor
	var release func() error
	switch raw := unwrapObservedExecutor(executor).(type) {
	case *sql.DB:
		conn, pinErr := raw.Conn(ctx)
		if pinErr != nil {
			return result, fmt.Errorf("orm: pin aggregate plan connection: %w", pinErr)
		}
		session, release = conn, conn.Close
	case *sql.Conn:
		session = raw
	case *sql.Tx:
		session = raw
	default:
		return result, fmt.Errorf("orm: aggregate plan requires *sql.DB, *sql.Conn, or *sql.Tx executor")
	}
	if release != nil {
		defer func() {
			if release != nil {
				_ = release()
			}
		}()
	}
	operation, prefix := StatementExplain, explainPrefix
	if analyze {
		operation, prefix = StatementExplainAnalyze, explainAnalyzePrefix
	}
	statement := prefix + c.sql
	metadata := statementRuntimeMetadata{source: runtimecapture.SourcePlan, terminal: "aggregate_explain", model: c.source.Name()}
	if analyze {
		metadata.terminal = "aggregate_explain_analyze"
	}
	observation := beginStatementObservationWithMetadata(ctx, operation, statement, c.arguments, metadata)
	started := time.Now()
	rows, err := session.QueryContext(ctx, statement, c.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: query aggregate plan: %w", err)
	} else if rows == nil {
		err = fmt.Errorf("orm: aggregate plan executor returned nil rows")
	} else if analyze {
		result.Executed, err = collectExplainAnalyzeRows(rows, resolver)
	} else {
		result.Planned, err = collectExplainRows(rows)
	}
	elapsed := time.Since(started)
	if err == nil {
		result.Warnings, result.WarningsError = collectPlanWarnings(ctx, session)
	}
	if release != nil {
		result.WarningsError = errors.Join(result.WarningsError, release())
		release = nil
	}
	observation.finishOutcomeDuration(0, false, int64(len(result.Planned)+len(result.Executed)), err == nil, err, elapsed)
	return result, err
}

func (q *AggregateQuery[T]) planAccessResolver(c compiledAggregate) (planAccessResolver, error) {
	resolver := planAccessResolver{hasRoot: true, root: planAccessBinding{alias: aggregateRootAlias, physicalTable: c.source.TableName(), model: c.source.Name()}}
	nextAlias := 0
	err := resolver.appendRelationPredicates(c.source, q.predicates, "", &nextAlias)
	return resolver, err
}

func collectPlanWarnings(ctx context.Context, session QueryExecutor) ([]PlanWarning, error) {
	rows, err := session.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return nil, fmt.Errorf("orm: read plan warnings: %w", err)
	}
	if rows == nil {
		return nil, fmt.Errorf("orm: plan warnings executor returned nil rows")
	}
	columns, err := rows.Columns()
	if err != nil {
		return nil, closeRowsAfterError("plan warnings", rows, err)
	}
	if err := validatePlanColumns("SHOW WARNINGS", columns, []string{"Level", "Code", "Message"}); err != nil {
		return nil, closeRowsAfterError("plan warnings", rows, err)
	}
	warnings := make([]PlanWarning, 0)
	for rows.Next() {
		var warning PlanWarning
		if err := rows.Scan(&warning.Level, &warning.Code, &warning.Message); err != nil {
			return nil, closeRowsAfterError("plan warnings", rows, fmt.Errorf("orm: scan plan warning: %w", err))
		}
		warnings = append(warnings, warning)
	}
	if err := finishRows("plan warnings", rows); err != nil {
		return nil, err
	}
	return warnings, nil
}

// Diagnostics examines this returned plan without I/O. Existing runtime facts
// apply to Executed; large aggregate scans are informational, not suppressed.
// PLN005 reports recognized table tasks using a different requested engine;
// PLN006 reports an enforced MPP request with recognized storage tasks but no MPP.
// Unknown tasks remain unknown and are never treated as proof of fallback.
// Raw server warning messages are available separately in Warnings.
func (p AggregatePlan) Diagnostics() []check.Diagnostic {
	diagnostics := p.Executed.Diagnostics()
	for i := range diagnostics {
		if diagnostics[i].Code == codePlanLargeTableFullScan {
			diagnostics[i].Severity = check.SeverityInfo
			diagnostics[i].Suggestion = "Compare aggregate input and result cardinality, latency, and ServerRU with representative and selective inputs"
		}
	}
	var mismatch []check.Evidence
	storage, mpp, unknown := false, false, false
	inspect := func(id, task, access string) {
		info := parsePlanTask(task)
		unknown = unknown || !info.Known
		mpp = mpp || info.Kind == "mpp"
		if info.Engine == "" {
			return
		}
		storage = true
		if p.Requested.Engine != "" && info.Engine != p.Requested.Engine && planAccessObjectTable(access) != "" {
			mismatch = append(mismatch, check.Evidence{Message: fmt.Sprintf("Operator %s uses %s for %s; requested %s", planOperatorIdentifier(id), info.Engine, access, p.Requested.Engine)})
		}
	}
	if p.Executed != nil {
		for _, row := range p.Executed {
			inspect(row.ID, row.Task, row.AccessObject)
		}
	} else {
		for _, row := range p.Planned {
			inspect(row.ID, row.Task, row.AccessObject)
		}
	}
	if len(mismatch) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{Code: "PLN005", Severity: check.SeverityWarning, Title: "Plan differs from requested storage", Message: "Recognized table access tasks use a different storage engine", Evidence: mismatch, Suggestion: "Inspect same-statement warnings and replica readiness", Suppressible: true, Reference: "https://docs.pingcap.com/tidb/stable/use-tidb-to-read-tiflash/"})
	}
	if p.Requested.MPP == MPPEnforce && storage && !mpp && !unknown {
		diagnostics = append(diagnostics, check.Diagnostic{Code: "PLN006", Severity: check.SeverityWarning, Title: "Plan does not use requested MPP", Message: "Recognized storage tasks do not use MPP despite MPPEnforce", Suggestion: "Inspect same-statement warnings for unsupported operations or missing replicas", Suppressible: true, Reference: "https://docs.pingcap.com/tidb/stable/use-tiflash-mpp-mode/"})
	}
	return diagnostics
}
