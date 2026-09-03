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
	relationTopNManyTargetAlias  = "tidbgo_m0"
	relationTopNKeyAlias         = "tidbgo_k0"
	relationTopNManySQLCapacity  = 128
)

type relationTopNPlan struct {
	predicate    predicate
	metadata     *relationTopNMetadata
	junctionOnly bool
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
	junction               *relationTopNJunctionMetadata
}

type relationTopNJunctionMetadata struct {
	tableName     string
	sourceColumns []string
	targetColumns []string
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
	associationTable := metadata.target.TableName()
	associationColumns := metadata.targetColumns
	predicateAlias := relationTopNAssociationAlias
	if metadata.junction != nil {
		associationTable = metadata.junction.tableName
		associationColumns = metadata.junction.sourceColumns
		sqlCapacity += relationTopNManySQLCapacity
		if !plan.junctionOnly {
			predicateAlias = relationTopNManyTargetAlias
		}
	}

	var query strings.Builder
	query.Grow(sqlCapacity)
	query.WriteString("SELECT ")
	writeRelationTopNColumns(&query, inlinePreloadRootAlias, base.scanPlan.columns)
	writeInlinePreloadColumns(&query, inline)
	query.WriteString(" FROM (SELECT ")
	writeRelationTopNColumns(&query, relationTopNAssociationAlias, associationColumns)
	query.WriteString(" FROM ")
	writeQuotedIdentifier(&query, associationTable)
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, relationTopNAssociationAlias)
	if metadata.junction != nil && !plan.junctionOnly {
		query.WriteString(" JOIN ")
		writeQuotedIdentifier(&query, metadata.target.TableName())
		query.WriteString(" AS ")
		writeQuotedIdentifier(&query, relationTopNManyTargetAlias)
		query.WriteString(" ON (")
		writeRelationColumnEqualities(
			&query,
			relationTopNManyTargetAlias,
			metadata.targetColumns,
			relationTopNAssociationAlias,
			metadata.junction.targetColumns,
		)
		query.WriteByte(')')
	}

	arguments := make([]any, 0, argumentCount)
	wroteWhere := false
	if softDeleteField, exists := metadata.target.SoftDeleteField(); exists {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, predicateAlias, softDeleteField.ColumnName())
		wroteWhere = true
	}
	targetPredicates := predicateCompiler{
		descriptor: metadata.target,
		query:      &query,
		arguments:  arguments,
		qualifier:  predicateAlias,
	}
	for index := range plan.predicate.children {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		var err error
		if plan.junctionOnly {
			err = targetPredicates.writeRelationTopNJunctionPredicate(plan.predicate.children[index], metadata)
		} else {
			err = targetPredicates.write(plan.predicate.children[index])
		}
		if err != nil {
			return compiledSelect{}, false, fmt.Errorf("orm: SELECT relation TopN predicate %s.%s: %w", descriptor.Name(), metadata.relationName, err)
		}
	}

	writeRelationTopNOrder(&query, relationTopNAssociationAlias, associationColumns, selection.orderBy)
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
		associationColumns,
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
			predicate:    candidate.predicate,
			metadata:     metadata,
			junctionOnly: relationTopNCanFilterJunction(metadata, candidate.predicate.children),
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
	if relation.Kind() == model.RelationManyToMany {
		junction, ok := relation.Junction()
		if !ok {
			return nil, fmt.Errorf("orm: SELECT relation TopN %s.%s has no junction metadata", source.Name(), relation.GoName())
		}
		junctionSourceColumns := junction.SourceColumns()
		junctionTargetColumns := junction.TargetColumns()
		if len(junctionSourceColumns) != len(sourceKey) || len(junctionTargetColumns) != len(targetKey) {
			return nil, fmt.Errorf("orm: SELECT relation TopN %s.%s has invalid junction metadata", source.Name(), relation.GoName())
		}
		metadata.junction = &relationTopNJunctionMetadata{
			tableName:     junction.TableName(),
			sourceColumns: junctionSourceColumns,
			targetColumns: junctionTargetColumns,
		}
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
	fixedByRelation := metadata.targetKeyGoNames
	if metadata.junction != nil {
		// A many-to-many target key varies by root. The complete target primary
		// key must therefore be fixed by Equal predicates; the pure-junction
		// contract then makes the source-target pair unique.
		fixedByRelation = nil
	}
	for _, field := range metadata.targetPrimaryGoNames {
		if relationTopNFieldNameExists(fixedByRelation, field) || conjunctiveEqualFieldExists(predicates, field) {
			continue
		}
		return false
	}
	return true
}

func relationTopNCanFilterJunction(metadata *relationTopNMetadata, predicates []predicate) bool {
	if metadata.junction == nil {
		return false
	}
	if _, softDelete := metadata.target.SoftDeleteField(); softDelete {
		return false
	}
	if len(metadata.targetKeyGoNames) != len(metadata.targetPrimaryGoNames) {
		return false
	}
	for _, field := range metadata.targetPrimaryGoNames {
		if _, exists := relationTopNJunctionTargetColumn(metadata, field); !exists {
			return false
		}
	}
	for index := range predicates {
		if !relationTopNJunctionPredicateOnly(metadata, predicates[index]) {
			return false
		}
	}
	return true
}

func relationTopNJunctionPredicateOnly(metadata *relationTopNMetadata, current predicate) bool {
	switch current.operator {
	case predicateEqual:
		_, exists := relationTopNJunctionTargetColumn(metadata, current.field)
		return exists
	case predicateAnd:
		if len(current.children) < 2 {
			return false
		}
		for index := range current.children {
			if !relationTopNJunctionPredicateOnly(metadata, current.children[index]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func relationTopNJunctionTargetColumn(metadata *relationTopNMetadata, field string) (string, bool) {
	for index := range metadata.targetKeyGoNames {
		if metadata.targetKeyGoNames[index] == field {
			return metadata.junction.targetColumns[index], true
		}
	}
	return "", false
}

func (c *predicateCompiler) writeRelationTopNJunctionPredicate(current predicate, metadata *relationTopNMetadata) error {
	switch current.operator {
	case predicateEqual:
		field, exists := c.descriptor.FieldByGoName(current.field)
		if !exists || field.IsComputed() {
			return fmt.Errorf("orm: %s predicate field %s.%s is not a mapped scalar field", c.operationName(), c.descriptor.Name(), current.field)
		}
		if err := c.requireValues(current, field, 1); err != nil {
			return err
		}
		column, exists := relationTopNJunctionTargetColumn(metadata, current.field)
		if !exists {
			return fmt.Errorf("orm: SELECT relation TopN target field %s.%s has no junction column", c.descriptor.Name(), current.field)
		}
		writeQualifiedIdentifier(c.query, c.qualifier, column)
		c.query.WriteString(" = ?")
		c.arguments = append(c.arguments, current.values[0])
		return nil
	case predicateAnd:
		if current.field != "" || len(current.values) != 0 || len(current.children) < 2 {
			return fmt.Errorf("orm: AND %s predicate must contain at least two children", c.operationName())
		}
		c.query.WriteByte('(')
		for index := range current.children {
			if index != 0 {
				c.query.WriteString(" AND ")
			}
			if err := c.writeRelationTopNJunctionPredicate(current.children[index], metadata); err != nil {
				return err
			}
		}
		c.query.WriteByte(')')
		return nil
	default:
		return fmt.Errorf("orm: SELECT relation TopN junction predicate has unsupported operator %d", current.operator)
	}
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
