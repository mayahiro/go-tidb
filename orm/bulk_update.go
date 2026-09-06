package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

// UpdateManyQuery builds primary-key-bounded UPDATE statements with different
// values for each input row. It neither inserts rows nor changes model values.
// Like other mutable builders, it is not safe for concurrent mutation.
type UpdateManyQuery[T any] struct {
	values      []T
	fields      []string
	withDeleted bool
}

// UpdateMany starts a bulk UPDATE from a slice of structs or struct pointers.
// Without field names it writes every writable mapped non-primary-key field;
// otherwise it writes only the selected Go fields, just like Update.
//
// Inputs must identify distinct database rows. Exact duplicate native keys and
// nil keys are rejected before execution. Database collation, precision, and
// custom driver.Valuer equivalence cannot be verified offline; callers must
// ensure uniqueness under those rules. This is a set operation, not an ordered
// sequence of updates or a replacement for per-row conditional updates.
func UpdateMany[T any](values []T, fields ...string) *UpdateManyQuery[T] {
	return &UpdateManyQuery[T]{values: values, fields: append([]string(nil), fields...)}
}

// WithDeleted allows this bulk UPDATE to match logically deleted rows. The
// model must declare a soft-delete field; selecting it permits bulk restore.
func (q *UpdateManyQuery[T]) WithDeleted() *UpdateManyQuery[T] {
	if q != nil {
		q.withDeleted = true
	}
	return q
}

