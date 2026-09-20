package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

const aggregateCompareWarmup = 2

// AggregateCompareOptions describes one explicitly repeatable comparison case.
// Keep input selectivity, dataset version, driver/session settings, and snapshot
// policy equivalent for the same Case. The library cannot establish that fact.
type AggregateCompareOptions struct {
	// Case is a stable scenario name, not a bind value or user identifier. It
	// accepts 1-128 ASCII letters, digits, dots, underscores, and hyphens,
	// beginning with a letter or digit. Use it with --workload in baseline CLI calls.
	Case string
	// Samples is the measured SELECT count per variant, from 5 to 1000.
	// Zero selects five. Each variant additionally runs two warmup SELECTs.
	Samples int
	// MaxRows bounds each result retained for value comparison. Zero selects
	// 100,000. Exceeding it fails the comparison without changing query LIMIT.
	MaxRows int
	// FloatAbsoluteTolerance and FloatRelativeTolerance apply only to driver
	// float32/float64 results. Zero requires exact equality. Relative tolerance is in
	// [0,1]; both must be finite and nonnegative. DECIMAL bytes remain exact.
	FloatAbsoluteTolerance float64
	FloatRelativeTolerance float64
}

// AggregateComparison reports an explicit comparison of auto, tikv, and
// tiflash_mpp requests in that order. Complete means all result checks, samples,
// plan queries, warning probes, and requested-policy checks succeeded. It does
// not prove that separate SELECT executions used the observed plan or inputs
// stayed unchanged. On error, already collected samples and plans are retained.
type AggregateComparison struct {
	// Options contains the effective defaults and value-comparison tolerances.
	Options  AggregateCompareOptions
	Complete bool
	Variants []AggregateVariantComparison
}

// AggregateVariantComparison describes one requested policy and its samples.
// SQL is a bind-free template. Plan comes from a separate EXPLAIN ANALYZE.
// PlanStatus is unrequested, matched, mismatch, or unknown; it never labels the
// ordinary samples' actual engine. Error describes this variant's failure.
// Policy checks include observed related-table accesses as well as the source;
// unknown table bindings cannot establish a match.
type AggregateVariantComparison struct {
	Name          string
	Requested     ReadPolicy
	SQL           string
	Fingerprint   string
	Model         string
	ArgumentCount int
	Rows          int64
	Samples       []AggregateComparisonSample
	LatencyMS     AggregateComparisonStatistics
	ServerRU      AggregateComparisonStatistics
	Plan          AggregatePlan
	PlanStatus    string
	Error         error
}

// AggregateComparisonSample is one successfully read, RU-measured, and
// value-checked ordinary SELECT. Duration includes raw database/sql scanning and
// row closure, excluding compilation, pinning, argument freezing, equality
// checks, probes, plan queries, and callbacks. It does not measure ScanAll's
// destination mapping or application-owned Scanner implementations.
type AggregateComparisonSample struct {
	StartedAt          time.Time
	Duration           time.Duration
	ServerRU           float64
	DiagnosticDuration time.Duration
}

// AggregateComparisonStatistics summarizes successful samples. LatencyMS uses
// milliseconds; ServerRU uses TiDB ru_consumption, not billed RU. A zero sample
// count is not evidence of zero cost. No winner or regression threshold is inferred.
type AggregateComparisonStatistics struct {
	Count   int
	Minimum float64
	Median  float64
	Mean    float64
	Maximum float64
}

