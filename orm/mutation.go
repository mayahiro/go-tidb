package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

const maxMutationParameters = 65535

type compiledMutation struct {
	modelName  string
	descriptor *model.Descriptor
	sql        string
	arguments  []any
	autoRandom *mutationFieldPlan
	root       reflect.Value
	noOp       bool
}

// InsertQuery builds and executes one INSERT for an application-owned model.
//
// An AUTO_RANDOM primary key is omitted and populated from the execution
// result after a successful INSERT.
type InsertQuery[T any] struct {
	value *T
}

// Insert starts one INSERT without performing database I/O.
func Insert[T any](value *T) *InsertQuery[T] {
	return &InsertQuery[T]{value: value}
}

// Build compiles the INSERT and returns SQL plus bind arguments without
// executing a custom driver.Valuer or accessing a database.
func (q *InsertQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the INSERT through an explicit database/sql executor and
// returns the affected row count.
func (q *InsertQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	observation := beginStatementObservation(ctx, StatementInsert, compiled.sql, compiled.arguments)
	result, err := executor.ExecContext(ctx, compiled.sql, compiled.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute INSERT for %s: %w", compiled.modelName, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute INSERT for %s: executor returned a nil result", compiled.modelName)
		observation.finish(0, false, err)
		return 0, err
	}
	if compiled.autoRandom != nil {
		generated, generatedErr := result.LastInsertId()
		if generatedErr != nil {
			err = fmt.Errorf("orm: read generated AUTO_RANDOM value for %s.%s: %w", compiled.modelName, compiled.autoRandom.field.GoName(), generatedErr)
			observation.finish(0, false, err)
			return 0, err
		}
		if assignErr := assignGeneratedInteger(compiled.root, compiled.descriptor, *compiled.autoRandom, generated); assignErr != nil {
			observation.finish(0, false, assignErr)
			return 0, assignErr
		}
	}
	return finishMutationStatementObservation(observation, result, "INSERT", compiled.modelName)
}

func (q *InsertQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil INSERT query")
	}
	root, descriptor, err := mutationModelValue(q.value, "INSERT")
	if err != nil {
		return compiledMutation{}, err
	}
	plan := mutationPlanFor(descriptor)
	if plan.insertErr != nil {
		return compiledMutation{}, plan.insertErr
	}
	arguments, err := mutationArguments(root, descriptor, plan.insertFields)
	if err != nil {
		return compiledMutation{}, err
	}
	return compiledMutation{
		modelName:  descriptor.Name(),
		descriptor: descriptor,
		sql:        plan.insertSQL,
		arguments:  arguments,
		autoRandom: plan.autoRandom,
		root:       root,
	}, nil
}

// InsertManyQuery builds and executes multi-row INSERT statements from model
// values or model pointers.
//
// AUTO_RANDOM fields are omitted and are not populated on individual slice
// elements because one multi-row result does not expose every generated ID.
// Execution automatically splits statements at TiDB's placeholder limit.
type InsertManyQuery[T any] struct {
	values []T
}

// InsertMany starts a bulk INSERT without performing database I/O.
// Values may be a slice of structs or a slice of pointers to structs.
func InsertMany[T any](values []T) *InsertManyQuery[T] {
	return &InsertManyQuery[T]{values: values}
}

