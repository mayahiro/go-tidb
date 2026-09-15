package orm

import (
	"context"
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

// AggregateExpression is an immutable model field or aggregate function.
// Fields use exported source Go names; As names an exported destination field.
type AggregateExpression struct {
	function string
	field    string
	alias    string
	aliased  bool
}

// Field selects a source Go field that must also occur in GroupBy.
// Its default output name is the source Go field name.
func Field(name string) AggregateExpression { return AggregateExpression{field: name} }

// CountAll counts input rows, including rows containing NULL values.
func CountAll() AggregateExpression { return AggregateExpression{function: "COUNT(*)"} }

// Count counts non-NULL values of a source Go field.
func Count(field string) AggregateExpression {
	return AggregateExpression{function: "COUNT", field: field}
}

// CountDistinct counts distinct non-NULL values of one source Go field.
func CountDistinct(field string) AggregateExpression {
	return AggregateExpression{function: "COUNT DISTINCT", field: field}
}

// Sum sums non-NULL values of a source Go field. Empty inputs produce SQL NULL.
// TiDB determines the result's SQL numeric type; use a Scanner for exact decimals.
func Sum(field string) AggregateExpression { return AggregateExpression{function: "SUM", field: field} }

// Avg averages non-NULL values of a source Go field. Empty inputs produce SQL NULL.
// TiDB determines the result's SQL numeric type; use a Scanner for exact decimals.
func Avg(field string) AggregateExpression { return AggregateExpression{function: "AVG", field: field} }

// Min selects the minimum non-NULL value. Empty inputs produce SQL NULL.
func Min(field string) AggregateExpression { return AggregateExpression{function: "MIN", field: field} }

// Max selects the maximum non-NULL value. Empty inputs produce SQL NULL.
func Max(field string) AggregateExpression { return AggregateExpression{function: "MAX", field: field} }

// As returns an expression with an explicit output Go field name.
// Aggregate functions require As, including when scanning a scalar slice.
func (e AggregateExpression) As(name string) AggregateExpression {
	e.alias, e.aliased = name, true
	return e
}

// AggregateQuery builds one read-only, single-table aggregate SELECT offline.
// It supports scalar predicates and soft deletion, but not relations, joins,
// preload, raw expressions, or window functions. Concurrent reads are safe after
// construction; concurrent mutation is not supported.
type AggregateQuery[T any] struct {
	expressions []AggregateExpression
	groupBy     []string
	predicates  []predicate
	having      []predicate
	orderBy     []orderTerm
	pagination  pagination
	withDeleted bool
	policy      ReadPolicy
}

// Aggregate starts an aggregate query over the application-owned source model T.
// T is a non-pointer struct; no primary key or database connection is required.
func Aggregate[T any]() *AggregateQuery[T] { return &AggregateQuery[T]{} }

// Select appends expressions in result-column order. At least one is required.
func (q *AggregateQuery[T]) Select(expressions ...AggregateExpression) *AggregateQuery[T] {
	if q != nil {
		q.expressions = append(q.expressions, expressions...)
	}
	return q
}

// GroupBy appends source Go field names in grouping order.
// Every selected non-aggregate field must be listed, even when a physical key
// could imply a functional dependency. GroupBy does not imply result ordering.
func (q *AggregateQuery[T]) GroupBy(fields ...string) *AggregateQuery[T] {
	if q != nil {
		q.groupBy = append(q.groupBy, fields...)
	}
	return q
}

// Where appends scalar source-model predicates joined by AND. Has is rejected.
func (q *AggregateQuery[T]) Where(predicates ...Predicate) *AggregateQuery[T] {
	if q != nil {
		for _, p := range predicates {
			q.predicates = append(q.predicates, p.value)
		}
	}
	return q
}

// Having filters groups using selected output Go names, not source column names.
// It accepts comparisons, In, NotIn, Between, IsNull, IsNotNull, And, Or, and Not.
// References compile to their expressions, avoiding alias/base-column ambiguity.
func (q *AggregateQuery[T]) Having(predicates ...Predicate) *AggregateQuery[T] {
	if q != nil {
		for _, p := range predicates {
			q.having = append(q.having, p.value)
		}
	}
	return q
}

// OrderBy appends Asc/Desc terms referencing selected output Go names.
func (q *AggregateQuery[T]) OrderBy(terms ...OrderTerm) *AggregateQuery[T] {
	if q != nil {
		for _, term := range terms {
			q.orderBy = append(q.orderBy, term.value)
		}
	}
	return q
}

// Limit sets the maximum number of result groups, replacing any previous limit.
func (q *AggregateQuery[T]) Limit(value int64) *AggregateQuery[T] {
	if q != nil {
		q.pagination.limit, q.pagination.limitSet = value, true
	}
	return q
}

// Offset skips result groups and requires Limit. Use OrderBy for stable paging.
func (q *AggregateQuery[T]) Offset(value int64) *AggregateQuery[T] {
	if q != nil {
		q.pagination.offset, q.pagination.offsetSet = value, true
	}
	return q
}

// WithDeleted includes soft-deleted source rows in the aggregation.
func (q *AggregateQuery[T]) WithDeleted() *AggregateQuery[T] {
	if q != nil {
		q.withDeleted = true
	}
	return q
}

// ReadFrom requests TiKV or TiFlash for this query's single source table.
// It generates an optimizer hint, not an execution guarantee. A later call wins.
func (q *AggregateQuery[T]) ReadFrom(engine StorageEngine) *AggregateQuery[T] {
	if q != nil {
		q.policy.Engine, q.policy.engineSet = engine, true
	}
	return q
}

// MPP requests cost-based or enforced MPP selection for this SQL statement.
// MPPEnforce does not guarantee MPP execution and conflicts with explicit TiKV.
func (q *AggregateQuery[T]) MPP(mode MPPMode) *AggregateQuery[T] {
	if q != nil {
		q.policy.MPP, q.policy.mppSet = mode, true
	}
	return q
}

// Build validates model, expressions, grouping, output references, and hints
// offline. It returns detached bind arguments without invoking driver.Valuer.
// SQL numeric coercion and value conversion are checked by TiDB/database/sql
// during execution, not inferred from the source model's Go representation.
func (q *AggregateQuery[T]) Build() (string, []any, error) {
	c, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return c.sql, c.arguments, nil
}

// ScanAll executes the aggregate SELECT into a non-nil pointer to a slice.
// Struct fields match exact output Go names; destination tags are ignored.
// Scalar elements require one output. SQL NULL and numeric overflow use
// database/sql conversion, including pointers and user-owned sql.Scanner types.
// Aggregate results do not apply the source's soft-delete NULL-to-zero rule.
// Mapping is checked before I/O; destination is replaced only after successful
// scanning and closing, with a non-nil empty slice when no groups are returned.
// No EXPLAIN, warning probe, replica query, or session SET is added implicitly.
func (q *AggregateQuery[T]) ScanAll(ctx context.Context, executor QueryExecutor, destination any) error {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return err
	}
	c, err := q.compile()
	if err != nil {
		return err
	}
	target := reflect.ValueOf(destination)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("orm: aggregate ScanAll destination must be a non-nil pointer to a slice")
	}
	columns := make([]scanProjectionColumn, len(c.outputs))
	for i, output := range c.outputs {
		columns[i].name = output.name
	}
	plan, scalar, err := compileProjectionScanPlan(target.Elem().Type(), columns)
	if err != nil {
		return err
	}
	collector := scanAllPlan{source: c.source, slice: target.Elem().Type(), scalar: scalar, target: plan}
	ctx = executorStatementContext(ctx, executor)
	metadata := statementRuntimeMetadata{source: runtimecapture.SourceTypedAggregate, terminal: "scan_all", model: c.source.Name()}
	rows, err := queryTextRowsWithMetadata(ctx, executor, c.source.Name(), c.sql, c.arguments, metadata)
	if err != nil {
		return err
	}
	values, err := collector.collect(rows)
	if err != nil {
		return err
	}
	target.Elem().Set(values)
	return nil
}

