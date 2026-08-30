package orm

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

const (
	preloadParameterBudget              = 5000
	tidbPreparedStatementParameterLimit = 65535
)

type preloadPlan struct {
	sourceName        string
	sourceType        reflect.Type
	relationName      string
	relationKind      model.RelationKind
	relationIndex     []int
	sourceKey         []model.Field
	sourceKeyIndex    [][]int
	targetKey         []model.Field
	targetKeyIndex    [][]int
	targetKeyColumns  []string
	targetType        reflect.Type
	targetTable       string
	targetStatement   *selectStatement
	junction          *preloadJunctionPlan
	orderBy           []preloadOrderTerm
	children          []*preloadPlan
	inlineChildren    []*preloadPlan
	sourceAlias       string
	targetAlias       string
	targetKeyScan     []int
	softDelete        *preloadSoftDeletePlan
	batchSize         int
	inlineColumnCount int
	retainTarget      bool
	inline            bool
	loadAllSources    bool
	withDeleted       bool
}

type preloadSoftDeletePlan struct {
	column string
}

type preloadOrderTerm struct {
	column    string
	direction orderDirection
}

type preloadJunctionPlan struct {
	tableName     string
	sourceColumns []string
	targetColumns []string
}

type preloadPlanKey struct {
	sourceType   reflect.Type
	relationName string
}

type preloadPlanResult struct {
	plan *preloadPlan
	err  error
}

type preloadKey struct {
	component  any
	components []any
}

type preloadLookupKey struct {
	kind   byte
	first  uint64
	second uint64
	text   string
}

type preloadParentIndexes struct {
	first int
	more  []int
}

type preloadParentSet struct {
	value  reflect.Value
	values []reflect.Value
}

var preloadPlanCache sync.Map

type preloadNode struct {
	name        string
	explicit    bool
	withDeleted bool
	projection  []string
	orderBy     []orderTerm
	children    []*preloadNode
}

func compilePreloadPlans(descriptor *model.Descriptor, requests []preloadRequest) ([]*preloadPlan, error) {
	nodes, err := buildPreloadNodes(descriptor, requests)
	if err != nil {
		return nil, err
	}
	plans := make([]*preloadPlan, len(nodes))
	for index, node := range nodes {
		plan, compileErr := compilePreloadNode(descriptor, node)
		if compileErr != nil {
			return nil, compileErr
		}
		plans[index] = plan
	}
	return plans, nil
}

func buildPreloadNodes(descriptor *model.Descriptor, requests []preloadRequest) ([]*preloadNode, error) {
	var roots []*preloadNode
	for _, request := range requests {
		if request.path == "" {
			return nil, fmt.Errorf("orm: SELECT preload for %s requires a relation path", descriptor.Name())
		}
		parts := strings.Split(request.path, ".")
		current := &roots
		var node *preloadNode
		for index, name := range parts {
			if name == "" {
				return nil, fmt.Errorf("orm: SELECT preload for %s has invalid relation path %q", descriptor.Name(), request.path)
			}
			node = findPreloadNode(*current, name)
			if node == nil {
				node = &preloadNode{name: name}
				*current = append(*current, node)
			}
			if index == len(parts)-1 {
				if node.explicit {
					return nil, fmt.Errorf("orm: SELECT preload for %s repeats relation path %q", descriptor.Name(), request.path)
				}
				node.explicit = true
				if err := applyPreloadOptions(descriptor, request, node); err != nil {
					return nil, err
				}
			}
			current = &node.children
		}
	}
	return roots, nil
}

func findPreloadNode(nodes []*preloadNode, name string) *preloadNode {
	for _, node := range nodes {
		if node.name == name {
			return node
		}
	}
	return nil
}

func applyPreloadOptions(descriptor *model.Descriptor, request preloadRequest, node *preloadNode) error {
	seen := make(map[preloadOptionKind]bool, len(request.options))
	for _, option := range request.options {
		if seen[option.kind] {
			return fmt.Errorf("orm: SELECT Preload for %s path %q repeats an option", descriptor.Name(), request.path)
		}
		seen[option.kind] = true
		switch option.kind {
		case preloadOptionFields:
			if len(option.fields) == 0 {
				return fmt.Errorf("orm: SELECT PreloadFields for %s path %q requires at least one field", descriptor.Name(), request.path)
			}
			node.projection = append([]string(nil), option.fields...)
		case preloadOptionOrderBy:
			if len(option.orderBy) == 0 {
				return fmt.Errorf("orm: SELECT PreloadOrderBy for %s path %q requires at least one term", descriptor.Name(), request.path)
			}
			node.orderBy = append([]orderTerm(nil), option.orderBy...)
		case preloadOptionWithDeleted:
			node.withDeleted = true
		default:
			return fmt.Errorf("orm: SELECT Preload for %s path %q has an invalid option", descriptor.Name(), request.path)
		}
	}
	return nil
}

