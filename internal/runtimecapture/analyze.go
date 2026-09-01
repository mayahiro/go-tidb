package runtimecapture

import (
	"cmp"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/querycheck"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const (
	codeIncompleteMetadata = "RUN001"
	codePossibleNPlusOne   = "RUN002"
	codeServerRUFailure    = "RUN003"
	codeRelationTopN       = querycheck.CodeRelationTopNFallback
)

// Statistics summarizes the captured execution set without claiming database
// round trips outside go-tidb.
type Statistics struct {
	Captures                int     `json:"captures"`
	Scopes                  int     `json:"scopes"`
	Statements              int     `json:"statements"`
	AuxiliaryStatements     int     `json:"auxiliary_statements"`
	Fingerprints            int     `json:"fingerprints"`
	BatchGroups             int     `json:"batch_groups"`
	SplitBatches            int     `json:"split_batches"`
	QueryShapeStatements    int     `json:"query_shape_statements"`
	SchemaCheckedStatements int     `json:"schema_checked_statements"`
	ServerRUSamples         int     `json:"server_ru_samples"`
	ServerRUErrors          int     `json:"server_ru_errors"`
	ServerRUTotal           float64 `json:"server_ru_total"`
	TargetDuration          int64   `json:"target_duration_ns"`
	DiagnosticDuration      int64   `json:"diagnostic_duration_ns"`
}

// FingerprintServerRU contains constant-space ServerRU statistics for one
// bind-free statement or query fingerprint. Count includes unsampled captured
// statements. Samples and Errors can overlap when a usable value precedes a
// connection-release error. Total, Mean, Minimum, and Maximum use successful
// samples only and remain zero when no sample succeeded.
type FingerprintServerRU struct {
	Fingerprint string  `json:"fingerprint"`
	Count       int     `json:"count"`
	Samples     int     `json:"samples"`
	Errors      int     `json:"errors"`
	Total       float64 `json:"total"`
	Mean        float64 `json:"mean"`
	Minimum     float64 `json:"min"`
	Maximum     float64 `json:"max"`
}

// Analysis contains deterministic runtime statistics, fingerprint-sorted
// ServerRU aggregates, and diagnostics.
type Analysis struct {
	Statistics            Statistics            `json:"statistics"`
	ServerRUByFingerprint []FingerprintServerRU `json:"server_ru_by_fingerprint"`
	Diagnostics           []check.Diagnostic    `json:"diagnostics"`
}

// AnalysisOption configures offline runtime analysis.
type AnalysisOption func(*analysisConfiguration)

type analysisConfiguration struct {
	catalog       *physicalschema.Catalog
	schemaEnabled bool
}

