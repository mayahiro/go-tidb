package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

type compiledCount struct {
	modelName string
	sql       string
	arguments []any
}

var countBaseCache sync.Map

// Count returns the number of rows represented by the SELECT using an
// explicit executor.
//
// Count ignores projection and ordinary ordering because they do not change
// the number of rows. Predicates, Limit, Offset, and SeekAfter remain
// effective. OrderBy still defines an active SeekAfter cursor.
func (q *SelectQuery[T]) Count(ctx context.Context, executor QueryExecutor) (int64, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compileCount()
	if err != nil {
		return 0, err
	}
	rows, err := queryTextRows(ctx, executor, compiled.modelName, compiled.sql, compiled.arguments)
	if err != nil {
		return 0, err
	}
	return scanCount(compiled.modelName, rows)
}

func (q *SelectQuery[T]) compileCount() (compiledCount, error) {
	if q == nil {
		return compiledCount{}, fmt.Errorf("orm: compile a nil SELECT query")
	}
	modelType := q.selection.modelType
	if modelType == nil {
		modelType = reflect.TypeFor[T]()
	}
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return compiledCount{}, fmt.Errorf("orm: SELECT query model must be a non-pointer struct, got %v", modelType)
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return compiledCount{}, fmt.Errorf("orm: compile SELECT model: %w", err)
	}

	selection := q.selection
	selection.modelType = modelType
	paginated := selection.pagination.limitSet || selection.pagination.offsetSet
	baseSQL := countBase(descriptor)
	if paginated {
		baseSQL = existsBase(descriptor)
	}
	clauses, err := compileUnorderedClauses(descriptor, baseSQL, &selection)
	if err != nil {
		return compiledCount{}, err
	}
	if paginated {
		clauses.sql = wrapCount(clauses.sql)
	}
	return compiledCount{
		modelName: descriptor.Name(),
		sql:       clauses.sql,
		arguments: clauses.arguments,
	}, nil
}

func scanCount(modelName string, rows resultRows) (int64, error) {
	if !rows.Next() {
		noRowsErr := fmt.Errorf("orm: scan %s count: %w", modelName, sql.ErrNoRows)
		if err := finishRowsWithOutcome(modelName, rows, noRowsErr); err != nil {
			return 0, err
		}
		return 0, noRowsErr
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		return 0, closeRowsAfterError(modelName, rows, fmt.Errorf("orm: scan %s count: %w", modelName, err))
	}
	if err := finishRows(modelName, rows); err != nil {
		return 0, err
	}
	return count, nil
}

func countBase(descriptor *model.Descriptor) string {
	modelType := descriptor.Type()
	if cached, ok := countBaseCache.Load(modelType); ok {
		return cached.(string)
	}
	var query strings.Builder
	query.Grow(len("SELECT COUNT(*) FROM ") + len(descriptor.TableName()) + 2)
	query.WriteString("SELECT COUNT(*) FROM ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	result, _ := countBaseCache.LoadOrStore(modelType, query.String())
	return result.(string)
}

func wrapCount(inner string) string {
	const prefix = "SELECT COUNT(*) FROM ("
	const suffix = ") AS `tidbgo_count`"
	var query strings.Builder
	query.Grow(len(prefix) + len(inner) + len(suffix))
	query.WriteString(prefix)
	query.WriteString(inner)
	query.WriteString(suffix)
	return query.String()
}
