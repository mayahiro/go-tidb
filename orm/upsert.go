package orm

import (
	"context"
	"fmt"
	"strings"
)

// UpsertQuery builds and executes one INSERT ON DUPLICATE KEY UPDATE for an
// application-owned model.
type UpsertQuery[T any] struct {
	value  *T
	fields []string
}

// Upsert starts one INSERT ON DUPLICATE KEY UPDATE without database I/O.
//
// Without field names, every writable mapped non-primary-key field is updated
// on a unique-key conflict. With field names, only those Go fields are updated.
// TiDB selects the conflict from any primary or unique key; this API does not
// accept a conflict target.
func Upsert[T any](value *T, fields ...string) *UpsertQuery[T] {
	return &UpsertQuery[T]{value: value, fields: append([]string(nil), fields...)}
}

// Build compiles the UPSERT and returns SQL plus bind arguments without
// accessing a database.
func (q *UpsertQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the UPSERT and returns the database-reported affected row
// count.
//
// Exec never changes an AUTO_RANDOM field because sql.Result cannot reliably
// distinguish an insert from a duplicate-key update. Use Insert when the
// generated ID must be assigned to the model.
func (q *UpsertQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	observation := beginTypedMutationStatementObservation(ctx, StatementUpsert, compiled.sql, compiled.arguments, compiled.modelName, "upsert")
	if observation != nil && observation.event.ServerRU != nil {
		executor = observation.prepareServerRUExecExecutor(ctx, executor)
	}
	result, err := executor.ExecContext(ctx, compiled.sql, compiled.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute UPSERT for %s: %w", compiled.modelName, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute UPSERT for %s: executor returned a nil result", compiled.modelName)
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "UPSERT", compiled.modelName)
}

func (q *UpsertQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil UPSERT query")
	}
	root, descriptor, err := mutationModelValue(q.value, "UPSERT")
	if err != nil {
		return compiledMutation{}, err
	}
	plan := mutationPlanFor(descriptor)
	if plan.insertErr != nil {
		return compiledMutation{}, plan.insertErr
	}
	updateFields, err := mutationUpdateFields(descriptor, plan, q.fields, "UPSERT")
	if err != nil {
		return compiledMutation{}, err
	}
	arguments, err := mutationArguments(root, descriptor, plan.insertFields)
	if err != nil {
		return compiledMutation{}, err
	}
	return compiledMutation{
		modelName:  descriptor.Name(),
		descriptor: descriptor,
		sql:        appendOnDuplicateKeyUpdate(plan.insertSQL, updateFields),
		arguments:  arguments,
	}, nil
}

// UpsertManyQuery builds and executes INSERT ON DUPLICATE KEY UPDATE
// statements from model values or model pointers.
//
// Execution automatically splits statements at TiDB's placeholder limit.
// AUTO_RANDOM fields are omitted and are not populated on individual elements.
type UpsertManyQuery[T any] struct {
	values []T
	fields []string
}

// UpsertMany starts a bulk UPSERT without performing database I/O.
// Values may be a slice of structs or a slice of pointers to structs.
//
// Without field names, every writable mapped non-primary-key field is updated
// on a unique-key conflict. With field names, only those Go fields are updated.
// TiDB selects the conflict from any primary or unique key; this API does not
// accept a conflict target.
func UpsertMany[T any](values []T, fields ...string) *UpsertManyQuery[T] {
	return &UpsertManyQuery[T]{values: values, fields: append([]string(nil), fields...)}
}

// StatementCount returns the exact number of UPSERT statements planned for a
// successful Exec after applying TiDB's placeholder limit.
//
// It validates model metadata and selected update fields without inspecting
// element values, executing custom driver.Valuer implementations, or accessing
// a database. Build and Exec retain value validation. An empty slice returns
// zero.
func (q *UpsertManyQuery[T]) StatementCount() (int, error) {
	plan, err := q.prepare()
	if err != nil {
		return 0, err
	}
	return plan.statementCount()
}

// Build compiles one bulk UPSERT statement and returns SQL plus bind
// arguments. It reports an error when execution requires multiple statements.
// An empty slice is a no-op represented by empty SQL and no arguments.
func (q *UpsertManyQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes one or more automatically bounded bulk UPSERT statements and
// returns their summed database-reported affected row count.
// An empty slice returns zero without calling the executor.
//
// Exec does not start a transaction. If a later statement fails, it returns
// the affected count from completed statements with the error. Pass a
// caller-owned transaction when every statement must be atomic.
func (q *UpsertManyQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	plan, err := q.prepare()
	if err != nil {
		return 0, err
	}
	return plan.exec(ctx, executor)
}

func (q *UpsertManyQuery[T]) compile() (compiledMutation, error) {
	plan, err := q.prepare()
	if err != nil {
		return compiledMutation{}, err
	}
	return plan.compileSingle()
}

func (q *UpsertManyQuery[T]) prepare() (bulkMutationPlan, error) {
	if q == nil {
		return bulkMutationPlan{}, fmt.Errorf("orm: compile a nil bulk UPSERT query")
	}
	return prepareBulkMutation(q.values, q.fields, "bulk UPSERT", true)
}

func appendOnDuplicateKeyUpdate(statement string, fields []mutationFieldPlan) string {
	capacity := len(statement) + len(" ON DUPLICATE KEY UPDATE ")
	for index, field := range fields {
		if index != 0 {
			capacity += len(", ")
		}
		capacity += len(field.field.ColumnName())*2 + len("`` = VALUES(``)")
	}
	var query strings.Builder
	query.Grow(capacity)
	query.WriteString(statement)
	query.WriteString(" ON DUPLICATE KEY UPDATE ")
	for index, field := range fields {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, field.field.ColumnName())
		query.WriteString(" = VALUES(")
		writeQuotedIdentifier(&query, field.field.ColumnName())
		query.WriteByte(')')
	}
	return query.String()
}
