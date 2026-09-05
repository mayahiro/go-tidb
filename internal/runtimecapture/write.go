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
	bulk := "InsertMany"
	if group.key.operation == "UPSERT" {
		bulk = "UpsertMany"
	}
	ruTotal := "unavailable"
	if group.ruSamples != 0 {
		ruTotal = strconv.FormatFloat(group.ruTotal, 'g', -1, 64)
	}
	return check.Diagnostic{
		Code:     codeRepeatedWrite,
		Severity: check.SeverityWarning,
		Title:    "Repeated single-row write may be batchable",
		Message: "One runtime scope attempted the same typed " + group.key.operation +
			" statement " + strconv.Itoa(group.count) + " times",
		Evidence: []check.Evidence{
			{Message: "Query fingerprint: " + group.key.fingerprint},
			{Message: fmt.Sprintf("Capture: %s, scope: %d, terminal: %s", group.key.scope.capture, group.key.scope.scope, group.key.terminal)},
			{Message: fmt.Sprintf("Captured write attempts: %d, reported errors: %d", group.count, group.errors)},
			{Message: "Captured target duration: " + time.Duration(group.duration).String()},
			{Message: fmt.Sprintf("Captured statement ServerRU: total=%s, samples=%d/%d, collection_errors=%d", ruTotal, group.ruSamples, group.count, group.ruErrors)},
			{Message: "ServerRU covers measured attempts only, excludes BEGIN/COMMIT, and is not billed RU; attempts do not prove distinct rows or committed changes"},
		},
		Suggestion:   "Review whether " + bulk + " can replace these calls without changing generated-ID use, execution order, or transaction boundaries; check intentional retries first and measure latency and RU before changing the operation",
		Suppressible: true,
	}
}
