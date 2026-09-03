package orm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/internal/relationtopn"
	"github.com/mayahiro/go-tidb/model"
)

const (
	relationTopNAssociationAlias = "tidbgo_a0"
	relationTopNKeyAlias         = "tidbgo_k0"
)

type relationTopNPlan struct {
	predicate predicate
	metadata  *relationTopNMetadata
}

type relationTopNAnalysis struct {
	plan         relationTopNPlan
	optimized    bool
	candidate    bool
	relationName string
	reason       string
}

type relationTopNMetadata struct {
	relationName           string
	target                 *model.Descriptor
	sourceGoNames          []string
	sourceColumns          []string
	targetKeyGoNames       []string
	targetColumns          []string
	targetPrimaryGoNames   []string
	sourceIsRootPrimaryKey bool
}

type relationTopNMetadataKey struct {
	sourceType   reflect.Type
	relationName string
}

type relationTopNMetadataResult struct {
	metadata *relationTopNMetadata
	err      error
}

type rootCollectionHas struct {
	predicate predicate
	relation  model.Relation
	direct    bool
}

type rootCollectionHasSearch struct {
	count int
	first rootCollectionHas
}

var relationTopNMetadataCache sync.Map

func compileRelationTopNSelect(descriptor *model.Descriptor, base *selectStatement, preloads []*preloadPlan, selection *selectQuery) (compiledSelect, bool, error) {
	analysis, err := analyzeRelationTopN(descriptor, selection)
	if err != nil {
		return compiledSelect{}, false, err
	}
	if !analysis.optimized {
		return compiledSelect{}, false, nil
	}
	if err := validatePagination(selection.pagination); err != nil {
		return compiledSelect{}, false, err
	}

	plan := analysis.plan
	metadata := plan.metadata
	inline := inlinePreloadPlans(preloads)
	if len(inline) != 0 {
		nextAlias := 1
		assignInlinePreloadAliases(inline, inlinePreloadRootAlias, &nextAlias)
	}

	argumentCount, sqlCapacity := predicateCompileCapacity(plan.predicate.children)
	argumentCount++
	if selection.pagination.offsetSet {
		argumentCount++
	}
	sqlCapacity += len(base.sql) + inlinePreloadSQLCapacity(inline) + 256

	var query strings.Builder
	query.Grow(sqlCapacity)
	query.WriteString("SELECT ")
	writeRelationTopNColumns(&query, inlinePreloadRootAlias, base.scanPlan.columns)
	writeInlinePreloadColumns(&query, inline)
	query.WriteString(" FROM (SELECT ")
	writeRelationTopNColumns(&query, relationTopNAssociationAlias, metadata.targetColumns)
	query.WriteString(" FROM ")
	writeQuotedIdentifier(&query, metadata.target.TableName())
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, relationTopNAssociationAlias)

	arguments := make([]any, 0, argumentCount)
	wroteWhere := false
	if softDeleteField, exists := metadata.target.SoftDeleteField(); exists {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, relationTopNAssociationAlias, softDeleteField.ColumnName())
		wroteWhere = true
	}
	targetPredicates := predicateCompiler{
		descriptor: metadata.target,
		query:      &query,
		arguments:  arguments,
		qualifier:  relationTopNAssociationAlias,
	}
	for index := range plan.predicate.children {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		if err := targetPredicates.write(plan.predicate.children[index]); err != nil {
			return compiledSelect{}, false, fmt.Errorf("orm: SELECT relation TopN predicate %s.%s: %w", descriptor.Name(), metadata.relationName, err)
		}
	}

	writeRelationTopNOrder(&query, relationTopNAssociationAlias, metadata.targetColumns, selection.orderBy)
	query.WriteString(" LIMIT ?")
	targetPredicates.arguments = append(targetPredicates.arguments, selection.pagination.limit)
	if selection.pagination.offsetSet {
		query.WriteString(" OFFSET ?")
		targetPredicates.arguments = append(targetPredicates.arguments, selection.pagination.offset)
	}
	query.WriteString(") AS ")
	writeQuotedIdentifier(&query, relationTopNKeyAlias)
	query.WriteString(" JOIN ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, inlinePreloadRootAlias)
	query.WriteString(" ON (")
	writeRelationColumnEqualities(
		&query,
		relationTopNKeyAlias,
		metadata.targetColumns,
		inlinePreloadRootAlias,
		metadata.sourceColumns,
	)
	query.WriteByte(')')
	writeInlinePreloadJoins(&query, inline)
	writeRelationTopNOrder(&query, inlinePreloadRootAlias, metadata.sourceColumns, selection.orderBy)

	return compiledSelect{
		statement: &selectStatement{
			sql:            query.String(),
			scanPlan:       base.scanPlan,
			qualifier:      inlinePreloadRootAlias,
			inlinePreloads: inline,
		},
		arguments: targetPredicates.arguments,
		preloads:  preloads,
	}, true, nil
}