func compilePreloadNode(source *model.Descriptor, node *preloadNode) (*preloadPlan, error) {
	base, err := preloadPlanFor(source, node.name)
	if err != nil {
		return nil, err
	}
	plan := *base
	target, err := model.DescribeType(plan.targetType)
	if err != nil {
		return nil, fmt.Errorf("orm: describe SELECT preload target %s.%s: %w", source.Name(), node.name, err)
	}
	if node.withDeleted && plan.softDelete == nil {
		return nil, fmt.Errorf("orm: SELECT PreloadWithDeleted for %s.%s requires a soft-delete field on %s", source.Name(), node.name, target.Name())
	}
	plan.withDeleted = node.withDeleted
	plan.children = make([]*preloadPlan, len(node.children))
	for index, child := range node.children {
		current, compileErr := compilePreloadNode(target, child)
		if compileErr != nil {
			return nil, compileErr
		}
		plan.children[index] = current
	}
	plan.inlineChildren = inlinePreloadPlans(plan.children)
	projection := preloadTargetProjection(node.projection, &plan)
	if projection != nil {
		statement, compileErr := compileSelectProjection(target, projection)
		if compileErr != nil {
			return nil, fmt.Errorf("orm: compile SELECT preload target %s.%s: %w", source.Name(), node.name, compileErr)
		}
		plan.targetStatement = statement
	}
	plan.orderBy, err = compilePreloadOrderBy(target, node.orderBy)
	if err != nil {
		return nil, fmt.Errorf("orm: compile SELECT preload target %s.%s: %w", source.Name(), node.name, err)
	}
	if plan.inline && len(plan.orderBy) != 0 {
		return nil, fmt.Errorf("orm: SELECT PreloadOrderBy for %s.%s requires a collection relation", source.Name(), node.name)
	}
	plan.targetKeyScan, err = preloadTargetKeyScanIndexes(&plan)
	if err != nil {
		return nil, err
	}
	if !plan.inline {
		rootAlias := inlinePreloadRootAlias
		if plan.junction != nil {
			rootAlias = manyToManyTargetAlias
		}
		plan.targetStatement = compileInlinePreloadStatement(target, plan.targetStatement, plan.children, rootAlias, "")
	}
	plan.inlineColumnCount = len(plan.targetStatement.scanPlan.fields) + inlinePreloadColumnCount(plan.inlineChildren)
	return &plan, nil
}

func preloadTargetProjection(projection []string, plan *preloadPlan) []string {
	if projection == nil {
		return nil
	}
	result := append([]string(nil), projection...)
	for _, field := range plan.targetKey {
		if !preloadProjectionContains(result, field.GoName()) {
			result = append(result, field.GoName())
		}
	}
	for _, child := range plan.children {
		if child.inline {
			continue
		}
		for _, field := range child.sourceKey {
			if !preloadProjectionContains(result, field.GoName()) {
				result = append(result, field.GoName())
			}
		}
	}
	return result
}

func compilePreloadOrderBy(descriptor *model.Descriptor, terms []orderTerm) ([]preloadOrderTerm, error) {
	result := make([]preloadOrderTerm, len(terms))
	for index := range terms {
		field, err := resolveOrderField(descriptor, terms, index)
		if err != nil {
			return nil, err
		}
		result[index] = preloadOrderTerm{column: field.ColumnName(), direction: terms[index].direction}
	}
	return result, nil
}

func preloadPlanFor(source *model.Descriptor, relationName string) (*preloadPlan, error) {
	key := preloadPlanKey{sourceType: source.Type(), relationName: relationName}
	if cached, ok := preloadPlanCache.Load(key); ok {
		result := cached.(preloadPlanResult)
		return result.plan, result.err
	}

	relation, ok := source.RelationByName(relationName)
	if !ok {
		err := fmt.Errorf("orm: SELECT preload relation %s.%s is not a mapped relation field", source.Name(), relationName)
		result, _ := preloadPlanCache.LoadOrStore(key, preloadPlanResult{err: err})
		return nil, result.(preloadPlanResult).err
	}
	plan, err := compilePreloadPlan(source, relation)
	result, _ := preloadPlanCache.LoadOrStore(key, preloadPlanResult{plan: plan, err: err})
	cached := result.(preloadPlanResult)
	return cached.plan, cached.err
}