// Compare explicitly executes auto, TiKV, and TiFlash MPP variants of this
// aggregate without mutating it. Existing ReadFrom/MPP requests are replaced;
// auto emits no policy hint and inherits the pinned session's settings.
// It requires *sql.DB, *sql.Conn, *sql.Tx, or their Observe wrappers.
//
// The comparison freezes standard bind arguments, including driver.Valuer
// results, once before I/O. Callers must provide fixed data or a suitable
// read snapshot and must not concurrently use a borrowed session or mutate q.
// Grouped queries require OrderBy; include tie-breakers for stable ordering.
// All raw result values and their order are compared with the first auto
// warmup. NULL and DECIMAL are exact; explicit tolerances cover floating values.
//
// There are two rotated warmup rounds, then Samples rotated measurement rounds.
// Every SELECT is followed immediately by its same-session ServerRU probe.
// Finally, each variant executes EXPLAIN ANALYZE and SHOW WARNINGS separately.
// Defaults run 21 ordinary SELECTs, 21 RU probes, and three plan/warning pairs.
// No replica DDL, session SET, transaction, or automatic hint adoption is added.
//
// One connection is pinned for the complete comparison. Observer and capture
// callbacks are delivered after release of an internally pinned connection,
// including on failure. They include warmups and plans; use WriteCapture for
// measurement-only baseline input. Borrowed connections/transactions stay open.
func (q *AggregateQuery[T]) Compare(ctx context.Context, executor QueryExecutor, options AggregateCompareOptions) (report AggregateComparison, err error) {
	if err = validateQueryExecution(ctx, executor); err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if err = options.normalize(); err != nil {
		return report, err
	}
	original, err := q.compile()
	if err != nil {
		return report, err
	}
	resolver, err := q.planAccessResolver(original)
	if err != nil {
		return report, err
	}
	if len(q.groupBy) != 0 && len(q.orderBy) == 0 {
		return report, fmt.Errorf("orm: aggregate Compare requires OrderBy for grouped results")
	}
	// Validate session affinity before evaluating user-owned Valuers.
	raw := unwrapObservedExecutor(executor)
	switch raw.(type) {
	case *sql.DB, *sql.Conn, *sql.Tx:
	default:
		return report, fmt.Errorf("orm: aggregate Compare requires *sql.DB, *sql.Conn, or *sql.Tx executor")
	}
	arguments, err := snapshotAggregateCompareArguments(original.arguments)
	if err != nil {
		return report, err
	}
	policies := []ReadPolicy{{}, {Engine: TiKV}, {Engine: TiFlash, MPP: MPPEnforce}}
	names := []string{"auto", "tikv", "tiflash_mpp"}
	report = AggregateComparison{Options: options, Variants: make([]AggregateVariantComparison, 3)}
	compiled := make([]compiledAggregate, 3)
	for i, policy := range policies {
		copy := *q
		copy.policy = policy
		compiled[i], err = copy.compile()
		if err != nil {
			return report, err
		}
		report.Variants[i] = AggregateVariantComparison{
			Name: names[i], Requested: policy, SQL: compiled[i].sql,
			Fingerprint: runtimecapture.StatementFingerprint("SELECT", compiled[i].sql),
			Model:       original.source.Name(), ArgumentCount: len(arguments),
			PlanStatus: "unknown", Samples: make([]AggregateComparisonSample, 0, options.Samples),
		}
	}
	ctx = executorStatementContext(ctx, executor)
	var session aggregateCompareSession
	var release func() error
	switch value := raw.(type) {
	case *sql.DB:
		conn, pinErr := value.Conn(ctx)
		if pinErr != nil {
			return report, fmt.Errorf("orm: pin aggregate Compare connection: %w", pinErr)
		}
		session, release = conn, conn.Close
	case *sql.Conn:
		session = value
	case *sql.Tx:
		session = value
	}
	var observations []aggregateCompareObservation
	defer func() {
		if release != nil {
			if releaseErr := release(); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("orm: release aggregate Compare connection: %w", releaseErr))
			}
		}
		for i := range report.Variants {
			summarizeAggregateComparison(&report.Variants[i])
		}
		report.Complete = err == nil
		for _, pending := range observations {
			pending.finish()
		}
	}()
	var reference, buffer []any
	haveReference := false
	for round := 0; round < aggregateCompareWarmup+options.Samples; round++ {
		for step := range compiled {
			index := (round + step) % len(compiled)
			variant := &report.Variants[index]
			if err = ctx.Err(); err != nil {
				variant.Error = err
				return report, err
			}
			phase := "aggregate_compare_warmup"
			if round >= aggregateCompareWarmup {
				phase = "aggregate_compare"
			}
			observation := beginStatementObservationWithMetadata(ctx, StatementSelect, variant.SQL, original.arguments, statementRuntimeMetadata{source: runtimecapture.SourceTypedAggregate, terminal: phase, model: variant.Model})
			sample := AggregateComparisonSample{StartedAt: time.Now()}
			buffer, err = readAggregateComparisonRows(ctx, session, compiled[index], arguments, buffer, options.MaxRows)
			sample.Duration = time.Since(sample.StartedAt)
			targetErr := err
			var ru ServerRUObservation
			if err == nil {
				probeStarted := time.Now()
				sample.ServerRU, err = readLastServerRU(ctx, session)
				sample.DiagnosticDuration = time.Since(probeStarted)
				ru = ServerRUObservation{Value: sample.ServerRU, Known: err == nil, DiagnosticDuration: sample.DiagnosticDuration, AuxiliaryStatements: 1, Error: err}
			} else {
				ru.Error = fmt.Errorf("orm: aggregate Compare did not probe RU after a failed SELECT")
			}
			rows := int64(len(buffer) / len(original.outputs))
			if observation != nil {
				observation.event.StartedAt = sample.StartedAt
				observation.event.ServerRU = &ru
				if observation.event.Warnings != nil {
					observation.event.Warnings.Error = ErrWarningsWithServerRU
				}
				observations = append(observations, aggregateCompareObservation{observation: observation, duration: sample.Duration, rows: rows, err: targetErr})
			}
			if err == nil {
				if !haveReference {
					reference, buffer, haveReference = buffer, nil, true
				} else {
					err = equalAggregateComparisonRows(reference, buffer, original.outputs, options)
				}
			}
			if err != nil {
				variant.Error = fmt.Errorf("orm: aggregate Compare %s round %d: %w", variant.Name, round+1, err)
				return report, variant.Error
			}
			variant.Rows = rows
			if round >= aggregateCompareWarmup {
				variant.Samples = append(variant.Samples, sample)
			}
		}
	}
	// Plans execute separately, after all ordinary measurements. Neither their
	// RU nor their duration becomes a SELECT sample.
	var planErrors []error
	for index := range compiled {
		variant := &report.Variants[index]
		var pending aggregateCompareObservation
		variant.Plan, pending, variant.Error = inspectAggregateComparisonPlan(ctx, session, compiled[index], arguments, original.arguments, variant.Requested, resolver)
		if pending.observation != nil {
			observations = append(observations, pending)
		}
		if variant.Error == nil {
			variant.PlanStatus = aggregateComparisonPlanStatus(variant.Plan, original.source.TableName())
			if variant.PlanStatus == "mismatch" || variant.PlanStatus == "unknown" {
				variant.Error = fmt.Errorf("separate plan policy check is %s", variant.PlanStatus)
			}
		}
		if variant.Error != nil {
			planErrors = append(planErrors, fmt.Errorf("orm: aggregate Compare %s: %w", variant.Name, variant.Error))
		}
		if contextErr := ctx.Err(); contextErr != nil {
			planErrors = append(planErrors, contextErr)
			break
		}
	}
	err = errors.Join(planErrors...)
	return report, err
}

