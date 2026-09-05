package runtimecapture

import (
	"fmt"
	"math"
)

// ScopeMetric summarizes one value per captured scope, not per statement.
// Without complete coverage, ServerRU values contain only measured attempts.
type ScopeMetric struct {
	Total   float64 `json:"total"`
	Mean    float64 `json:"mean"`
	Minimum float64 `json:"min"`
	Maximum float64 `json:"max"`
}

// WorkloadMetrics describes scopes explicitly declared by the caller to be
// repetitions of the same operation and input conditions. Statements counts
// recognized DML, including SELECT. TransactionStatements counts excluded
// BEGIN, COMMIT, and ROLLBACK events, whose RU is not measured.
type WorkloadMetrics struct {
	Name                  string      `json:"name"`
	Scopes                int         `json:"scopes"`
	Statements            int         `json:"statements"`
	TransactionStatements int         `json:"transaction_statements"`
	ServerRU              ScopeMetric `json:"server_ru"`
	StatementCount        ScopeMetric `json:"statement_count"`
}

// WorkloadServerRU adds measurement coverage to per-scope metrics. Complete
// scopes have at least one recognized DML, exact RU coverage, no collection or
// statement errors, and no unsupported statements. Completeness refers only to
// captured records; missing operations or an omitted tail cannot be inferred.
type WorkloadServerRU struct {
	WorkloadMetrics
	CompleteScopes        int  `json:"complete_scopes"`
	Samples               int  `json:"samples"`
	CollectionErrors      int  `json:"collection_errors"`
	StatementErrors       int  `json:"statement_errors"`
	UnsupportedStatements int  `json:"unsupported_statements"`
	Overflow              bool `json:"overflow,omitempty"`
}

// WithWorkload enables per-scope aggregation for an explicitly named, uniform
// workload. It neither filters records nor infers operation identity from SQL.
// Callers must provide completed captures with one operation per scope and
// equivalent input conditions. No runtime instrumentation is added.
func WithWorkload(name string) AnalysisOption {
	return func(configuration *analysisConfiguration) {
		configuration.workloadEnabled = true
		configuration.workloadName = name
	}
}

// ValidateWorkloadName accepts a bounded, printable identifier suitable for
// persisted baselines and text reports. Names must not contain user data.
func ValidateWorkloadName(name string) error {
	if len(name) > 0 && len(name) <= 128 {
		valid := true
		for index := range len(name) {
			value := name[index]
			if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || index > 0 && (value == '-' || value == '_' || value == '.') {
				continue
			}
			valid = false
			break
		}
		if valid {
			return nil
		}
	}
	return fmt.Errorf("workload name must be 1-128 ASCII letters, digits, dots, underscores, or hyphens, starting with a letter or digit")
}

type workloadScope struct {
	statements       int
	samples          int
	collectionErrors int
	statementErrors  int
	unsupported      int
	transactions     int
	ru               float64
	overflow         bool
}

type workloadAccumulator struct {
	indices map[scopeKey]int
	scopes  []workloadScope
}

func (accumulator *workloadAccumulator) add(key scopeKey, record Record) {
	index, exists := accumulator.indices[key]
	if !exists {
		index = len(accumulator.scopes)
		accumulator.indices[key] = index
		accumulator.scopes = append(accumulator.scopes, workloadScope{})
	}
	scope := &accumulator.scopes[index]
	if record.Error != "" {
		scope.statementErrors++
	}
	switch record.Operation {
	case "SELECT", "INSERT", "UPSERT", "UPDATE", "DELETE":
		scope.statements++
		if record.ServerRU != nil {
			if record.ServerRU.Known {
				scope.samples++
				scope.overflow = scope.overflow || scope.ru > math.MaxFloat64-record.ServerRU.Value
				scope.ru = addServerRUSaturated(scope.ru, record.ServerRU.Value)
			}
			if record.ServerRU.Error != "" {
				scope.collectionErrors++
			}
		}
	case "BEGIN", "COMMIT", "ROLLBACK":
		scope.transactions++
	default:
		// Unrecognized raw SQL and diagnostic queries cannot silently disappear
		// from an operation budget. Keep setup and plan probes outside the scope.
		scope.unsupported++
	}
}

func (accumulator *workloadAccumulator) finish(name string) *WorkloadServerRU {
	result := &WorkloadServerRU{WorkloadMetrics: WorkloadMetrics{Name: name, Scopes: len(accumulator.scopes)}}
	// Insertion order makes accumulation deterministic for the same artifact,
	// even when records from concurrent scopes are interleaved.
	for index, scope := range accumulator.scopes {
		result.Statements += scope.statements
		result.Samples += scope.samples
		result.CollectionErrors += scope.collectionErrors
		result.StatementErrors += scope.statementErrors
		result.UnsupportedStatements += scope.unsupported
		result.TransactionStatements += scope.transactions
		if scope.statements > 0 && scope.samples == scope.statements && scope.collectionErrors == 0 && scope.statementErrors == 0 && scope.unsupported == 0 && !scope.overflow {
			result.CompleteScopes++
		}
		result.Overflow = result.Overflow || scope.overflow || result.ServerRU.Total > math.MaxFloat64-scope.ru
		result.ServerRU.add(scope.ru, index+1)
		result.StatementCount.add(float64(scope.statements), index+1)
	}
	return result
}

