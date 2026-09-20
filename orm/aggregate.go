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

// AggregateExpression is an immutable model field, calendar key, or aggregate function.
// Fields use exported Go names, optionally prefixed by a to-one relation path;
// As names an exported destination field.
type AggregateExpression struct {
	function  string
	field     string
	alias     string
	aliased   bool
	condition *predicate
}

// Field selects a Go field whose output must also occur in GroupBy. A dotted
// path traverses to-one relations with declared unique target keys using LEFT
// JOINs. Missing or soft-deleted targets produce NULL. Collection paths are
// rejected to preserve one input row per source. Related fields require As.
// A source field's default output name is its Go field name.
func Field(name string) AggregateExpression { return AggregateExpression{field: name} }

// Date extracts the calendar date of a source Go field as SQL DATE. Use As to
// name its output and GroupBy that name. NULL remains NULL. TIMESTAMP follows
// the session time zone; DATETIME retains its stored calendar fields. The ORM
// does not change time zones or infer the physical column type from the Go type.
// With go-sql-driver/mysql, parseTime=true returns time.Time in the driver's loc;
// use sql.NullTime or *time.Time for nullable results.
func Date(field string) AggregateExpression {
	return AggregateExpression{function: "DATE", field: field}
}

// YearMonth extracts a source Go field's calendar year and month as SQL integer
// year*100+month, for example 202609. It includes the year, not just month number.
// Use As to name its output and GroupBy that name. NULL remains NULL; scan into
// int64 or sql.NullInt64. It follows Date's session and physical-type semantics.
func YearMonth(field string) AggregateExpression {
	return AggregateExpression{function: "YEAR_MONTH", field: field}
}

// CountAll counts input rows, including rows containing NULL values.
func CountAll() AggregateExpression { return AggregateExpression{function: "COUNT(*)"} }

// CountIf counts input rows for which condition is SQL TRUE. FALSE and NULL
// conditions do not count. Empty inputs and groups without matches return zero.
// The condition uses source Go fields and existing scalar predicates, including
// And, Or, Not, and Has. Has tests existence without multiplying input rows.
// Use As to name the output.
func CountIf(condition Predicate) AggregateExpression {
	return AggregateExpression{function: "COUNT(*)", condition: &condition.value}
}

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

// SumIf sums non-NULL field values in rows for which condition is SQL TRUE.
// It returns SQL NULL when no matching non-NULL value exists, including empty
// inputs. Field and condition use source Go names; condition supports the same
// predicates as CountIf, including Has. Use As and a nullable destination when needed.
// TiDB determines the SQL numeric type, as for Sum.
func SumIf(field string, condition Predicate) AggregateExpression {
	return AggregateExpression{function: "SUM", field: field, condition: &condition.value}
}

// Avg averages non-NULL values of a source Go field. Empty inputs produce SQL NULL.
// TiDB determines the result's SQL numeric type; use a Scanner for exact decimals.
func Avg(field string) AggregateExpression { return AggregateExpression{function: "AVG", field: field} }

// Min selects the minimum non-NULL value. Empty inputs produce SQL NULL.
func Min(field string) AggregateExpression { return AggregateExpression{function: "MIN", field: field} }

// Max selects the maximum non-NULL value. Empty inputs produce SQL NULL.
func Max(field string) AggregateExpression { return AggregateExpression{function: "MAX", field: field} }

// As returns an expression with an explicit output Go field name.
// Calendar keys and aggregate functions require As, including scalar slices.
func (e AggregateExpression) As(name string) AggregateExpression {
	e.alias, e.aliased = name, true
	return e
}

// AggregateQuery builds one read-only aggregate SELECT over source-model rows.
// Where supports scalar and relation-existence predicates and soft deletion.
// Output field paths can traverse to-one relations with declared unique keys.
// Preload and raw expressions are not exposed. Concurrent reads are safe after
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
	windows     []WindowExpression
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

