package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

type compiledExists struct {
	modelName string
	sql       string
	arguments []any
}

var existsBaseCache sync.Map

// Exists reports whether the SELECT has at least one row using an explicit
// executor.
//
// Exists applies SELECT 1 and LIMIT 1 without mutating the query. Projection
// and ordering do not affect existence and are omitted, except that OrderBy
// still defines an active SeekAfter cursor. Predicates, Offset, and SeekAfter
// remain effective.
func (q *SelectQuery[T]) Exists(ctx context.Context, executor QueryExecutor) (bool, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return false, err
	}
	compiled, err := q.compileExists()
	if err != nil {
		return false, err
	}
	metadata := runtimeTypedSelectMetadata(compiled.modelName, "exists")
	rows, err := queryTextRowsWithMetadata(ctx, executor, compiled.modelName, compiled.sql, compiled.arguments, metadata)
	if err != nil {
		return false, err
	}
	exists := rows.Next()
	if err := finishRows(compiled.modelName, rows); err != nil {
		return false, err
	}
	return exists, nil
}

func (q *SelectQuery[T]) compileExists() (compiledExists, error) {
	if q == nil {
		return compiledExists{}, fmt.Errorf("orm: compile a nil SELECT query")
	}
	modelType := q.selection.modelType
	if modelType == nil {
		modelType = reflect.TypeFor[T]()
	}
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return compiledExists{}, fmt.Errorf("orm: SELECT query model must be a non-pointer struct, got %v", modelType)
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return compiledExists{}, fmt.Errorf("orm: compile SELECT model: %w", err)
	}

	selection := q.selection
	selection.modelType = modelType
	selection.pagination.limit = 1
	selection.pagination.limitSet = true
	clauses, err := compileUnorderedClauses(descriptor, existsBase(descriptor), &selection)
	if err != nil {
		return compiledExists{}, err
	}
	return compiledExists{
		modelName: descriptor.Name(),
		sql:       clauses.sql,
		arguments: clauses.arguments,
	}, nil
}

func existsBase(descriptor *model.Descriptor) string {
	modelType := descriptor.Type()
	if cached, ok := existsBaseCache.Load(modelType); ok {
		return cached.(string)
	}
	var query strings.Builder
	query.Grow(len("SELECT 1 FROM ") + len(descriptor.TableName()) + 2)
	query.WriteString("SELECT 1 FROM ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	result, _ := existsBaseCache.LoadOrStore(modelType, query.String())
	return result.(string)
}
