package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

// RawQuery executes explicitly supplied SQL and scans returned column names
// into an application-owned model.
//
// RawQuery does not provide relation hydration or typed SQL diagnostics.
type RawQuery[T any] struct {
	statement string
	arguments []any
}

// Raw starts a typed raw query without performing database I/O.
//
// Returned SQL column names must match mapped column names on T. Expressions
// should use an AS alias, and computed fields can receive those aliases.
// Raw does not parse or sanitize the statement. Callers must keep untrusted
// values out of the SQL text and pass them separately through placeholders.
func Raw[T any](statement string, arguments ...any) *RawQuery[T] {
	return &RawQuery[T]{
		statement: statement,
		arguments: append([]any(nil), arguments...),
	}
}

// Build validates the raw query model and returns the original SQL plus bind
// arguments without executing a custom driver.Valuer or accessing a database.
func (q *RawQuery[T]) Build() (string, []any, error) {
	if _, err := q.descriptor(); err != nil {
		return "", nil, err
	}
	return q.statement, append([]any(nil), q.arguments...), nil
}

// All executes the raw query and scans every row.
func (q *RawQuery[T]) All(ctx context.Context, executor QueryExecutor) ([]T, error) {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return nil, err
	}
	descriptor, err := q.descriptor()
	if err != nil {
		return nil, err
	}
	metadata := runtimeRawMetadata(descriptor.Name(), "all")
	rows, err := queryTextRowsWithMetadata(ctx, executor, descriptor.Name(), q.statement, q.arguments, metadata)
	if err != nil {
		return nil, err
	}
	plan, err := rawRowsScanPlan(descriptor, rows)
	if err != nil {
		return nil, closeRowsAfterError(descriptor.Name(), rows, err)
	}
	return collectRows[T](plan, rows)
}

// First executes the raw query and returns its first row without rewriting the
// supplied SQL. It returns sql.ErrNoRows when no row is returned.
func (q *RawQuery[T]) First(ctx context.Context, executor QueryExecutor) (T, error) {
	return q.one(ctx, executor, false)
}

// Only executes the raw query and requires exactly one returned row without
// rewriting the supplied SQL.
func (q *RawQuery[T]) Only(ctx context.Context, executor QueryExecutor) (T, error) {
	return q.one(ctx, executor, true)
}

func (q *RawQuery[T]) one(ctx context.Context, executor QueryExecutor, only bool) (T, error) {
	var zero T
	if err := validateQueryExecution(ctx, executor); err != nil {
		return zero, err
	}
	descriptor, err := q.descriptor()
	if err != nil {
		return zero, err
	}
	terminal := "first"
	if only {
		terminal = "only"
	}
	metadata := runtimeRawMetadata(descriptor.Name(), terminal)
	rows, err := queryTextRowsWithMetadata(ctx, executor, descriptor.Name(), q.statement, q.arguments, metadata)
	if err != nil {
		return zero, err
	}
	plan, err := rawRowsScanPlan(descriptor, rows)
	if err != nil {
		return zero, closeRowsAfterError(descriptor.Name(), rows, err)
	}
	return collectOne[T](plan, rows, only)
}

func (q *RawQuery[T]) descriptor() (*model.Descriptor, error) {
	if q == nil {
		return nil, fmt.Errorf("orm: compile a nil raw query")
	}
	if strings.TrimSpace(q.statement) == "" {
		return nil, fmt.Errorf("orm: raw query SQL must not be empty")
	}
	modelType := reflect.TypeFor[T]()
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: raw query model must be a non-pointer struct, got %v", modelType)
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return nil, fmt.Errorf("orm: describe raw query model: %w", err)
	}
	return descriptor, nil
}

type rawScanPlanKey struct {
	modelType reflect.Type
	columns   string
}

var rawScanPlanCache sync.Map

func rawRowsScanPlan(descriptor *model.Descriptor, rows interface{ Columns() ([]string, error) }) (*scanPlan, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("orm: read %s raw query columns: %w", descriptor.Name(), err)
	}
	key := rawScanPlanKey{modelType: descriptor.Type(), columns: strings.Join(columns, "\x00")}
	if cached, ok := rawScanPlanCache.Load(key); ok {
		result := cached.(scanPlanResult)
		return result.plan, result.err
	}
	plan, compileErr := compileRawScanPlan(descriptor, columns)
	result, _ := rawScanPlanCache.LoadOrStore(key, scanPlanResult{plan: plan, err: compileErr})
	cached := result.(scanPlanResult)
	return cached.plan, cached.err
}

func compileRawScanPlan(descriptor *model.Descriptor, columns []string) (*scanPlan, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("orm: raw query for %s returned no columns", descriptor.Name())
	}
	fields := make([]model.Field, len(columns))
	seen := make(map[string]bool, len(columns))
	for index, column := range columns {
		if seen[column] {
			return nil, fmt.Errorf("orm: raw query for %s repeats result column %q", descriptor.Name(), column)
		}
		field, exists := descriptor.FieldByColumn(column)
		if !exists {
			return nil, fmt.Errorf("orm: raw query result column %q is not mapped on %s; add an AS alias or model field", column, descriptor.Name())
		}
		if !field.CanScan() {
			return nil, fmt.Errorf("orm: raw query result field %s.%s cannot be read from a database row", descriptor.Name(), field.GoName())
		}
		seen[column] = true
		fields[index] = field
	}
	return compileScanPlanFields(descriptor, fields)
}

// RawExec executes explicitly supplied mutation SQL through a caller-owned
// database/sql executor and returns the affected row count.
//
// RawExec bypasses typed model, predicate, and mutation-safety validation. It
// does not parse or sanitize the statement. Callers must keep untrusted values
// out of the SQL text and pass them separately through placeholders.
func RawExec(ctx context.Context, executor ExecExecutor, statement string, arguments ...any) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	if strings.TrimSpace(statement) == "" {
		return 0, fmt.Errorf("orm: raw mutation SQL must not be empty")
	}
	metadata := runtimeRawMetadata("", "exec")
	observation := beginStatementObservationWithMetadata(ctx, inferStatementOperation(statement), statement, arguments, metadata)
	if observation != nil && observation.event.ServerRU != nil {
		executor = observation.prepareServerRUExecExecutor(ctx, executor)
	}
	result, err := executor.ExecContext(ctx, statement, arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute raw mutation: %w", err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute raw mutation: executor returned a nil result")
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "raw mutation", "raw SQL")
}