// WithSchema enables physical index-prefix diagnostics using a catalog parsed
// from an offline SQL schema snapshot.
func WithSchema(catalog *physicalschema.Catalog) AnalysisOption {
	return func(configuration *analysisConfiguration) {
		configuration.catalog = catalog
		configuration.schemaEnabled = true
	}
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

type queryPatternKey struct {
	fingerprint    string
	limitPositive  bool
	offsetPositive bool
	offsetValue    int64
	compilerReason string
}

type fingerprintServerRUAccumulator struct {
	samples int
	errors  int
	total   float64
	mean    float64
	minimum float64
	maximum float64
}

func (accumulator *fingerprintServerRUAccumulator) add(serverRU ServerRU) {
	if serverRU.Known {
		accumulator.samples++
		accumulator.total = addServerRUSaturated(accumulator.total, serverRU.Value)
		accumulator.mean += (serverRU.Value - accumulator.mean) / float64(accumulator.samples)
		if accumulator.samples == 1 || serverRU.Value < accumulator.minimum {
			accumulator.minimum = serverRU.Value
		}
		if accumulator.samples == 1 || serverRU.Value > accumulator.maximum {
			accumulator.maximum = serverRU.Value
		}
	}
	if serverRU.Error != "" {
		accumulator.errors++
	}
}

func (accumulator fingerprintServerRUAccumulator) statistics(fingerprint string, count int) FingerprintServerRU {
	return FingerprintServerRU{
		Fingerprint: fingerprint,
		Count:       count,
		Samples:     accumulator.samples,
		Errors:      accumulator.errors,
		Total:       accumulator.total,
		Mean:        accumulator.mean,
		Minimum:     accumulator.minimum,
		Maximum:     accumulator.maximum,
	}
}

type analyzer struct {
	configuration    analysisConfiguration
	analysis         Analysis
	captures         map[string]struct{}
	scopes           map[scopeKey]struct{}
	fingerprints     map[string]int
	serverRU         map[string]fingerprintServerRUAccumulator
	batches          map[batchKey]int
	repeated         map[repeatedQueryKey]int
	repeatedGroups   []repeatedQueryGroup
	metadataErrors   map[string]struct{}
	queryDiagnostics map[string]struct{}
	queryPatterns    map[queryPatternKey]struct{}
	schemaPatterns   map[queryPatternKey]struct{}
}

func newAnalyzer(options ...AnalysisOption) *analyzer {
	result := &analyzer{
		analysis: Analysis{
			ServerRUByFingerprint: make([]FingerprintServerRU, 0),
			Diagnostics:           make([]check.Diagnostic, 0),
		},
		captures:         make(map[string]struct{}),
		scopes:           make(map[scopeKey]struct{}),
		fingerprints:     make(map[string]int),
		batches:          make(map[batchKey]int),
		repeated:         make(map[repeatedQueryKey]int),
		repeatedGroups:   make([]repeatedQueryGroup, 0),
		metadataErrors:   make(map[string]struct{}),
		queryDiagnostics: make(map[string]struct{}),
		queryPatterns:    make(map[queryPatternKey]struct{}),
		schemaPatterns:   make(map[queryPatternKey]struct{}),
	}
	for _, option := range options {
		if option != nil {
			option(&result.configuration)
		}
	}
	return result
}

func (analyzer *analyzer) add(record Record) {
	analyzer.captures[record.CaptureID] = struct{}{}
	scope := scopeKey{capture: record.CaptureID, scope: record.ScopeID}
	analyzer.scopes[scope] = struct{}{}
	analyzer.fingerprints[record.Fingerprint]++
	analyzer.analysis.Statistics.Statements++
	analyzer.analysis.Statistics.TargetDuration = addDurationSaturated(analyzer.analysis.Statistics.TargetDuration, record.DurationNS)
	if record.ServerRU != nil {
		if analyzer.serverRU == nil {
			analyzer.serverRU = make(map[string]fingerprintServerRUAccumulator)
		}
		fingerprintStatistics := analyzer.serverRU[record.Fingerprint]
		firstFailure := record.ServerRU.Error != "" && fingerprintStatistics.errors == 0
		fingerprintStatistics.add(*record.ServerRU)
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
			if firstFailure {
				analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, serverRUFailureDiagnostic(record))
			}
		}
		analyzer.serverRU[record.Fingerprint] = fingerprintStatistics
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
	if record.Query != nil {
		analyzer.analysis.Statistics.QueryShapeStatements++
		patternKey := runtimeQueryPatternKey(record)
		if _, exists := analyzer.queryPatterns[patternKey]; !exists {
			analyzer.queryPatterns[patternKey] = struct{}{}
			analyzer.appendQueryDiagnostics(record, querycheck.Diagnostics(*record.Query), true)
		}
		if analyzer.configuration.schemaEnabled {
			analyzer.analysis.Statistics.SchemaCheckedStatements++
			if _, exists := analyzer.schemaPatterns[patternKey]; !exists {
				analyzer.schemaPatterns[patternKey] = struct{}{}
				analyzer.appendQueryDiagnostics(
					record,
					querycheck.IndexDiagnostics(*record.Query, analyzer.configuration.catalog),
					false,
				)
			}
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

func runtimeQueryPatternKey(record Record) queryPatternKey {
	return queryPatternKey{
		fingerprint:    record.Fingerprint,
		limitPositive:  record.Query.Limit.Positive,
		offsetPositive: record.Query.Offset.Positive,
		offsetValue:    record.Query.Offset.Value,
		compilerReason: record.Query.Compiler.Reason,
	}
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
	for fingerprint, accumulator := range analyzer.serverRU {
		analyzer.analysis.ServerRUByFingerprint = append(
			analyzer.analysis.ServerRUByFingerprint,
			accumulator.statistics(fingerprint, analyzer.fingerprints[fingerprint]),
		)
	}
	slices.SortFunc(analyzer.analysis.ServerRUByFingerprint, func(first, second FingerprintServerRU) int {
		return cmp.Compare(first.Fingerprint, second.Fingerprint)
	})
	analyzer.analysis.Statistics.BatchGroups = len(analyzer.batches)
	for _, count := range analyzer.batches {
		if count > 1 {
			analyzer.analysis.Statistics.SplitBatches++
		}
	}
	return analyzer.analysis
}

// Analyze produces offline diagnostics from records without database access.
func Analyze(records []Record, options ...AnalysisOption) Analysis {
	analyzer := newAnalyzer(options...)
	for index := range records {
		analyzer.add(records[index])
	}
	return analyzer.finish()
}

// AnalyzeReader streams a versioned runtime artifact into offline analysis
// without retaining every statement record in memory.
func AnalyzeReader(reader io.Reader, options ...AnalysisOption) (Analysis, error) {
	analyzer := newAnalyzer(options...)
	if err := decodeEach(reader, func(record Record) {
		analyzer.add(record)
	}); err != nil {
		return Analysis{}, err
	}
	return analyzer.finish(), nil
}

func (analyzer *analyzer) appendQueryDiagnostics(record Record, diagnostics []check.Diagnostic, includeFingerprint bool) {
	for index := range diagnostics {
		diagnostic := diagnostics[index]
		key := runtimeQueryDiagnosticKey(record.Fingerprint, diagnostic)
		if _, exists := analyzer.queryDiagnostics[key]; exists {
			continue
		}
		analyzer.queryDiagnostics[key] = struct{}{}
		if includeFingerprint {
			evidence := make([]check.Evidence, 0, len(diagnostic.Evidence)+1)
			evidence = append(evidence, check.Evidence{Message: "Query fingerprint: " + record.Fingerprint})
			diagnostic.Evidence = append(evidence, diagnostic.Evidence...)
		}
		analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, diagnostic)
	}
}

func runtimeQueryDiagnosticKey(fingerprint string, diagnostic check.Diagnostic) string {
	key := fingerprint + "\x00" + diagnostic.Code + "\x00" + diagnostic.Message
	for _, evidence := range diagnostic.Evidence {
		key += "\x00" + evidence.Message
	}
	return key
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
		Message:  "go-tidb encountered an error while collecting ServerRU or releasing its same-session connection",
		Evidence: []check.Evidence{
			{Message: "Query fingerprint: " + record.Fingerprint},
			{Message: record.ServerRU.Error},
		},
		Suggestion:   "Verify TiDB support, context lifetime, and use of *sql.DB, *sql.Conn, or an active *sql.Tx executor",
		Suppressible: false,
		Reference:    "https://docs.pingcap.com/tidb/stable/system-variables/#tidb_last_query_info",
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
		"runtime: captures=%d scopes=%d statements=%d fingerprints=%d batch_groups=%d split_batches=%d query_shape_statements=%d schema_checked_statements=%d target_duration=%s auxiliary_statements=%d diagnostic_duration=%s server_ru_samples=%d server_ru_errors=%d server_ru_total=%s",
		statistics.Captures,
		statistics.Scopes,
		statistics.Statements,
		statistics.Fingerprints,
		statistics.BatchGroups,
		statistics.SplitBatches,
		statistics.QueryShapeStatements,
		statistics.SchemaCheckedStatements,
		time.Duration(statistics.TargetDuration),
		statistics.AuxiliaryStatements,
		time.Duration(statistics.DiagnosticDuration),
		statistics.ServerRUSamples,
		statistics.ServerRUErrors,
		strconv.FormatFloat(statistics.ServerRUTotal, 'g', -1, 64),
	)
}

// FormatFingerprintServerRU renders one stable human-readable ServerRU
// aggregate line.
func FormatFingerprintServerRU(statistics FingerprintServerRU) string {
	return fmt.Sprintf(
		"server_ru_fingerprint: fingerprint=%s count=%d samples=%d errors=%d total=%s mean=%s min=%s max=%s",
		statistics.Fingerprint,
		statistics.Count,
		statistics.Samples,
		statistics.Errors,
		strconv.FormatFloat(statistics.Total, 'g', -1, 64),
		strconv.FormatFloat(statistics.Mean, 'g', -1, 64),
		strconv.FormatFloat(statistics.Minimum, 'g', -1, 64),
		strconv.FormatFloat(statistics.Maximum, 'g', -1, 64),
	)
}
