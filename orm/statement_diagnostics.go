package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type diagnosticSession interface {
	QueryExecutor
	serverRUQueryer
}

type statementDiagnosticCollector struct {
	ctx     context.Context
	session diagnosticSession
	release func() error
}

func (o *statementObservation) prepareDiagnosticQueryExecutor(ctx context.Context, executor QueryExecutor) QueryExecutor {
	if prepared, ok := o.prepareDiagnosticExecutor(ctx, executor).(QueryExecutor); ok {
		return prepared
	}
	return executor
}

func (o *statementObservation) prepareDiagnosticExecExecutor(ctx context.Context, executor ExecExecutor) ExecExecutor {
	if prepared, ok := o.prepareDiagnosticExecutor(ctx, executor).(ExecExecutor); ok {
		return prepared
	}
	return executor
}

func (o *statementObservation) prepareDiagnosticExecutor(ctx context.Context, executor any) any {
	if o == nil || o.event.ServerRU == nil && o.event.Warnings == nil {
		return executor
	}
	defer func() { o.event.StartedAt = time.Now() }()
	var session diagnosticSession
	var release func() error
	switch raw := unwrapObservedExecutor(executor).(type) {
	case *sql.DB:
		started := time.Now()
		conn, err := raw.Conn(ctx)
		o.addDiagnosticDuration(time.Since(started))
		if err != nil {
			o.addDiagnosticError(fmt.Errorf("orm: pin connection for automatic diagnostic collection: %w", err))
			return executor
		}
		session, release = conn, conn.Close
	case *sql.Conn:
		session = raw
	case *sql.Tx:
		session = raw
	default:
		o.addDiagnosticError(fmt.Errorf("orm: automatic diagnostic collection requires *sql.DB, *sql.Conn, or *sql.Tx executor"))
		return executor
	}
	o.diagnosticCollector = &statementDiagnosticCollector{ctx: ctx, session: session, release: release}
	return session
}

func (o *statementObservation) addDiagnosticDuration(elapsed time.Duration) {
	if o.event.ServerRU != nil {
		o.event.ServerRU.DiagnosticDuration += elapsed
	} else if o.event.Warnings != nil {
		o.event.Warnings.DiagnosticDuration += elapsed
	}
}

func (o *statementObservation) addDiagnosticError(err error) {
	if o.event.ServerRU != nil {
		o.addServerRUError(err)
	} else if o.event.Warnings != nil {
		o.event.Warnings.Error = errors.Join(o.event.Warnings.Error, err)
	}
}

func (o *statementObservation) collectDiagnostics() {
	if o == nil || o.diagnosticCollector == nil {
		return
	}
	c := o.diagnosticCollector
	o.diagnosticCollector = nil
	started := time.Now()
	if ru := o.event.ServerRU; ru != nil {
		ru.AuxiliaryStatements++
		value, err := readLastServerRU(c.ctx, c.session)
		if err != nil {
			o.addServerRUError(err)
		} else {
			ru.Value, ru.Known = value, true
		}
	} else if w := o.event.Warnings; w != nil {
		if o.event.Error != nil {
			w.Error = errors.New("orm: warnings were not collected after a failed statement or result scan")
		} else {
			w.AuxiliaryStatements++
			w.Warnings, w.Error = collectStatementWarnings(c.ctx, c.session)
			w.Known = w.Error == nil
		}
	}
	o.addDiagnosticDuration(time.Since(started))
	if c.release != nil {
		started = time.Now()
		err := c.release()
		o.addDiagnosticDuration(time.Since(started))
		if err != nil {
			o.addDiagnosticError(fmt.Errorf("orm: release automatic diagnostic connection: %w", err))
		}
	}
}