func compilePreloadPlan(source *model.Descriptor, relation model.Relation) (*preloadPlan, error) {
	inline := relation.Kind() == model.RelationBelongsTo || relation.Kind() == model.RelationHasOne
	sourceKey := relation.SourceKey()
	targetKey := relation.TargetKey()
	if len(sourceKey) == 0 || len(targetKey) == 0 {
		return nil, fmt.Errorf("orm: SELECT preload relation %s.%s has invalid key metadata", source.Name(), relation.GoName())
	}
	if relation.Kind() != model.RelationManyToMany && len(sourceKey) != len(targetKey) {
		return nil, fmt.Errorf("orm: SELECT preload relation %s.%s has invalid key metadata", source.Name(), relation.GoName())
	}
	if len(sourceKey) > tidbPreparedStatementParameterLimit {
		return nil, fmt.Errorf("orm: SELECT preload relation %s.%s has %d key fields, exceeding TiDB's %d-placeholder statement limit", source.Name(), relation.GoName(), len(sourceKey), tidbPreparedStatementParameterLimit)
	}
	for index := range sourceKey {
		if sourceKey[index].IsComputed() {
			return nil, fmt.Errorf("orm: SELECT preload source key field %s.%s cannot be computed", source.Name(), sourceKey[index].GoName())
		}
		if !inline && !sourceKey[index].CanScan() {
			return nil, fmt.Errorf("orm: SELECT preload source key field %s.%s cannot be read from a database row", source.Name(), sourceKey[index].GoName())
		}
		if !inline && !sourceKey[index].CanValue() {
			return nil, fmt.Errorf("orm: SELECT preload source key field %s.%s cannot be used as a database argument", source.Name(), sourceKey[index].GoName())
		}
	}
	for index := range targetKey {
		if targetKey[index].IsComputed() {
			return nil, fmt.Errorf("orm: SELECT preload target key field %s.%s cannot be computed", relation.TargetType().Name(), targetKey[index].GoName())
		}
		if !targetKey[index].CanScan() {
			return nil, fmt.Errorf("orm: SELECT preload target key field %s.%s cannot be read from a database row", relation.TargetType().Name(), targetKey[index].GoName())
		}
		if !inline && relation.Kind() != model.RelationManyToMany && !targetKey[index].CanValue() {
			return nil, fmt.Errorf("orm: SELECT preload target key field %s.%s cannot identify a hydrated relation row", relation.TargetType().Name(), targetKey[index].GoName())
		}
	}

	var junctionPlan *preloadJunctionPlan
	if relation.Kind() == model.RelationManyToMany {
		junction, ok := relation.Junction()
		if !ok {
			return nil, fmt.Errorf("orm: SELECT preload relation %s.%s has no junction metadata", source.Name(), relation.GoName())
		}
		sourceColumns := junction.SourceColumns()
		targetColumns := junction.TargetColumns()
		if len(sourceColumns) != len(sourceKey) || len(targetColumns) != len(targetKey) {
			return nil, fmt.Errorf("orm: SELECT preload relation %s.%s has invalid junction metadata", source.Name(), relation.GoName())
		}
		junctionPlan = &preloadJunctionPlan{
			tableName:     junction.TableName(),
			sourceColumns: sourceColumns,
			targetColumns: targetColumns,
		}
	}

	target, err := model.DescribeType(relation.TargetType())
	if err != nil {
		return nil, fmt.Errorf("orm: describe SELECT preload target %s.%s: %w", source.Name(), relation.GoName(), err)
	}
	targetStatement, err := compileDefaultSelect(target)
	if err != nil {
		return nil, fmt.Errorf("orm: compile SELECT preload target %s.%s: %w", source.Name(), relation.GoName(), err)
	}
	var softDelete *preloadSoftDeletePlan
	softDeleteField, hasSoftDelete := target.SoftDeleteField()
	if hasSoftDelete {
		softDelete = &preloadSoftDeletePlan{column: softDeleteField.ColumnName()}
	}

	batchSize := max(1, min(preloadParameterBudget/len(sourceKey), tidbPreparedStatementParameterLimit/len(sourceKey)))
	sourceKeyIndex := make([][]int, len(sourceKey))
	targetKeyIndex := make([][]int, len(targetKey))
	targetKeyColumns := make([]string, len(targetKey))
	for index := range sourceKey {
		sourceKeyIndex[index] = sourceKey[index].Index()
	}
	for index := range targetKey {
		targetKeyIndex[index] = targetKey[index].Index()
		targetKeyColumns[index] = targetKey[index].ColumnName()
	}
	relationIndex := relation.Index()
	relationField := source.Type().FieldByIndex(relationIndex)
	retainTarget := relationField.Type.Kind() == reflect.Pointer || relationField.Type.Elem().Kind() == reflect.Pointer
	return &preloadPlan{
		sourceName:       source.Name(),
		sourceType:       source.Type(),
		relationName:     relation.GoName(),
		relationKind:     relation.Kind(),
		relationIndex:    relationIndex,
		sourceKey:        sourceKey,
		sourceKeyIndex:   sourceKeyIndex,
		targetKey:        targetKey,
		targetKeyIndex:   targetKeyIndex,
		targetKeyColumns: targetKeyColumns,
		targetType:       relation.TargetType(),
		targetTable:      target.TableName(),
		targetStatement:  targetStatement,
		junction:         junctionPlan,
		batchSize:        batchSize,
		retainTarget:     retainTarget,
		inline:           inline,
		softDelete:       softDelete,
	}, nil
}

func preloadProjection(projection []string, plans []*preloadPlan) []string {
	if projection == nil || len(projection) == 0 {
		return projection
	}

	result := projection
	copied := false
	for _, plan := range plans {
		if plan.inline {
			continue
		}
		for _, field := range plan.sourceKey {
			name := field.GoName()
			if preloadProjectionContains(result, name) {
				continue
			}
			if !copied {
				result = append([]string(nil), projection...)
				copied = true
			}
			result = append(result, name)
		}
	}
	return result
}

