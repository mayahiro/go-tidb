package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

// RelationKey holds the ordered components of one composite relation key.
//
// Use CompositeKey to construct a value. Single-column relations use their
// scalar key value directly instead.
type RelationKey struct {
	values []any
}

// CompositeKey constructs one ordered key for a relation whose source or
// target mapping contains multiple columns.
func CompositeKey(values ...any) RelationKey {
	return RelationKey{values: append([]any(nil), values...)}
}

// RelationAddQuery builds and executes one pure many-to-many junction INSERT.
type RelationAddQuery[T any] struct {
	relation       string
	source         any
	targets        []any
	ignoreExisting bool
}

// AddRelation starts one multi-row junction INSERT without performing
// database I/O.
//
// T is the source model, relation is its exported Go relation field name, and
// source and targets are relation-key values. Use CompositeKey for a mapping
// with multiple key columns. An empty target slice is a no-op.
func AddRelation[T, SourceKey, TargetKey any](relation string, source SourceKey, targets ...TargetKey) *RelationAddQuery[T] {
	return &RelationAddQuery[T]{
		relation: relation,
		source:   source,
		targets:  relationTargetValues(targets),
	}
}

// IgnoreExisting changes the INSERT to keep an existing duplicate junction
// key through a no-op ON DUPLICATE KEY UPDATE clause.
//
// Without this option, TiDB reports a duplicate-key error.
func (q *RelationAddQuery[T]) IgnoreExisting() *RelationAddQuery[T] {
	if q == nil {
		return nil
	}
	q.ignoreExisting = true
	return q
}

// Build compiles the junction INSERT and returns SQL plus bind arguments
// without accessing a database or executing custom driver.Valuer methods.
func (q *RelationAddQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the junction INSERT through an explicit database/sql executor
// and returns the affected row count.
func (q *RelationAddQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	if compiled.noOp {
		return 0, nil
	}
	return executeRelationMutation(ctx, executor, compiled, "INSERT")
}

func (q *RelationAddQuery[T]) compile() (compiledRelationMutation, error) {
	if q == nil {
		return compiledRelationMutation{}, fmt.Errorf("orm: compile a nil relation INSERT query")
	}
	plan, err := relationMutationPlanFor(reflect.TypeFor[T](), q.relation)
	if err != nil {
		return compiledRelationMutation{}, err
	}
	source, err := appendRelationKeyArguments(make([]any, 0, len(plan.sourceColumns)), q.source, len(plan.sourceColumns), plan.path, "source", 0)
	if err != nil {
		return compiledRelationMutation{}, err
	}
	if len(q.targets) == 0 {
		return compiledRelationMutation{path: plan.path, noOp: true}, nil
	}
	parametersPerRow := len(plan.sourceColumns) + len(plan.targetColumns)
	if len(q.targets) > maxMutationParameters/parametersPerRow {
		return compiledRelationMutation{}, relationParameterLimitError(plan.path, len(q.targets)*parametersPerRow)
	}
	arguments := make([]any, 0, len(q.targets)*parametersPerRow)
	for index, target := range q.targets {
		arguments = append(arguments, source...)
		var targetErr error
		arguments, targetErr = appendRelationKeyArguments(arguments, target, len(plan.targetColumns), plan.path, "target", index+1)
		if targetErr != nil {
			return compiledRelationMutation{}, targetErr
		}
	}
	return compiledRelationMutation{
		path:      plan.path,
		sql:       renderRelationInsert(plan, len(q.targets), q.ignoreExisting),
		arguments: arguments,
	}, nil
}

// RelationDeleteQuery builds and executes one pure many-to-many junction
// DELETE.
type RelationDeleteQuery[T any] struct {
	relation string
	source   any
	targets  []any
	clear    bool
}

// RemoveRelation starts one junction DELETE for source-target pairs without
// performing database I/O.
//
// T is the source model, relation is its exported Go relation field name, and
// source and targets are relation-key values. Use CompositeKey for a mapping
// with multiple key columns. An empty target slice is a no-op.
func RemoveRelation[T, SourceKey, TargetKey any](relation string, source SourceKey, targets ...TargetKey) *RelationDeleteQuery[T] {
	return &RelationDeleteQuery[T]{
		relation: relation,
		source:   source,
		targets:  relationTargetValues(targets),
	}
}

// ClearRelation starts one junction DELETE for every target associated with a
// source key without performing database I/O.
func ClearRelation[T, SourceKey any](relation string, source SourceKey) *RelationDeleteQuery[T] {
	return &RelationDeleteQuery[T]{relation: relation, source: source, clear: true}
}

// Build compiles the junction DELETE and returns SQL plus bind arguments
// without accessing a database or executing custom driver.Valuer methods.
func (q *RelationDeleteQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.sql, compiled.arguments, nil
}

// Exec executes the junction DELETE through an explicit database/sql executor
// and returns the affected row count.
func (q *RelationDeleteQuery[T]) Exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if err := validateMutationExecution(ctx, executor); err != nil {
		return 0, err
	}
	compiled, err := q.compile()
	if err != nil {
		return 0, err
	}
	if compiled.noOp {
		return 0, nil
	}
	return executeRelationMutation(ctx, executor, compiled, "DELETE")
}

