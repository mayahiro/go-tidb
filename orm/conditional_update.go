package orm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type assignmentOperator uint8

const (
	assignmentSet assignmentOperator = iota + 1
	assignmentIncrement
)

type assignment struct {
	operator assignmentOperator
	field    string
	value    any
}

// Assignment is an immutable column assignment created by Set or Increment.
type Assignment struct {
	value assignment
}

// Set assigns value to one mapped field.
//
// A nil or typed-nil value is passed as SQL NULL. The value remains a bind
// argument and is never interpolated into the generated SQL.
func Set(field string, value any) Assignment {
	return Assignment{value: assignment{operator: assignmentSet, field: field, value: value}}
}

// Increment atomically adds delta to one mapped numeric field.
//
// Custom driver.Valuer-backed fields are accepted because they can represent
// application-selected DECIMAL types. The database remains responsible for
// validating that the physical column supports numeric addition.
func Increment(field string, delta any) Assignment {
	return Assignment{value: assignment{operator: assignmentIncrement, field: field, value: delta}}
}

// UpdateWhereQuery builds and executes one predicate-bounded UPDATE.
//
// An UpdateWhereQuery performs no I/O until Exec is called and is not safe for
// concurrent mutation.
type UpdateWhereQuery[T any] struct {
	assignments []assignment
	predicates  []predicate
	withDeleted bool
}

// UpdateWhere starts a predicate-bounded UPDATE with explicit assignments.
//
// At least one assignment and one scalar predicate are required. Primary-key,
// AUTO_RANDOM, and computed fields cannot be assigned.
func UpdateWhere[T any](assignments ...Assignment) *UpdateWhereQuery[T] {
	values := make([]assignment, len(assignments))
	for index := range assignments {
		values[index] = assignments[index].value
	}
	return &UpdateWhereQuery[T]{assignments: values}
}

// Where appends scalar predicates joined by AND in call order.
//
// Relation predicates are rejected because a conditional update operates on
// exactly one base table.
func (q *UpdateWhereQuery[T]) Where(predicates ...Predicate) *UpdateWhereQuery[T] {
	if q == nil {
		return nil
	}
	for index := range predicates {
		q.predicates = append(q.predicates, predicates[index].value)
	}
	return q
}

// WithDeleted allows this conditional UPDATE to match logically deleted rows.
// It is primarily useful for restoring multiple rows by clearing their
// soft-delete field.
func (q *UpdateWhereQuery[T]) WithDeleted() *UpdateWhereQuery[T] {
	if q == nil {
		return nil
	}
	q.withDeleted = true
	return q
}