func preloadProjectionContains(projection []string, name string) bool {
	for _, current := range projection {
		if current == name {
			return true
		}
	}
	return false
}

func executePreloads(ctx context.Context, executor QueryExecutor, plans []*preloadPlan, value reflect.Value) error {
	parents := preloadParentSet{value: value}
	for _, plan := range plans {
		if err := executePreloadPlan(ctx, executor, plan, parents); err != nil {
			return err
		}
	}
	return nil
}

func executePreloadPlan(ctx context.Context, executor QueryExecutor, plan *preloadPlan, parents preloadParentSet) error {
	if !plan.inline {
		keys, parentIndexes, err := preparePreloadParents(plan, parents, !plan.loadAllSources)
		if err != nil {
			return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, err)
		}
		if plan.loadAllSources && len(parentIndexes) != 0 {
			query := compilePreloadAll(plan)
			rows, queryErr := queryTextRows(ctx, executor, plan.targetType.Name(), query, nil)
			if queryErr != nil {
				return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, queryErr)
			}
			if hydrateErr := hydratePreloadBatch(plan, parents, parentIndexes, rows); hydrateErr != nil {
				return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, hydrateErr)
			}
		} else {
			for start := 0; start < len(keys); start += plan.batchSize {
				end := min(start+plan.batchSize, len(keys))
				query, arguments := compilePreloadBatch(plan, keys[start:end])
				rows, queryErr := queryTextRows(ctx, executor, plan.targetType.Name(), query, arguments)
				if queryErr != nil {
					return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, queryErr)
				}
				if hydrateErr := hydratePreloadBatch(plan, parents, parentIndexes, rows); hydrateErr != nil {
					return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, hydrateErr)
				}
			}
		}
	}
	if len(plan.children) == 0 {
		return nil
	}
	targets, err := collectPreloadedTargets(plan, parents)
	if err != nil {
		return fmt.Errorf("orm: preload %s.%s: %w", plan.sourceName, plan.relationName, err)
	}
	if targets.Len() == 0 {
		return nil
	}
	for _, child := range plan.children {
		if err := executePreloadPlan(ctx, executor, child, targets); err != nil {
			return err
		}
	}
	return nil
}

func preparePreloadParents(plan *preloadPlan, parents preloadParentSet, includeArguments bool) ([]preloadKey, map[preloadLookupKey]preloadParentIndexes, error) {
	var keys []preloadKey
	if includeArguments {
		keys = make([]preloadKey, 0, parents.Len())
	}
	parentIndexes := make(map[preloadLookupKey]preloadParentIndexes, parents.Len())
	for index := 0; index < parents.Len(); index++ {
		parent := parents.Index(index)
		relation, err := preloadRelationField(parent, plan.relationIndex)
		if err != nil {
			return nil, nil, err
		}
		relation.SetZero()

		lookup, component, components, valid, err := preloadModelKey(parent, plan.sourceKey, plan.sourceKeyIndex, includeArguments)
		if err != nil {
			return nil, nil, err
		}
		if !valid {
			continue
		}
		indexes, exists := parentIndexes[lookup]
		if !exists {
			if includeArguments {
				keys = append(keys, preloadKey{component: component, components: components})
			}
			indexes.first = index
		} else {
			indexes.more = append(indexes.more, index)
		}
		parentIndexes[lookup] = indexes
	}
	return keys, parentIndexes, nil
}

func compilePreloadAll(plan *preloadPlan) string {
	if plan.junction != nil {
		return compileManyToManyPreloadAll(plan)
	}

	var query strings.Builder
	query.Grow(len(plan.targetStatement.sql) + len(plan.orderBy)*16)
	query.WriteString(plan.targetStatement.sql)
	if plan.softDelete != nil && !plan.withDeleted {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, plan.targetStatement.qualifier, plan.softDelete.column)
	}
	writePreloadOrderBy(&query, plan.targetStatement.qualifier, plan.orderBy)
	return query.String()
}

func compilePreloadBatch(plan *preloadPlan, keys []preloadKey) (string, []any) {
	if plan.junction != nil {
		return compileManyToManyPreloadBatch(plan, keys)
	}

	var query strings.Builder
	query.Grow(len(plan.targetStatement.sql) + len(keys)*len(plan.targetKeyColumns)*4 + len(" WHERE "))
	query.WriteString(plan.targetStatement.sql)
	query.WriteString(" WHERE ")
	if plan.softDelete != nil && !plan.withDeleted {
		writePreloadSoftDeletePredicate(&query, plan.targetStatement.qualifier, plan.softDelete.column)
		query.WriteString(" AND ")
	}
	arguments := writePreloadKeyPredicate(&query, plan.targetStatement.qualifier, plan.targetKeyColumns, keys)
	writePreloadOrderBy(&query, plan.targetStatement.qualifier, plan.orderBy)
	return query.String(), arguments
}

