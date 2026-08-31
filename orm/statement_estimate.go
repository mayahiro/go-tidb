package orm

import (
	"fmt"
	"math"
)

// StatementCountEstimate bounds the number of SQL statements used by an ORM
// operation without accessing a database.
//
// Minimum is the smallest possible count. Maximum is meaningful only when
// MaximumKnown is true. An unknown maximum is reported as zero because row and
// relation cardinalities cannot be proven from the query builder alone.
type StatementCountEstimate struct {
	Minimum      int64
	Maximum      int64
	MaximumKnown bool
}

// Exact reports whether Minimum and Maximum prove one statement count.
func (estimate StatementCountEstimate) Exact() bool {
	return estimate.MaximumKnown && estimate.Minimum == estimate.Maximum
}

// EstimateAllStatements returns a static bound for the number of SQL
// statements used by All, including its root SELECT and collection preloads.
//
// The estimate applies the same offline validation as Build and does not
// execute custom driver.Valuer implementations. Inline to-one joins do not add
// statements. A positive Limit bounds the root row count; unrestricted root
// collections loaded from their complete source add at most one statement
// each. MaximumKnown is false when nested collection cardinality or an
// unbounded keyed collection prevents a finite upper bound from being proven.
// Empty results, NULL relation keys, and duplicate relation keys can make the
// executed count smaller than a known maximum.
func (q *SelectQuery[T]) EstimateAllStatements() (StatementCountEstimate, error) {
	compiled, err := q.compile()
	if err != nil {
		return StatementCountEstimate{}, err
	}

	parentRows := statementRowUpperBound{}
	if q.selection.pagination.limitSet {
		parentRows.maximum = q.selection.pagination.limit
		parentRows.known = true
	}
	additional, known, err := estimatePreloadStatementMaximum(compiled.preloads, parentRows)
	if err != nil {
		return StatementCountEstimate{}, err
	}
	estimate := StatementCountEstimate{Minimum: 1}
	if !known {
		return estimate, nil
	}
	maximum, err := addStatementCounts(1, additional)
	if err != nil {
		return StatementCountEstimate{}, err
	}
	estimate.Maximum = maximum
	estimate.MaximumKnown = true
	return estimate, nil
}

type statementRowUpperBound struct {
	maximum int64
	known   bool
}

func estimatePreloadStatementMaximum(plans []*preloadPlan, parentRows statementRowUpperBound) (int64, bool, error) {
	var total int64
	for _, plan := range plans {
		current, known, err := estimatePreloadPlanStatementMaximum(plan, parentRows)
		if err != nil {
			return 0, false, err
		}
		if !known {
			return 0, false, nil
		}
		total, err = addStatementCounts(total, current)
		if err != nil {
			return 0, false, err
		}
	}
	return total, true, nil
}

func estimatePreloadPlanStatementMaximum(plan *preloadPlan, parentRows statementRowUpperBound) (int64, bool, error) {
	if parentRows.known && parentRows.maximum == 0 {
		return 0, true, nil
	}
	if plan.inline {
		return estimatePreloadStatementMaximum(plan.children, parentRows)
	}

	var own int64
	if plan.loadAllSources {
		own = 1
	} else {
		if !parentRows.known {
			return 0, false, nil
		}
		if plan.batchSize <= 0 {
			return 0, false, fmt.Errorf("orm: estimate All statements for %s.%s with invalid preload batch size %d", plan.sourceName, plan.relationName, plan.batchSize)
		}
		own = statementBatchCount(parentRows.maximum, int64(plan.batchSize))
	}

	children, known, err := estimatePreloadStatementMaximum(plan.children, statementRowUpperBound{})
	if err != nil {
		return 0, false, err
	}
	if !known {
		return 0, false, nil
	}
	total, err := addStatementCounts(own, children)
	if err != nil {
		return 0, false, err
	}
	return total, true, nil
}

func statementBatchCount(rowCount, rowsPerStatement int64) int64 {
	if rowCount == 0 {
		return 0
	}
	return (rowCount-1)/rowsPerStatement + 1
}

func addStatementCounts(left, right int64) (int64, error) {
	if right > math.MaxInt64-left {
		return 0, fmt.Errorf("orm: estimated All statement count exceeds %d", int64(math.MaxInt64))
	}
	return left + right, nil
}
