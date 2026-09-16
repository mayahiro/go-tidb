package orm

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/vector"
)

// VectorSearchMode chooses the result semantics independently of storage hints.
type VectorSearchMode uint8

const (
	// VectorExact evaluates eligible rows and breaks distance ties by the declared
	// primary key. The multi-column order excludes TiDB's single-order ANN path.
	VectorExact VectorSearchMode = iota
	// VectorApproximate permits TiDB's ANN index path. Recall and tied ordering
	// are not guaranteed. TiDB can still choose a full scan, especially with filters.
	VectorApproximate
)

// VectorQuery builds a Top-K vector search over model T. Where and the active
// soft-delete scope always filter before nearest-neighbor selection. No filter
// is silently moved after LIMIT to enable an ANN index. Exact is the default.
// Build is offline. Constructed queries support concurrent reads, not mutation.
type VectorQuery[T any] struct {
	field, distanceName string
	input               vector.Vector
	metric              vector.Metric
	limit               int64
	mode                VectorSearchMode
	projection          []string
	predicates          []predicate
	withDeleted         bool
	policy              ReadPolicy
}

// Nearest starts an exact Top-K search using an explicit metric and non-NULL,
// positive-dimensional query vector. Exact mode requires a declared primary key.
// The default projection is every mapped source field plus a Distance output.
// Select a narrow projection to avoid retrieving embeddings unnecessarily.
func Nearest[T any](field string, input vector.Vector, metric vector.Metric, limit int64) *VectorQuery[T] {
	return &VectorQuery[T]{field: field, input: input, metric: metric, limit: limit, distanceName: "Distance"}
}

// Select appends source Go fields; the distance output is always appended last.
func (q *VectorQuery[T]) Select(fields ...string) *VectorQuery[T] {
	if q != nil {
		if len(fields) == 0 && q.projection == nil {
			q.projection = []string{}
		}
		q.projection = append(q.projection, fields...)
	}
	return q
}

// DistanceAs names the appended distance output, defaulting to Distance.
// The name must be an exported Go identifier distinct from projected fields.
func (q *VectorQuery[T]) DistanceAs(name string) *VectorQuery[T] {
	if q != nil {
		q.distanceName = name
	}
	return q
}

// Mode explicitly selects exact or approximate search semantics.
func (q *VectorQuery[T]) Mode(mode VectorSearchMode) *VectorQuery[T] {
	if q != nil {
		q.mode = mode
	}
	return q
}

// Where appends scalar or Has predicates applied before vector Top-K selection.
func (q *VectorQuery[T]) Where(predicates ...Predicate) *VectorQuery[T] {
	if q != nil {
		for _, predicate := range predicates {
			q.predicates = append(q.predicates, predicate.value)
		}
	}
	return q
}

// WithDeleted includes soft-deleted source rows explicitly. Related Has targets
// retain their active scopes, as in ordinary SELECTs.
func (q *VectorQuery[T]) WithDeleted() *VectorQuery[T] {
	if q != nil {
		q.withDeleted = true
	}
	return q
}

// ReadFrom requests a storage engine for every table in this search.
// Storage hints do not change the selected exact/approximate search mode.
func (q *VectorQuery[T]) ReadFrom(engine StorageEngine) *VectorQuery[T] {
	if q != nil {
		q.policy.Engine, q.policy.engineSet = engine, true
	}
	return q
}

// MPP requests statement-level MPP selection without changing session settings.
func (q *VectorQuery[T]) MPP(mode MPPMode) *VectorQuery[T] {
	if q != nil {
		q.policy.MPP, q.policy.mppSet = mode, true
	}
	return q
}

// Build returns validated SQL and detached arguments without I/O or calling
// custom driver.Valuer methods. Physical vector dimensions require SchemaDiagnostics
// or server validation. A NULL distance can be scanned into a nullable Go value.
func (q *VectorQuery[T]) Build() (string, []any, error) {
	c, err := q.compileVector(nil)
	return c.sql, c.arguments, err
}

// ScanAll executes the search into a slice of structs matching selected Go names
// plus the distance output. It uses AggregateQuery.ScanAll's atomic destination,
// NULL, Scanner, and conversion rules. It adds no plan or capability probes.
func (q *VectorQuery[T]) ScanAll(ctx context.Context, executor QueryExecutor, destination any) error {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return err
	}
	c, err := q.compileVector(nil)
	if err != nil {
		return err
	}
	target := reflect.ValueOf(destination)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("orm: vector ScanAll destination must be a non-nil pointer to a slice")
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
	metadata := statementRuntimeMetadata{source: runtimecapture.SourceTypedVector, terminal: "scan_all", model: c.source.Name()}
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