// GroupBy appends selected output Go names in grouping order, as Having and
// OrderBy do. Outputs must be Field, Date, or YearMonth expressions, not aggregate
// functions. Every non-aggregate expression must be grouped; equivalent selected
// expressions can share one group key. No physical functional dependency is
// inferred. Duplicate group expressions are rejected. GroupBy does not order rows.
func (q *AggregateQuery[T]) GroupBy(outputs ...string) *AggregateQuery[T] {
	if q != nil {
		q.groupBy = append(q.groupBy, outputs...)
	}
	return q
}

// Where appends source-model predicates joined by AND. Has filters by related
// row existence, including nested relations, without multiplying source rows.
// Related targets and via edges retain their active soft-delete scopes.
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

// WithDeleted includes soft-deleted source rows in the aggregation. Related
// targets and via edges in Has retain their active soft-delete scopes.
func (q *AggregateQuery[T]) WithDeleted() *AggregateQuery[T] {
	if q != nil {
		q.withDeleted = true
	}
	return q
}

// ReadFrom requests TiKV or TiFlash for the source and every related target and
// junction table, using aliases in their respective query blocks.
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

type aggregateOutput struct {
	name, function, column string
	qualifier              string
	condition              *predicate
}
type compiledAggregate struct {
	source    *model.Descriptor
	sql       string
	arguments []any
	outputs   []aggregateOutput
}

const aggregateRootAlias = "a"

func (q *AggregateQuery[T]) compile() (compiledAggregate, error) {
	return q.compileWithResolver(nil)
}

func (q *AggregateQuery[T]) compileWithResolver(resolver *planAccessResolver) (compiledAggregate, error) {
	if q == nil {
		return compiledAggregate{}, fmt.Errorf("orm: compile a nil aggregate query")
	}
	if len(q.windows) != 0 {
		return q.compileWindows(resolver)
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
	outputs := make([]aggregateOutput, len(q.expressions))
	var joins aggregateJoins
	conditionalArgs, conditionalCapacity := 0, 0
	for i, expr := range q.expressions {
		name := expr.alias
		if !expr.aliased && expr.function == "" {
			name = expr.field
		}
		if !validAggregateOutputName(name) {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate output %q requires an exported Go name of at most 64 bytes; use As", name)
		}
		for _, earlier := range outputs[:i] {
			if strings.EqualFold(earlier.name, name) {
				return compiledAggregate{}, fmt.Errorf("orm: aggregate output name %q collides with %q", name, earlier.name)
			}
		}
		output := aggregateOutput{name: name, function: expr.function, condition: expr.condition, qualifier: aggregateRootAlias}
		if expr.condition != nil {
			conditions := []predicate{*expr.condition}
			args, capacity := predicateCompileCapacity(conditions)
			if predicatesHaveRelation(conditions) {
				capacity += relationPredicateExtraSQLCapacity(d, conditions)
			}
			conditionalArgs += args
			conditionalCapacity += capacity
		}
		if expr.function != "COUNT(*)" {
			field, qualifier, err := joins.resolve(d, expr.field)
			if err != nil {
				return compiledAggregate{}, err
			}
			output.column = field.ColumnName()
			output.qualifier = qualifier
		}
		outputs[i] = output
	}
	groups := make([]int, len(q.groupBy))
	for i, name := range q.groupBy {
		index := -1
		for j, output := range outputs {
			if output.name == name {
				index = j
				break
			}
		}
		if index < 0 {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate GroupBy references unknown output %q", name)
		}
		if !outputs[index].isGroupKey() {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate GroupBy output %s is an aggregate function", name)
		}
		for _, earlier := range groups[:i] {
			if outputs[index].sameExpression(outputs[earlier]) {
				return compiledAggregate{}, fmt.Errorf("orm: aggregate GroupBy repeats expression for output %s", name)
			}
		}
		groups[i] = index
	}
	for _, output := range outputs {
		if !output.isGroupKey() {
			continue
		}
		grouped := false
		for _, group := range groups {
			grouped = grouped || output.sameExpression(outputs[group])
		}
		if !grouped {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate selected output %s must occur in GroupBy", output.name)
		}
	}
	argumentCount, capacity := predicateCompileCapacity(q.predicates)
	if predicatesHaveRelation(q.predicates) {
		capacity += relationPredicateExtraSQLCapacity(d, q.predicates)
	}
	havingArgs, havingCapacity := predicateCompileCapacity(q.having)
	var sql strings.Builder
	sql.Grow(128 + capacity + havingCapacity + conditionalCapacity + len(outputs)*64 + len(groups)*32 + len(q.orderBy)*64)
	args := make([]any, 0, argumentCount+havingArgs+conditionalArgs+2)
	if resolver != nil {
		*resolver = planAccessResolver{hasRoot: true, root: planAccessBinding{alias: aggregateRootAlias, physicalTable: d.TableName(), model: d.Name()}}
		for _, join := range joins {
			resolver.add(planAccessBinding{alias: join.alias, physicalTable: join.plan.target.TableName(), model: join.plan.target.Name(), relationPath: join.path})
		}
	}
	compiler := predicateCompiler{descriptor: d, query: &sql, arguments: args, qualifier: aggregateRootAlias, relationEngine: q.policy.Engine, planAccess: resolver}
	sql.WriteString("SELECT ")
	joins.writePolicy(&sql, q.policy)
	for i, output := range outputs {
		if i != 0 {
			sql.WriteString(", ")
		}
		if err := output.write(&compiler); err != nil {
			return compiledAggregate{}, err
		}
		sql.WriteString(" AS ")
		writeQuotedIdentifier(&sql, output.name)
	}
	sql.WriteString(" FROM ")
	writeQuotedIdentifier(&sql, d.TableName())
	sql.WriteString(" AS `a`")
	joins.write(&sql)
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
		if err := outputs[group].write(&compiler); err != nil {
			return compiledAggregate{}, err
		}
	}
	having := aggregatePredicateCompiler{predicateCompiler: &compiler, outputs: outputs}
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
		if err := output.write(&compiler); err != nil {
			return compiledAggregate{}, err
		}
		if order.direction == orderAscending {
			sql.WriteString(" ASC")
		} else {
			sql.WriteString(" DESC")
		}
	}
	args = compiler.arguments
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