func (q *RelationDeleteQuery[T]) compile() (compiledRelationMutation, error) {
	if q == nil {
		return compiledRelationMutation{}, fmt.Errorf("orm: compile a nil relation DELETE query")
	}
	plan, err := relationMutationPlanFor(reflect.TypeFor[T](), q.relation)
	if err != nil {
		return compiledRelationMutation{}, err
	}
	if q.clear {
		arguments, err := appendRelationKeyArguments(make([]any, 0, len(plan.sourceColumns)), q.source, len(plan.sourceColumns), plan.path, "source", 0)
		if err != nil {
			return compiledRelationMutation{}, err
		}
		return compiledRelationMutation{
			path:      plan.path,
			sql:       renderRelationClear(plan),
			arguments: arguments,
		}, nil
	}
	if len(q.targets) == 0 {
		if _, err := appendRelationKeyArguments(nil, q.source, len(plan.sourceColumns), plan.path, "source", 0); err != nil {
			return compiledRelationMutation{}, err
		}
		return compiledRelationMutation{path: plan.path, noOp: true}, nil
	}
	parameterCount := len(plan.sourceColumns) + len(q.targets)*len(plan.targetColumns)
	if parameterCount > maxMutationParameters {
		return compiledRelationMutation{}, relationParameterLimitError(plan.path, parameterCount)
	}
	arguments := make([]any, 0, parameterCount)
	arguments, err = appendRelationKeyArguments(arguments, q.source, len(plan.sourceColumns), plan.path, "source", 0)
	if err != nil {
		return compiledRelationMutation{}, err
	}
	for index, target := range q.targets {
		var targetErr error
		arguments, targetErr = appendRelationKeyArguments(arguments, target, len(plan.targetColumns), plan.path, "target", index+1)
		if targetErr != nil {
			return compiledRelationMutation{}, targetErr
		}
	}
	return compiledRelationMutation{
		path:      plan.path,
		sql:       renderRelationRemove(plan, len(q.targets)),
		arguments: arguments,
	}, nil
}

type compiledRelationMutation struct {
	path      string
	sql       string
	arguments []any
	noOp      bool
}

type relationMutationPlan struct {
	path          string
	table         string
	sourceColumns []string
	targetColumns []string
}

type relationMutationPlanKey struct {
	sourceType reflect.Type
	relation   string
}

type relationMutationPlanResult struct {
	plan *relationMutationPlan
	err  error
}

var relationMutationPlanCache sync.Map

func relationMutationPlanFor(sourceType reflect.Type, relationName string) (*relationMutationPlan, error) {
	key := relationMutationPlanKey{sourceType: sourceType, relation: relationName}
	if cached, ok := relationMutationPlanCache.Load(key); ok {
		result := cached.(relationMutationPlanResult)
		return result.plan, result.err
	}
	plan, err := compileRelationMutationPlan(sourceType, relationName)
	result, _ := relationMutationPlanCache.LoadOrStore(key, relationMutationPlanResult{plan: plan, err: err})
	stored := result.(relationMutationPlanResult)
	return stored.plan, stored.err
}

