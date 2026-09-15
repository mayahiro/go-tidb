package orm

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

// WriteCapture writes only one variant's measured SELECT samples as existing
// RuntimeCapture JSON Lines. It requires a complete comparison, excludes
// warmups, plans, warnings, bind values, and result values, and performs no I/O
// except writing to the caller-owned writer. Each sample has its own scope.
// variant is auto, tikv, or tiflash_mpp. Use separate files per Case and variant,
// and pass Case to --workload in tidbgo baseline/analyze. This export does not
// establish equal inputs, encode Case into SQL identity, or compare engines
// through the existing per-fingerprint regression policy.
func (r AggregateComparison) WriteCapture(writer io.Writer, variant string) error {
	if nilPredicateArgument(writer) {
		return fmt.Errorf("orm: aggregate comparison capture requires a writer")
	}
	if !r.Complete {
		return fmt.Errorf("orm: aggregate comparison capture requires a complete comparison")
	}
	if err := runtimecapture.ValidateWorkloadName(r.Options.Case); err != nil {
		return fmt.Errorf("orm: aggregate comparison capture Case: %w", err)
	}
	var selected *AggregateVariantComparison
	for i := range r.Variants {
		if r.Variants[i].Name == variant {
			selected = &r.Variants[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("orm: aggregate comparison has no variant %q", variant)
	}
	if selected.Error != nil || selected.Plan.WarningsError != nil || (selected.PlanStatus != "matched" && selected.PlanStatus != "unrequested") || r.Options.Samples < 5 || len(selected.Samples) != r.Options.Samples || selected.Rows < 0 {
		return fmt.Errorf("orm: aggregate comparison capture has incomplete samples or plan coverage")
	}
	if selected.Fingerprint != runtimecapture.StatementFingerprint("SELECT", selected.SQL) {
		return fmt.Errorf("orm: aggregate comparison capture SQL differs from its fingerprint")
	}
	captureID := newRuntimeCaptureID()
	recordAt := func(index int) runtimecapture.Record {
		sample := selected.Samples[index]
		return runtimecapture.Record{
			Version: runtimecapture.Version, CaptureID: captureID, ScopeID: uint64(index + 1), Sequence: 1,
			Operation: "SELECT", Source: runtimecapture.SourceTypedAggregate, Terminal: "aggregate_compare", Model: selected.Model,
			Fingerprint: selected.Fingerprint, SQL: selected.SQL, ArgumentCount: selected.ArgumentCount,
			StartedAt: sample.StartedAt, DurationNS: sample.Duration.Nanoseconds(), RowsReturned: selected.Rows, RowsReturnedKnown: true,
			ServerRU: &runtimecapture.ServerRU{Value: sample.ServerRU, Known: true, DiagnosticDurationNS: sample.DiagnosticDuration.Nanoseconds(), AuxiliaryStatements: 1},
		}
	}
	// Validate all records before writing any bytes. Writer failures may still
	// leave partial output; callers own the file and must discard it on error.
	for i := range selected.Samples {
		if err := recordAt(i).Validate(); err != nil {
			return fmt.Errorf("orm: validate aggregate comparison capture: %w", err)
		}
	}
	for i := range selected.Samples {
		data, err := json.Marshal(recordAt(i))
		if err != nil {
			return fmt.Errorf("orm: encode aggregate comparison capture: %w", err)
		}
		data = append(data, '\n')
		n, err := writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("orm: write aggregate comparison capture: %w", err)
		}
	}
	return nil
}

func summarizeAggregateComparison(variant *AggregateVariantComparison) {
	latency := make([]float64, len(variant.Samples))
	ru := make([]float64, len(variant.Samples))
	for i, sample := range variant.Samples {
		latency[i] = float64(sample.Duration) / 1e6
		ru[i] = sample.ServerRU
	}
	variant.LatencyMS = aggregateComparisonStatistics(latency)
	variant.ServerRU = aggregateComparisonStatistics(ru)
}

func aggregateComparisonStatistics(values []float64) AggregateComparisonStatistics {
	if len(values) == 0 {
		return AggregateComparisonStatistics{}
	}
	slices.Sort(values)
	statistics := AggregateComparisonStatistics{Count: len(values), Minimum: values[0], Maximum: values[len(values)-1], Median: values[len(values)/2]}
	if len(values)%2 == 0 {
		statistics.Median = values[len(values)/2-1]/2 + values[len(values)/2]/2
	}
	for i, value := range values {
		statistics.Mean += (value - statistics.Mean) / float64(i+1)
	}
	return statistics
}
