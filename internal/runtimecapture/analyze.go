package runtimecapture

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
)

const (
	codeIncompleteMetadata = "RUN001"
	codePossibleNPlusOne   = "RUN002"
	codeServerRUFailure    = "RUN003"
	codeRelationTopN       = "QRY005"
)

// Statistics summarizes the captured execution set without claiming database
// round trips outside go-tidb.
type Statistics struct {
	Captures            int     `json:"captures"`
	Scopes              int     `json:"scopes"`
	Statements          int     `json:"statements"`
	AuxiliaryStatements int     `json:"auxiliary_statements"`
	Fingerprints        int     `json:"fingerprints"`
	BatchGroups         int     `json:"batch_groups"`
	SplitBatches        int     `json:"split_batches"`
	ServerRUSamples     int     `json:"server_ru_samples"`
	ServerRUErrors      int     `json:"server_ru_errors"`
	ServerRUTotal       float64 `json:"server_ru_total"`
	TargetDuration      int64   `json:"target_duration_ns"`
	DiagnosticDuration  int64   `json:"diagnostic_duration_ns"`
}

// Analysis contains deterministic runtime statistics and diagnostics.
type Analysis struct {
	Statistics  Statistics         `json:"statistics"`
	Diagnostics []check.Diagnostic `json:"diagnostics"`
}

type scopeKey struct {
	capture string
	scope   uint64
}

type repeatedQueryKey struct {
	scope       scopeKey
	operation   string
	fingerprint string
	terminal    string
}

type repeatedQueryGroup struct {
	key      repeatedQueryKey
	model    string
	source   Source
	count    int
	duration int64
}

type batchKey struct {
	capture string
	group   uint64
}

type analyzer struct {
	analysis       Analysis
	captures       map[string]struct{}
	scopes         map[scopeKey]struct{}
	fingerprints   map[string]struct{}
	batches        map[batchKey]int
	repeated       map[repeatedQueryKey]int
	repeatedGroups []repeatedQueryGroup
	metadataErrors map[string]struct{}
	fallbacks      map[string]struct{}
	serverRUErrors map[string]struct{}
}

func newAnalyzer() *analyzer {
	return &analyzer{
		analysis:       Analysis{Diagnostics: make([]check.Diagnostic, 0)},
		captures:       make(map[string]struct{}),
		scopes:         make(map[scopeKey]struct{}),
		fingerprints:   make(map[string]struct{}),
		batches:        make(map[batchKey]int),
		repeated:       make(map[repeatedQueryKey]int),
		repeatedGroups: make([]repeatedQueryGroup, 0),
		metadataErrors: make(map[string]struct{}),
		fallbacks:      make(map[string]struct{}),
	}
}

