package orm

import (
	"fmt"
	"reflect"
)

// SelectQuery builds and executes a scalar SELECT for an application-owned
// model struct.
//
// A SelectQuery performs no I/O until a connected terminal such as All is
// called and is not safe for concurrent mutation.
type SelectQuery[T any] struct {
	selection selectQuery
}

// Query starts a scalar SELECT for T without accessing a database.
//
// T must be a non-pointer named struct accepted by the model package.
func Query[T any]() *SelectQuery[T] {
	return &SelectQuery[T]{selection: selectQuery{modelType: reflect.TypeFor[T]()}}
}

// Select appends exported Go field names to the result projection.
//
// Without Select, every mapped scalar field is selected.
func (q *SelectQuery[T]) Select(fields ...string) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	if len(fields) == 0 && q.selection.projection == nil {
		q.selection.projection = []string{}
		return q
	}
	q.selection.projection = append(q.selection.projection, fields...)
	return q
}

// Preload appends one direct or pure many-to-many relation path to hydrate
// without lazy loading. Belongs-to and has-one relations use inline LEFT JOINs;
// has-many and pure many-to-many relations use deterministic secondary
// SELECTs. An unrestricted All loads each root collection source once, while
// constrained and nested collection loads use bounded parameter batches.
//
// Preload performs no I/O until All, First, or Only is called. Build validates
// each relation path and returns the complete parent SQL, including inline
// joins. Keyed collection bind values depend on preceding rows and are built
// during execution. Dot-separated paths request nested relations. Optional
// projection applies to any relation; ordering applies only to collections.
func (q *SelectQuery[T]) Preload(path string, options ...PreloadOption) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	q.selection.preloads = append(q.selection.preloads, preloadRequest{
		path:    path,
		options: append([]PreloadOption(nil), options...),
	})
	return q
}

// WithDeleted includes logically deleted root rows in this SELECT.
//
// Relation preloads remain independently filtered unless their Preload call
// uses PreloadWithDeleted.
func (q *SelectQuery[T]) WithDeleted() *SelectQuery[T] {
	if q == nil {
		return nil
	}
	q.selection.withDeleted = true
	return q
}

// Where appends predicates joined by AND in call order.
func (q *SelectQuery[T]) Where(predicates ...Predicate) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	for index := range predicates {
		q.selection.predicates = append(q.selection.predicates, predicates[index].value)
	}
	return q
}

// OrderBy appends deterministic ordering terms in call order.
func (q *SelectQuery[T]) OrderBy(terms ...OrderTerm) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	for index := range terms {
		q.selection.orderBy = append(q.selection.orderBy, terms[index].value)
	}
	return q
}

// SeekAfter enables keyset pagination using values in OrderBy order.
//
// A nil or typed-nil value represents SQL NULL and no cursor field name is
// repeated because each value is matched positionally to OrderBy.
func (q *SelectQuery[T]) SeekAfter(values ...any) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	cursor := make([]cursorValue, len(values))
	for index, value := range values {
		if nilPredicateArgument(value) {
			cursor[index].null = true
			continue
		}
		cursor[index].value = value
	}
	q.selection.seekAfter = cursor
	return q
}

// Limit sets the maximum number of rows returned by the query.
//
// A later call replaces the previous value.
func (q *SelectQuery[T]) Limit(value int64) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	q.selection.pagination.limit = value
	q.selection.pagination.limitSet = true
	return q
}

// Offset sets the number of ordered rows skipped by the query.
//
// Offset requires Limit and cannot be combined with SeekAfter. A later call
// replaces the previous value.
func (q *SelectQuery[T]) Offset(value int64) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	q.selection.pagination.offset = value
	q.selection.pagination.offsetSet = true
	return q
}

// Build compiles the query offline and returns SQL plus bind arguments.
//
// Build validates model metadata and query structure without executing custom
// driver.Valuer implementations or accessing a database.
func (q *SelectQuery[T]) Build() (string, []any, error) {
	compiled, err := q.compile()
	if err != nil {
		return "", nil, err
	}
	return compiled.statement.sql, compiled.arguments, nil
}

func (q *SelectQuery[T]) compile() (compiledSelect, error) {
	if q == nil {
		return compiledSelect{}, fmt.Errorf("orm: compile a nil SELECT query")
	}
	return compileQuerySelection[T](&q.selection)
}

func (q *SelectQuery[T]) compileWithLimit(value int64) (compiledSelect, error) {
	if q == nil {
		return compiledSelect{}, fmt.Errorf("orm: compile a nil SELECT query")
	}
	selection := q.selection
	selection.pagination.limit = value
	selection.pagination.limitSet = true
	return compileQuerySelection[T](&selection)
}

func compileQuerySelection[T any](selection *selectQuery) (compiledSelect, error) {
	modelType := selection.modelType
	if modelType == nil {
		modelType = reflect.TypeFor[T]()
	}
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return compiledSelect{}, fmt.Errorf("orm: SELECT query model must be a non-pointer struct, got %v", modelType)
	}
	if selection.modelType == modelType {
		return compileSelect(selection)
	}
	copy := *selection
	copy.modelType = modelType
	return compileSelect(&copy)
}