// Build compiles one multi-row INSERT and returns SQL plus bind arguments. It
// reports an error when execution requires multiple statements.
// An empty slice is a no-op represented by empty SQL and no arguments.
func (q *InsertManyQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes one or more automatically bounded multi-row INSERT statements
// and returns their summed affected row count.
// An empty slice returns zero without calling the executor.
//
// Exec does not start a transaction. If a later statement fails, it returns
// the affected count from completed statements with the error. Pass a
// caller-owned transaction when every statement must be atomic.
func (q *InsertManyQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	plan, err := q.prepare()
	if err != nil {
		return 0, err
	}
	return plan.exec(ctx, executor)
}

func (q *InsertManyQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil bulk INSERT query")
	}
	descriptor, pointerElements, err := insertManyDescriptor[T]("bulk INSERT")
	if err != nil {
		return compiledMutation{}, err
	}
	if len(q.values) == 0 {
		return compiledMutation{modelName: descriptor.Name(), noOp: true}, nil
	}
	plan := mutationPlanFor(descriptor)
	if plan.insertErr != nil {
		return compiledMutation{}, plan.insertErr
	}
	rowsPerStatement := maxMutationParameters
	if len(plan.insertFields) != 0 {
		rowsPerStatement = maxMutationParameters / len(plan.insertFields)
		if rowsPerStatement == 0 {
			return compiledMutation{}, fmt.Errorf("orm: bulk INSERT for %s has %d insert fields, exceeding TiDB's %d-placeholder statement limit", descriptor.Name(), len(plan.insertFields), maxMutationParameters)
		}
	}
	if len(q.values) > rowsPerStatement {
		return compiledMutation{}, bulkBuildStatementLimitError("bulk INSERT", descriptor, len(q.values), rowsPerStatement)
	}
	arguments := make([]any, len(q.values)*len(plan.insertFields))
	if err := fillInsertManyArguments(arguments, reflect.ValueOf(q.values), descriptor, plan.insertFields, pointerElements, 0, len(q.values), "bulk INSERT"); err != nil {
		return compiledMutation{}, err
	}
	return compiledMutation{
		modelName:  descriptor.Name(),
		descriptor: descriptor,
		sql:        renderInsert(descriptor.TableName(), plan.insertFields, len(q.values)),
		arguments:  arguments,
	}, nil
}

func (q *InsertManyQuery[T]) prepare() (bulkMutationPlan, error) {
	if q == nil {
		return bulkMutationPlan{}, fmt.Errorf("orm: compile a nil bulk INSERT query")
	}
	return prepareBulkMutation(q.values, nil, "bulk INSERT", false)
}

func fillInsertManyArguments(arguments []any, values reflect.Value, descriptor *model.Descriptor, fields []mutationFieldPlan, pointerElements bool, start, end int, operation string) error {
	fieldCount := len(fields)
	if pointerElements {
		for index := start; index < end; index++ {
			root := values.Index(index)
			if root.IsNil() {
				return fmt.Errorf("orm: %s row %d: %s is nil", operation, index, descriptor.Name())
			}
			argumentStart := (index - start) * fieldCount
			if err := fillMutationArguments(arguments[argumentStart:argumentStart+fieldCount], root.Elem(), descriptor, fields); err != nil {
				return fmt.Errorf("orm: %s row %d: %w", operation, index, err)
			}
		}
		return nil
	}
	for index := start; index < end; index++ {
		argumentStart := (index - start) * fieldCount
		if err := fillMutationArguments(arguments[argumentStart:argumentStart+fieldCount], values.Index(index), descriptor, fields); err != nil {
			return fmt.Errorf("orm: %s row %d: %w", operation, index, err)
		}
	}
	return nil
}

// UpdateQuery builds and executes a primary-key UPDATE.
type UpdateQuery[T any] struct {
	value       *T
	fields      []string
	withDeleted bool
}

// Update starts a primary-key UPDATE.
//
// Without field names, Update writes every mapped non-primary-key field except
// computed fields. With field names, it writes only those Go fields. Every
// declared primary-key component is read from value and included in the WHERE
// clause.
func Update[T any](value *T, fields ...string) *UpdateQuery[T] {
	return &UpdateQuery[T]{value: value, fields: append([]string(nil), fields...)}
}

// WithDeleted allows this primary-key UPDATE to match a logically deleted
// row. It is primarily useful for restoring a row by clearing its soft-delete
// field.
func (q *UpdateQuery[T]) WithDeleted() *UpdateQuery[T] {
	if q == nil {
		return nil
	}
	q.withDeleted = true
	return q
}

// Build compiles the UPDATE and returns SQL plus bind arguments without
// accessing a database.
func (q *UpdateQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the primary-key UPDATE and returns the affected row count.
func (q *UpdateQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	observation := beginStatementObservation(ctx, StatementUpdate, compiled.sql, compiled.arguments)
	result, err := executor.ExecContext(ctx, compiled.sql, compiled.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute UPDATE for %s: %w", compiled.modelName, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute UPDATE for %s: executor returned a nil result", compiled.modelName)
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "UPDATE", compiled.modelName)
}

