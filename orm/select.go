package orm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

type selectStatement struct {
	sql            string
	scanPlan       *scanPlan
	qualifier      string
	inlinePreloads []*preloadPlan
}

type compiledSelect struct {
	statement *selectStatement
	arguments []any
	preloads  []*preloadPlan
}

type selectQuery struct {
	modelType   reflect.Type
	projection  []string
	predicates  []predicate
	orderBy     []orderTerm
	seekAfter   []cursorValue
	pagination  pagination
	preloads    []preloadRequest
	withDeleted bool
}

var (
	defaultSelectCache           sync.Map
	defaultSoftDeleteSelectCache sync.Map
)

func compileSelect(query *selectQuery) (compiledSelect, error) {
	descriptor, err := model.DescribeType(query.modelType)
	if err != nil {
		return compiledSelect{}, fmt.Errorf("orm: compile SELECT model: %w", err)
	}
	if err := validateWithDeleted(descriptor, query.withDeleted, "SELECT"); err != nil {
		return compiledSelect{}, err
	}
	if len(query.preloads) == 0 {
		return compileSelectWithoutPreloads(descriptor, query)
	}
	preloads, err := compilePreloadPlans(descriptor, query.preloads)
	if err != nil {
		return compiledSelect{}, err
	}
	if selectLoadsEverySourceRow(descriptor, query) {
		for _, preload := range preloads {
			if !preload.inline {
				preload.loadAllSources = true
			}
		}
	}
	projection := preloadProjection(query.projection, preloads)
	statement, err := compileSelectProjection(descriptor, projection)
	if err != nil {
		return compiledSelect{}, err
	}
	if compiled, optimized, compileErr := compileRelationTopNSelect(descriptor, statement, preloads, query); compileErr != nil {
		return compiledSelect{}, compileErr
	} else if optimized {
		return compiled, nil
	}
	rootSoftDeleteColumn := ""
	onlyDefaultSoftDeleteScope := selectUsesOnlyDefaultSoftDeleteScope(descriptor, query)
	if onlyDefaultSoftDeleteScope {
		field, _ := descriptor.SoftDeleteField()
		rootSoftDeleteColumn = field.ColumnName()
		if query.projection == nil && !preloadsContainInline(preloads) {
			statement, err = compileDefaultSoftDeleteSelect(descriptor)
			if err != nil {
				return compiledSelect{}, err
			}
			return compiledSelect{statement: statement, preloads: preloads}, nil
		}
	}
	statement = compileInlinePreloadStatement(descriptor, statement, preloads, inlinePreloadRootAlias, rootSoftDeleteColumn)
	if onlyDefaultSoftDeleteScope {
		return compiledSelect{statement: statement, preloads: preloads}, nil
	}
	if !selectNeedsClauses(descriptor, query) {
		return compiledSelect{statement: statement, preloads: preloads}, nil
	}
	compiled, err := compileSelectClauses(descriptor, statement, query)
	if err != nil {
		return compiledSelect{}, err
	}
	compiled.preloads = preloads
	return compiled, nil
}

func preloadsContainInline(plans []*preloadPlan) bool {
	for _, plan := range plans {
		if plan.inline {
			return true
		}
	}
	return false
}

func selectLoadsEverySourceRow(descriptor *model.Descriptor, query *selectQuery) bool {
	_, hasSoftDelete := descriptor.SoftDeleteField()
	return (!hasSoftDelete || query.withDeleted) &&
		len(query.predicates) == 0 &&
		query.seekAfter == nil &&
		!query.pagination.limitSet &&
		!query.pagination.offsetSet
}

func compileSelectWithoutPreloads(descriptor *model.Descriptor, query *selectQuery) (compiledSelect, error) {
	if query.projection == nil && selectUsesOnlyDefaultSoftDeleteScope(descriptor, query) {
		statement, err := compileDefaultSoftDeleteSelect(descriptor)
		if err != nil {
			return compiledSelect{}, err
		}
		return compiledSelect{statement: statement}, nil
	}
	statement, err := compileSelectProjection(descriptor, query.projection)
	if err != nil {
		return compiledSelect{}, err
	}
	if compiled, optimized, compileErr := compileRelationTopNSelect(descriptor, statement, nil, query); compileErr != nil {
		return compiledSelect{}, compileErr
	} else if optimized {
		return compiled, nil
	}
	if !selectNeedsClauses(descriptor, query) {
		return compiledSelect{statement: statement}, nil
	}
	return compileSelectClauses(descriptor, statement, query)
}

