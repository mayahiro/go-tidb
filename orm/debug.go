package orm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DebugReport groups completed ORM statements captured during one Debug
// callback.
type DebugReport struct {
	// StartedAt is the local time immediately before the callback starts.
	StartedAt time.Time
	// Duration is the callback's wall-clock duration, including observer work.
	Duration time.Duration
	// StatementDuration is the sum of the captured StatementEvent durations.
	// It can exceed Duration when statements execute concurrently.
	StatementDuration time.Duration
	// ServerRU summarizes opt-in automatic ServerRU collection. It is nil when
	// no captured statement requested the diagnostic.
	ServerRU *ServerRUSummary
	// Statements contains completed events in observer delivery order. It is
	// non-nil even when the callback executes no statements.
	Statements []StatementEvent
}

// ServerRUSummary aggregates high-cost ServerRU diagnostics independently of
// target statement counts and durations.
type ServerRUSummary struct {
	// DiagnosticDuration is the summed time added by ServerRU collection. It can
	// exceed a containing operation duration when statements run concurrently.
	DiagnosticDuration time.Duration
	// AuxiliaryStatements is the number of diagnostic SQL statements attempted.
	AuxiliaryStatements int
	// Samples is the number of successfully decoded ServerRU values.
	Samples int
	// Errors is the number of statements with a ServerRU diagnostic error.
	Errors int
	// Total is the sum of successfully decoded values and is not billed RU.
	Total float64
}

// Debug runs callback with a derived context and returns one report containing
// every ORM statement completed through that context before callback returns.
//
// Callback must use the supplied context for statements to be captured. Debug
// performs no database I/O unless CollectServerRU is passed or inherited from
// the observation context. It never runs EXPLAIN. An inherited
// StatementObserver continues to receive the same events. Argument values are
// excluded from the report unless IncludeStatementArguments is passed,
// independently of the inherited observer's argument setting.
//
// A callback error is returned unchanged together with the completed report.
// Panics propagate. Callback must finish any goroutines using the supplied
// context before it returns.
func Debug(ctx context.Context, callback func(context.Context) error, options ...StatementObserverOption) (DebugReport, error) {
	if ctx == nil {
		return DebugReport{}, fmt.Errorf("orm: debug with a nil context")
	}
	if callback == nil {
		return DebugReport{}, fmt.Errorf("orm: debug with a nil callback")
	}

	reportConfiguration := statementObserverContextValue{}
	for _, option := range options {
		if option != nil {
			option.applyStatementObserver(&reportConfiguration)
		}
	}

	parent := statementObserverContextValue{}
	if inherited := statementObserverContext(ctx); inherited != nil {
		parent = *inherited
	}
	collector := newDebugReportCollector()
	includeArguments := reportConfiguration.hasOption(statementObserverIncludeArguments) ||
		parent.observer != nil && parent.hasOption(statementObserverIncludeArguments)
	collectServerRU := reportConfiguration.hasOption(statementObserverCollectServerRU) ||
		parent.observer != nil && parent.hasOption(statementObserverCollectServerRU)
	contextOptions := parent.options & statementRuntimeCollectServerRU
	if includeArguments {
		contextOptions |= statementObserverIncludeArguments
	}
	if collectServerRU {
		contextOptions |= statementObserverCollectServerRU
	}
	debugContext := context.WithValue(ctx, statementObserverContextKey{}, &statementObserverContextValue{
		observer:       debugStatementObserver(parent, collector, reportConfiguration.hasOption(statementObserverIncludeArguments)),
		options:        contextOptions,
		runtimeCapture: parent.runtimeCapture,
		runtimeScope:   parent.runtimeScope,
	})

	startedAt := time.Now()
	err := callback(debugContext)
	return collector.finish(startedAt, time.Since(startedAt)), err
}

type debugReportCollector struct {
	mutex             sync.Mutex
	open              bool
	statementDuration time.Duration
	statements        []StatementEvent
	inlineStatements  [2]StatementEvent
}

func newDebugReportCollector() *debugReportCollector {
	collector := &debugReportCollector{open: true}
	collector.statements = collector.inlineStatements[:0]
	return collector
}

func (collector *debugReportCollector) append(event StatementEvent) {
	collector.mutex.Lock()
	if collector.open {
		collector.statements = append(collector.statements, event)
		collector.statementDuration += event.Duration
	}
	collector.mutex.Unlock()
}

func (collector *debugReportCollector) finish(startedAt time.Time, duration time.Duration) DebugReport {
	collector.mutex.Lock()
	collector.open = false
	report := DebugReport{
		StartedAt:         startedAt,
		Duration:          duration,
		StatementDuration: collector.statementDuration,
		ServerRU:          summarizeServerRU(collector.statements),
		Statements:        collector.statements,
	}
	collector.mutex.Unlock()
	return report
}

func summarizeServerRU(statements []StatementEvent) *ServerRUSummary {
	var summary *ServerRUSummary
	for index := range statements {
		observation := statements[index].ServerRU
		if observation == nil {
			continue
		}
		if summary == nil {
			summary = &ServerRUSummary{}
		}
		summary.DiagnosticDuration += observation.DiagnosticDuration
		summary.AuxiliaryStatements += observation.AuxiliaryStatements
		if observation.Known {
			summary.Samples++
			summary.Total += observation.Value
		}
		if observation.Error != nil {
			summary.Errors++
		}
	}
	return summary
}

func debugStatementObserver(parent statementObserverContextValue, collector *debugReportCollector, includeArguments bool) StatementObserver {
	return func(event StatementEvent) {
		reportEvent := event
		reportEvent.ServerRU = cloneServerRUObservation(event.ServerRU)
		if !includeArguments {
			reportEvent.Arguments = nil
		}
		collector.append(reportEvent)

		if parent.observer == nil {
			return
		}
		parentEvent := event
		parentEvent.ServerRU = cloneServerRUObservation(event.ServerRU)
		if !parent.hasOption(statementObserverIncludeArguments) {
			parentEvent.Arguments = nil
		} else if includeArguments && parentEvent.Arguments != nil {
			parentEvent.Arguments = append([]any(nil), parentEvent.Arguments...)
		}
		parent.observer(parentEvent)
	}
}

func cloneServerRUObservation(observation *ServerRUObservation) *ServerRUObservation {
	if observation == nil {
		return nil
	}
	clone := *observation
	return &clone
}