func compileManyToManyPreloadBatch(plan *preloadPlan, keys []preloadKey) (string, []any) {
	junction := plan.junction
	var query strings.Builder
	query.Grow(len(junction.tableName) + len(plan.targetTable) + len(keys)*len(junction.sourceColumns)*4 + len(plan.targetStatement.sql) + 64)
	writeManyToManyPreloadSelect(&query, plan)
	query.WriteString(" WHERE ")
	if plan.softDelete != nil && !plan.withDeleted {
		writePreloadSoftDeletePredicate(&query, "t", plan.softDelete.column)
		query.WriteString(" AND ")
	}
	arguments := writePreloadKeyPredicate(&query, "j", junction.sourceColumns, keys)
	writePreloadOrderBy(&query, "t", plan.orderBy)
	return query.String(), arguments
}

func compileManyToManyPreloadAll(plan *preloadPlan) string {
	junction := plan.junction
	var query strings.Builder
	query.Grow(len(junction.tableName) + len(plan.targetTable) + len(plan.targetStatement.sql) + 64)
	writeManyToManyPreloadSelect(&query, plan)
	if plan.softDelete != nil && !plan.withDeleted {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, "t", plan.softDelete.column)
	}
	writePreloadOrderBy(&query, "t", plan.orderBy)
	return query.String()
}

func writeManyToManyPreloadSelect(query *strings.Builder, plan *preloadPlan) {
	junction := plan.junction
	query.WriteString("SELECT ")
	for index, column := range junction.sourceColumns {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQualifiedIdentifier(query, "j", column)
	}
	for _, column := range plan.targetStatement.scanPlan.columns {
		query.WriteString(", ")
		writeQualifiedIdentifier(query, "t", column)
	}
	writeInlinePreloadColumns(query, plan.targetStatement.inlinePreloads)
	query.WriteString(" FROM ")
	writeQuotedIdentifier(query, junction.tableName)
	query.WriteString(" AS `j` JOIN ")
	writeQuotedIdentifier(query, plan.targetTable)
	query.WriteString(" AS `t` ON (")
	for index := range junction.targetColumns {
		if index != 0 {
			query.WriteString(" AND ")
		}
		writeQualifiedIdentifier(query, "t", plan.targetKeyColumns[index])
		query.WriteString(" = ")
		writeQualifiedIdentifier(query, "j", junction.targetColumns[index])
	}
	query.WriteByte(')')
	writeInlinePreloadJoins(query, plan.targetStatement.inlinePreloads)
}

func writePreloadOrderBy(query *strings.Builder, qualifier string, terms []preloadOrderTerm) {
	if len(terms) == 0 {
		return
	}
	query.WriteString(" ORDER BY ")
	for index, term := range terms {
		if index != 0 {
			query.WriteString(", ")
		}
		writeMaybeQualifiedIdentifier(query, qualifier, term.column)
		if term.direction == orderDescending {
			query.WriteString(" DESC")
		} else {
			query.WriteString(" ASC")
		}
	}
}

func writePreloadSoftDeletePredicate(query *strings.Builder, qualifier, column string) {
	writeMaybeQualifiedIdentifier(query, qualifier, column)
	query.WriteString(" IS NULL")
}

func writePreloadKeyPredicate(query *strings.Builder, qualifier string, columns []string, keys []preloadKey) []any {
	arguments := make([]any, 0, len(keys)*len(columns))
	if len(columns) == 1 {
		writeMaybeQualifiedIdentifier(query, qualifier, columns[0])
		query.WriteString(" IN (")
		for index, key := range keys {
			if index != 0 {
				query.WriteString(", ")
			}
			query.WriteByte('?')
			arguments = append(arguments, key.component)
		}
		query.WriteByte(')')
		return arguments
	}

	query.WriteByte('(')
	for keyIndex, key := range keys {
		if keyIndex != 0 {
			query.WriteString(" OR ")
		}
		query.WriteByte('(')
		for fieldIndex, column := range columns {
			if fieldIndex != 0 {
				query.WriteString(" AND ")
			}
			writeMaybeQualifiedIdentifier(query, qualifier, column)
			query.WriteString(" = ?")
			arguments = append(arguments, key.components[fieldIndex])
		}
		query.WriteByte(')')
	}
	query.WriteByte(')')
	return arguments
}

func writeMaybeQualifiedIdentifier(query *strings.Builder, qualifier, identifier string) {
	if qualifier == "" {
		writeQuotedIdentifier(query, identifier)
		return
	}
	writeQualifiedIdentifier(query, qualifier, identifier)
}

func writeQualifiedIdentifier(query *strings.Builder, qualifier, identifier string) {
	writeQuotedIdentifier(query, qualifier)
	query.WriteByte('.')
	writeQuotedIdentifier(query, identifier)
}