func selectUsesOnlyDefaultSoftDeleteScope(descriptor *model.Descriptor, query *selectQuery) bool {
	_, hasSoftDelete := descriptor.SoftDeleteField()
	return hasSoftDelete && !query.withDeleted &&
		len(query.predicates) == 0 &&
		len(query.orderBy) == 0 &&
		query.seekAfter == nil &&
		!query.pagination.limitSet &&
		!query.pagination.offsetSet
}

func compileDefaultSoftDeleteSelect(descriptor *model.Descriptor) (*selectStatement, error) {
	modelType := descriptor.Type()
	if cached, ok := defaultSoftDeleteSelectCache.Load(modelType); ok {
		return cached.(*selectStatement), nil
	}
	base, err := compileDefaultSelect(descriptor)
	if err != nil {
		return nil, err
	}
	field, exists := descriptor.SoftDeleteField()
	if !exists {
		return nil, fmt.Errorf("orm: compile default soft-delete SELECT for %s without a soft-delete field", descriptor.Name())
	}

	var query strings.Builder
	query.Grow(len(base.sql) + len(" WHERE `` IS NULL") + len(field.ColumnName()))
	query.WriteString(base.sql)
	query.WriteString(" WHERE ")
	writeActiveSoftDeletePredicate(&query, "", field)
	statement := &selectStatement{sql: query.String(), scanPlan: base.scanPlan}
	result, _ := defaultSoftDeleteSelectCache.LoadOrStore(modelType, statement)
	return result.(*selectStatement), nil
}

func selectNeedsClauses(descriptor *model.Descriptor, query *selectQuery) bool {
	_, softDelete := descriptor.SoftDeleteField()
	return softDelete && !query.withDeleted ||
		len(query.predicates) != 0 ||
		len(query.orderBy) != 0 ||
		query.seekAfter != nil ||
		query.pagination.limitSet ||
		query.pagination.offsetSet
}

func compileSelectProjection(descriptor *model.Descriptor, projection []string) (*selectStatement, error) {
	if projection == nil {
		return compileDefaultSelect(descriptor)
	}
	if len(projection) == 0 {
		return nil, fmt.Errorf("orm: SELECT projection for %s must contain at least one mapped scalar field", descriptor.Name())
	}

	fields := make([]model.Field, len(projection))
	seen := make(map[string]bool, len(projection))
	for index, name := range projection {
		if seen[name] {
			return nil, fmt.Errorf("orm: SELECT projection for %s repeats field %q", descriptor.Name(), name)
		}
		field, ok := descriptor.FieldByGoName(name)
		if !ok {
			return nil, fmt.Errorf("orm: SELECT projection field %s.%s is not a mapped scalar field", descriptor.Name(), name)
		}
		if field.IsComputed() {
			return nil, fmt.Errorf("orm: SELECT projection field %s.%s is computed and available only to raw query results", descriptor.Name(), name)
		}
		seen[name] = true
		fields[index] = field
	}
	scanPlan, err := compileScanPlanFields(descriptor, fields)
	if err != nil {
		return nil, err
	}
	return &selectStatement{
		sql:      renderSelect(descriptor.TableName(), scanPlan.columns),
		scanPlan: scanPlan,
	}, nil
}

func compileDefaultSelect(descriptor *model.Descriptor) (*selectStatement, error) {
	modelType := descriptor.Type()
	if cached, ok := defaultSelectCache.Load(modelType); ok {
		return cached.(*selectStatement), nil
	}
	fields := baseTableFields(descriptor)
	if len(fields) == 0 {
		return nil, fmt.Errorf("orm: SELECT model %s has no base-table fields", descriptor.Name())
	}
	scanPlan, err := compileScanPlanFields(descriptor, fields)
	if err != nil {
		return nil, err
	}
	statement := &selectStatement{
		sql:      renderSelect(descriptor.TableName(), scanPlan.columns),
		scanPlan: scanPlan,
	}
	result, _ := defaultSelectCache.LoadOrStore(modelType, statement)
	return result.(*selectStatement), nil
}

func baseTableFields(descriptor *model.Descriptor) []model.Field {
	fields := descriptor.Fields()
	result := make([]model.Field, 0, len(fields))
	for _, field := range fields {
		if !field.IsComputed() {
			result = append(result, field)
		}
	}
	return result
}

func renderSelect(table string, columns []string) string {
	var query strings.Builder
	query.Grow(len("SELECT  FROM ") + len(table) + 2 + len(columns)*4)
	query.WriteString("SELECT ")
	for index, column := range columns {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQuotedIdentifier(&query, column)
	}
	query.WriteString(" FROM ")
	writeQuotedIdentifier(&query, table)
	return query.String()
}

func writeQuotedIdentifier(query *strings.Builder, identifier string) {
	query.WriteByte('`')
	query.WriteString(identifier)
	query.WriteByte('`')
}