func compileRelationMutationPlan(sourceType reflect.Type, relationName string) (*relationMutationPlan, error) {
	if sourceType == nil || sourceType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: relation mutation model must be a non-pointer struct, got %v", sourceType)
	}
	descriptor, err := model.DescribeType(sourceType)
	if err != nil {
		return nil, fmt.Errorf("orm: describe relation mutation model: %w", err)
	}
	relation, exists := descriptor.RelationByName(relationName)
	if !exists {
		return nil, fmt.Errorf("orm: relation mutation field %s.%s is not a mapped relation", descriptor.Name(), relationName)
	}
	path := descriptor.Name() + "." + relation.GoName()
	if relation.Kind() != model.RelationManyToMany {
		return nil, fmt.Errorf("orm: relation mutation %s must be a pure many-to-many relation", path)
	}
	junction, exists := relation.Junction()
	if !exists {
		return nil, fmt.Errorf("orm: relation mutation %s has no junction metadata", path)
	}
	sourceFields := relation.SourceKey()
	targetFields := relation.TargetKey()
	sourceColumns := junction.SourceColumns()
	targetColumns := junction.TargetColumns()
	if len(sourceFields) == 0 || len(sourceFields) != len(sourceColumns) || len(targetFields) == 0 || len(targetFields) != len(targetColumns) {
		return nil, fmt.Errorf("orm: relation mutation %s has invalid junction key metadata", path)
	}
	for _, field := range sourceFields {
		if !field.CanValue() {
			return nil, fmt.Errorf("orm: relation mutation source field %s.%s cannot be used as a database argument", descriptor.Name(), field.GoName())
		}
	}
	for _, field := range targetFields {
		if !field.CanValue() {
			return nil, fmt.Errorf("orm: relation mutation target field %s.%s cannot be used as a database argument", relation.TargetType().Name(), field.GoName())
		}
	}
	return &relationMutationPlan{
		path:          path,
		table:         junction.TableName(),
		sourceColumns: sourceColumns,
		targetColumns: targetColumns,
	}, nil
}

func relationTargetValues[T any](targets []T) []any {
	if len(targets) == 0 {
		return nil
	}
	values := make([]any, len(targets))
	for index := range targets {
		values[index] = targets[index]
	}
	return values
}

func appendRelationKeyArguments(arguments []any, value any, width int, path, role string, position int) ([]any, error) {
	if width == 1 {
		if _, composite := value.(RelationKey); composite {
			return nil, fmt.Errorf("orm: relation key %s has one column and requires a scalar value", relationKeyLabel(path, role, position))
		}
		if nilPredicateArgument(value) {
			return nil, fmt.Errorf("orm: relation key %s must not be nil", relationKeyLabel(path, role, position))
		}
		return append(arguments, value), nil
	}
	key, composite := value.(RelationKey)
	if !composite {
		return nil, fmt.Errorf("orm: relation key %s has %d columns and requires CompositeKey", relationKeyLabel(path, role, position), width)
	}
	if len(key.values) != width {
		return nil, fmt.Errorf("orm: relation key %s requires %d components, got %d", relationKeyLabel(path, role, position), width, len(key.values))
	}
	for index, component := range key.values {
		if nilPredicateArgument(component) {
			return nil, fmt.Errorf("orm: relation key %s component %d must not be nil", relationKeyLabel(path, role, position), index+1)
		}
	}
	return append(arguments, key.values...), nil
}

func relationKeyLabel(path, role string, position int) string {
	if position == 0 {
		return path + " " + role
	}
	return fmt.Sprintf("%s %s %d", path, role, position)
}

func renderRelationInsert(plan *relationMutationPlan, rows int, ignoreExisting bool) string {
	var query strings.Builder
	query.Grow(relationInsertSQLCapacity(plan, rows, ignoreExisting))
	query.WriteString("INSERT INTO ")
	writeQuotedIdentifier(&query, plan.table)
	query.WriteString(" (")
	writeRelationColumns(&query, plan.sourceColumns, 0)
	writeRelationColumns(&query, plan.targetColumns, len(plan.sourceColumns))
	query.WriteString(") VALUES ")
	columnCount := len(plan.sourceColumns) + len(plan.targetColumns)
	for row := range rows {
		if row != 0 {
			query.WriteString(", ")
		}
		query.WriteByte('(')
		for column := range columnCount {
			if column != 0 {
				query.WriteString(", ")
			}
			query.WriteByte('?')
		}
		query.WriteByte(')')
	}
	if ignoreExisting {
		query.WriteString(" ON DUPLICATE KEY UPDATE ")
		writeQuotedIdentifier(&query, plan.sourceColumns[0])
		query.WriteString(" = ")
		writeQuotedIdentifier(&query, plan.sourceColumns[0])
	}
	return query.String()
}

