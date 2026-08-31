package orm

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/mayahiro/go-tidb/check"
)

const (
	codePlanIncompleteStatistics = "PLN001"
	codePlanEstimateDivergence   = "PLN002"
	codePlanLargeTableFullScan   = "PLN003"
	codePlanDiskUsage            = "PLN004"

	planEstimateRatioThreshold = 100
	planEstimateRowsThreshold  = 1_000
	planFullScanRowsThreshold  = 10_000

	planStatisticsReference = "https://docs.pingcap.com/tidb/stable/statistics/"
	planEstimateReference   = "https://docs.pingcap.com/tidb/stable/explain-walkthrough/"
	planFullScanReference   = "https://docs.pingcap.com/tidb/stable/explain-walkthrough/"
	planDiskReference       = "https://docs.pingcap.com/tidb/stable/configure-memory-usage/#disk-spill"
)

// Diagnostics checks this already executed TiDB runtime plan without database
// I/O or another EXPLAIN statement.
//
// It reports pseudo or partial statistics, row estimates that differ by at
// least 100 times when either side has at least 1,000 rows, TableFullScan
// operators that output at least 10,000 rows, and positive recognized disk
// usage. Timing, RU, and free-form execution details are not parsed. The result
// is deterministic and is a non-nil empty slice when no issue is found.
func (plan ExplainAnalyzePlan) Diagnostics() []check.Diagnostic {
	var incompleteStatistics []check.Evidence
	var estimateDivergence []check.Evidence
	var largeTableFullScan []check.Evidence
	var diskUsage []check.Evidence

	for index := range plan {
		row := plan[index]
		operator := planOperatorIdentifier(row.ID)
		statisticsStatus := planStatisticsStatus(row.OperatorInfo)
		if statisticsStatus != "" {
			incompleteStatistics = append(incompleteStatistics, check.Evidence{
				Message: planOperatorAccess(operator, row.AccessObject) + " reported " + statisticsStatus,
			})
		} else if evidence, ok := planEstimateDivergenceEvidence(row, operator); ok {
			estimateDivergence = append(estimateDivergence, check.Evidence{Message: evidence})
		}
		if planOperatorName(operator) == "TableFullScan" && row.ActRows >= planFullScanRowsThreshold {
			largeTableFullScan = append(largeTableFullScan, check.Evidence{
				Message: planOperatorAccess(operator, row.AccessObject) + " produced " + strconv.FormatInt(row.ActRows, 10) + " rows",
			})
		}
		if positivePlanUsage(row.Disk) {
			diskUsage = append(diskUsage, check.Evidence{
				Message: operator + " reported " + strings.TrimSpace(row.Disk) + " of disk usage",
			})
		}
	}

	diagnosticCount := 0
	if len(incompleteStatistics) != 0 {
		diagnosticCount++
	}
	if len(estimateDivergence) != 0 {
		diagnosticCount++
	}
	if len(largeTableFullScan) != 0 {
		diagnosticCount++
	}
	if len(diskUsage) != 0 {
		diagnosticCount++
	}
	diagnostics := make([]check.Diagnostic, 0, diagnosticCount)
	if len(incompleteStatistics) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codePlanIncompleteStatistics,
			Severity:     check.SeverityWarning,
			Title:        "Plan uses incomplete statistics",
			Message:      planOperatorCount(len(incompleteStatistics)) + " used pseudo or partial statistics",
			Evidence:     incompleteStatistics,
			Suggestion:   "Inspect statistics health and refresh table or predicate-column statistics when they are stale or missing, then compare the plan again",
			Suppressible: true,
			Reference:    planStatisticsReference,
		})
	}
	if len(estimateDivergence) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codePlanEstimateDivergence,
			Severity:     check.SeverityWarning,
			Title:        "Estimated and actual rows diverge",
			Message:      planOperatorCount(len(estimateDivergence)) + " differed by at least 100 times with at least 1,000 estimated or actual rows",
			Evidence:     estimateDivergence,
			Suggestion:   "Refresh relevant statistics and inspect data distribution, predicates, and join order before comparing the plan again",
			Suppressible: true,
			Reference:    planEstimateReference,
		})
	}
	if len(largeTableFullScan) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codePlanLargeTableFullScan,
			Severity:     check.SeverityWarning,
			Title:        "Plan performs a large table full scan",
			Message:      planOperatorCount(len(largeTableFullScan)) + " output at least 10,000 rows through TableFullScan",
			Evidence:     largeTableFullScan,
			Suggestion:   "Verify that reading the complete table is intentional, otherwise review predicates, indexes, and the query driving table",
			Suppressible: true,
			Reference:    planFullScanReference,
		})
	}
	if len(diskUsage) != 0 {
		diagnostics = append(diagnostics, check.Diagnostic{
			Code:         codePlanDiskUsage,
			Severity:     check.SeverityWarning,
			Title:        "Plan used disk for intermediate data",
			Message:      planOperatorCount(len(diskUsage)) + " reported positive disk usage",
			Evidence:     diskUsage,
			Suggestion:   "Reduce rows before memory-intensive operators or review indexes, join shape, grouping, ordering, and the applicable memory quota",
			Suppressible: true,
			Reference:    planDiskReference,
		})
	}
	return diagnostics
}

