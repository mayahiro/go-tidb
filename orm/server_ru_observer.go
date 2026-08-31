package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ServerRUObservation describes one opt-in same-session ServerRU collection
// attempt associated with a completed target statement.
//
// DiagnosticDuration covers connection pinning, the auxiliary ServerRU query,
// and release of an internally pinned connection. AuxiliaryStatements counts
// only attempted diagnostic SQL statements. Error never replaces the target
// statement result.
type ServerRUObservation struct {
	// Value is TiDB's reported ru_consumption when Known is true.
	Value float64
	// Known reports whether Value was decoded successfully.
	Known bool
	// DiagnosticDuration is time added by automatic ServerRU collection.
	DiagnosticDuration time.Duration
	// AuxiliaryStatements is the number of diagnostic SQL statements attempted.
	AuxiliaryStatements int
	// Error is a connection-pinning, query, decode, or connection-release error.
	Error error
}

type statementServerRUCollector struct {
	ctx     context.Context
	session serverRUQueryer
	release func() error
}

func statementServerRUCollectionEnabled(value *statementObserverContextValue) bool {
	if value == nil {
		return false
	}
	observerEnabled := value.observer != nil && value.options&statementObserverCollectServerRU != 0
	captureEnabled := value.runtimeCapture != nil && value.options&statementRuntimeCollectServerRU != 0
	return observerEnabled || captureEnabled
}

func serverRUStatementOperation(operation StatementOperation) bool {
	switch operation {
	case StatementSelect, StatementInsert, StatementUpsert, StatementUpdate, StatementDelete:
		return true
	default:
		return false
	}
}

func (observation *statementObservation) prepareServerRUQueryExecutor(ctx context.Context, executor QueryExecutor) QueryExecutor {
	prepared := observation.prepareServerRUExecutor(ctx, executor)
	if queryExecutor, ok := prepared.(QueryExecutor); ok {
		return queryExecutor
	}
	return executor
}

func (observation *statementObservation) prepareServerRUExecExecutor(ctx context.Context, executor ExecExecutor) ExecExecutor {
	prepared := observation.prepareServerRUExecutor(ctx, executor)
	if execExecutor, ok := prepared.(ExecExecutor); ok {
		return execExecutor
	}
	return executor
}

func (observation *statementObservation) prepareServerRUExecutor(ctx context.Context, executor any) any {
	if observation == nil || observation.event.ServerRU == nil {
		return executor
	}
	defer func() {
		observation.event.StartedAt = time.Now()
	}()

	switch session := executor.(type) {
	case *sql.DB:
		startedAt := time.Now()
		connection, err := session.Conn(ctx)
		observation.event.ServerRU.DiagnosticDuration += time.Since(startedAt)
		if err != nil {
			observation.addServerRUError(fmt.Errorf("orm: pin connection for automatic ServerRU collection: %w", err))
			return executor
		}
		observation.serverRUCollector = &statementServerRUCollector{
			ctx:     ctx,
			session: connection,
			release: connection.Close,
		}
		return connection
	case *sql.Conn:
		observation.serverRUCollector = &statementServerRUCollector{ctx: ctx, session: session}
		return session
	case *sql.Tx:
		observation.serverRUCollector = &statementServerRUCollector{ctx: ctx, session: session}
		return session
	default:
		observation.addServerRUError(fmt.Errorf("orm: automatic ServerRU collection requires *sql.DB, *sql.Conn, or *sql.Tx executor"))
		return executor
	}
}

func (observation *statementObservation) collectServerRU() {
	if observation == nil || observation.serverRUCollector == nil {
		return
	}
	collector := observation.serverRUCollector
	observation.serverRUCollector = nil

	startedAt := time.Now()
	observation.event.ServerRU.AuxiliaryStatements++
	value, err := readLastServerRU(collector.ctx, collector.session)
	observation.event.ServerRU.DiagnosticDuration += time.Since(startedAt)
	if err != nil {
		observation.addServerRUError(err)
	} else {
		observation.event.ServerRU.Value = value
		observation.event.ServerRU.Known = true
	}

	if collector.release == nil {
		return
	}
	startedAt = time.Now()
	err = collector.release()
	observation.event.ServerRU.DiagnosticDuration += time.Since(startedAt)
	if err != nil {
		observation.addServerRUError(fmt.Errorf("orm: release automatic ServerRU connection: %w", err))
	}
}

func (observation *statementObservation) addServerRUError(err error) {
	if observation == nil || observation.event.ServerRU == nil || err == nil {
		return
	}
	observation.event.ServerRU.Error = errors.Join(observation.event.ServerRU.Error, err)
}