func (q *UpdateQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil UPDATE query")
	}
	root, descriptor, err := mutationModelValue(q.value, "UPDATE")
	if err != nil {
		return compiledMutation{}, err
	}
	plan := mutationPlanFor(descriptor)
	if err := validateWithDeleted(descriptor, q.withDeleted, "UPDATE"); err != nil {
		return compiledMutation{}, err
	}
	if len(plan.primaryKey) == 0 {
		return compiledMutation{}, fmt.Errorf("orm: UPDATE for %s requires a declared primary key", descriptor.Name())
	}
	if plan.primaryKeyErr != nil {
		return compiledMutation{}, plan.primaryKeyErr
	}
	fields := plan.updateFields
	statement := plan.updateSQL
	if len(q.fields) == 0 {
		if plan.updateErr != nil {
			return compiledMutation{}, plan.updateErr
		}
	} else {
		fields, err = selectedUpdateFields(descriptor, q.fields, "UPDATE")
		if err != nil {
			return compiledMutation{}, err
		}
		statement = renderPrimaryKeyUpdate(descriptor.TableName(), fields, plan.primaryKey, plan.softDelete)
	}
	if q.withDeleted {
		statement = renderPrimaryKeyUpdate(descriptor.TableName(), fields, plan.primaryKey, nil)
	}
	arguments := make([]any, len(fields)+len(plan.primaryKey))
	if err := fillMutationArguments(arguments[:len(fields)], root, descriptor, fields); err != nil {
		return compiledMutation{}, err
	}
	if err := fillPrimaryKeyArguments(arguments[len(fields):], root, descriptor, plan.primaryKey, "UPDATE"); err != nil {
		return compiledMutation{}, err
	}
	return compiledMutation{
		modelName:  descriptor.Name(),
		descriptor: descriptor,
		sql:        statement,
		arguments:  arguments,
	}, nil
}

// DeleteQuery builds and executes a primary-key or predicate-bounded deletion.
// Models with a soft-delete field use an active-row UPDATE; other models use a
// physical DELETE.
type DeleteQuery[T any] struct {
	value      *T
	predicates []predicate
	where      bool
}

// Delete starts a deletion using every declared primary-key value from model.
func Delete[T any](value *T) *DeleteQuery[T] {
	return &DeleteQuery[T]{value: value}
}

// DeleteWhere starts a deletion using explicit scalar predicates.
//
// At least one predicate is required and relation predicates are rejected.
func DeleteWhere[T any](predicates ...Predicate) *DeleteQuery[T] {
	values := make([]predicate, len(predicates))
	for index := range predicates {
		values[index] = predicates[index].value
	}
	return &DeleteQuery[T]{predicates: values, where: true}
}

// Build compiles the deletion statement and returns SQL plus bind arguments
// without accessing a database.
func (q *DeleteQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the deletion statement and returns the affected row count.
func (q *DeleteQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	observation := beginStatementObservation(ctx, inferStatementOperation(compiled.sql), compiled.sql, compiled.arguments)
	result, err := executor.ExecContext(ctx, compiled.sql, compiled.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute DELETE for %s: %w", compiled.modelName, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute DELETE for %s: executor returned a nil result", compiled.modelName)
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "DELETE", compiled.modelName)
}

