package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
)

// QueryExecutor executes a context-aware query and is implemented by
// *sql.DB, *sql.Conn, and *sql.Tx.
//
// It does not open or configure a connection.
type QueryExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ExecExecutor executes a context-aware mutation and is implemented by
// *sql.DB, *sql.Conn, and *sql.Tx.
//
// It does not open or configure a connection.
type ExecExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Executor can execute both queries and mutations through an existing
// database/sql connection boundary.
type Executor interface {
	QueryExecutor
	ExecExecutor
}

var (
	_ QueryExecutor = (*sql.DB)(nil)
	_ QueryExecutor = (*sql.Conn)(nil)
	_ QueryExecutor = (*sql.Tx)(nil)
	_ ExecExecutor  = (*sql.DB)(nil)
	_ ExecExecutor  = (*sql.Conn)(nil)
	_ ExecExecutor  = (*sql.Tx)(nil)
	_ Executor      = (*sql.DB)(nil)
	_ Executor      = (*sql.Conn)(nil)
	_ Executor      = (*sql.Tx)(nil)
)

type resultRows interface {
	rowScanner
	Next() bool
	Err() error
	Close() error
}

type queryResultRows interface {
	resultRows
	Columns() ([]string, error)
}

// All executes the SELECT with an explicit executor and scans every row.
//
// All returns a non-nil empty slice when the result contains no rows. It closes
// rows before returning and reports scan, iteration, and close failures.
func (q *SelectQuery[T]) All(ctx context.Context, executor QueryExecutor) ([]T, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return nil, err
	}
	compiled, err := q.compile()
	if err != nil {
		return nil, err
	}
	metadata := runtimeSelectMetadata(ctx, &q.selection, compiled, "all")
	rows, err := queryRows(ctx, executor, compiled, metadata)
	if err != nil {
		return nil, err
	}
	values, err := collectSelectRows[T](compiled.statement, rows)
	if err != nil {
		return nil, err
	}
	if len(compiled.preloads) != 0 && len(values) != 0 {
		if err := executePreloads(ctx, executor, compiled.preloads, reflect.ValueOf(values)); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func collectSelectRows[T any](statement *selectStatement, rows resultRows) ([]T, error) {
	values := make([]T, 0)
	decoder := statement.newDecoder(0)
	var zero T
	for rows.Next() {
		values = append(values, zero)
		if err := decoder.scan(rows, &values[len(values)-1]); err != nil {
			return nil, closeRowsAfterError(statement.scanPlan.modelType.Name(), rows, err)
		}
	}

	if err := finishRows(statement.scanPlan.modelType.Name(), rows); err != nil {
		return nil, err
	}
	return values, nil
}

func validateQueryExecution(ctx context.Context, executor QueryExecutor) error {
	if ctx == nil {
		return fmt.Errorf("orm: query rows with a nil context")
	}
	if nilPredicateArgument(executor) {
		return fmt.Errorf("orm: query rows with a nil executor")
	}
	return nil
}

func queryRows(ctx context.Context, executor QueryExecutor, compiled compiledSelect, metadata statementRuntimeMetadata) (queryResultRows, error) {
	return queryTextRowsWithMetadata(
		ctx,
		executor,
		compiled.statement.scanPlan.modelType.Name(),
		compiled.statement.sql,
		compiled.arguments,
		metadata,
	)
}

func queryTextRows(ctx context.Context, executor QueryExecutor, modelName, query string, arguments []any) (queryResultRows, error) {
	return queryTextRowsWithMetadata(ctx, executor, modelName, query, arguments, statementRuntimeMetadata{})
}

func queryTextRowsWithMetadata(ctx context.Context, executor QueryExecutor, modelName, query string, arguments []any, metadata statementRuntimeMetadata) (queryResultRows, error) {
	return queryTextRowsOperationWithMetadata(ctx, executor, StatementSelect, modelName, query, arguments, metadata)
}

func queryTextRowsOperation(ctx context.Context, executor QueryExecutor, operation StatementOperation, modelName, query string, arguments []any) (queryResultRows, error) {
	return queryTextRowsOperationWithMetadata(ctx, executor, operation, modelName, query, arguments, statementRuntimeMetadata{})
}

func queryTextRowsOperationWithMetadata(ctx context.Context, executor QueryExecutor, operation StatementOperation, modelName, query string, arguments []any, metadata statementRuntimeMetadata) (queryResultRows, error) {
	observation := beginStatementObservationWithMetadata(ctx, operation, query, arguments, metadata)
	if observation != nil && observation.event.ServerRU != nil {
		executor = observation.prepareServerRUQueryExecutor(ctx, executor)
	}
	rows, err := executor.QueryContext(ctx, query, arguments...)
	if err != nil {
		err = fmt.Errorf("orm: query %s rows: %w", modelName, err)
		observation.finish(0, false, err)
		return nil, err
	}
	if rows == nil {
		err = fmt.Errorf("orm: query %s rows: executor returned nil rows", modelName)
		observation.finish(0, false, err)
		return nil, err
	}
	if observation != nil {
		if observation.runtime != nil {
			return &capturedQueryRows{Rows: rows, observation: observation}, nil
		}
		return &observedQueryRows{Rows: rows, observation: observation}, nil
	}
	return rows, nil
}

func collectRows[T any](plan *scanPlan, rows resultRows) ([]T, error) {
	values := make([]T, 0)
	decoder := plan.newDecoder()
	var zero T
	for rows.Next() {
		values = append(values, zero)
		if err := decoder.scan(rows, &values[len(values)-1]); err != nil {
			return nil, closeRowsAfterError(plan.modelType.Name(), rows, err)
		}
	}

	if err := finishRows(plan.modelType.Name(), rows); err != nil {
		return nil, err
	}
	return values, nil
}

func finishRows(modelName string, rows resultRows) error {
	return finishRowsWithOutcome(modelName, rows, nil)
}

func finishRowsWithOutcome(modelName string, rows resultRows, outcomeErr error) error {
	closeErr := rows.Close()
	iterationErr := rows.Err()
	if iterationErr != nil {
		iterationErr = fmt.Errorf("orm: iterate %s rows: %w", modelName, iterationErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("orm: close %s rows: %w", modelName, closeErr)
	}
	finishRowsStatementObservation(rows, errors.Join(outcomeErr, iterationErr, closeErr))
	return errors.Join(iterationErr, closeErr)
}

func closeRowsAfterError(modelName string, rows resultRows, operationErr error) error {
	finishErr := finishRowsWithOutcome(modelName, rows, operationErr)
	if finishErr == nil {
		return operationErr
	}
	return errors.Join(operationErr, finishErr)
}
