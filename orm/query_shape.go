package orm

import (
	"fmt"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/model"
)

func buildSelectQueryShape(
	descriptor *model.Descriptor,
	selection *selectQuery,
	compiled compiledSelect,
	relationTopN relationTopNAnalysis,
) (queryshape.Query, error) {
	predicates, err := buildQueryShapePredicates(descriptor, selection.predicates)
	if err != nil {
		return queryshape.Query{}, err
	}
	order, err := buildQueryShapeOrder(descriptor, selection.orderBy)
	if err != nil {
		return queryshape.Query{}, err
	}

	shape := queryshape.Query{
		Model:      descriptor.Name(),
		Table:      descriptor.TableName(),
		Projection: append([]string(nil), compiled.statement.scanPlan.columns...),
		Predicates: predicates,
		Order:      order,
		SeekAfter:  selection.seekAfter != nil,
		Limit: queryshape.Bound{
			Set:      selection.pagination.limitSet,
			Positive: selection.pagination.limit > 0,
			Value:    selection.pagination.limit,
		},
		Offset: queryshape.Bound{
			Set:      selection.pagination.offsetSet,
			Positive: selection.pagination.offset > 0,
			Value:    selection.pagination.offset,
		},
		WithDeleted: selection.withDeleted,
		Preloads:    buildQueryShapePreloads(compiled.preloads, ""),
		Compiler:    buildQueryShapeCompilerDecision(relationTopN),
	}
	if softDeleteField, active := activeSoftDeleteField(descriptor, selection.withDeleted); active {
		shape.SoftDeleteColumn = softDeleteField.ColumnName()
	}
	shape.IndexAccesses = buildQueryShapeIndexAccesses(descriptor, selection, shape, relationTopN)
	return shape, nil
}

func buildQueryShapePredicates(descriptor *model.Descriptor, predicates []predicate) ([]queryshape.Predicate, error) {
	result := make([]queryshape.Predicate, len(predicates))
	for index := range predicates {
		current, err := buildQueryShapePredicate(descriptor, predicates[index])
		if err != nil {
			return nil, err
		}
		result[index] = current
	}
	return result, nil
}

func buildQueryShapePredicate(descriptor *model.Descriptor, current predicate) (queryshape.Predicate, error) {
	operator, err := queryShapePredicateOperator(current.operator)
	if err != nil {
		return queryshape.Predicate{}, err
	}
	result := queryshape.Predicate{
		Operator:   operator,
		Field:      current.field,
		ValueCount: len(current.values),
	}

	switch current.operator {
	case predicateAnd, predicateOr, predicateNot:
		result.Children, err = buildQueryShapePredicates(descriptor, current.children)
		return result, err
	case predicateHasRelation:
		relation, exists := descriptor.RelationByName(current.field)
		if !exists {
			return queryshape.Predicate{}, fmt.Errorf("orm: query shape relation %s.%s is unavailable", descriptor.Name(), current.field)
		}
		target, describeErr := model.DescribeType(relation.TargetType())
		if describeErr != nil {
			return queryshape.Predicate{}, fmt.Errorf("orm: describe query shape relation target %s.%s: %w", descriptor.Name(), relation.GoName(), describeErr)
		}
		result.Field = ""
		result.Relation = relation.GoName()
		result.RelationKind = string(relation.Kind())
		result.RelationSourceColumns = relationFieldColumns(relation.SourceKey())
		result.RelationTargetColumns = relationFieldColumns(relation.TargetKey())
		result.Table = target.TableName()
		if junction, exists := relation.Junction(); exists {
			result.JunctionTable = junction.TableName()
			result.JunctionSourceColumns = junction.SourceColumns()
			result.JunctionTargetColumns = junction.TargetColumns()
		}
		if softDeleteField, exists := target.SoftDeleteField(); exists {
			result.SoftDeleteColumn = softDeleteField.ColumnName()
		}
		result.Children, err = buildQueryShapePredicates(target, current.children)
		return result, err
	default:
		field, exists := descriptor.FieldByGoName(current.field)
		if !exists || field.IsComputed() {
			return queryshape.Predicate{}, fmt.Errorf("orm: query shape field %s.%s is unavailable", descriptor.Name(), current.field)
		}
		result.Table = descriptor.TableName()
		result.Column = field.ColumnName()
		return result, nil
	}
}