func (q *DeleteQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil DELETE query")
	}
	descriptor, err := mutationDescriptor[T]("DELETE")
	if err != nil {
		return compiledMutation{}, err
	}
	if q.where {
		if q.value != nil {
			return compiledMutation{}, fmt.Errorf("orm: DELETE for %s cannot combine a model primary key with explicit predicates", descriptor.Name())
		}
		whereSQL, arguments, whereErr := compileMutationWhere(descriptor, q.predicates, "DELETE")
		if whereErr != nil {
			return compiledMutation{}, whereErr
		}
		statement := "DELETE FROM " + quoteIdentifier(descriptor.TableName()) + whereSQL
		plan := mutationPlanFor(descriptor)
		if plan.softDelete != nil {
			statement = renderSoftDeleteWhere(descriptor.TableName(), whereSQL, plan.softDelete)
		}
		return compiledMutation{
			modelName:  descriptor.Name(),
			descriptor: descriptor,
			sql:        statement,
			arguments:  arguments,
		}, nil
	}
	root, described, err := mutationModelValue(q.value, "DELETE")
	if err != nil {
		return compiledMutation{}, err
	}
	plan := mutationPlanFor(described)
	if len(plan.primaryKey) == 0 {
		return compiledMutation{}, fmt.Errorf("orm: DELETE for %s requires a declared primary key", described.Name())
	}
	if plan.primaryKeyErr != nil {
		return compiledMutation{}, plan.primaryKeyErr
	}
	arguments := make([]any, len(plan.primaryKey))
	if err := fillPrimaryKeyArguments(arguments, root, described, plan.primaryKey, "DELETE"); err != nil {
		return compiledMutation{}, err
	}
	return compiledMutation{
		modelName:  described.Name(),
		descriptor: described,
		sql:        plan.deleteSQL,
		arguments:  arguments,
	}, nil
}

func mutationDescriptor[T any](operation string) (*model.Descriptor, error) {
	modelType := reflect.TypeFor[T]()
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: %s model must be a non-pointer struct, got %v", operation, modelType)
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return nil, fmt.Errorf("orm: describe %s model: %w", operation, err)
	}
	return descriptor, nil
}

func insertManyDescriptor[T any](operation string) (*model.Descriptor, bool, error) {
	modelType := reflect.TypeFor[T]()
	pointerElements := modelType != nil && modelType.Kind() == reflect.Pointer
	if pointerElements {
		modelType = modelType.Elem()
	}
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return nil, false, fmt.Errorf("orm: %s model must be a struct or pointer to struct, got %v", operation, reflect.TypeFor[T]())
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return nil, false, fmt.Errorf("orm: describe %s model: %w", operation, err)
	}
	return descriptor, pointerElements, nil
}

func mutationModelValue[T any](value *T, operation string) (reflect.Value, *model.Descriptor, error) {
	descriptor, err := mutationDescriptor[T](operation)
	if err != nil {
		return reflect.Value{}, nil, err
	}
	if value == nil {
		return reflect.Value{}, nil, fmt.Errorf("orm: %s %s from a nil model", operation, descriptor.Name())
	}
	return reflect.ValueOf(value).Elem(), descriptor, nil
}

func renderInsert(table string, fields []mutationFieldPlan, rows int) string {
	var query strings.Builder
	query.Grow(insertSQLCapacity(table, fields, rows))
	query.WriteString("INSERT INTO ")
	writeQuotedIdentifier(&query, table)
	query.WriteString(" (")
	for index, field := range fields {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, field.field.ColumnName())
	}
	query.WriteString(") VALUES ")
	for row := 0; row < rows; row++ {
		if row != 0 {
			query.WriteString(", ")
		}
		query.WriteByte('(')
		for index := range fields {
			if index != 0 {
				query.WriteString(", ")
			}
			query.WriteByte('?')
		}
		query.WriteByte(')')
	}
	return query.String()
}

func insertSQLCapacity(table string, fields []mutationFieldPlan, rows int) int {
	capacity := len("INSERT INTO ") + len(table) + len("`` () VALUES ")
	for index, field := range fields {
		if index != 0 {
			capacity += len(", ")
		}
		capacity += len(field.field.ColumnName()) + len("``")
	}
	rowLength := len("()")
	if len(fields) != 0 {
		rowLength = len(fields) + (len(fields)-1)*len(", ") + len("()")
	}
	capacity += rows * rowLength
	if rows > 1 {
		capacity += (rows - 1) * len(", ")
	}
	return capacity
}

func renderPrimaryKeyUpdate(table string, fields, primaryKey []mutationFieldPlan, softDelete *mutationFieldPlan) string {
	var query strings.Builder
	query.WriteString("UPDATE ")
	writeQuotedIdentifier(&query, table)
	query.WriteString(" SET ")
	for index, field := range fields {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, field.field.ColumnName())
		query.WriteString(" = ?")
	}
	query.WriteString(" WHERE ")
	writePrimaryKeyPredicates(&query, primaryKey)
	writeActiveMutationSoftDeletePredicate(&query, softDelete)
	return query.String()
}