type aggregateCompareSession interface {
	QueryExecutor
	serverRUQueryer
}

type aggregateCompareObservation struct {
	observation *statementObservation
	duration    time.Duration
	rows        int64
	err         error
}

func (p aggregateCompareObservation) finish() {
	p.observation.finishOutcomeDuration(0, false, p.rows, p.err == nil, p.err, p.duration)
}

func (o *AggregateCompareOptions) normalize() error {
	if err := runtimecapture.ValidateWorkloadName(o.Case); err != nil {
		return fmt.Errorf("orm: aggregate Compare Case: %w", err)
	}
	if o.Samples == 0 {
		o.Samples = 5
	}
	if o.Samples < 5 || o.Samples > 1000 {
		return fmt.Errorf("orm: aggregate Compare Samples must be between 5 and 1000")
	}
	if o.MaxRows == 0 {
		o.MaxRows = 100000
	}
	if o.MaxRows < 1 {
		return fmt.Errorf("orm: aggregate Compare MaxRows must be positive")
	}
	if math.IsNaN(o.FloatAbsoluteTolerance) || math.IsInf(o.FloatAbsoluteTolerance, 0) || o.FloatAbsoluteTolerance < 0 || math.IsNaN(o.FloatRelativeTolerance) || math.IsInf(o.FloatRelativeTolerance, 0) || o.FloatRelativeTolerance < 0 || o.FloatRelativeTolerance > 1 {
		return fmt.Errorf("orm: aggregate Compare float tolerances must be finite and nonnegative, with relative tolerance at most 1")
	}
	return nil
}

func inspectAggregateComparisonPlan(ctx context.Context, session aggregateCompareSession, c compiledAggregate, arguments, original []any, policy ReadPolicy, resolver planAccessResolver) (plan AggregatePlan, pending aggregateCompareObservation, err error) {
	plan.Requested = policy
	statement := explainAnalyzePrefix + c.sql
	pending.observation = beginStatementObservationWithMetadata(ctx, StatementExplainAnalyze, statement, original, statementRuntimeMetadata{source: runtimecapture.SourcePlan, terminal: "aggregate_compare_plan", model: c.source.Name()})
	started := time.Now()
	if pending.observation != nil {
		pending.observation.event.StartedAt = started
	}
	rows, err := session.QueryContext(ctx, statement, arguments...)
	if err == nil {
		if rows == nil {
			err = fmt.Errorf("orm: aggregate Compare plan executor returned nil rows")
		} else {
			plan.Executed, err = collectExplainAnalyzeRows(rows, resolver)
		}
	}
	pending.duration, pending.rows, pending.err = time.Since(started), int64(len(plan.Executed)), err
	if err == nil {
		warningStarted := time.Now()
		plan.Warnings, plan.WarningsError = collectStatementWarnings(ctx, session)
		pending.observation.recordPlanWarnings(plan.Warnings, plan.WarningsError, time.Since(warningStarted), 1)
		err = plan.WarningsError
	}
	return plan, pending, err
}

func aggregateComparisonPlanStatus(plan AggregatePlan, table string) string {
	if plan.Requested.Engine == "" && plan.Requested.MPP == "" {
		return "unrequested"
	}
	found, mpp, unknown, mismatch := false, false, false, false
	for _, row := range plan.Executed {
		task := row.TaskInfo()
		unknown = unknown || !task.Known
		mpp = mpp || task.Kind == "mpp"
		if task.Engine == "" || planAccessObjectTable(row.AccessObject) == "" {
			continue
		}
		if row.PhysicalTable == "" {
			unknown = true
		}
		found = found || row.PhysicalTable == table
		mismatch = mismatch || (plan.Requested.Engine != "" && task.Engine != plan.Requested.Engine)
	}
	if mismatch {
		return "mismatch"
	}
	if !found || unknown {
		return "unknown"
	}
	if plan.Requested.MPP == MPPEnforce && !mpp {
		return "mismatch"
	}
	return "matched"
}
