package orm

import (
	"context"
	"fmt"
	"strings"
)

const (
	explainPrefix        = "EXPLAIN "
	explainAnalyzePrefix = "EXPLAIN ANALYZE "
)

var explainColumnNames = [...]string{"id", "estRows", "task", "access object", "operator info"}

var explainAnalyzeColumnNames = [...]string{
	"id",
	"estRows",
	"actRows",
	"task",
	"access object",
	"execution info",
	"operator info",
	"memory",
	"disk",
}

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

// ExplainAnalyzeRow describes one operator in TiDB's default row-format
// EXPLAIN ANALYZE output.
type ExplainAnalyzeRow struct {
	// ID is the operator identifier and contains TiDB's plan-tree indentation.
	ID string
	// EstRows is TiDB's estimated number of output rows for the operator.
	EstRows float64
	// ActRows is the actual number of output rows observed for the operator.
	ActRows int64
	// Task identifies where TiDB executed the operator, such as root or cop.
	Task string
	// AccessObject describes the table, partition, or index that was accessed.
	AccessObject string
	// ExecutionInfo contains TiDB's operator timing, loops, RPC, and RU details.
	ExecutionInfo string
	// OperatorInfo contains operator-specific planning details.
	OperatorInfo string
	// Memory is TiDB's formatted peak operator memory usage or N/A.
	Memory string
	// Disk is TiDB's formatted peak operator disk usage or N/A.
	Disk string
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
	rows, err := q.queryExplainRows(ctx, executor, StatementExplain, explainPrefix)
	if err != nil {
		return nil, err
	}
	return collectExplainRows(rows)
}

// ExplainAnalyze executes this SELECT and asks TiDB for its default row-format
// runtime plan.
//
// Calling ExplainAnalyze is an explicit opt-in to executing the complete root
// SELECT and consuming its database resources. It never accepts mutation or
// raw SQL because it is a terminal on SelectQuery. The result includes actual
// operator rows, execution details, memory, and disk usage. Inline to-one joins
// execute as part of the root SELECT. Collection preload statements are not
// executed or analyzed.
func (q *SelectQuery[T]) ExplainAnalyze(ctx context.Context, executor QueryExecutor) ([]ExplainAnalyzeRow, error) {
	rows, err := q.queryExplainRows(ctx, executor, StatementExplainAnalyze, explainAnalyzePrefix)
	if err != nil {
		return nil, err
	}
	return collectExplainAnalyzeRows(rows)
}

func (q *SelectQuery[T]) queryExplainRows(ctx context.Context, executor QueryExecutor, operation StatementOperation, prefix string) (queryResultRows, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return nil, err
	}
	compiled, err := q.compile()
	if err != nil {
		return nil, err
	}
	statement := prefix + compiled.statement.sql
	metadata := runtimePlanMetadata(runtimeSelectMetadata(ctx, &q.selection, compiled, strings.ToLower(string(operation))))
	return queryTextRowsOperationWithMetadata(
		ctx,
		executor,
		operation,
		string(operation),
		statement,
		compiled.arguments,
		metadata,
	)
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
	return validatePlanColumns("EXPLAIN", columns, explainColumnNames[:])
}

func collectExplainAnalyzeRows(rows queryResultRows) ([]ExplainAnalyzeRow, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, closeRowsAfterError("EXPLAIN ANALYZE", rows, fmt.Errorf("orm: read EXPLAIN ANALYZE columns: %w", err))
	}
	if err := validatePlanColumns("EXPLAIN ANALYZE", columns, explainAnalyzeColumnNames[:]); err != nil {
		return nil, closeRowsAfterError("EXPLAIN ANALYZE", rows, err)
	}

	result := make([]ExplainAnalyzeRow, 0, 4)
	for rows.Next() {
		var row ExplainAnalyzeRow
		if err := rows.Scan(
			&row.ID,
			&row.EstRows,
			&row.ActRows,
			&row.Task,
			&row.AccessObject,
			&row.ExecutionInfo,
			&row.OperatorInfo,
			&row.Memory,
			&row.Disk,
		); err != nil {
			return nil, closeRowsAfterError("EXPLAIN ANALYZE", rows, fmt.Errorf("orm: scan EXPLAIN ANALYZE row: %w", err))
		}
		result = append(result, row)
	}
	if err := finishRows("EXPLAIN ANALYZE", rows); err != nil {
		return nil, err
	}
	return result, nil
}

func validatePlanColumns(operation string, columns, expected []string) error {
	if len(columns) != len(expected) {
		return fmt.Errorf("orm: TiDB %s returned columns %q, want %q", operation, columns, expected)
	}
	for index, column := range columns {
		if column != expected[index] {
			return fmt.Errorf("orm: TiDB %s returned columns %q, want %q", operation, columns, expected)
		}
	}
	return nil
}
