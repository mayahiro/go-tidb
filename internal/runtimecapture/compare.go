package runtimecapture

import (
	"fmt"
	"math"
	"strconv"

	"github.com/mayahiro/go-tidb/check"
)

const (
	codeServerRURegression            = "RU001"
	codeServerRUComparisonUnavailable = "RU002"

	// ServerRUComparisonMinimumSamples is the fixed number of complete
	// measurements required on each side of a comparison.
	ServerRUComparisonMinimumSamples = 5
	// ServerRUComparisonMeanRatio is the fixed allowed ratio between the
	// current and baseline per-statement means.
	ServerRUComparisonMeanRatio = 1.30
)

// ServerRUComparisonStatus describes the result for one bind-free
// fingerprint.
type ServerRUComparisonStatus string

const (
	// ServerRUComparisonPass means the current mean stayed within the fixed
	// comparison limit.
	ServerRUComparisonPass ServerRUComparisonStatus = "pass"
	// ServerRUComparisonRegression means the current mean exceeded the fixed
	// comparison limit.
	ServerRUComparisonRegression ServerRUComparisonStatus = "regression"
	// ServerRUComparisonMissingBaseline means the current capture contains a
	// measured fingerprint absent from the baseline.
	ServerRUComparisonMissingBaseline ServerRUComparisonStatus = "missing_baseline"
	// ServerRUComparisonMissingCurrent means the baseline fingerprint is absent
	// from the current capture.
	ServerRUComparisonMissingCurrent ServerRUComparisonStatus = "missing_current"
	// ServerRUComparisonCollectionError means current ServerRU collection was
	// not clean enough for comparison.
	ServerRUComparisonCollectionError ServerRUComparisonStatus = "collection_error"
	// ServerRUComparisonIncompleteCoverage means at least one captured
	// statement was not measured.
	ServerRUComparisonIncompleteCoverage ServerRUComparisonStatus = "incomplete_coverage"
	// ServerRUComparisonInsufficientSamples means at least one side contains
	// fewer than the required number of measurements.
	ServerRUComparisonInsufficientSamples ServerRUComparisonStatus = "insufficient_samples"
)

// ServerRUComparisonPolicy describes the fixed offline regression policy.
type ServerRUComparisonPolicy struct {
	MinimumSamples   int     `json:"minimum_samples"`
	MaximumMeanRatio float64 `json:"maximum_mean_ratio"`
}

// ServerRUComparisonSummary counts comparison results by category.
type ServerRUComparisonSummary struct {
	Fingerprints int `json:"fingerprints"`
	Passed       int `json:"passed"`
	Regressions  int `json:"regressions"`
	Unavailable  int `json:"unavailable"`
}

// FingerprintServerRUComparison records the measurement coverage, means, and
// effective limit used for one fingerprint.
type FingerprintServerRUComparison struct {
	Fingerprint     string                   `json:"fingerprint"`
	Status          ServerRUComparisonStatus `json:"status"`
	BaselineCount   int                      `json:"baseline_count"`
	BaselineSamples int                      `json:"baseline_samples"`
	BaselineMean    float64                  `json:"baseline_mean"`
	BaselineMaximum float64                  `json:"baseline_max"`
	CurrentCount    int                      `json:"current_count"`
	CurrentSamples  int                      `json:"current_samples"`
	CurrentErrors   int                      `json:"current_errors"`
	CurrentMean     float64                  `json:"current_mean"`
	Limit           float64                  `json:"limit"`
}

// ServerRUComparison contains deterministic fingerprint-sorted results for a
// saved baseline and a current runtime analysis.
type ServerRUComparison struct {
	Policy  ServerRUComparisonPolicy        `json:"policy"`
	Summary ServerRUComparisonSummary       `json:"summary"`
	Entries []FingerprintServerRUComparison `json:"entries"`
}