func (analyzer *analyzer) add(record Record) {
	analyzer.captures[record.CaptureID] = struct{}{}
	scope := scopeKey{capture: record.CaptureID, scope: record.ScopeID}
	analyzer.scopes[scope] = struct{}{}
	analyzer.fingerprints[record.Fingerprint] = struct{}{}
	analyzer.analysis.Statistics.Statements++
	analyzer.analysis.Statistics.TargetDuration = addDurationSaturated(analyzer.analysis.Statistics.TargetDuration, record.DurationNS)
	if record.ServerRU != nil {
		analyzer.analysis.Statistics.AuxiliaryStatements += record.ServerRU.AuxiliaryStatements
		analyzer.analysis.Statistics.DiagnosticDuration = addDurationSaturated(
			analyzer.analysis.Statistics.DiagnosticDuration,
			record.ServerRU.DiagnosticDurationNS,
		)
		if record.ServerRU.Known {
			analyzer.analysis.Statistics.ServerRUSamples++
			analyzer.analysis.Statistics.ServerRUTotal = addServerRUSaturated(
				analyzer.analysis.Statistics.ServerRUTotal,
				record.ServerRU.Value,
			)
		}
		if record.ServerRU.Error != "" {
			analyzer.analysis.Statistics.ServerRUErrors++
			key := record.Fingerprint
			if _, exists := analyzer.serverRUErrors[key]; !exists {
				if analyzer.serverRUErrors == nil {
					analyzer.serverRUErrors = make(map[string]struct{})
				}
				analyzer.serverRUErrors[key] = struct{}{}
				analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, serverRUFailureDiagnostic(record))
			}
		}
	}

	if record.Batch != nil {
		key := batchKey{capture: record.CaptureID, group: record.Batch.Group}
		if previous, exists := analyzer.batches[key]; !exists || record.Batch.Count > previous {
			analyzer.batches[key] = record.Batch.Count
		}
	}
	if record.MetadataError != "" {
		key := record.Fingerprint + "\x00" + record.MetadataError
		if _, exists := analyzer.metadataErrors[key]; !exists {
			analyzer.metadataErrors[key] = struct{}{}
			analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, incompleteMetadataDiagnostic(record))
		}
	}
	if record.Query != nil && record.Query.Compiler.Rewrite == queryshape.CompilerRewriteRelationTopNFallback {
		if _, exists := analyzer.fallbacks[record.Fingerprint]; !exists {
			analyzer.fallbacks[record.Fingerprint] = struct{}{}
			analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, relationTopNFallbackDiagnostic(record))
		}
	}
	if !nPlusOneCandidate(record) {
		return
	}
	key := repeatedQueryKey{
		scope:       scope,
		operation:   record.Operation,
		fingerprint: record.Fingerprint,
		terminal:    record.Terminal,
	}
	groupIndex, exists := analyzer.repeated[key]
	if !exists {
		groupIndex = len(analyzer.repeatedGroups)
		analyzer.repeated[key] = groupIndex
		analyzer.repeatedGroups = append(analyzer.repeatedGroups, repeatedQueryGroup{
			key:    key,
			model:  record.Model,
			source: record.Source,
		})
	}
	analyzer.repeatedGroups[groupIndex].count++
	analyzer.repeatedGroups[groupIndex].duration = addDurationSaturated(analyzer.repeatedGroups[groupIndex].duration, record.DurationNS)
}

func (analyzer *analyzer) finish() Analysis {
	for _, group := range analyzer.repeatedGroups {
		if group.count > 1 {
			analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, possibleNPlusOneDiagnostic(group))
		}
	}
	analyzer.analysis.Statistics.Captures = len(analyzer.captures)
	analyzer.analysis.Statistics.Scopes = len(analyzer.scopes)
	analyzer.analysis.Statistics.Fingerprints = len(analyzer.fingerprints)
	analyzer.analysis.Statistics.BatchGroups = len(analyzer.batches)
	for _, count := range analyzer.batches {
		if count > 1 {
			analyzer.analysis.Statistics.SplitBatches++
		}
	}
	return analyzer.analysis
}

// Analyze produces offline diagnostics from records without database access.
func Analyze(records []Record) Analysis {
	analyzer := newAnalyzer()
	for index := range records {
		analyzer.add(records[index])
	}
	return analyzer.finish()
}

// AnalyzeReader streams a versioned runtime artifact into offline analysis
// without retaining every statement record in memory.
func AnalyzeReader(reader io.Reader) (Analysis, error) {
	analyzer := newAnalyzer()
	if err := decodeEach(reader, func(record Record) {
		analyzer.add(record)
	}); err != nil {
		return Analysis{}, err
	}
	return analyzer.finish(), nil
}

func addDurationSaturated(current, added int64) int64 {
	if added > 0 && current > math.MaxInt64-added {
		return math.MaxInt64
	}
	return current + added
}

func addServerRUSaturated(current, added float64) float64 {
	if current > math.MaxFloat64-added {
		return math.MaxFloat64
	}
	return current + added
}

func nPlusOneCandidate(record Record) bool {
	return record.Operation == "SELECT" &&
		record.Source != SourcePreload
}