func hydratePreloadBatch(plan *preloadPlan, parents preloadParentSet, parentIndexes map[preloadLookupKey]preloadParentIndexes, rows resultRows) error {
	if plan.junction != nil {
		return hydrateManyToManyPreloadBatch(plan, parents, parentIndexes, rows)
	}

	decoder := plan.targetStatement.newDecoder(0)
	var reusableTarget reflect.Value
	if !plan.retainTarget {
		reusableTarget = reflect.New(plan.targetType)
	}
	var seen map[preloadLookupKey]bool
	if plan.relationKind != model.RelationHasMany {
		seen = make(map[preloadLookupKey]bool)
	}
	for rows.Next() {
		target := reusableTarget
		if !target.IsValid() {
			target = reflect.New(plan.targetType)
		} else {
			target.Elem().SetZero()
		}
		if err := decoder.scan(rows, target.Interface()); err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		lookup, _, _, valid, err := preloadModelKey(target.Elem(), plan.targetKey, plan.targetKeyIndex, false)
		if err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		if !valid {
			if plan.loadAllSources {
				continue
			}
			return closeRowsAfterError(plan.targetType.Name(), rows, fmt.Errorf("orm: hydrated %s row has a NULL relation key", plan.targetType.Name()))
		}
		indexes, ok := parentIndexes[lookup]
		if !ok {
			if plan.loadAllSources {
				continue
			}
			return closeRowsAfterError(plan.targetType.Name(), rows, fmt.Errorf("orm: hydrated %s row has an unrequested relation key", plan.targetType.Name()))
		}
		if seen != nil {
			if seen[lookup] {
				return closeRowsAfterError(plan.targetType.Name(), rows, fmt.Errorf("orm: relation returned multiple %s rows for one %s.%s key", plan.targetType.Name(), plan.sourceName, plan.relationName))
			}
			seen[lookup] = true
		}
		relation, err := preloadRelationField(parents.Index(indexes.first), plan.relationIndex)
		if err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		assignPreloadedTarget(relation, target, false)
		for _, parentIndex := range indexes.more {
			relation, err = preloadRelationField(parents.Index(parentIndex), plan.relationIndex)
			if err != nil {
				return closeRowsAfterError(plan.targetType.Name(), rows, err)
			}
			assignPreloadedTarget(relation, target, true)
		}
	}
	return finishRows(plan.targetType.Name(), rows)
}

type manyToManyPreloadDecoder struct {
	plan         *preloadPlan
	decoder      *rowDecoder
	sourceFields []any
}

func newManyToManyPreloadDecoder(plan *preloadPlan) *manyToManyPreloadDecoder {
	return &manyToManyPreloadDecoder{
		plan:         plan,
		decoder:      plan.targetStatement.newDecoder(len(plan.sourceKey)),
		sourceFields: make([]any, len(plan.sourceKey)),
	}
}

func (d *manyToManyPreloadDecoder) scan(row rowScanner, source, target reflect.Value) error {
	for index, field := range d.plan.sourceKey {
		address, err := scanFieldAddress(source, d.plan.sourceKeyIndex[index])
		if err != nil {
			clear(d.sourceFields)
			return fmt.Errorf("orm: bind junction source field %s.%s: %w", d.plan.sourceName, field.GoName(), err)
		}
		d.sourceFields[index] = address.Interface()
	}

	err := d.decoder.scanWithPrefix(row, target.Addr().Interface(), d.sourceFields)
	clear(d.sourceFields)
	if err != nil {
		return fmt.Errorf("orm: scan %s many-to-many row: %w", d.plan.targetType.Name(), err)
	}
	return nil
}

func hydrateManyToManyPreloadBatch(plan *preloadPlan, parents preloadParentSet, parentIndexes map[preloadLookupKey]preloadParentIndexes, rows resultRows) error {
	decoder := newManyToManyPreloadDecoder(plan)
	source := reflect.New(plan.sourceType).Elem()
	var reusableTarget reflect.Value
	if !plan.retainTarget {
		reusableTarget = reflect.New(plan.targetType)
	}
	for rows.Next() {
		source.SetZero()
		target := reusableTarget
		if !target.IsValid() {
			target = reflect.New(plan.targetType)
		} else {
			target.Elem().SetZero()
		}
		if err := decoder.scan(rows, source, target.Elem()); err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		lookup, _, _, valid, err := preloadModelKey(source, plan.sourceKey, plan.sourceKeyIndex, false)
		if err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		if !valid {
			if plan.loadAllSources {
				continue
			}
			return closeRowsAfterError(plan.targetType.Name(), rows, fmt.Errorf("orm: hydrated %s junction row has a NULL source key", plan.targetType.Name()))
		}
		indexes, ok := parentIndexes[lookup]
		if !ok {
			if plan.loadAllSources {
				continue
			}
			return closeRowsAfterError(plan.targetType.Name(), rows, fmt.Errorf("orm: hydrated %s row has an unrequested junction source key", plan.targetType.Name()))
		}
		relation, err := preloadRelationField(parents.Index(indexes.first), plan.relationIndex)
		if err != nil {
			return closeRowsAfterError(plan.targetType.Name(), rows, err)
		}
		assignPreloadedTarget(relation, target, false)
		for _, parentIndex := range indexes.more {
			relation, err = preloadRelationField(parents.Index(parentIndex), plan.relationIndex)
			if err != nil {
				return closeRowsAfterError(plan.targetType.Name(), rows, err)
			}
			assignPreloadedTarget(relation, target, true)
		}
	}
	return finishRows(plan.targetType.Name(), rows)
}

