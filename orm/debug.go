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
	// Statements contains completed events in observer delivery order. It is
	// non-nil even when the callback executes no statements.
	Statements []StatementEvent
}

// Debug runs callback with a derived context and returns one report containing
// every ORM statement completed through that context before callback returns.
//
// Callback must use the supplied context for statements to be captured. Debug
// performs no database I/O and does not run EXPLAIN or read ServerRU. An
// inherited StatementObserver continues to receive the same events. Argument
// values are excluded from the report unless IncludeStatementArguments is
// passed, independently of the inherited observer's argument setting.
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

	parent, _ := ctx.Value(statementObserverContextKey{}).(statementObserverContextValue)
	collector := newDebugReportCollector()
	debugContext := context.WithValue(ctx, statementObserverContextKey{}, statementObserverContextValue{
		observer: debugStatementObserver(parent, collector, reportConfiguration.includeArguments),
		includeArguments: reportConfiguration.includeArguments ||
			(parent.observer != nil && parent.includeArguments),
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
		Statements:        collector.statements,
	}
	collector.mutex.Unlock()
	return report
}

func debugStatementObserver(parent statementObserverContextValue, collector *debugReportCollector, includeArguments bool) StatementObserver {
	return func(event StatementEvent) {
		reportEvent := event
		if !includeArguments {
			reportEvent.Arguments = nil
		}
		collector.append(reportEvent)

		if parent.observer == nil {
			return
		}
		parentEvent := event
		if !parent.includeArguments {
			parentEvent.Arguments = nil
		} else if includeArguments && parentEvent.Arguments != nil {
			parentEvent.Arguments = append([]any(nil), parentEvent.Arguments...)
		}
		parent.observer(parentEvent)
	}
}