// CompareServerRU compares current per-statement means with a validated saved
// baseline without database access. A comparable fingerprint requires exact
// measurement coverage and at least five successful samples on each side. A
// regression exceeds both 130 percent of the baseline mean and the maximum
// value observed in the baseline.
func CompareServerRU(analysis Analysis, baseline ServerRUBaseline) (ServerRUComparison, error) {
	if err := baseline.Validate(); err != nil {
		return ServerRUComparison{}, fmt.Errorf("compare ServerRU baseline: %w", err)
	}
	if err := validateCurrentServerRU(analysis.ServerRUByFingerprint); err != nil {
		return ServerRUComparison{}, fmt.Errorf("compare current ServerRU: %w", err)
	}

	baselineEntries := baseline.ServerRUByFingerprint
	currentEntries := analysis.ServerRUByFingerprint
	comparison := ServerRUComparison{
		Policy: ServerRUComparisonPolicy{
			MinimumSamples:   ServerRUComparisonMinimumSamples,
			MaximumMeanRatio: ServerRUComparisonMeanRatio,
		},
		Entries: make([]FingerprintServerRUComparison, 0, max(len(baselineEntries), len(currentEntries))),
	}
	for baselineIndex, currentIndex := 0, 0; baselineIndex < len(baselineEntries) || currentIndex < len(currentEntries); {
		var baselineEntry *FingerprintServerRUBaseline
		var currentEntry *FingerprintServerRU
		switch {
		case baselineIndex == len(baselineEntries):
			currentEntry = &currentEntries[currentIndex]
			currentIndex++
		case currentIndex == len(currentEntries):
			baselineEntry = &baselineEntries[baselineIndex]
			baselineIndex++
		case baselineEntries[baselineIndex].Fingerprint < currentEntries[currentIndex].Fingerprint:
			baselineEntry = &baselineEntries[baselineIndex]
			baselineIndex++
		case currentEntries[currentIndex].Fingerprint < baselineEntries[baselineIndex].Fingerprint:
			currentEntry = &currentEntries[currentIndex]
			currentIndex++
		default:
			baselineEntry = &baselineEntries[baselineIndex]
			currentEntry = &currentEntries[currentIndex]
			baselineIndex++
			currentIndex++
		}
		entry := compareServerRUFingerprint(baselineEntry, currentEntry)
		comparison.Entries = append(comparison.Entries, entry)
		switch entry.Status {
		case ServerRUComparisonPass:
			comparison.Summary.Passed++
		case ServerRUComparisonRegression:
			comparison.Summary.Regressions++
		default:
			comparison.Summary.Unavailable++
		}
	}
	comparison.Summary.Fingerprints = len(comparison.Entries)
	return comparison, nil
}

func compareServerRUFingerprint(
	baseline *FingerprintServerRUBaseline,
	current *FingerprintServerRU,
) FingerprintServerRUComparison {
	entry := FingerprintServerRUComparison{}
	if baseline != nil {
		entry.Fingerprint = baseline.Fingerprint
		entry.BaselineCount = baseline.Count
		entry.BaselineSamples = baseline.Samples
		entry.BaselineMean = baseline.Mean
		entry.BaselineMaximum = baseline.Maximum
		entry.Limit = serverRUComparisonLimit(baseline.Mean, baseline.Maximum)
	}
	if current != nil {
		entry.Fingerprint = current.Fingerprint
		entry.CurrentCount = current.Count
		entry.CurrentSamples = current.Samples
		entry.CurrentErrors = current.Errors
		entry.CurrentMean = current.Mean
	}

	switch {
	case baseline == nil:
		entry.Status = ServerRUComparisonMissingBaseline
	case current == nil:
		entry.Status = ServerRUComparisonMissingCurrent
	case current.Errors != 0:
		entry.Status = ServerRUComparisonCollectionError
	case baseline.Samples != baseline.Count || current.Samples != current.Count:
		entry.Status = ServerRUComparisonIncompleteCoverage
	case baseline.Samples < ServerRUComparisonMinimumSamples || current.Samples < ServerRUComparisonMinimumSamples:
		entry.Status = ServerRUComparisonInsufficientSamples
	case current.Mean > entry.Limit:
		entry.Status = ServerRUComparisonRegression
	default:
		entry.Status = ServerRUComparisonPass
	}
	return entry
}