func assignPreloadedTarget(relation, target reflect.Value, copyPointer bool) {
	switch relation.Kind() {
	case reflect.Pointer:
		if copyPointer {
			clone := reflect.New(target.Elem().Type())
			clone.Elem().Set(target.Elem())
			target = clone
		}
		relation.Set(target)
	case reflect.Slice:
		if relation.Type().Elem().Kind() == reflect.Pointer {
			if copyPointer {
				clone := reflect.New(target.Elem().Type())
				clone.Elem().Set(target.Elem())
				target = clone
			}
			relation.Set(reflect.Append(relation, target))
			return
		}
		relation.Set(reflect.Append(relation, target.Elem()))
	}
}

func collectPreloadedTargets(plan *preloadPlan, parents preloadParentSet) (preloadParentSet, error) {
	values := make([]reflect.Value, 0)
	for index := 0; index < parents.Len(); index++ {
		relation, err := preloadRelationField(parents.Index(index), plan.relationIndex)
		if err != nil {
			return preloadParentSet{}, err
		}
		switch relation.Kind() {
		case reflect.Pointer:
			if !relation.IsNil() {
				values = append(values, relation.Elem())
			}
		case reflect.Slice:
			for itemIndex := 0; itemIndex < relation.Len(); itemIndex++ {
				item := relation.Index(itemIndex)
				if item.Kind() == reflect.Pointer {
					if item.IsNil() {
						continue
					}
					item = item.Elem()
				}
				values = append(values, item)
			}
		default:
			return preloadParentSet{}, fmt.Errorf("relation field %s.%s has unsupported hydrated type %s", plan.sourceName, plan.relationName, relation.Type())
		}
	}
	return preloadParentSet{values: values}, nil
}

func preloadModelKey(root reflect.Value, fields []model.Field, indexes [][]int, includeArguments bool) (preloadLookupKey, any, []any, bool, error) {
	if len(fields) == 1 {
		value, ok, err := preloadFieldValue(root, indexes[0])
		if err != nil {
			return preloadLookupKey{}, nil, nil, false, fmt.Errorf("read relation key field %s: %w", fields[0].GoName(), err)
		}
		if !ok {
			return preloadLookupKey{}, nil, nil, false, nil
		}
		lookup, argument, null, err := preloadKeyField(fields[0], value, includeArguments)
		if err != nil {
			return preloadLookupKey{}, nil, nil, false, fmt.Errorf("encode relation key field %s: %w", fields[0].GoName(), err)
		}
		return lookup, argument, nil, !null, nil
	}

	buffer := make([]byte, 0, len(fields)*16)
	var arguments []any
	if includeArguments {
		arguments = make([]any, 0, len(fields))
	}
	for index, field := range fields {
		value, ok, err := preloadFieldValue(root, indexes[index])
		if err != nil {
			return preloadLookupKey{}, nil, nil, false, fmt.Errorf("read relation key field %s: %w", field.GoName(), err)
		}
		if !ok {
			return preloadLookupKey{}, nil, nil, false, nil
		}
		lookup, argument, null, err := preloadKeyField(field, value, includeArguments)
		if err != nil {
			return preloadLookupKey{}, nil, nil, false, fmt.Errorf("encode relation key field %s: %w", field.GoName(), err)
		}
		if null {
			return preloadLookupKey{}, nil, nil, false, nil
		}
		buffer, err = appendCompositePreloadKey(buffer, lookup)
		if err != nil {
			return preloadLookupKey{}, nil, nil, false, err
		}
		if includeArguments {
			arguments = append(arguments, argument)
		}
	}
	return preloadLookupKey{kind: 'c', text: string(buffer)}, nil, arguments, true, nil
}

func preloadKeyField(field model.Field, value reflect.Value, includeArgument bool) (preloadLookupKey, any, bool, error) {
	original := value
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return preloadLookupKey{}, nil, true, nil
		}
		value = value.Elem()
	}

	switch field.Kind() {
	case model.KindBool:
		current := value.Bool()
		var number uint64
		if current {
			number = 1
		}
		lookup := preloadLookupKey{kind: 'b', first: number}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindInt:
		current := value.Int()
		lookup := preloadLookupKey{kind: 'i', first: uint64(current)}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindUint:
		current := value.Uint()
		lookup := preloadLookupKey{kind: 'u', first: current}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindFloat:
		current := value.Float()
		if current == 0 {
			current = 0
		}
		lookup := preloadLookupKey{kind: 'f', first: math.Float64bits(current)}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindString:
		current := value.String()
		lookup := preloadLookupKey{kind: 's', text: current}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindBytes:
		if value.IsNil() {
			return preloadLookupKey{}, nil, true, nil
		}
		current := value.Bytes()
		lookup := preloadLookupKey{kind: 'x', text: string(current)}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindTime:
		current := value.Interface().(time.Time)
		lookup := preloadLookupKey{kind: 't', first: uint64(current.Unix()), second: uint64(current.Nanosecond())}
		if !includeArgument {
			return lookup, nil, false, nil
		}
		return lookup, current, false, nil
	case model.KindCustom:
		current, err := preloadDriverValue(original)
		if err != nil {
			return preloadLookupKey{}, nil, false, err
		}
		if current == nil {
			return preloadLookupKey{}, nil, true, nil
		}
		if bytes, ok := current.([]byte); ok && bytes == nil {
			return preloadLookupKey{}, nil, true, nil
		}
		lookup, err := preloadDriverLookupKey(current)
		if !includeArgument {
			return lookup, nil, false, err
		}
		return lookup, current, false, err
	default:
		return preloadLookupKey{}, nil, false, fmt.Errorf("unsupported model field kind %d", field.Kind())
	}
}