func validAggregateOutputName(name string) bool {
	first, _ := utf8.DecodeRuneInString(name)
	return token.IsIdentifier(name) && unicode.IsUpper(first) && len(name) <= 64
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

func (output aggregateOutput) isGroupKey() bool {
	return output.function == "" || output.function == "DATE" || output.function == "YEAR_MONTH"
}

func (output aggregateOutput) sameExpression(other aggregateOutput) bool {
	return output.function == other.function && output.column == other.column && output.qualifier == other.qualifier
}

func (output aggregateOutput) write(compiler *predicateCompiler) error {
	sql := compiler.query
	if output.condition != nil {
		if output.function == "COUNT(*)" {
			sql.WriteString("COUNT(CASE WHEN ")
		} else {
			sql.WriteString("SUM(CASE WHEN ")
		}
		// An existence value inside CASE must retain unmatched source rows, so
		// the WHERE-only semi-join rewrite hint does not apply here.
		previousConditional := compiler.conditional
		compiler.conditional = true
		err := compiler.write(*output.condition)
		compiler.conditional = previousConditional
		if err != nil {
			return fmt.Errorf("orm: conditional aggregate %s: %w", output.name, err)
		}
		sql.WriteString(" THEN ")
		if output.function == "COUNT(*)" {
			sql.WriteByte('1')
		} else {
			writeQualifiedIdentifier(sql, output.qualifier, output.column)
		}
		sql.WriteString(" END)")
		return nil
	}
	if output.function == "COUNT(*)" {
		sql.WriteString("COUNT(*)")
		return nil
	}
	if output.function == "YEAR_MONTH" {
		sql.WriteString("EXTRACT(YEAR_MONTH FROM ")
	} else if output.function == "COUNT DISTINCT" {
		sql.WriteString("COUNT(DISTINCT ")
	} else if output.function != "" {
		sql.WriteString(output.function)
		sql.WriteByte('(')
	}
	writeQualifiedIdentifier(sql, output.qualifier, output.column)
	if output.function != "" {
		sql.WriteByte(')')
	}
	return nil
}
