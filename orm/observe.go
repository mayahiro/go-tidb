package orm

import "context"

// Observe attaches a default observer to an existing executor without opening
// a connection or taking ownership of its lifecycle. Share the returned
// executor with repositories; ORM queries, preloads, mutations, and Transaction
// inherit its configuration without per-call context setup.
//
// WithStatementObserver overrides this default for one context, including nil
// to disable it. WithRuntimeCapture remains independent. Arguments and ServerRU
// collection are opt-in. Wrapping an observed executor replaces its default
// observer; passing nil removes that default. Direct database/sql method calls
// are not observed; use ORM terminals, including Raw and RawExec.
func Observe(executor Executor, observer StatementObserver, options ...StatementObserverOption) Executor {
	var inherited *statementObserverContextValue
	if previous, ok := executor.(*observedExecutor); ok {
		executor = previous.Executor
		inherited = previous.observation
	}
	if nilPredicateArgument(executor) || observer == nil && (inherited == nil || inherited.runtimeCapture == nil) {
		return executor
	}
	value := &statementObserverContextValue{observer: observer, observerSet: true}
	if inherited != nil {
		value.runtimeCapture = inherited.runtimeCapture
		value.runtimeScope = inherited.runtimeScope
		value.options = inherited.options & statementRuntimeCollectServerRU
	}
	for _, option := range options {
		if option != nil {
			option.applyStatementObserver(value)
		}
	}
	return &observedExecutor{Executor: executor, observation: value}
}

type observedExecutor struct {
	Executor
	observation *statementObserverContextValue
}

func unwrapObservedExecutor(executor any) any {
	if observed, ok := executor.(*observedExecutor); ok {
		return observed.Executor
	}
	return executor
}

// Resolve once at the terminal boundary so compiler metadata and secondary
// statements share the effective observation context. Plain executors take no
// allocation and do not inspect context values here.
func executorStatementContext(ctx context.Context, executor any) context.Context {
	observed, ok := executor.(*observedExecutor)
	if !ok {
		return ctx
	}
	defaults := observed.observation
	parent := statementObserverContext(ctx)
	if parent == defaults {
		return ctx
	}
	if parent == nil {
		return context.WithValue(ctx, statementObserverContextKey{}, defaults)
	}
	if parent.observerSet && (parent.runtimeCapture != nil || defaults.runtimeCapture == nil) {
		return ctx
	}
	value := *defaults
	if parent.observerSet {
		value.observer = parent.observer
		value.observerSet = true
		value.options &^= statementObserverIncludeArguments | statementObserverCollectServerRU
		value.options |= parent.options & (statementObserverIncludeArguments | statementObserverCollectServerRU)
	}
	if parent.runtimeCapture != nil {
		value.runtimeCapture = parent.runtimeCapture
		value.runtimeScope = parent.runtimeScope
		value.options &^= statementRuntimeCollectServerRU
		value.options |= parent.options & statementRuntimeCollectServerRU
	}
	return context.WithValue(ctx, statementObserverContextKey{}, &value)
}