func analyzeRelationTopN(descriptor *model.Descriptor, selection *selectQuery) (relationTopNAnalysis, error) {
	if !selection.pagination.limitSet || selection.pagination.limit <= 0 || len(selection.orderBy) == 0 {
		return relationTopNAnalysis{}, nil
	}

	search := rootCollectionHasSearch{}
	for index := range selection.predicates {
		collectRootCollectionHas(descriptor, selection.predicates[index], true, &search)
	}
	if search.count == 0 {
		return relationTopNAnalysis{}, nil
	}
	candidate := search.first
	relationName := candidate.relation.GoName()
	rootSoftDelete := false
	if _, filterSoftDeleted := activeSoftDeleteField(descriptor, selection.withDeleted); filterSoftDeleted {
		rootSoftDelete = true
	}
	outcome := relationtopn.DecideStructural(
		search.count,
		candidate.direct,
		candidate.relation.Kind() == model.RelationHasMany,
		selection.seekAfter != nil,
		len(selection.predicates),
		rootSoftDelete,
	)
	if outcome != relationtopn.OutcomeNeedsMetadata {
		decision := relationtopn.Decision(outcome, relationName)
		return relationTopNAnalysis{
			candidate:    outcome != relationtopn.OutcomeNone,
			relationName: decision.Relation,
			reason:       decision.Reason,
		}, nil
	}

	metadata, err := relationTopNMetadataFor(descriptor, candidate.relation)
	if err != nil {
		return relationTopNAnalysis{}, err
	}
	outcome = relationtopn.DecideMetadata(
		metadata.sourceIsRootPrimaryKey,
		relationTopNOrderMatches(metadata.sourceGoNames, selection.orderBy),
		relationTopNUniquePerRoot(metadata, candidate.predicate.children),
	)
	analysis := relationTopNAnalysis{
		optimized:    outcome == relationtopn.OutcomeOptimized,
		candidate:    true,
		relationName: relationName,
	}
	if !analysis.optimized {
		analysis.reason = relationtopn.Decision(outcome, relationName).Reason
	}
	if analysis.optimized {
		analysis.plan = relationTopNPlan{
			predicate: candidate.predicate,
			metadata:  metadata,
		}
	}
	return analysis, nil
}

func collectRootCollectionHas(descriptor *model.Descriptor, current predicate, direct bool, search *rootCollectionHasSearch) {
	switch current.operator {
	case predicateHasRelation:
		relation, ok := descriptor.RelationByName(current.field)
		if ok && relation.IsCollection() {
			search.count++
			if search.count == 1 {
				search.first = rootCollectionHas{
					predicate: current,
					relation:  relation,
					direct:    direct,
				}
			}
		}
	case predicateAnd, predicateOr, predicateNot:
		for index := range current.children {
			collectRootCollectionHas(descriptor, current.children[index], false, search)
		}
	}
}