func renderRelationRemove(plan *relationMutationPlan, targets int) string {
	var query strings.Builder
	query.Grow(relationDeleteSQLCapacity(plan, targets))
	query.WriteString("DELETE FROM ")
	writeQuotedIdentifier(&query, plan.table)
	query.WriteString(" WHERE ")
	writeRelationEqualities(&query, plan.sourceColumns)
	query.WriteString(" AND ")
	if len(plan.targetColumns) == 1 {
		writeQuotedIdentifier(&query, plan.targetColumns[0])
		query.WriteString(" IN (")
		for index := range targets {
			if index != 0 {
				query.WriteString(", ")
			}
			query.WriteByte('?')
		}
		query.WriteByte(')')
		return query.String()
	}
	query.WriteByte('(')
	for target := range targets {
		if target != 0 {
			query.WriteString(" OR ")
		}
		query.WriteByte('(')
		writeRelationEqualities(&query, plan.targetColumns)
		query.WriteByte(')')
	}
	query.WriteByte(')')
	return query.String()
}

func renderRelationClear(plan *relationMutationPlan) string {
	var query strings.Builder
	query.Grow(relationDeleteSQLCapacity(plan, 0))
	query.WriteString("DELETE FROM ")
	writeQuotedIdentifier(&query, plan.table)
	query.WriteString(" WHERE ")
	writeRelationEqualities(&query, plan.sourceColumns)
	return query.String()
}

func writeRelationColumns(query *strings.Builder, columns []string, preceding int) {
	for index, column := range columns {
		if preceding+index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(query, column)
	}
}

func writeRelationEqualities(query *strings.Builder, columns []string) {
	for index, column := range columns {
		if index != 0 {
			query.WriteString(" AND ")
		}
		writeQuotedIdentifier(query, column)
		query.WriteString(" = ?")
	}
}

func relationInsertSQLCapacity(plan *relationMutationPlan, rows int, ignoreExisting bool) int {
	capacity := len("INSERT INTO  () VALUES ") + len(plan.table) + len("``")
	if ignoreExisting {
		capacity += len(" ON DUPLICATE KEY UPDATE  = ") + len(plan.sourceColumns[0])*2 + len("````")
	}
	columnCount := len(plan.sourceColumns) + len(plan.targetColumns)
	for _, column := range plan.sourceColumns {
		capacity += len(column) + len("``") + len(", ")
	}
	for _, column := range plan.targetColumns {
		capacity += len(column) + len("``") + len(", ")
	}
	rowLength := len("()") + columnCount + (columnCount-1)*len(", ")
	capacity += rows*rowLength + (rows-1)*len(", ")
	return capacity
}

func relationDeleteSQLCapacity(plan *relationMutationPlan, targets int) int {
	capacity := len("DELETE FROM  WHERE ") + len(plan.table) + len("``")
	for index, column := range plan.sourceColumns {
		capacity += len(column) + len("`` = ?")
		if index != 0 {
			capacity += len(" AND ")
		}
	}
	if targets == 0 {
		return capacity
	}
	capacity += len(" AND ")
	if len(plan.targetColumns) == 1 {
		capacity += len(plan.targetColumns[0]) + len("`` IN ()") + targets
		if targets > 1 {
			capacity += (targets - 1) * len(", ")
		}
		return capacity
	}
	capacity += len("()")
	for target := range targets {
		if target != 0 {
			capacity += len(" OR ")
		}
		capacity += len("()")
		for index, column := range plan.targetColumns {
			capacity += len(column) + len("`` = ?")
			if index != 0 {
				capacity += len(" AND ")
			}
		}
	}
	return capacity
}

func relationParameterLimitError(path string, count int) error {
	return fmt.Errorf("orm: relation mutation %s requires %d parameters, exceeding TiDB's %d-placeholder statement limit", path, count, maxMutationParameters)
}

func executeRelationMutation(ctx context.Context, executor ExecExecutor, compiled compiledRelationMutation, operation string) (int64, error) {
	terminal := "relation_insert"
	if operation == "DELETE" {
		terminal = "relation_delete"
	}
	observation := beginRelationMutationStatementObservation(ctx, inferStatementOperation(compiled.sql), compiled.sql, compiled.arguments, compiled.path, terminal)
	result, err := executor.ExecContext(ctx, compiled.sql, compiled.arguments...)
	if err != nil {
		err = fmt.Errorf("orm: execute relation %s for %s: %w", operation, compiled.path, err)
		observation.finish(0, false, err)
		return 0, err
	}
	if nilPredicateArgument(result) {
		err = fmt.Errorf("orm: execute relation %s for %s: executor returned a nil result", operation, compiled.path)
		observation.finish(0, false, err)
		return 0, err
	}
	return finishMutationStatementObservation(observation, result, "relation "+operation, compiled.path)
}