// Build compiles one UPDATE without database I/O or calling driver.Valuer.
// Empty input produces empty SQL and nil arguments. It reports an error when
// the input requires multiple statements; Exec performs automatic batching.
func (q *UpdateManyQuery[T]) Build() (string, []any, error) {
	plan, err := q.prepare()
	if err != nil {
		return "", nil, err
	}
	if !plan.noOp {
		if err := plan.validatePointerElements(); err != nil {
			return "", nil, err
		}
		if err := plan.validateUpdateKeys(); err != nil {
			return "", nil, err
		}
	}
	compiled, err := plan.compileSingle()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec updates existing rows through the supplied executor and returns the sum
// of database-reported affected counts, not necessarily the input length.
// Empty input does not call the executor. Soft-deleted rows are excluded unless
// WithDeleted is set. The models and their primary keys remain unchanged.
//
// Statements split at the placeholder limit. Exec never begins a transaction
// or retries: a later failure returns the count from completed statements and
// the error. Use Transaction or a caller-owned transaction for atomic batches.
func (q *UpdateManyQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	ctx = executorStatementContext(ctx, executor)
	plan, err := q.prepare()
	if err != nil {
		return 0, err
	}
	return plan.exec(ctx, executor)
}

func (q *UpdateManyQuery[T]) prepare() (bulkMutationPlan, error) {
	if q == nil {
		return bulkMutationPlan{}, fmt.Errorf("orm: compile a nil bulk UPDATE query")
	}
	descriptor, pointers, err := bulkMutationDescriptor[T]("bulk UPDATE")
	if err != nil {
		return bulkMutationPlan{}, err
	}
	if err := validateWithDeleted(descriptor, q.withDeleted, "bulk UPDATE"); err != nil {
		return bulkMutationPlan{}, err
	}
	result := bulkMutationPlan{
		operation: "bulk UPDATE", descriptor: descriptor, values: reflect.ValueOf(q.values),
		pointerElements: pointers, noOp: len(q.values) == 0,
	}
	if result.noOp {
		return result, nil
	}
	plan := mutationPlanFor(descriptor)
	if plan.primaryKeyErr != nil {
		return bulkMutationPlan{}, plan.primaryKeyErr
	}
	result.primaryKey = plan.primaryKey
	result.updateFields, err = mutationUpdateFields(descriptor, plan, q.fields, "bulk UPDATE")
	if err != nil {
		return bulkMutationPlan{}, err
	}
	if !q.withDeleted {
		result.softDelete = plan.softDelete
	}
	return result, nil
}

func (plan bulkMutationPlan) updateRow(index int) reflect.Value {
	row := plan.values.Index(index)
	if plan.pointerElements {
		return row.Elem()
	}
	return row
}

func (plan bulkMutationPlan) validateUpdateKeys() error {
	known := true
	for _, key := range plan.primaryKey {
		if key.field.UsesValuer() {
			known = false
		}
	}
	// Numeric single-column keys need neither boxing nor composite strings.
	numeric := false
	if len(plan.primaryKey) == 1 {
		switch plan.primaryKey[0].field.Kind() {
		case model.KindBool, model.KindInt, model.KindUint, model.KindFloat:
			numeric = true
		}
	}
	var numbers map[uint64]int
	var keys map[preloadLookupKey]int
	if known && plan.values.Len() > 1 {
		if numeric {
			numbers = make(map[uint64]int, plan.values.Len())
		} else {
			keys = make(map[preloadLookupKey]int, plan.values.Len())
		}
	}
	var buffer []byte
	for index := 0; index < plan.values.Len(); index++ {
		root := plan.updateRow(index)
		buffer = buffer[:0]
		var lookup preloadLookupKey
		for _, key := range plan.primaryKey {
			value, null, err := modelFieldValue(root, key.index)
			if err != nil {
				return fmt.Errorf("orm: bulk UPDATE row %d: read primary-key field %s.%s: %w", index, plan.descriptor.Name(), key.field.GoName(), err)
			}
			for !null && value.Kind() == reflect.Pointer {
				if value.IsNil() {
					null = true
					break
				}
				value = value.Elem()
			}
			if null || nilReflectValue(value) {
				return fmt.Errorf("orm: bulk UPDATE row %d: primary-key field %s.%s must not be nil", index, plan.descriptor.Name(), key.field.GoName())
			}
			if !known || plan.values.Len() == 1 {
				continue
			}
			lookup, _, _, err = preloadKeyField(key.field, value, false)
			if err != nil {
				return fmt.Errorf("orm: bulk UPDATE row %d: primary-key field %s.%s: %w", index, plan.descriptor.Name(), key.field.GoName(), err)
			}
			if len(plan.primaryKey) > 1 {
				buffer, err = appendCompositePreloadKey(buffer, lookup)
				if err != nil {
					return err
				}
			}
		}
		if !known || plan.values.Len() == 1 {
			continue
		}
		if len(plan.primaryKey) > 1 {
			lookup = preloadLookupKey{kind: 'c', text: string(buffer)}
		}
		var previous int
		var exists bool
		if numeric {
			previous, exists = numbers[lookup.first]
			numbers[lookup.first] = index
		} else {
			previous, exists = keys[lookup]
			keys[lookup] = index
		}
		if exists {
			return fmt.Errorf("orm: bulk UPDATE row %d repeats the primary key of row %d for %s", index, previous, plan.descriptor.Name())
		}
	}
	return nil
}

func (plan bulkMutationPlan) updateParametersPerRow() int {
	return (len(plan.primaryKey)+1)*len(plan.updateFields) + len(plan.primaryKey)
}

func (plan bulkMutationPlan) compileUpdateRange(start, end int, statement string) (compiledMutation, error) {
	rows := end - start
	keyCount, fieldCount := len(plan.primaryKey), len(plan.updateFields)
	argumentCount := rows * plan.updateParametersPerRow()
	if rows == 1 {
		argumentCount = keyCount + fieldCount
	}
	arguments := make([]any, argumentCount)
	for index := start; index < end; index++ {
		root := plan.updateRow(index)
		keyStart := argumentCount - rows*keyCount + (index-start)*keyCount
		keys := arguments[keyStart : keyStart+keyCount]
		if err := fillPrimaryKeyArguments(keys, root, plan.descriptor, plan.primaryKey, "bulk UPDATE"); err != nil {
			return compiledMutation{}, fmt.Errorf("orm: bulk UPDATE row %d: %w", index, err)
		}
		for fieldIndex, field := range plan.updateFields {
			argument, err := mutationArgument(root, plan.descriptor, field)
			if err != nil {
				return compiledMutation{}, fmt.Errorf("orm: bulk UPDATE row %d: %w", index, err)
			}
			position := fieldIndex
			if rows > 1 {
				position = (fieldIndex*rows + index - start) * (keyCount + 1)
				copy(arguments[position:position+keyCount], keys)
				position += keyCount
			}
			arguments[position] = argument
		}
	}
	if statement == "" {
		if rows == 1 {
			base := mutationPlanFor(plan.descriptor)
			// Default fields are borrowed from the immutable model plan. Reuse
			// its single-row SQL without a cache keyed by field selections.
			if len(plan.updateFields) == len(base.updateFields) && &plan.updateFields[0] == &base.updateFields[0] && plan.softDelete == base.softDelete {
				statement = base.updateSQL
			} else {
				statement = renderPrimaryKeyUpdate(plan.descriptor.TableName(), plan.updateFields, plan.primaryKey, plan.softDelete)
			}
		} else {
			statement = plan.renderUpdateMany(rows)
		}
	}
	return compiledMutation{modelName: plan.descriptor.Name(), descriptor: plan.descriptor, sql: statement, arguments: arguments}, nil
}

func (plan bulkMutationPlan) renderUpdateMany(rows int) string {
	var query strings.Builder
	query.Grow(plan.updateManySQLCapacity(rows))
	query.WriteString("UPDATE ")
	writeQuotedIdentifier(&query, plan.descriptor.TableName())
	query.WriteString(" SET ")
	for index, field := range plan.updateFields {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, field.field.ColumnName())
		query.WriteString(" = CASE ")
		if len(plan.primaryKey) == 1 {
			writeQuotedIdentifier(&query, plan.primaryKey[0].field.ColumnName())
			query.WriteByte(' ')
		}
		for range rows {
			query.WriteString("WHEN ")
			if len(plan.primaryKey) == 1 {
				query.WriteByte('?')
			} else {
				writePrimaryKeyPredicates(&query, plan.primaryKey)
			}
			query.WriteString(" THEN ? ")
		}
		query.WriteString("ELSE ")
		writeQuotedIdentifier(&query, field.field.ColumnName())
		query.WriteString(" END")
	}
	query.WriteString(" WHERE ")
	if len(plan.primaryKey) > 1 {
		query.WriteByte('(')
	}
	for index, key := range plan.primaryKey {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, key.field.ColumnName())
	}
	if len(plan.primaryKey) > 1 {
		query.WriteByte(')')
	}
	query.WriteString(" IN (")
	for row := range rows {
		if row != 0 {
			query.WriteString(", ")
		}
		if len(plan.primaryKey) > 1 {
			query.WriteByte('(')
		}
		for index := range plan.primaryKey {
			if index != 0 {
				query.WriteString(", ")
			}
			query.WriteByte('?')
		}
		if len(plan.primaryKey) > 1 {
			query.WriteByte(')')
		}
	}
	query.WriteByte(')')
	writeActiveMutationSoftDeletePredicate(&query, plan.softDelete)
	return query.String()
}