func serverRUComparisonLimit(mean, maximum float64) float64 {
	ratioLimit := mean * ServerRUComparisonMeanRatio
	if math.IsInf(ratioLimit, 1) {
		ratioLimit = math.MaxFloat64
	}
	return max(ratioLimit, maximum)
}

func validateCurrentServerRU(statistics []FingerprintServerRU) error {
	for index, entry := range statistics {
		if entry.Fingerprint == "" {
			return fmt.Errorf("server_ru_by_fingerprint[%d] requires fingerprint", index)
		}
		if index > 0 && entry.Fingerprint <= statistics[index-1].Fingerprint {
			return fmt.Errorf("server_ru_by_fingerprint[%d] fingerprint must be unique and sorted", index)
		}
		if entry.Count < 1 {
			return fmt.Errorf("server_ru_by_fingerprint[%d] requires a positive count", index)
		}
		if entry.Samples < 0 || entry.Samples > entry.Count {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has invalid sample count", index)
		}
		if entry.Errors < 0 || entry.Errors > entry.Count {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has invalid error count", index)
		}
		values := []struct {
			name  string
			value float64
		}{
			{name: "total", value: entry.Total},
			{name: "mean", value: entry.Mean},
			{name: "min", value: entry.Minimum},
			{name: "max", value: entry.Maximum},
		}
		for _, value := range values {
			if value.value < 0 || math.IsNaN(value.value) || math.IsInf(value.value, 0) {
				return fmt.Errorf("server_ru_by_fingerprint[%d] has invalid %s", index, value.name)
			}
		}
		if entry.Samples == 0 {
			if entry.Total != 0 || entry.Mean != 0 || entry.Minimum != 0 || entry.Maximum != 0 {
				return fmt.Errorf("server_ru_by_fingerprint[%d] has statistics without samples", index)
			}
			continue
		}
		if entry.Minimum > entry.Mean || entry.Mean > entry.Maximum {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent min, mean, and max", index)
		}
		expectedMean := entry.Total / float64(entry.Samples)
		tolerance := math.Max(1, math.Abs(expectedMean)) * 1e-12
		if entry.Total == math.MaxFloat64 && entry.Mean < expectedMean-tolerance {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent total, sample count, and mean", index)
		}
		if entry.Total != math.MaxFloat64 && math.Abs(entry.Mean-expectedMean) > tolerance {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent total, sample count, and mean", index)
		}
	}
	return nil
}

// Diagnostics returns non-suppressible errors for regressions and incomplete
// comparisons. Passing entries do not produce diagnostics.
func (comparison ServerRUComparison) Diagnostics() []check.Diagnostic {
	regressions := make([]check.Evidence, 0, comparison.Summary.Regressions)
	unavailable := make([]check.Evidence, 0, comparison.Summary.Unavailable)
	for _, entry := range comparison.Entries {
		switch entry.Status {
		case ServerRUComparisonRegression:
			regressions = append(regressions, check.Evidence{Message: formatServerRURegressionEvidence(entry)})
		case ServerRUComparisonPass:
		default:
			unavailable = append(unavailable, check.Evidence{Message: formatServerRUUnavailableEvidence(entry)})
		}
	}

	var diagnostics []check.Diagnostic
	if len(regressions) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codeServerRURegression,
			Severity:     check.SeverityError,
			Title:        "ServerRU mean regressed from the baseline",
			Message:      "One or more fingerprints exceeded both the allowed mean ratio and the maximum observed baseline value",
			Evidence:     regressions,
			Suggestion:   "Review the query plan and workload, then replace the baseline only after accepting the measured cost",
			Suppressible: false,
		})
	}
	if len(unavailable) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codeServerRUComparisonUnavailable,
			Severity:     check.SeverityError,
			Title:        "ServerRU baseline comparison is incomplete",
			Message:      "One or more fingerprints could not be compared with complete repeatable measurements",
			Evidence:     unavailable,
			Suggestion:   "Capture the same workload with ServerRU collection enabled for every statement and at least five successful samples per fingerprint",
			Suppressible: false,
		})
	}
	return diagnostics
}

