package orm

import (
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

const (
	rootPageSourceAlias = "tidbgo_p0"
	rootPageKeyAlias    = "tidbgo_k0"
	rootPageMaxLimit    = 100
)

type rootPageKeyMetadata struct {
	primaryColumns []string
	uniqueGoNames  [][]string
}

var rootPageKeyCache sync.Map

func rootPageKeys(descriptor *model.Descriptor) rootPageKeyMetadata {
	if cached, ok := rootPageKeyCache.Load(descriptor.Type()); ok {
		return cached.(rootPageKeyMetadata)
	}
	keys := rootPageKeyMetadata{
		primaryColumns: relationFieldColumns(descriptor.PrimaryKeyFields()),
		uniqueGoNames:  relationTopNUniqueGoNames(descriptor),
	}
	cached, _ := rootPageKeyCache.LoadOrStore(descriptor.Type(), keys)
	return cached.(rootPageKeyMetadata)
}

// compileRootPageSelect exposes a narrow, potentially covering root access
// before fetching page rows and their to-one preloads. It neither guesses an
// index name nor reads schema or statistics. Large pages and offsets retain
// the ordinary SELECT because the extra lookup can increase their cost.
func compileRootPageSelect(descriptor *model.Descriptor, base *selectStatement, preloads []*preloadPlan, selection *selectQuery) (compiledSelect, bool, error) {
	if !rootPageCandidate(descriptor, base, preloads, selection) {
		return compiledSelect{}, false, nil
	}
	keys := rootPageKeys(descriptor)
	if len(keys.primaryColumns) == 0 {
		return compiledSelect{}, false, nil
	}
	for _, key := range keys.uniqueGoNames {
		fixed := true
		for _, field := range key {
			fixed = fixed && rootPageEqualityField(selection.predicates, field)
		}
		if fixed {
			return compiledSelect{}, false, nil
		}
	}
	if !rootPageInlineUnique(preloads) {
		return compiledSelect{}, false, nil
	}
	inline := inlinePreloadPlans(preloads)
	nextAlias := 1
	assignInlinePreloadAliases(inline, inlinePreloadRootAlias, &nextAlias)

	argumentCount, capacity := predicateCompileCapacity(selection.predicates)
	argumentCount++
	if selection.pagination.offsetSet {
		argumentCount++
	}
	capacity += 2*len(base.sql) + inlinePreloadSQLCapacity(inline) + 2*orderCompileCapacity(selection.orderBy) + 256
	var query strings.Builder
	query.Grow(capacity)
	query.WriteString("SELECT ")
	writeRelationTopNColumns(&query, inlinePreloadRootAlias, base.scanPlan.columns)
	writeInlinePreloadColumns(&query, inline)
	query.WriteString(" FROM (SELECT ")
	writeRelationTopNColumns(&query, rootPageSourceAlias, keys.primaryColumns)
	query.WriteString(" FROM ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, rootPageSourceAlias)
	predicates := predicateCompiler{
		descriptor: descriptor,
		query:      &query,
		arguments:  make([]any, 0, argumentCount),
		qualifier:  rootPageSourceAlias,
	}
	wroteWhere := false
	if field, active := activeSoftDeleteField(descriptor, selection.withDeleted); active {
		query.WriteString(" WHERE ")
		writeActiveSoftDeletePredicate(&query, rootPageSourceAlias, field)
		wroteWhere = true
	}
	for _, predicate := range selection.predicates {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		if err := predicates.write(predicate); err != nil {
			return compiledSelect{}, false, err
		}
	}
	if err := writeOrderBy(&query, descriptor, selection.orderBy, rootPageSourceAlias); err != nil {
		return compiledSelect{}, false, err
	}
	query.WriteString(" LIMIT ?")
	predicates.arguments = append(predicates.arguments, selection.pagination.limit)
	if selection.pagination.offsetSet {
		query.WriteString(" OFFSET ?")
		predicates.arguments = append(predicates.arguments, selection.pagination.offset)
	}
	query.WriteString(") AS ")
	writeQuotedIdentifier(&query, rootPageKeyAlias)
	query.WriteString(" STRAIGHT_JOIN ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, inlinePreloadRootAlias)
	query.WriteString(" ON (")
	writeRelationColumnEqualities(&query, rootPageKeyAlias, keys.primaryColumns, inlinePreloadRootAlias, keys.primaryColumns)
	query.WriteByte(')')
	writeInlinePreloadJoins(&query, inline)
	if err := writeOrderBy(&query, descriptor, selection.orderBy, inlinePreloadRootAlias); err != nil {
		return compiledSelect{}, false, err
	}
	return compiledSelect{
		statement: &selectStatement{
			sql:            query.String(),
			scanPlan:       base.scanPlan,
			qualifier:      inlinePreloadRootAlias,
			inlinePreloads: inline,
		},
		arguments: predicates.arguments,
		preloads:  preloads,
		rootPage:  true,
	}, true, nil
}

func rootPageCandidate(descriptor *model.Descriptor, base *selectStatement, preloads []*preloadPlan, selection *selectQuery) bool {
	if !selection.pagination.limitSet || selection.pagination.limit <= 0 || selection.pagination.limit > rootPageMaxLimit ||
		selection.pagination.offset != 0 || selection.seekAfter != nil || len(selection.orderBy) == 0 ||
		!preloadsContainInline(preloads) {
		return false
	}
	count, exact := rootPageEqualities(descriptor, selection.predicates)
	if !exact || count == 0 {
		return false
	}
	nonKeyOrder := false
	for index, term := range selection.orderBy {
		field, err := resolveOrderField(descriptor, selection.orderBy, index)
		if err != nil || term.direction != selection.orderBy[0].direction {
			return false
		}
		if !field.IsPrimaryKey() && !rootPageEqualityField(selection.predicates, field.GoName()) {
			nonKeyOrder = true
		}
	}
	if !nonKeyOrder {
		return false
	}
	// A root projection already contained in the key/filter/order access
	// does not benefit from a separate row lookup unless an inline join
	// needs another root field, even when that field is not projected.
	for _, projected := range base.scanPlan.fields {
		field, _ := descriptor.FieldByGoName(projected.goName)
		if rootPageNeedsRowField(field, selection) {
			return true
		}
	}
	for _, plan := range preloads {
		if !plan.inline {
			continue
		}
		for _, field := range plan.sourceKey {
			if rootPageNeedsRowField(field, selection) {
				return true
			}
		}
	}
	return false
}

func rootPageNeedsRowField(field model.Field, selection *selectQuery) bool {
	if field.IsPrimaryKey() || rootPageEqualityField(selection.predicates, field.GoName()) || field.IsSoftDelete() && !selection.withDeleted {
		return false
	}
	for _, term := range selection.orderBy {
		if term.field == field.GoName() {
			return false
		}
	}
	return true
}

func rootPageEqualities(descriptor *model.Descriptor, predicates []predicate) (int, bool) {
	count := 0
	for _, current := range predicates {
		switch current.operator {
		case predicateEqual:
			field, ok := descriptor.FieldByGoName(current.field)
			if !ok || field.IsComputed() {
				return 0, false
			}
			count++
		case predicateAnd:
			children, exact := rootPageEqualities(descriptor, current.children)
			if !exact {
				return 0, false
			}
			count += children
		default:
			return 0, false
		}
	}
	return count, true
}

func rootPageEqualityField(predicates []predicate, field string) bool {
	for _, current := range predicates {
		if current.operator == predicateEqual && current.field == field || current.operator == predicateAnd && rootPageEqualityField(current.children, field) {
			return true
		}
	}
	return false
}

func rootPageInlineUnique(preloads []*preloadPlan) bool {
	for _, plan := range preloads {
		if !plan.inline {
			continue
		}
		descriptor, err := model.DescribeType(plan.targetType)
		if err != nil {
			return false
		}
		unique := false
		for _, key := range rootPageKeys(descriptor).uniqueGoNames {
			covered := true
			for _, name := range key {
				found := false
				for _, field := range plan.targetKey {
					found = found || field.GoName() == name
				}
				covered = covered && found
			}
			unique = unique || covered
		}
		if !unique || !rootPageInlineUnique(plan.inlineChildren) {
			return false
		}
	}
	return true
}