func (metric *ScopeMetric) add(value float64, count int) {
	metric.Total = addServerRUSaturated(metric.Total, value)
	metric.Mean += (value - metric.Mean) / float64(count)
	if count == 1 || value < metric.Minimum {
		metric.Minimum = value
	}
	if count == 1 || value > metric.Maximum {
		metric.Maximum = value
	}
}

func (workload WorkloadMetrics) validate() error {
	if err := ValidateWorkloadName(workload.Name); err != nil {
		return err
	}
	if workload.Scopes < 0 || workload.Statements < 0 || workload.TransactionStatements < 0 {
		return fmt.Errorf("workload has negative counts")
	}
	if err := workload.ServerRU.validate(workload.Scopes); err != nil {
		return fmt.Errorf("workload server_ru: %w", err)
	}
	if err := workload.StatementCount.validate(workload.Scopes); err != nil {
		return fmt.Errorf("workload statement_count: %w", err)
	}
	if workload.StatementCount.Total != float64(workload.Statements) || math.Trunc(workload.StatementCount.Minimum) != workload.StatementCount.Minimum || math.Trunc(workload.StatementCount.Maximum) != workload.StatementCount.Maximum {
		return fmt.Errorf("workload statement_count must describe integer statement counts")
	}
	return nil
}

func (metric ScopeMetric) validate(scopes int) error {
	for _, value := range [...]float64{metric.Total, metric.Mean, metric.Minimum, metric.Maximum} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid metric value")
		}
	}
	if scopes == 0 {
		if metric != (ScopeMetric{}) {
			return fmt.Errorf("nonzero metrics without scopes")
		}
		return nil
	}
	if metric.Minimum > metric.Mean || metric.Mean > metric.Maximum {
		return fmt.Errorf("inconsistent min, mean, and max")
	}
	if metric.Maximum > metric.Total {
		return fmt.Errorf("maximum exceeds total")
	}
	expected := metric.Total / float64(scopes)
	tolerance := math.Max(1, math.Abs(expected)) * 1e-12
	if metric.Total == math.MaxFloat64 {
		if metric.Mean < expected-tolerance {
			return fmt.Errorf("inconsistent saturated total and mean")
		}
	} else if math.Abs(metric.Mean-expected) > tolerance {
		return fmt.Errorf("inconsistent total, scopes, and mean")
	}
	return nil
}

func (workload WorkloadServerRU) validate() error {
	if err := workload.WorkloadMetrics.validate(); err != nil {
		return err
	}
	if workload.CompleteScopes < 0 || workload.CompleteScopes > workload.Scopes || workload.Samples < 0 || workload.Samples > workload.Statements || workload.CollectionErrors < 0 || workload.CollectionErrors > workload.Statements || workload.StatementErrors < 0 || workload.UnsupportedStatements < 0 {
		return fmt.Errorf("workload has invalid measurement coverage")
	}
	if workload.Samples < workload.CompleteScopes || workload.StatementErrors > workload.Statements+workload.TransactionStatements+workload.UnsupportedStatements {
		return fmt.Errorf("workload coverage exceeds captured statement counts")
	}
	if workload.CompleteScopes == workload.Scopes && workload.Scopes > 0 && (workload.Samples != workload.Statements || workload.StatementCount.Minimum < 1 || workload.CollectionErrors != 0 || workload.StatementErrors != 0 || workload.UnsupportedStatements != 0) {
		return fmt.Errorf("workload complete scopes disagree with measurement coverage")
	}
	if workload.Samples == 0 && workload.ServerRU != (ScopeMetric{}) {
		return fmt.Errorf("workload has ServerRU metrics without samples")
	}
	return nil
}

func workloadCoverageProblem(workload WorkloadServerRU) string {
	switch {
	case workload.Overflow:
		return "ServerRU totals overflowed"
	case workload.CollectionErrors != 0:
		return "ServerRU collection or connection release failed"
	case workload.StatementErrors != 0:
		return "captured statements reported execution or result-processing errors"
	case workload.UnsupportedStatements != 0:
		return "captured statements include unsupported operations other than transaction control"
	case workload.CompleteScopes != workload.Scopes:
		return "every scope requires at least one DML statement and complete ServerRU coverage"
	case workload.Scopes < ServerRUComparisonMinimumSamples:
		return "at least five complete scopes are required"
	default:
		return ""
	}
}

// FormatWorkloadServerRU renders scoped totals with explicit coverage. It
// never treats missing samples as evidence of a free operation.
func FormatWorkloadServerRU(workload WorkloadServerRU) string {
	return fmt.Sprintf("server_ru_workload: name=%s scopes=%d complete_scopes=%d statements=%d samples=%d collection_errors=%d statement_errors=%d unsupported_statements=%d transaction_statements=%d total=%s scope_mean=%s scope_min=%s scope_max=%s statement_mean=%s statement_max=%s overflow=%t",
		workload.Name, workload.Scopes, workload.CompleteScopes, workload.Statements, workload.Samples, workload.CollectionErrors, workload.StatementErrors, workload.UnsupportedStatements, workload.TransactionStatements,
		formatServerRUFloat(workload.ServerRU.Total), formatServerRUFloat(workload.ServerRU.Mean), formatServerRUFloat(workload.ServerRU.Minimum), formatServerRUFloat(workload.ServerRU.Maximum), formatServerRUFloat(workload.StatementCount.Mean), formatServerRUFloat(workload.StatementCount.Maximum), workload.Overflow)
}
