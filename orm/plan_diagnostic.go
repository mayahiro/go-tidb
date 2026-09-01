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
		row := &plan[index]
		operator := planOperatorIdentifier(row.ID)
		statisticsStatus := planStatisticsStatus(row.OperatorInfo)
		if statisticsStatus != "" {
			incompleteStatistics = append(incompleteStatistics, check.Evidence{
				Message: planOperatorEvidence(operator, row, " reported ", statisticsStatus, ""),
			})
		} else if evidence, ok := planEstimateDivergenceEvidence(row, operator); ok {
			estimateDivergence = append(estimateDivergence, check.Evidence{Message: evidence})
		}
		if planOperatorName(operator) == "TableFullScan" && row.ActRows >= planFullScanRowsThreshold {
			largeTableFullScan = append(largeTableFullScan, check.Evidence{
				Message: planOperatorEvidence(operator, row, " produced ", strconv.FormatInt(row.ActRows, 10), " rows"),
			})
		}
		if positivePlanUsage(row.Disk) {
			diskUsage = append(diskUsage, check.Evidence{
				Message: planOperatorEvidence(operator, row, " reported ", strings.TrimSpace(row.Disk), " of disk usage"),
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

func planEstimateDivergenceEvidence(row *ExplainAnalyzeRow, operator string) (string, bool) {
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
	estimated := strconv.FormatFloat(row.EstRows, 'f', -1, 64)
	actualRows := strconv.FormatInt(row.ActRows, 10)
	ratio := ""
	if smaller != 0 {
		ratio = strconv.FormatFloat(larger/smaller, 'f', 2, 64)
	}
	accessObject := strings.TrimSpace(row.AccessObject)
	capacity := planOperatorAccessCapacity(operator, accessObject, row) +
		len(" estimated  rows and produced  rows") + len(estimated) + len(actualRows)
	if ratio != "" {
		capacity += len(" (x difference)") + len(ratio)
	}
	var message strings.Builder
	message.Grow(capacity)
	writePlanOperatorAccess(&message, operator, accessObject, row)
	message.WriteString(" estimated ")
	message.WriteString(estimated)
	message.WriteString(" rows and produced ")
	message.WriteString(actualRows)
	message.WriteString(" rows")
	if ratio != "" {
		message.WriteString(" (")
		message.WriteString(ratio)
		message.WriteString("x difference)")
	}
	return message.String(), true
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

func planOperatorEvidence(operator string, row *ExplainAnalyzeRow, action, value, unit string) string {
	accessObject := strings.TrimSpace(row.AccessObject)
	capacity := planOperatorAccessCapacity(operator, accessObject, row) + len(action) + len(value) + len(unit)
	var result strings.Builder
	result.Grow(capacity)
	writePlanOperatorAccess(&result, operator, accessObject, row)
	result.WriteString(action)
	result.WriteString(value)
	result.WriteString(unit)
	return result.String()
}

func planOperatorAccessCapacity(operator, accessObject string, row *ExplainAnalyzeRow) int {
	capacity := len(operator)
	if accessObject != "" {
		capacity += len(" on ") + len(accessObject)
	}
	if row.PhysicalTable == "" && row.Model == "" && row.RelationPath == "" {
		return capacity
	}
	capacity += len(" []")
	fieldCount := 0
	if row.PhysicalTable != "" {
		capacity += len("physical table=") + len(row.PhysicalTable)
		fieldCount++
	}
	if row.Model != "" {
		capacity += len("model=") + len(row.Model)
		fieldCount++
	}
	if row.RelationPath != "" {
		capacity += len("relation=") + len(row.RelationPath)
		fieldCount++
	}
	return capacity + max(0, fieldCount-1)*len(", ")
}

func writePlanOperatorAccess(result *strings.Builder, operator, accessObject string, row *ExplainAnalyzeRow) {
	result.WriteString(operator)
	if accessObject != "" {
		result.WriteString(" on ")
		result.WriteString(accessObject)
	}
	if row.PhysicalTable == "" && row.Model == "" && row.RelationPath == "" {
		return
	}
	result.WriteString(" [")
	wrote := false
	if row.PhysicalTable != "" {
		result.WriteString("physical table=")
		result.WriteString(row.PhysicalTable)
		wrote = true
	}
	if row.Model != "" {
		if wrote {
			result.WriteString(", ")
		}
		result.WriteString("model=")
		result.WriteString(row.Model)
		wrote = true
	}
	if row.RelationPath != "" {
		if wrote {
			result.WriteString(", ")
		}
		result.WriteString("relation=")
		result.WriteString(row.RelationPath)
	}
	result.WriteByte(']')
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