func formatServerRURegressionEvidence(entry FingerprintServerRUComparison) string {
	return fmt.Sprintf(
		"Fingerprint %s: current mean %s RU exceeds limit %s RU (baseline mean %s RU, observed max %s RU)",
		entry.Fingerprint,
		formatServerRUFloat(entry.CurrentMean),
		formatServerRUFloat(entry.Limit),
		formatServerRUFloat(entry.BaselineMean),
		formatServerRUFloat(entry.BaselineMaximum),
	)
}

func formatServerRUUnavailableEvidence(entry FingerprintServerRUComparison) string {
	prefix := "Fingerprint " + entry.Fingerprint + ": "
	switch entry.Status {
	case ServerRUComparisonMissingBaseline:
		return prefix + "current measurement has no baseline entry"
	case ServerRUComparisonMissingCurrent:
		return prefix + "baseline entry has no current measurement"
	case ServerRUComparisonCollectionError:
		return fmt.Sprintf("%scurrent collection reported %d error(s)", prefix, entry.CurrentErrors)
	case ServerRUComparisonIncompleteCoverage:
		return fmt.Sprintf(
			"%sincomplete measurement coverage (baseline %d/%d, current %d/%d)",
			prefix,
			entry.BaselineSamples,
			entry.BaselineCount,
			entry.CurrentSamples,
			entry.CurrentCount,
		)
	case ServerRUComparisonInsufficientSamples:
		return fmt.Sprintf(
			"%sfewer than %d complete samples (baseline %d, current %d)",
			prefix,
			ServerRUComparisonMinimumSamples,
			entry.BaselineSamples,
			entry.CurrentSamples,
		)
	default:
		return prefix + "comparison status is unavailable"
	}
}

// FormatFingerprintServerRUComparison renders one stable human-readable
// comparison line.
func FormatFingerprintServerRUComparison(entry FingerprintServerRUComparison) string {
	return fmt.Sprintf(
		"server_ru_comparison: fingerprint=%s status=%s baseline_count=%d baseline_samples=%d baseline_mean=%s baseline_max=%s current_count=%d current_samples=%d current_errors=%d current_mean=%s limit=%s",
		entry.Fingerprint,
		entry.Status,
		entry.BaselineCount,
		entry.BaselineSamples,
		formatServerRUFloat(entry.BaselineMean),
		formatServerRUFloat(entry.BaselineMaximum),
		entry.CurrentCount,
		entry.CurrentSamples,
		entry.CurrentErrors,
		formatServerRUFloat(entry.CurrentMean),
		formatServerRUFloat(entry.Limit),
	)
}

// FormatServerRUComparisonSummary renders one stable human-readable summary
// line.
func FormatServerRUComparisonSummary(comparison ServerRUComparison) string {
	return fmt.Sprintf(
		"server_ru_comparison_summary: fingerprints=%d passed=%d regressions=%d unavailable=%d minimum_samples=%d maximum_mean_ratio=%s",
		comparison.Summary.Fingerprints,
		comparison.Summary.Passed,
		comparison.Summary.Regressions,
		comparison.Summary.Unavailable,
		comparison.Policy.MinimumSamples,
		formatServerRUFloat(comparison.Policy.MaximumMeanRatio),
	)
}

func formatServerRUFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