func relationTopNMetadataFor(source *model.Descriptor, relation model.Relation) (*relationTopNMetadata, error) {
	key := relationTopNMetadataKey{sourceType: source.Type(), relationName: relation.GoName()}
	if cached, ok := relationTopNMetadataCache.Load(key); ok {
		result := cached.(relationTopNMetadataResult)
		return result.metadata, result.err
	}
	metadata, err := compileRelationTopNMetadata(source, relation)
	result, _ := relationTopNMetadataCache.LoadOrStore(key, relationTopNMetadataResult{metadata: metadata, err: err})
	cached := result.(relationTopNMetadataResult)
	return cached.metadata, cached.err
}

func compileRelationTopNMetadata(source *model.Descriptor, relation model.Relation) (*relationTopNMetadata, error) {
	target, err := model.DescribeType(relation.TargetType())
	if err != nil {
		return nil, fmt.Errorf("orm: describe SELECT relation TopN target %s.%s: %w", source.Name(), relation.GoName(), err)
	}
	sourceKey := relation.SourceKey()
	targetKey := relation.TargetKey()
	metadata := &relationTopNMetadata{
		relationName:           relation.GoName(),
		target:                 target,
		sourceGoNames:          relationFieldGoNames(sourceKey),
		sourceColumns:          relationFieldColumns(sourceKey),
		targetKeyGoNames:       relationFieldGoNames(targetKey),
		targetColumns:          relationFieldColumns(targetKey),
		targetPrimaryGoNames:   relationFieldGoNames(target.PrimaryKeyFields()),
		sourceIsRootPrimaryKey: sameRelationFields(sourceKey, source.PrimaryKeyFields()),
	}
	return metadata, nil
}

func relationFieldGoNames(fields []model.Field) []string {
	result := make([]string, len(fields))
	for index := range fields {
		result[index] = fields[index].GoName()
	}
	return result
}

func relationTopNUniquePerRoot(metadata *relationTopNMetadata, predicates []predicate) bool {
	if len(metadata.targetPrimaryGoNames) == 0 {
		return false
	}
	for _, field := range metadata.targetPrimaryGoNames {
		if relationTopNFieldNameExists(metadata.targetKeyGoNames, field) || conjunctiveEqualFieldExists(predicates, field) {
			continue
		}
		return false
	}
	return true
}

func relationTopNFieldNameExists(fields []string, name string) bool {
	for index := range fields {
		if fields[index] == name {
			return true
		}
	}
	return false
}

func conjunctiveEqualFieldExists(predicates []predicate, field string) bool {
	for index := range predicates {
		current := predicates[index]
		switch current.operator {
		case predicateEqual:
			if current.field == field {
				return true
			}
		case predicateAnd:
			if conjunctiveEqualFieldExists(current.children, field) {
				return true
			}
		}
	}
	return false
}

func relationTopNOrderMatches(sourceGoNames []string, orderBy []orderTerm) bool {
	if len(sourceGoNames) != len(orderBy) {
		return false
	}
	for index := range sourceGoNames {
		if sourceGoNames[index] != orderBy[index].field {
			return false
		}
		if orderBy[index].direction != orderAscending && orderBy[index].direction != orderDescending {
			return false
		}
	}
	return true
}

func sameRelationFields(left, right []model.Field) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].GoName() != right[index].GoName() {
			return false
		}
	}
	return true
}

func writeRelationTopNColumns(query *strings.Builder, qualifier string, columns []string) {
	for index := range columns {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQualifiedIdentifier(query, qualifier, columns[index])
	}
}

func writeRelationTopNOrder(query *strings.Builder, qualifier string, columns []string, orderBy []orderTerm) {
	query.WriteString(" ORDER BY ")
	for index := range columns {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQualifiedIdentifier(query, qualifier, columns[index])
		if orderBy[index].direction == orderDescending {
			query.WriteString(" DESC")
		} else {
			query.WriteString(" ASC")
		}
	}
}