func queryShapePredicateOperator(operator predicateOperator) (queryshape.PredicateOperator, error) {
	switch operator {
	case predicateEqual:
		return queryshape.PredicateEqual, nil
	case predicateNotEqual:
		return queryshape.PredicateNotEqual, nil
	case predicateGreaterThan:
		return queryshape.PredicateGreaterThan, nil
	case predicateGreaterThanOrEqual:
		return queryshape.PredicateGreaterThanOrEqual, nil
	case predicateLessThan:
		return queryshape.PredicateLessThan, nil
	case predicateLessThanOrEqual:
		return queryshape.PredicateLessThanOrEqual, nil
	case predicateIn:
		return queryshape.PredicateIn, nil
	case predicateNotIn:
		return queryshape.PredicateNotIn, nil
	case predicateIsNull:
		return queryshape.PredicateIsNull, nil
	case predicateIsNotNull:
		return queryshape.PredicateIsNotNull, nil
	case predicateBetween:
		return queryshape.PredicateBetween, nil
	case predicateContains:
		return queryshape.PredicateContains, nil
	case predicateHasPrefix:
		return queryshape.PredicateHasPrefix, nil
	case predicateHasSuffix:
		return queryshape.PredicateHasSuffix, nil
	case predicateHasRelation:
		return queryshape.PredicateHasRelation, nil
	case predicateAnd:
		return queryshape.PredicateAnd, nil
	case predicateOr:
		return queryshape.PredicateOr, nil
	case predicateNot:
		return queryshape.PredicateNot, nil
	default:
		return "", fmt.Errorf("orm: query shape has unknown predicate operator %d", operator)
	}
}

func buildQueryShapeOrder(descriptor *model.Descriptor, order []orderTerm) ([]queryshape.OrderTerm, error) {
	result := make([]queryshape.OrderTerm, len(order))
	for index := range order {
		field, err := resolveOrderField(descriptor, order, index)
		if err != nil {
			return nil, err
		}
		direction, err := queryShapeOrderDirection(order[index].direction)
		if err != nil {
			return nil, err
		}
		result[index] = queryshape.OrderTerm{
			Field:     field.GoName(),
			Column:    field.ColumnName(),
			Direction: direction,
		}
	}
	return result, nil
}

func queryShapeOrderDirection(direction orderDirection) (queryshape.OrderDirection, error) {
	switch direction {
	case orderAscending:
		return queryshape.OrderAscending, nil
	case orderDescending:
		return queryshape.OrderDescending, nil
	default:
		return "", fmt.Errorf("orm: query shape has unknown order direction %d", direction)
	}
}

func buildQueryShapePreloads(plans []*preloadPlan, parentPath string) []queryshape.Preload {
	if len(plans) == 0 {
		return nil
	}
	result := make([]queryshape.Preload, len(plans))
	for index := range plans {
		plan := plans[index]
		path := plan.relationName
		if parentPath != "" {
			path = parentPath + "." + path
		}
		order := make([]queryshape.OrderTerm, len(plan.orderBy))
		for orderIndex := range plan.orderBy {
			direction, _ := queryShapeOrderDirection(plan.orderBy[orderIndex].direction)
			order[orderIndex] = queryshape.OrderTerm{
				Column:    plan.orderBy[orderIndex].column,
				Direction: direction,
			}
		}
		result[index] = queryshape.Preload{
			Path:           path,
			Relation:       plan.relationName,
			Kind:           string(plan.relationKind),
			Table:          plan.targetTable,
			SourceColumns:  relationFieldColumns(plan.sourceKey),
			TargetColumns:  append([]string(nil), plan.targetKeyColumns...),
			Projection:     append([]string(nil), plan.targetStatement.scanPlan.columns...),
			Order:          order,
			Inline:         plan.inline,
			LoadAllSources: plan.loadAllSources,
			BatchSize:      plan.batchSize,
			WithDeleted:    plan.withDeleted,
			Children:       buildQueryShapePreloads(plan.children, path),
		}
		if plan.softDelete != nil && !plan.withDeleted {
			result[index].SoftDeleteColumn = plan.softDelete.column
		}
		if plan.junction != nil {
			result[index].JunctionTable = plan.junction.tableName
			result[index].JunctionSourceColumns = append([]string(nil), plan.junction.sourceColumns...)
			result[index].JunctionTargetColumns = append([]string(nil), plan.junction.targetColumns...)
		}
	}
	return result
}

