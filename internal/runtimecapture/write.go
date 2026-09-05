package runtimecapture

import (
	"fmt"
	"strconv"
	"time"

	"github.com/mayahiro/go-tidb/check"
)

// Only typed single-row terminals prove the input shape. In particular,
// neither SQL text nor RowsAffected identifies the input row count of an
// upsert, and even an unsplit or one-row Many operation is excluded.
func repeatedWriteCandidate(record Record) bool {
	if record.Source != SourceTypedMutation || record.Batch != nil {
		return false
	}
	return (record.Operation == "INSERT" && record.Terminal == "insert") ||
		(record.Operation == "UPSERT" && record.Terminal == "upsert")
}

// The terminal identifies an explicit update, not its affected row count or
// whether calls can be combined. Soft deletes also execute UPDATE but keep
// their delete terminals and are deliberately outside this diagnostic.
func repeatedUpdateCandidate(record Record) bool {
	return record.Source == SourceTypedMutation && record.Batch == nil &&
		record.Operation == "UPDATE" && (record.Terminal == "update" || record.Terminal == "update_where")
}

type repeatedWriteGroup struct {
	key       repeatedQueryKey
	count     int
	errors    int
	duration  int64
	ruSamples int
	ruErrors  int
	ruTotal   float64
}

func (analyzer *analyzer) addRepeatedWrite(record Record, scope scopeKey) {
	key := repeatedQueryKey{
		scope:       scope,
		operation:   record.Operation,
		fingerprint: record.Fingerprint,
		terminal:    record.Terminal,
	}
	if analyzer.repeatedWrites == nil {
		analyzer.repeatedWrites = make(map[repeatedQueryKey]int)
	}
	index, exists := analyzer.repeatedWrites[key]
	if !exists {
		index = len(analyzer.writeGroups)
		analyzer.repeatedWrites[key] = index
		analyzer.writeGroups = append(analyzer.writeGroups, repeatedWriteGroup{key: key})
	}
	group := &analyzer.writeGroups[index]
	group.count++
	group.duration = addDurationSaturated(group.duration, record.DurationNS)
	if record.Error != "" {
		group.errors++
	}
	if record.ServerRU != nil {
		if record.ServerRU.Known {
			group.ruSamples++
			group.ruTotal = addServerRUSaturated(group.ruTotal, record.ServerRU.Value)
		}
		if record.ServerRU.Error != "" {
			group.ruErrors++
		}
	}
}

func repeatedWriteDiagnostic(group repeatedWriteGroup) check.Diagnostic {
	if group.key.operation == "UPDATE" {
		return repeatedUpdateDiagnostic(group)
	}
	bulk := "InsertMany"
	if group.key.operation == "UPSERT" {
		bulk = "UpsertMany"
	}
	return check.Diagnostic{
		Code:     codeRepeatedWrite,
		Severity: check.SeverityWarning,
		Title:    "Repeated single-row write may be batchable",
		Message: "One runtime scope attempted the same typed " + group.key.operation +
			" statement " + strconv.Itoa(group.count) + " times",
		Evidence:     repeatedWriteEvidence(group),
		Suggestion:   "Review whether " + bulk + " can replace these calls without changing generated-ID use, execution order, or transaction boundaries; check intentional retries first and measure latency and RU before changing the operation",
		Suppressible: true,
	}
}

func repeatedUpdateDiagnostic(group repeatedWriteGroup) check.Diagnostic {
	return check.Diagnostic{
		Code:         codeRepeatedUpdate,
		Severity:     check.SeverityWarning,
		Title:        "Repeated UPDATE warrants application review",
		Message:      "One runtime scope attempted the same typed UPDATE statement " + strconv.Itoa(group.count) + " times; repetition does not prove that the calls can be combined",
		Evidence:     repeatedWriteEvidence(group),
		Suggestion:   "Review whether assignments and predicates allow fewer statements; preserve row-specific values, lease conditions, atomic increments, execution order, and transaction boundaries, check intentional retries, and measure latency and RU before changing the operation",
		Suppressible: true,
	}
}

func repeatedWriteEvidence(group repeatedWriteGroup) []check.Evidence {
	ruTotal := "unavailable"
	if group.ruSamples != 0 {
		ruTotal = strconv.FormatFloat(group.ruTotal, 'g', -1, 64)
	}
	return []check.Evidence{
		{Message: "Query fingerprint: " + group.key.fingerprint},
		{Message: fmt.Sprintf("Capture: %s, scope: %d, terminal: %s", group.key.scope.capture, group.key.scope.scope, group.key.terminal)},
		{Message: fmt.Sprintf("Captured write attempts: %d, reported errors: %d", group.count, group.errors)},
		{Message: "Captured target duration: " + time.Duration(group.duration).String()},
		{Message: fmt.Sprintf("Captured statement ServerRU: total=%s, samples=%d/%d, collection_errors=%d", ruTotal, group.ruSamples, group.count, group.ruErrors)},
		{Message: "ServerRU covers measured attempts only, excludes BEGIN/COMMIT, and is not billed RU; attempts do not prove distinct rows or committed changes"},
	}
}