func renderPrimaryKeyDelete(table string, primaryKey []mutationFieldPlan, softDelete *mutationFieldPlan) string {
	var query strings.Builder
	if softDelete != nil {
		query.WriteString("UPDATE ")
		writeQuotedIdentifier(&query, table)
		query.WriteString(" SET ")
		writeQuotedIdentifier(&query, softDelete.field.ColumnName())
		query.WriteString(" = ")
		query.WriteString(softDeleteCurrentTimestamp)
	} else {
		query.WriteString("DELETE FROM ")
		writeQuotedIdentifier(&query, table)
	}
	query.WriteString(" WHERE ")
	writePrimaryKeyPredicates(&query, primaryKey)
	writeActiveMutationSoftDeletePredicate(&query, softDelete)
	return query.String()
}

func renderSoftDeleteWhere(table, whereSQL string, softDelete *mutationFieldPlan) string {
	var query strings.Builder
	query.Grow(len("UPDATE  SET  =  AND  IS NULL") + len(table) + len(whereSQL) + len(softDeleteCurrentTimestamp) + len(softDelete.field.ColumnName())*2 + 8)
	query.WriteString("UPDATE ")
	writeQuotedIdentifier(&query, table)
	query.WriteString(" SET ")
	writeQuotedIdentifier(&query, softDelete.field.ColumnName())
	query.WriteString(" = ")
	query.WriteString(softDeleteCurrentTimestamp)
	query.WriteString(whereSQL)
	writeActiveMutationSoftDeletePredicate(&query, softDelete)
	return query.String()
}

func writeActiveMutationSoftDeletePredicate(query *strings.Builder, softDelete *mutationFieldPlan) {
	if softDelete == nil {
		return
	}
	query.WriteString(" AND ")
	writeQuotedIdentifier(query, softDelete.field.ColumnName())
	query.WriteString(" IS NULL")
}

func writePrimaryKeyPredicates(query *strings.Builder, primaryKey []mutationFieldPlan) {
	for index, field := range primaryKey {
		if index != 0 {
			query.WriteString(" AND ")
		}
		writeQuotedIdentifier(query, field.field.ColumnName())
		query.WriteString(" = ?")
	}
}

func compileMutationWhere(descriptor *model.Descriptor, predicates []predicate, operation string) (string, []any, error) {
	if len(predicates) == 0 {
		return "", nil, fmt.Errorf("orm: %s for %s requires at least one predicate", operation, descriptor.Name())
	}
	if predicatesHaveRelation(predicates) {
		return "", nil, fmt.Errorf("orm: %s for %s does not support relation predicates", operation, descriptor.Name())
	}
	argumentCount, sqlCapacity := predicateCompileCapacity(predicates)
	var query strings.Builder
	query.Grow(len(" WHERE ") + sqlCapacity)
	query.WriteString(" WHERE ")
	compiler := predicateCompiler{
		descriptor: descriptor,
		query:      &query,
		arguments:  make([]any, 0, argumentCount),
		operation:  operation,
	}
	for index := range predicates {
		if index != 0 {
			query.WriteString(" AND ")
		}
		if err := compiler.write(predicates[index]); err != nil {
			return "", nil, err
		}
	}
	return query.String(), compiler.arguments, nil
}

func quoteIdentifier(identifier string) string {
	var query strings.Builder
	query.Grow(len(identifier) + 2)
	writeQuotedIdentifier(&query, identifier)
	return query.String()
}

func validateMutationExecution(ctx context.Context, executor ExecExecutor) error {
	if ctx == nil {
		return fmt.Errorf("orm: execute mutation with a nil context")
	}
	if nilPredicateArgument(executor) {
		return fmt.Errorf("orm: execute mutation with a nil executor")
	}
	return nil
}

func mutationRowsAffected(result sql.Result, operation, modelName string) (int64, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("orm: read %s affected rows for %s: %w", operation, modelName, err)
	}
	return affected, nil
}