type aggregateOutput struct{ name, function, column string }
type compiledAggregate struct {
	source    *model.Descriptor
	sql       string
	arguments []any
	outputs   []aggregateOutput
}

const aggregateRootAlias = "a"

func (q *AggregateQuery[T]) compile() (compiledAggregate, error) {
	if q == nil {
		return compiledAggregate{}, fmt.Errorf("orm: compile a nil aggregate query")
	}
	modelType := reflect.TypeFor[T]()
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return compiledAggregate{}, fmt.Errorf("orm: aggregate source must be a non-pointer named struct")
	}
	d, err := model.Describe[T]()
	if err != nil {
		return compiledAggregate{}, fmt.Errorf("orm: aggregate model: %w", err)
	}
	if err := validateWithDeleted(d, q.withDeleted, "aggregate"); err != nil {
		return compiledAggregate{}, err
	}
	if err := validatePagination(q.pagination); err != nil {
		return compiledAggregate{}, err
	}
	if err := q.policy.validate(); err != nil {
		return compiledAggregate{}, err
	}
	if len(q.expressions) == 0 {
		return compiledAggregate{}, fmt.Errorf("orm: aggregate Select requires at least one expression")
	}
	if predicatesHaveRelation(q.predicates) {
		return compiledAggregate{}, fmt.Errorf("orm: aggregate Where does not support Has or relations")
	}
	groups := make([]string, len(q.groupBy))
	for i, name := range q.groupBy {
		field, err := aggregateSourceField(d, name)
		if err != nil {
			return compiledAggregate{}, err
		}
		for _, earlier := range q.groupBy[:i] {
			if earlier == name {
				return compiledAggregate{}, fmt.Errorf("orm: aggregate GroupBy repeats field %s", name)
			}
		}
		groups[i] = field.ColumnName()
	}
	outputs := make([]aggregateOutput, len(q.expressions))
	for i, expr := range q.expressions {
		name := expr.alias
		if !expr.aliased && expr.function == "" {
			name = expr.field
		}
		first, _ := utf8.DecodeRuneInString(name)
		if !token.IsIdentifier(name) || !unicode.IsUpper(first) || len(name) > 64 {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate output %q requires an exported Go name of at most 64 bytes; use As", name)
		}
		for _, earlier := range outputs[:i] {
			if strings.EqualFold(earlier.name, name) {
				return compiledAggregate{}, fmt.Errorf("orm: aggregate output name %q collides with %q", name, earlier.name)
			}
		}
		output := aggregateOutput{name: name, function: expr.function}
		if expr.function != "COUNT(*)" {
			field, err := aggregateSourceField(d, expr.field)
			if err != nil {
				return compiledAggregate{}, err
			}
			output.column = field.ColumnName()
		}
		if expr.function == "" {
			grouped := false
			for _, group := range q.groupBy {
				grouped = grouped || group == expr.field
			}
			if !grouped {
				return compiledAggregate{}, fmt.Errorf("orm: aggregate selected field %s must occur in GroupBy", expr.field)
			}
		}
		outputs[i] = output
	}
	argumentCount, capacity := predicateCompileCapacity(q.predicates)
	havingArgs, havingCapacity := predicateCompileCapacity(q.having)
	var sql strings.Builder
	sql.Grow(128 + capacity + havingCapacity + len(outputs)*64 + len(groups)*32 + len(q.orderBy)*64)
	sql.WriteString("SELECT ")
	q.policy.write(&sql)
	for i, output := range outputs {
		if i != 0 {
			sql.WriteString(", ")
		}
		output.write(&sql)
		sql.WriteString(" AS ")
		writeQuotedIdentifier(&sql, output.name)
	}
	sql.WriteString(" FROM ")
	writeQuotedIdentifier(&sql, d.TableName())
	sql.WriteString(" AS `a`")
	args := make([]any, 0, argumentCount+havingArgs+2)
	compiler := predicateCompiler{descriptor: d, query: &sql, arguments: args, qualifier: aggregateRootAlias}
	softDelete, active := activeSoftDeleteField(d, q.withDeleted)
	if active {
		sql.WriteString(" WHERE ")
		writeActiveSoftDeletePredicate(&sql, aggregateRootAlias, softDelete)
	}
	for i, predicate := range q.predicates {
		if i != 0 || active {
			sql.WriteString(" AND ")
		} else {
			sql.WriteString(" WHERE ")
		}
		if err := compiler.write(predicate); err != nil {
			return compiledAggregate{}, err
		}
	}
	for i, group := range groups {
		if i == 0 {
			sql.WriteString(" GROUP BY ")
		} else {
			sql.WriteString(", ")
		}
		writeQualifiedIdentifier(&sql, aggregateRootAlias, group)
	}
	having := aggregatePredicateCompiler{query: &sql, outputs: outputs, arguments: compiler.arguments}
	for i, p := range q.having {
		if i == 0 {
			sql.WriteString(" HAVING ")
		} else {
			sql.WriteString(" AND ")
		}
		if err := having.write(p); err != nil {
			return compiledAggregate{}, err
		}
	}
	for i, order := range q.orderBy {
		output, ok := aggregateOutputByName(outputs, order.field)
		if !ok {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate OrderBy references unknown output %q", order.field)
		}
		if order.direction != orderAscending && order.direction != orderDescending {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate OrderBy has invalid direction")
		}
		if i == 0 {
			sql.WriteString(" ORDER BY ")
		} else {
			sql.WriteString(", ")
		}
		output.write(&sql)
		if order.direction == orderAscending {
			sql.WriteString(" ASC")
		} else {
			sql.WriteString(" DESC")
		}
	}
	args = having.arguments
	if q.pagination.limitSet {
		sql.WriteString(" LIMIT ?")
		args = append(args, q.pagination.limit)
	}
	if q.pagination.offsetSet {
		sql.WriteString(" OFFSET ?")
		args = append(args, q.pagination.offset)
	}
	return compiledAggregate{source: d, sql: sql.String(), arguments: args, outputs: outputs}, nil
}

func aggregateSourceField(d *model.Descriptor, name string) (model.Field, error) {
	field, ok := d.FieldByGoName(name)
	if !ok || field.IsComputed() {
		return model.Field{}, fmt.Errorf("orm: aggregate field %s.%s is not a mapped base-table field", d.Name(), name)
	}
	return field, nil
}

func aggregateOutputByName(outputs []aggregateOutput, name string) (aggregateOutput, bool) {
	for _, output := range outputs {
		if output.name == name {
			return output, true
		}
	}
	return aggregateOutput{}, false
}

func (output aggregateOutput) write(sql *strings.Builder) {
	if output.function == "COUNT(*)" {
		sql.WriteString("COUNT(*)")
		return
	}
	if output.function == "COUNT DISTINCT" {
		sql.WriteString("COUNT(DISTINCT ")
	} else if output.function != "" {
		sql.WriteString(output.function)
		sql.WriteByte('(')
	}
	writeQualifiedIdentifier(sql, aggregateRootAlias, output.column)
	if output.function != "" {
		sql.WriteByte(')')
	}
}