func (plan bulkMutationPlan) updateManySQLCapacity(rows int) int {
	keyWidth, predicateWidth := 0, 0
	for index, key := range plan.primaryKey {
		width := len(key.field.ColumnName()) + 2
		keyWidth += width
		predicateWidth += width + len(" = ?")
		if index != 0 {
			keyWidth += len(", ")
			predicateWidth += len(" AND ")
		}
	}
	capacity := len("UPDATE `` SET ") + len(plan.descriptor.TableName())
	for index, field := range plan.updateFields {
		if index != 0 {
			capacity += len(", ")
		}
		capacity += 2*(len(field.field.ColumnName())+2) + len(" = CASE ELSE  END")
		armWidth := predicateWidth
		if len(plan.primaryKey) == 1 {
			capacity += keyWidth + 1
			armWidth = 1
		}
		capacity += rows * (len("WHEN  THEN ? ") + armWidth)
	}
	keyRowWidth := len(plan.primaryKey)*3 - 2
	if len(plan.primaryKey) > 1 {
		keyWidth += 2
		keyRowWidth += 2
	}
	capacity += len(" WHERE  IN ()") + keyWidth + rows*keyRowWidth + (rows-1)*2
	if plan.softDelete != nil {
		capacity += len(" AND `` IS NULL") + len(plan.softDelete.field.ColumnName())
	}
	return capacity
}