func incompleteMetadataDiagnostic(record Record) check.Diagnostic {
	return check.Diagnostic{
		Code:     codeIncompleteMetadata,
		Severity: check.SeverityWarning,
		Title:    "Runtime query metadata is incomplete",
		Message:  "go-tidb executed a statement but could not attach its complete typed query shape",
		Evidence: []check.Evidence{
			{Message: "Query fingerprint: " + record.Fingerprint},
			{Message: record.MetadataError},
		},
		Suggestion:   "Report the metadata error with the model and query shape that produced it",
		Suppressible: false,
	}
}

func serverRUFailureDiagnostic(record Record) check.Diagnostic {
	return check.Diagnostic{
		Code:     codeServerRUFailure,
		Severity: check.SeverityWarning,
		Title:    "Automatic ServerRU collection failed",
		Message:  "go-tidb completed a target statement without a usable same-session ServerRU sample",
		Evidence: []check.Evidence{
			{Message: "Query fingerprint: " + record.Fingerprint},
			{Message: record.ServerRU.Error},
		},
		Suggestion:   "Verify TiDB support, context lifetime, and use of *sql.DB, *sql.Conn, or an active *sql.Tx executor",
		Suppressible: false,
		Reference:    "https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info",
	}
}

func relationTopNFallbackDiagnostic(record Record) check.Diagnostic {
	relation := record.Query.Compiler.Relation
	message := "Captured SELECT for " + record.Query.Model + " used the relation-filter TopN fallback"
	if relation != "" {
		message += " for " + relation
	}
	evidence := []check.Evidence{{Message: "Query fingerprint: " + record.Fingerprint}}
	if reason := record.Query.Compiler.Reason; reason != "" {
		evidence = append(evidence, check.Evidence{Message: "Relation-first TopN was not applied because " + reason})
	}
	return check.Diagnostic{
		Code:         codeRelationTopN,
		Severity:     check.SeverityWarning,
		Title:        "Relation-filter TopN uses the EXISTS fallback",
		Message:      message,
		Evidence:     evidence,
		Suggestion:   "Inspect the captured query with Explain or ExplainAnalyze and verify whether the relation-first rewrite can preserve its semantics",
		Suppressible: true,
		Reference:    "https://docs.pingcap.com/tidb/stable/topn-limit-push-down/",
	}
}

func possibleNPlusOneDiagnostic(group repeatedQueryGroup) check.Diagnostic {
	target := "query"
	if group.model != "" {
		target = group.model
	}
	return check.Diagnostic{
		Code:     codePossibleNPlusOne,
		Severity: check.SeverityWarning,
		Title:    "Repeated SELECT may be an N+1 query",
		Message: "One runtime scope executed the same " + target + " SELECT " +
			strconv.Itoa(group.count) + " times",
		Evidence: []check.Evidence{
			{Message: "Query fingerprint: " + group.key.fingerprint},
			{Message: "Captured target duration: " + time.Duration(group.duration).String()},
			{Message: "Source: " + string(group.source) + ", terminal: " + nonEmptyRuntimeValue(group.key.terminal)},
		},
		Suggestion:   "Review whether the repeated lookup can be preloaded, collected into one IN query, or moved outside a loop",
		Suppressible: true,
	}
}

func nonEmptyRuntimeValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// FormatStatistics renders one stable human-readable runtime summary line.
func FormatStatistics(statistics Statistics) string {
	return fmt.Sprintf(
		"runtime: captures=%d scopes=%d statements=%d fingerprints=%d batch_groups=%d split_batches=%d target_duration=%s auxiliary_statements=%d diagnostic_duration=%s server_ru_samples=%d server_ru_errors=%d server_ru_total=%s",
		statistics.Captures,
		statistics.Scopes,
		statistics.Statements,
		statistics.Fingerprints,
		statistics.BatchGroups,
		statistics.SplitBatches,
		time.Duration(statistics.TargetDuration),
		statistics.AuxiliaryStatements,
		time.Duration(statistics.DiagnosticDuration),
		statistics.ServerRUSamples,
		statistics.ServerRUErrors,
		strconv.FormatFloat(statistics.ServerRUTotal, 'g', -1, 64),
	)
}
