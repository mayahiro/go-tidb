package orm

import (
	"context"
	"fmt"
)

const explainPrefix = "EXPLAIN "

var explainColumnNames = [...]string{"id", "estRows", "task", "access object", "operator info"}

// ExplainRow describes one operator in TiDB's default row-format EXPLAIN
// output.
type ExplainRow struct {
	// ID is the operator identifier and contains TiDB's plan-tree indentation.
	ID string
	// EstRows is TiDB's estimated number of output rows for the operator.
	EstRows float64
	// Task identifies where TiDB executes the operator, such as root or cop.
	Task string
	// AccessObject describes the table, partition, or index being accessed.
	AccessObject string
	// OperatorInfo contains operator-specific planning details.
	OperatorInfo string
}

// Explain asks TiDB for the default row-format execution plan of this SELECT.
//
// Explain never accepts mutation or raw SQL because it is a terminal on
// SelectQuery. It explains the root SELECT compiled by Build and All, including
// inline to-one joins. Collection preload statements depend on returned parent
// keys and are not included. The call performs database I/O but does not execute
// the root SELECT, although TiDB can evaluate certain subqueries during query
// optimization.
func (q *SelectQuery[T]) Explain(ctx context.Context, executor QueryExecutor) ([]ExplainRow, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return nil, err
	}
	compiled, err := q.compile()
	if err != nil {
		return nil, err
	}
	statement := explainPrefix + compiled.statement.sql
	rows, err := queryTextRowsOperation(
		ctx,
		executor,
		StatementExplain,
		"EXPLAIN",
		statement,
		compiled.arguments,
	)
	if err != nil {
		return nil, err
	}
	return collectExplainRows(rows)
}

func collectExplainRows(rows queryResultRows) ([]ExplainRow, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, closeRowsAfterError("EXPLAIN", rows, fmt.Errorf("orm: read EXPLAIN columns: %w", err))
	}
	if err := validateExplainColumns(columns); err != nil {
		return nil, closeRowsAfterError("EXPLAIN", rows, err)
	}

	result := make([]ExplainRow, 0, 4)
	for rows.Next() {
		var row ExplainRow
		if err := rows.Scan(&row.ID, &row.EstRows, &row.Task, &row.AccessObject, &row.OperatorInfo); err != nil {
			return nil, closeRowsAfterError("EXPLAIN", rows, fmt.Errorf("orm: scan EXPLAIN row: %w", err))
		}
		result = append(result, row)
	}
	if err := finishRows("EXPLAIN", rows); err != nil {
		return nil, err
	}
	return result, nil
}

func validateExplainColumns(columns []string) error {
	if len(columns) != len(explainColumnNames) {
		return fmt.Errorf("orm: TiDB EXPLAIN returned columns %q, want %q", columns, explainColumnNames)
	}
	for index, column := range columns {
		if column != explainColumnNames[index] {
			return fmt.Errorf("orm: TiDB EXPLAIN returned columns %q, want %q", columns, explainColumnNames)
		}
	}
	return nil
}
