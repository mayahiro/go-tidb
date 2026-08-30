package orm

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

// ErrMultipleRows reports that Only received more than one row.
var ErrMultipleRows = errors.New("orm: multiple rows in result set")

// First executes the SELECT with an explicit executor and returns its first row.
//
// First applies LIMIT 1 without mutating the query and replaces a previously
// configured Limit. It returns sql.ErrNoRows when no row matches. First does
// not add an implicit order, so use OrderBy when the selected row must be
// deterministic.
func (q *SelectQuery[T]) First(ctx context.Context, executor QueryExecutor) (T, error) {
	return q.one(ctx, executor, false)
}

// Only executes the SELECT with an explicit executor and requires one row.
//
// Only applies LIMIT 2 without mutating the query and replaces a previously
// configured Limit. It returns sql.ErrNoRows for zero rows and ErrMultipleRows
// for two or more rows.
func (q *SelectQuery[T]) Only(ctx context.Context, executor QueryExecutor) (T, error) {
	return q.one(ctx, executor, true)
}

func (q *SelectQuery[T]) one(ctx context.Context, executor QueryExecutor, only bool) (T, error) {
	var zero T
	if err := validateQueryExecution(ctx, executor); err != nil {
		return zero, err
	}
	limit := int64(1)
	if only {
		limit = 2
	}
	compiled, err := q.compileWithLimit(limit)
	if err != nil {
		return zero, err
	}
	rows, err := queryRows(ctx, executor, compiled)
	if err != nil {
		return zero, err
	}
	value, err := collectSelectOne[T](compiled.statement, rows, only)
	if err != nil {
		return zero, err
	}
	if len(compiled.preloads) != 0 {
		if err := executePreloads(ctx, executor, compiled.preloads, reflect.ValueOf(&value).Elem()); err != nil {
			return zero, err
		}
	}
	return value, nil
}

func collectSelectOne[T any](statement *selectStatement, rows resultRows, only bool) (T, error) {
	var value T
	modelName := statement.scanPlan.modelType.Name()
	if !rows.Next() {
		if err := finishRowsWithOutcome(modelName, rows, sql.ErrNoRows); err != nil {
			return value, err
		}
		return value, sql.ErrNoRows
	}

	decoder := statement.newDecoder(0)
	if err := decoder.scan(rows, &value); err != nil {
		return value, closeRowsAfterError(modelName, rows, err)
	}
	if only && rows.Next() {
		var zero T
		return zero, closeRowsAfterError(modelName, rows, ErrMultipleRows)
	}
	if err := finishRows(modelName, rows); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

func collectOne[T any](plan *scanPlan, rows resultRows, only bool) (T, error) {
	var value T
	if !rows.Next() {
		if err := finishRowsWithOutcome(plan.modelType.Name(), rows, sql.ErrNoRows); err != nil {
			return value, err
		}
		return value, sql.ErrNoRows
	}

	decoder := plan.newDecoder()
	if err := decoder.scan(rows, &value); err != nil {
		return value, closeRowsAfterError(plan.modelType.Name(), rows, err)
	}
	if only && rows.Next() {
		var zero T
		return zero, closeRowsAfterError(plan.modelType.Name(), rows, ErrMultipleRows)
	}
	if err := finishRows(plan.modelType.Name(), rows); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}
