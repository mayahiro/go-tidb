package orm

import (
	"math"
	"strings"
	"unicode"
)

// OperatorCardinality describes one operator's output, not rows consumed or
// transferred bytes. ActualKnown is true only for EXPLAIN ANALYZE observations.
type OperatorCardinality struct {
	ID             string
	Estimated      float64
	EstimatedKnown bool
	Actual         int64
	ActualKnown    bool
}

// OperatorSummary retains processing location and immediate child outputs.
// Children describe available inputs, not a measured consumed-row count: loops,
// early termination, joins, and distributed exchanges can change consumption.
type OperatorSummary struct {
	Operator string
	Task     PlanTask
	Output   OperatorCardinality
	Children []OperatorCardinality
}

// PlanSummary separates estimates from execution observations. TreeKnown is
// false for an unfamiliar/malformed indentation format; child relationships and
// Result are then unavailable. Operators and their individual outputs remain.
// This summary neither measures network bytes nor observes an ordinary SELECT.
type PlanSummary struct {
	Executed  bool
	TreeKnown bool
	Result    OperatorCardinality
	Operators []OperatorSummary
}

// Summary describes aggregate, join, filter, window, and exchange locations and
// output cardinalities without I/O. Final and partial aggregation remain separate.
func (p AggregatePlan) Summary() PlanSummary { return summarizeReadPlan(p.Planned, p.Executed) }

func summarizeReadPlan(planned []ExplainRow, executed ExplainAnalyzePlan) PlanSummary {
	result := PlanSummary{Executed: executed != nil}
	var ids []string
	appendRow := func(id, task string, estimate float64, actual int64) {
		identifier := planOperatorIdentifier(id)
		result.Operators = append(result.Operators, OperatorSummary{
			Operator: planOperatorName(identifier), Task: parsePlanTask(task),
			Output: OperatorCardinality{ID: identifier, Estimated: estimate, EstimatedKnown: estimate >= 0 && !math.IsNaN(estimate) && !math.IsInf(estimate, 0), Actual: actual, ActualKnown: result.Executed && actual >= 0},
		})
		ids = append(ids, id)
	}
	if result.Executed {
		for _, row := range executed {
			appendRow(row.ID, row.Task, row.EstRows, row.ActRows)
		}
	} else {
		for _, row := range planned {
			appendRow(row.ID, row.Task, row.EstRows, 0)
		}
	}
	if len(ids) == 0 {
		return result
	}
	parents := make([]int, len(ids))
	stack := make([]int, 0, 8)
	for i, id := range ids {
		depth, known := planTreeDepth(id)
		if !known || (i == 0 && depth != 0) || (i != 0 && (depth == 0 || depth > len(stack))) {
			return result
		}
		parents[i] = -1
		if depth != 0 {
			parents[i] = stack[depth-1]
		}
		stack = append(stack[:depth], i)
	}
	result.TreeKnown = true
	if result.Operators[0].Task.Kind == "root" {
		result.Result = result.Operators[0].Output
	}
	for i, parent := range parents {
		if parent >= 0 {
			result.Operators[parent].Children = append(result.Operators[parent].Children, result.Operators[i].Output)
		}
	}
	return result
}

func planTreeDepth(id string) (int, bool) {
	start := strings.IndexFunc(id, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if start < 0 {
		return 0, false
	}
	prefix := []rune(id[:start])
	if len(prefix) == 0 {
		return 0, true
	}
	if len(prefix)%2 != 0 || len(prefix) < 2 || prefix[len(prefix)-1] != '─' || (prefix[len(prefix)-2] != '└' && prefix[len(prefix)-2] != '├') {
		return 0, false
	}
	for i := 0; i < len(prefix)-2; i += 2 {
		if (prefix[i] != ' ' && prefix[i] != '│') || prefix[i+1] != ' ' {
			return 0, false
		}
	}
	return len(prefix) / 2, true
}