func (q *VectorQuery[T]) compileVector(resolver *planAccessResolver) (compiledAggregate, error) {
	if q == nil {
		return compiledAggregate{}, fmt.Errorf("orm: compile a nil vector query")
	}
	if err := q.input.ValidateDimensions(q.input.Dimensions()); err != nil {
		return compiledAggregate{}, err
	}
	function, err := vectorDistanceFunction(q.metric)
	if err != nil {
		return compiledAggregate{}, err
	}
	if q.limit < 1 || q.limit > math.MaxUint32 {
		return compiledAggregate{}, fmt.Errorf("orm: vector Top-K must be between 1 and 4294967295")
	}
	if q.mode != VectorExact && q.mode != VectorApproximate {
		return compiledAggregate{}, fmt.Errorf("orm: invalid vector search mode")
	}
	if reflect.TypeFor[T]().Kind() != reflect.Struct {
		return compiledAggregate{}, fmt.Errorf("orm: vector source must be a non-pointer struct")
	}
	d, err := model.Describe[T]()
	if err != nil {
		return compiledAggregate{}, err
	}
	field, err := vectorSourceField(d, q.field)
	if err != nil {
		return compiledAggregate{}, err
	}
	if err := validateWithDeleted(d, q.withDeleted, "vector search"); err != nil {
		return compiledAggregate{}, err
	}
	if err := q.policy.validate(); err != nil {
		return compiledAggregate{}, err
	}
	fields, err := selectProjectionFields(d, q.projection)
	if err != nil {
		return compiledAggregate{}, err
	}
	if !validAggregateOutputName(q.distanceName) {
		return compiledAggregate{}, fmt.Errorf("orm: vector distance output requires an exported Go name of at most 64 bytes")
	}
	keys := d.PrimaryKeyFields()
	if q.mode == VectorExact && len(keys) == 0 {
		return compiledAggregate{}, fmt.Errorf("orm: exact vector search requires a declared primary key for deterministic distance ties")
	}
	outputs := make([]aggregateOutput, 0, len(fields)+1)
	var sql strings.Builder
	sql.Grow(192 + len(fields)*40 + len(q.predicates)*64)
	sql.WriteString("SELECT ")
	q.policy.writeTables(&sql, []string{"v"})
	for i, selected := range fields {
		if strings.EqualFold(selected.GoName(), q.distanceName) {
			return compiledAggregate{}, fmt.Errorf("orm: vector distance output collides with selected field %s; use DistanceAs", selected.GoName())
		}
		if i != 0 {
			sql.WriteString(", ")
		}
		writeQualifiedIdentifier(&sql, "v", selected.ColumnName())
		sql.WriteString(" AS ")
		writeQuotedIdentifier(&sql, selected.GoName())
		outputs = append(outputs, aggregateOutput{name: selected.GoName()})
	}
	if len(fields) != 0 {
		sql.WriteString(", ")
	}
	sql.WriteString(function)
	sql.WriteByte('(')
	writeQualifiedIdentifier(&sql, "v", field.ColumnName())
	sql.WriteString(", ?) AS ")
	writeQuotedIdentifier(&sql, q.distanceName)
	outputs = append(outputs, aggregateOutput{name: q.distanceName})
	sql.WriteString(" FROM ")
	writeAliasedRelationTable(&sql, d.TableName(), "v")
	if resolver != nil {
		*resolver = planAccessResolver{hasRoot: true, root: planAccessBinding{alias: "v", physicalTable: d.TableName(), model: d.Name()}}
	}
	compiler := predicateCompiler{descriptor: d, query: &sql, arguments: []any{q.input}, qualifier: "v", relationEngine: q.policy.Engine, planAccess: resolver}
	softDelete, active := activeSoftDeleteField(d, q.withDeleted)
	if active {
		sql.WriteString(" WHERE ")
		writeActiveSoftDeletePredicate(&sql, "v", softDelete)
	}
	for i, predicate := range q.predicates {
		if active || i != 0 {
			sql.WriteString(" AND ")
		} else {
			sql.WriteString(" WHERE ")
		}
		if err := compiler.write(predicate); err != nil {
			return compiledAggregate{}, err
		}
	}
	sql.WriteString(" ORDER BY ")
	writeQuotedIdentifier(&sql, q.distanceName)
	sql.WriteString(" ASC")
	if q.mode == VectorExact {
		for _, key := range keys {
			sql.WriteString(", ")
			writeQualifiedIdentifier(&sql, "v", key.ColumnName())
			sql.WriteString(" ASC")
		}
	}
	sql.WriteString(" LIMIT ?")
	compiler.arguments = append(compiler.arguments, q.limit)
	return compiledAggregate{source: d, sql: sql.String(), arguments: compiler.arguments, outputs: outputs}, nil
}

func vectorDistanceFunction(metric vector.Metric) (string, error) {
	switch metric {
	case vector.L2:
		return "VEC_L2_DISTANCE", nil
	case vector.Cosine:
		return "VEC_COSINE_DISTANCE", nil
	default:
		return "", fmt.Errorf("orm: vector metric requires vector.L2 or vector.Cosine")
	}
}

func vectorSourceField(source *model.Descriptor, name string) (model.Field, error) {
	field, ok := source.FieldByGoName(name)
	if !ok || field.IsComputed() || field.BaseType() != reflect.TypeFor[vector.Vector]() {
		return model.Field{}, fmt.Errorf("orm: vector field %s.%s must be a mapped vector.Vector or pointer", source.Name(), name)
	}
	return field, nil
}

// BuildVectorIndex returns offline CREATE VECTOR INDEX SQL for one model field
// and metric. It executes no DDL. Provision the table's TiFlash replicas and a
// matching fixed-dimensional VECTOR column before executing the returned SQL.
func BuildVectorIndex[T any](fieldName, indexName string, metric vector.Metric) (string, error) {
	if !modelmeta.ValidSQLIdentifier(indexName) || len(indexName) > 64 {
		return "", fmt.Errorf("orm: vector index name must be a valid identifier of at most 64 bytes")
	}
	d, err := model.Describe[T]()
	if err != nil {
		return "", err
	}
	field, err := vectorSourceField(d, fieldName)
	if err != nil {
		return "", err
	}
	function, err := vectorDistanceFunction(metric)
	if err != nil {
		return "", err
	}
	return "CREATE VECTOR INDEX `" + indexName + "` ON `" + d.TableName() + "` ((" + function + "(`" + field.ColumnName() + "`))) USING HNSW", nil
}