func planStatisticsStatus(operatorInfo string) string {
	if strings.Contains(operatorInfo, "stats:pseudo") {
		return "stats:pseudo"
	}
	if strings.Contains(operatorInfo, "stats:partial") {
		return "stats:partial"
	}
	return ""
}

func planEstimateDivergenceEvidence(row ExplainAnalyzeRow, operator string) (string, bool) {
	actual := float64(row.ActRows)
	if row.ActRows < 0 || row.EstRows < 0 || math.IsNaN(row.EstRows) || math.IsInf(row.EstRows, 0) {
		return "", false
	}
	larger := math.Max(row.EstRows, actual)
	if larger < planEstimateRowsThreshold {
		return "", false
	}
	smaller := math.Min(row.EstRows, actual)
	if smaller != 0 && larger/smaller < planEstimateRatioThreshold {
		return "", false
	}
	message := operator + " estimated " + strconv.FormatFloat(row.EstRows, 'f', -1, 64) +
		" rows and produced " + strconv.FormatInt(row.ActRows, 10) + " rows"
	if smaller != 0 {
		message += " (" + strconv.FormatFloat(larger/smaller, 'f', 2, 64) + "x difference)"
	}
	return message, true
}

func planOperatorIdentifier(identifier string) string {
	trimmed := strings.TrimSpace(identifier)
	trimmed = strings.TrimLeftFunc(trimmed, func(value rune) bool {
		return !unicode.IsLetter(value)
	})
	if trimmed == "" {
		return "unknown operator"
	}
	return trimmed
}

func planOperatorName(identifier string) string {
	if end := strings.IndexAny(identifier, "_( "); end >= 0 {
		return identifier[:end]
	}
	return identifier
}

func planOperatorAccess(operator, accessObject string) string {
	accessObject = strings.TrimSpace(accessObject)
	if accessObject == "" {
		return operator
	}
	return operator + " on " + accessObject
}

func planOperatorCount(count int) string {
	if count == 1 {
		return "1 operator"
	}
	return strconv.Itoa(count) + " operators"
}

func positivePlanUsage(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, "N/A") {
		return false
	}
	numberEnd := 0
	for numberEnd < len(trimmed) {
		current := trimmed[numberEnd]
		if (current < '0' || current > '9') && current != '.' {
			break
		}
		numberEnd++
	}
	if numberEnd == 0 {
		return false
	}
	amount, err := strconv.ParseFloat(trimmed[:numberEnd], 64)
	if err != nil || amount <= 0 || math.IsInf(amount, 0) || math.IsNaN(amount) {
		return false
	}
	unit := strings.ToUpper(strings.TrimSpace(trimmed[numberEnd:]))
	switch unit {
	case "B", "BYTE", "BYTES", "KB", "KIB", "MB", "MIB", "GB", "GIB", "TB", "TIB", "PB", "PIB":
		return true
	default:
		return false
	}
}