func buildQueryShapeCompilerDecision(analysis relationTopNAnalysis) queryshape.CompilerDecision {
	switch {
	case analysis.optimized:
		return queryshape.CompilerDecision{
			Rewrite:  queryshape.CompilerRewriteRelationTopN,
			Relation: analysis.relationName,
		}
	case analysis.candidate:
		return queryshape.CompilerDecision{
			Rewrite:  queryshape.CompilerRewriteRelationTopNFallback,
			Relation: analysis.relationName,
			Reason:   analysis.reason,
		}
	default:
		return queryshape.CompilerDecision{Rewrite: queryshape.CompilerRewriteNone}
	}
}

func buildQueryShapeIndexAccesses(
	descriptor *model.Descriptor,
	selection *selectQuery,
	shape queryshape.Query,
	analysis relationTopNAnalysis,
) []queryshape.IndexAccess {
	if !selection.pagination.limitSet || selection.pagination.limit <= 0 || !queryShapeHasUniformOrder(shape.Order) {
		return nil
	}
	if analysis.optimized {
		equalityColumns, exact := queryShapeConjunctiveEqualityColumns(analysis.plan.metadata.target, analysis.plan.predicate.children)
		if !exact {
			return nil
		}
		if softDeleteField, exists := analysis.plan.metadata.target.SoftDeleteField(); exists {
			equalityColumns = appendQueryShapeColumn(equalityColumns, softDeleteField.ColumnName())
		}
		return []queryshape.IndexAccess{{
			Kind:            queryshape.IndexAccessRelationTopN,
			Table:           analysis.plan.metadata.target.TableName(),
			Relation:        analysis.relationName,
			EqualityColumns: equalityColumns,
			OrderColumns:    append([]string(nil), analysis.plan.metadata.targetColumns...),
		}}
	}
	if predicatesHaveRelation(selection.predicates) {
		return nil
	}
	equalityColumns, exact := queryShapeConjunctiveEqualityColumns(descriptor, selection.predicates)
	if !exact {
		return nil
	}
	if shape.SoftDeleteColumn != "" {
		equalityColumns = appendQueryShapeColumn(equalityColumns, shape.SoftDeleteColumn)
	}
	orderColumns := make([]string, len(shape.Order))
	for index := range shape.Order {
		orderColumns[index] = shape.Order[index].Column
	}
	return []queryshape.IndexAccess{{
		Kind:            queryshape.IndexAccessRootOrderedLimit,
		Table:           descriptor.TableName(),
		EqualityColumns: equalityColumns,
		OrderColumns:    orderColumns,
	}}
}

func queryShapeHasUniformOrder(order []queryshape.OrderTerm) bool {
	if len(order) == 0 {
		return false
	}
	direction := order[0].Direction
	for index := 1; index < len(order); index++ {
		if order[index].Direction != direction {
			return false
		}
	}
	return true
}

func queryShapeConjunctiveEqualityColumns(descriptor *model.Descriptor, predicates []predicate) ([]string, bool) {
	columns := make([]string, 0, len(predicates))
	for index := range predicates {
		current := predicates[index]
		switch current.operator {
		case predicateEqual:
			field, exists := descriptor.FieldByGoName(current.field)
			if !exists || field.IsComputed() {
				return nil, false
			}
			columns = appendQueryShapeColumn(columns, field.ColumnName())
		case predicateAnd:
			children, exact := queryShapeConjunctiveEqualityColumns(descriptor, current.children)
			if !exact {
				return nil, false
			}
			for _, column := range children {
				columns = appendQueryShapeColumn(columns, column)
			}
		default:
			return nil, false
		}
	}
	return columns, true
}

func appendQueryShapeColumn(columns []string, column string) []string {
	for _, existing := range columns {
		if existing == column {
			return columns
		}
	}
	return append(columns, column)
}