// Build compiles the UPDATE and returns SQL plus bind arguments without
// accessing a database or invoking custom driver.Valuer implementations.
func (q *UpdateWhereQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the UPDATE through an explicit database/sql executor and
// returns the affected row count.
func (q *UpdateWhereQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
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
		err = fmt.Errorf("orm: execute conditional UPDATE for %s: %w", compiled.modelName, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute conditional UPDATE for %s: executor returned a nil result", compiled.modelName)
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "conditional UPDATE", compiled.modelName)
}

func (q *UpdateWhereQuery[T]) compile() (compiledMutation, error) {
	if q == nil {
		return compiledMutation{}, fmt.Errorf("orm: compile a nil conditional UPDATE query")
	}
	descriptor, err := mutationDescriptor[T]("conditional UPDATE")
	if err != nil {
		return compiledMutation{}, err
	}
	if err := validateWithDeleted(descriptor, q.withDeleted, "conditional UPDATE"); err != nil {
		return compiledMutation{}, err
	}
	if len(q.assignments) == 0 {
		return compiledMutation{}, fmt.Errorf("orm: conditional UPDATE for %s requires at least one assignment", descriptor.Name())
	}
	if len(q.predicates) == 0 {
		return compiledMutation{}, fmt.Errorf("orm: conditional UPDATE for %s requires at least one predicate", descriptor.Name())
	}
	if predicatesHaveRelation(q.predicates) {
		return compiledMutation{}, fmt.Errorf("orm: conditional UPDATE for %s does not support relation predicates", descriptor.Name())
	}
	predicateArgumentCount, predicateSQLCapacity := predicateCompileCapacity(q.predicates)
	softDeleteField, filterSoftDeleted := activeSoftDeleteField(descriptor, q.withDeleted)
	if filterSoftDeleted {
		predicateSQLCapacity += len(" AND `` IS NULL") + len(softDeleteField.ColumnName())
	}
	argumentCount := len(q.assignments) + predicateArgumentCount
	if argumentCount > maxMutationParameters {
		return compiledMutation{}, fmt.Errorf("orm: conditional UPDATE for %s uses %d placeholders, exceeding TiDB's %d-placeholder statement limit", descriptor.Name(), argumentCount, maxMutationParameters)
	}
	var query strings.Builder
	query.Grow(conditionalUpdateSQLCapacity(descriptor.TableName(), q.assignments, predicateSQLCapacity))
	query.WriteString("UPDATE ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	query.WriteString(" SET ")
	arguments := make([]any, 0, argumentCount)
	if err := writeAssignments(&query, &arguments, descriptor, q.assignments); err != nil {
		return compiledMutation{}, err
	}
	query.WriteString(" WHERE ")
	compiler := predicateCompiler{
		descriptor: descriptor,
		query:      &query,
		arguments:  arguments,
		operation:  "conditional UPDATE",
	}
	for index := range q.predicates {
		if index != 0 {
			query.WriteString(" AND ")
		}
		if err := compiler.write(q.predicates[index]); err != nil {
			return compiledMutation{}, err
		}
	}
	if filterSoftDeleted {
		query.WriteString(" AND ")
		writeActiveSoftDeletePredicate(&query, "", softDeleteField)
	}
	return compiledMutation{
		modelName: descriptor.Name(),
		sql:       query.String(),
		arguments: compiler.arguments,
	}, nil
}

func writeAssignments(query *strings.Builder, arguments *[]any, descriptor *model.Descriptor, assignments []assignment) error {
	for index := range assignments {
		current := assignments[index]
		field, err := conditionalUpdateField(descriptor, current.field)
		if err != nil {
			return err
		}
		for previous := range index {
			if assignments[previous].field == current.field {
				return fmt.Errorf("orm: conditional UPDATE for %s repeats field %q", descriptor.Name(), current.field)
			}
		}
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(query, field.ColumnName())
		query.WriteString(" = ")
		switch current.operator {
		case assignmentSet:
			query.WriteByte('?')
		case assignmentIncrement:
			if nilPredicateArgument(current.value) {
				return fmt.Errorf("orm: conditional UPDATE increment for %s.%s requires a non-nil delta", descriptor.Name(), field.GoName())
			}
			if !incrementableField(field) {
				return fmt.Errorf("orm: conditional UPDATE increment field %s.%s must be numeric or driver.Valuer-backed", descriptor.Name(), field.GoName())
			}
			writeQuotedIdentifier(query, field.ColumnName())
			query.WriteString(" + ?")
		default:
			return fmt.Errorf("orm: conditional UPDATE for %s has an unknown assignment operator for field %q", descriptor.Name(), current.field)
		}
		value := current.value
		if current.operator == assignmentSet && field.IsSoftDelete() && field.PointerDepth() == 0 {
			if timestamp, ok := value.(time.Time); ok && timestamp.IsZero() {
				value = nil
			}
		}
		*arguments = append(*arguments, value)
	}
	return nil
}

func conditionalUpdateSQLCapacity(table string, assignments []assignment, predicateCapacity int) int {
	capacity := len("UPDATE ") + len(table) + len("`` SET  WHERE ") + predicateCapacity
	for index := range assignments {
		if index != 0 {
			capacity += len(", ")
		}
		capacity += len(assignments[index].field)*2 + len("`` = `` + ?")
	}
	return capacity
}

func conditionalUpdateField(descriptor *model.Descriptor, name string) (model.Field, error) {
	field, exists := descriptor.FieldByGoName(name)
	if !exists {
		return model.Field{}, fmt.Errorf("orm: conditional UPDATE field %s.%s is not a mapped scalar field", descriptor.Name(), name)
	}
	if field.IsPrimaryKey() || field.IsAutoRandom() {
		return model.Field{}, fmt.Errorf("orm: conditional UPDATE field %s.%s is a primary-key field", descriptor.Name(), name)
	}
	if field.IsComputed() {
		return model.Field{}, fmt.Errorf("orm: conditional UPDATE field %s.%s is computed", descriptor.Name(), name)
	}
	if !field.CanValue() {
		return model.Field{}, fmt.Errorf("orm: conditional UPDATE field %s.%s cannot be used as a database argument", descriptor.Name(), name)
	}
	return field, nil
}

func incrementableField(field model.Field) bool {
	switch field.Kind() {
	case model.KindInt, model.KindUint, model.KindFloat:
		return true
	case model.KindCustom:
		return field.UsesValuer()
	default:
		return false
	}
}
