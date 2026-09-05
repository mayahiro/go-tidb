package runtimecapture

import (
	"fmt"
	"math"

	"github.com/mayahiro/go-tidb/check"
)

const (
	codeWorkloadRegression  = "RU003"
	codeWorkloadUnavailable = "RU004"
)

// WorkloadComparison compares mean per-scope totals, independently of the
// per-fingerprint means. A passing workload does not suppress fingerprint
// changes or unavailable fingerprint comparisons.
type WorkloadComparison struct {
	Status              string            `json:"status"`
	Reason              string            `json:"reason,omitempty"`
	Baseline            *WorkloadMetrics  `json:"baseline,omitempty"`
	Current             *WorkloadServerRU `json:"current,omitempty"`
	ServerRULimit       float64           `json:"server_ru_limit"`
	StatementCountLimit float64           `json:"statement_count_limit"`
	ServerRURegressed   bool              `json:"server_ru_regressed"`
	StatementsRegressed bool              `json:"statements_regressed"`
}

func compareWorkload(current *WorkloadServerRU, baseline *WorkloadMetrics) *WorkloadComparison {
	if current == nil && baseline == nil {
		return nil
	}
	result := &WorkloadComparison{Status: "unavailable"}
	if baseline != nil {
		copy := *baseline
		result.Baseline = &copy
		result.ServerRULimit = serverRUComparisonLimit(baseline.ServerRU.Mean, baseline.ServerRU.Maximum)
		result.StatementCountLimit = serverRUComparisonLimit(baseline.StatementCount.Mean, baseline.StatementCount.Maximum)
	}
	if current != nil {
		copy := *current
		result.Current = &copy
	}
	switch {
	case baseline == nil:
		result.Reason = "the baseline has no workload metrics; create it with the same --workload name"
	case current == nil:
		result.Reason = "current analysis requires --workload with the baseline workload name"
	case current.Name != baseline.Name:
		result.Reason = "current and baseline workload names differ"
	default:
		result.Reason = workloadCoverageProblem(*current)
		if result.Reason == "" {
			result.Status = "pass"
			result.ServerRURegressed = current.ServerRU.Mean > result.ServerRULimit
			result.StatementsRegressed = current.StatementCount.Mean > result.StatementCountLimit
			if result.ServerRURegressed || result.StatementsRegressed {
				result.Status = "regression"
			}
		}
	}
	return result
}

func (workload WorkloadMetrics) validateBaseline(statistics []FingerprintServerRUBaseline) error {
	if err := workload.validate(); err != nil {
		return err
	}
	if workload.Scopes < ServerRUComparisonMinimumSamples || workload.StatementCount.Minimum < 1 {
		return fmt.Errorf("workload baseline requires at least five complete scopes with DML statements")
	}
	// Unlike descriptive totals, a saved operation budget cannot use saturated
	// sums. Baseline creation rejects overflow before discarding coverage fields.
	expected := workload.ServerRU.Total / float64(workload.Scopes)
	if math.Abs(workload.ServerRU.Mean-expected) > math.Max(1, math.Abs(expected))*1e-12 {
		return fmt.Errorf("workload baseline has inconsistent ServerRU total and mean")
	}
	var statements int
	var ru float64
	for _, entry := range statistics {
		statements += entry.Count
		ru = addServerRUSaturated(ru, entry.Total)
	}
	if statements != workload.Statements || math.Abs(ru-workload.ServerRU.Total) > math.Max(1, math.Abs(ru))*1e-12 {
		return fmt.Errorf("workload baseline totals disagree with fingerprint measurements")
	}
	return nil
}

func (comparison *WorkloadComparison) diagnostics() []check.Diagnostic {
	if comparison == nil || comparison.Status == "pass" {
		return nil
	}
	evidence := []check.Evidence{{Message: FormatWorkloadComparison(*comparison)}}
	if comparison.Current != nil {
		evidence = append(evidence, check.Evidence{Message: FormatWorkloadServerRU(*comparison.Current)})
	}
	evidence = append(evidence, check.Evidence{Message: "Each sample is one captured scope, not one statement; totals exclude transaction-control RU and diagnostic probes and are not billed or whole-transaction RU"})
	if comparison.Status == "regression" {
		return []check.Diagnostic{{
			Code: codeWorkloadRegression, Severity: check.SeverityError,
			Title:      "Workload cost regressed from the baseline",
			Message:    "Mean per-scope ServerRU or DML statement count exceeded both 130 percent of its baseline mean and its observed baseline maximum",
			Evidence:   evidence,
			Suggestion: "Review repeated statements, batching, query plans, and equivalent input conditions before accepting a new workload baseline",
		}}
	}
	return []check.Diagnostic{{
		Code: codeWorkloadUnavailable, Severity: check.SeverityError,
		Title:      "Workload baseline comparison is unavailable",
		Message:    comparison.Reason,
		Evidence:   evidence,
		Suggestion: "Use the same explicit --workload name and input conditions, one completed operation per scope, and at least five fully measured scopes without statement or collection errors; keep setup and diagnostic SQL outside captured scopes",
	}}
}

// FormatWorkloadComparison renders limits, statuses, and both declared names.
// Missing or mismatched workload identity is never inferred from SQL shapes.
func FormatWorkloadComparison(comparison WorkloadComparison) string {
	baselineName, currentName := "unavailable", "unavailable"
	var baselineRU, currentRU, baselineStatements, currentStatements float64
	if comparison.Baseline != nil {
		baselineName = comparison.Baseline.Name
		baselineRU = comparison.Baseline.ServerRU.Mean
		baselineStatements = comparison.Baseline.StatementCount.Mean
	}
	if comparison.Current != nil {
		currentName = comparison.Current.Name
		currentRU = comparison.Current.ServerRU.Mean
		currentStatements = comparison.Current.StatementCount.Mean
	}
	return fmt.Sprintf("server_ru_workload_comparison: status=%s baseline_name=%s current_name=%s baseline_scope_mean=%s current_scope_mean=%s server_ru_limit=%s baseline_statement_mean=%s current_statement_mean=%s statement_count_limit=%s server_ru_regressed=%t statements_regressed=%t reason=%q",
		comparison.Status, baselineName, currentName, formatServerRUFloat(baselineRU), formatServerRUFloat(currentRU), formatServerRUFloat(comparison.ServerRULimit), formatServerRUFloat(baselineStatements), formatServerRUFloat(currentStatements), formatServerRUFloat(comparison.StatementCountLimit), comparison.ServerRURegressed, comparison.StatementsRegressed, comparison.Reason)
}