func preloadDriverValue(value reflect.Value) (driver.Value, error) {
	for value.IsValid() {
		if value.CanInterface() {
			if valuer, ok := value.Interface().(driver.Valuer); ok {
				current, err := valuer.Value()
				if err != nil {
					return nil, err
				}
				if !driver.IsValue(current) {
					return nil, fmt.Errorf("driver.Valuer returned unsupported value %T", current)
				}
				return current, nil
			}
		}
		if value.Kind() != reflect.Pointer && value.CanAddr() && value.Addr().CanInterface() {
			if valuer, ok := value.Addr().Interface().(driver.Valuer); ok {
				current, err := valuer.Value()
				if err != nil {
					return nil, err
				}
				if !driver.IsValue(current) {
					return nil, fmt.Errorf("driver.Valuer returned unsupported value %T", current)
				}
				return current, nil
			}
		}
		if value.Kind() != reflect.Pointer || value.IsNil() {
			break
		}
		value = value.Elem()
	}
	return nil, fmt.Errorf("field does not expose driver.Valuer at runtime")
}

func preloadDriverLookupKey(value driver.Value) (preloadLookupKey, error) {
	switch current := value.(type) {
	case bool:
		var number uint64
		if current {
			number = 1
		}
		return preloadLookupKey{kind: 'b', first: number}, nil
	case int64:
		return preloadLookupKey{kind: 'i', first: uint64(current)}, nil
	case float64:
		if current == 0 {
			current = 0
		}
		return preloadLookupKey{kind: 'f', first: math.Float64bits(current)}, nil
	case string:
		return preloadLookupKey{kind: 's', text: current}, nil
	case []byte:
		return preloadLookupKey{kind: 'x', text: string(current)}, nil
	case time.Time:
		return preloadLookupKey{kind: 't', first: uint64(current.Unix()), second: uint64(current.Nanosecond())}, nil
	default:
		return preloadLookupKey{}, fmt.Errorf("driver.Valuer returned unsupported value %T", value)
	}
}

func appendCompositePreloadKey(buffer []byte, key preloadLookupKey) ([]byte, error) {
	buffer = append(buffer, key.kind)
	switch key.kind {
	case 'b', 'i', 'u', 'f':
		return binary.BigEndian.AppendUint64(buffer, key.first), nil
	case 't':
		buffer = binary.BigEndian.AppendUint64(buffer, key.first)
		return binary.BigEndian.AppendUint64(buffer, key.second), nil
	case 's', 'x':
		buffer = binary.BigEndian.AppendUint64(buffer, uint64(len(key.text)))
		return append(buffer, key.text...), nil
	default:
		return nil, fmt.Errorf("unsupported relation key representation %q", key.kind)
	}
}

func preloadFieldValue(root reflect.Value, index []int) (reflect.Value, bool, error) {
	current := root
	for _, fieldIndex := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return reflect.Value{}, false, nil
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || fieldIndex < 0 || fieldIndex >= current.NumField() {
			return reflect.Value{}, false, fmt.Errorf("invalid field index path")
		}
		current = current.Field(fieldIndex)
	}
	if !current.IsValid() || !current.CanInterface() {
		return reflect.Value{}, false, fmt.Errorf("field is not accessible")
	}
	return current, true, nil
}

func preloadRelationField(root reflect.Value, index []int) (reflect.Value, error) {
	current := root
	for depth, fieldIndex := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				if !current.CanSet() {
					return reflect.Value{}, fmt.Errorf("relation field path is not settable")
				}
				current.Set(reflect.New(current.Type().Elem()))
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || fieldIndex < 0 || fieldIndex >= current.NumField() {
			return reflect.Value{}, fmt.Errorf("invalid relation field index path")
		}
		current = current.Field(fieldIndex)
		if depth == len(index)-1 {
			if !current.CanSet() {
				return reflect.Value{}, fmt.Errorf("relation field is not settable")
			}
			return current, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("empty relation field index path")
}

func (p preloadParentSet) Len() int {
	if p.values != nil {
		return len(p.values)
	}
	if p.value.Kind() == reflect.Slice {
		return p.value.Len()
	}
	return 1
}

func (p preloadParentSet) Index(index int) reflect.Value {
	if p.values != nil {
		return p.values[index]
	}
	if p.value.Kind() == reflect.Slice {
		return p.value.Index(index)
	}
	return p.value
}
