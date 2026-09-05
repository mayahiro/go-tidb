package orm

import (
	"context"
	"fmt"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/model"
)

func beginConditionalMutationObservation(ctx context.Context, operation StatementOperation, compiled compiledMutation, predicates []predicate, withDeleted bool, terminal string) *statementObservation {
	value := statementObserverContext(ctx)
	if value == nil || value.observer == nil && value.runtimeCapture == nil {
		return nil
	}
	metadata := statementRuntimeMetadata{}
	if value.runtimeCapture != nil {
		metadata = runtimeTypedMutationMetadata(compiled.modelName, terminal)
		shape, err := buildMutationShape(compiled.descriptor, predicates, withDeleted)
		if err != nil {
			metadata.metadataError = err.Error()
		} else {
			metadata.mutation = &shape
		}
	}
	// Prepare optional metadata before starting the target statement timer.
	return beginStatementObservationForContext(value, operation, compiled.sql, compiled.arguments, metadata)
}

func buildMutationShape(descriptor *model.Descriptor, predicates []predicate, withDeleted bool) (queryshape.Mutation, error) {
	if descriptor == nil {
		return queryshape.Mutation{}, fmt.Errorf("orm: runtime conditional write model is unavailable")
	}
	conditions, err := buildMutationShapePredicates(descriptor, predicates)
	if err != nil {
		return queryshape.Mutation{}, err
	}
	shape := queryshape.Mutation{Model: descriptor.Name(), Table: descriptor.TableName(), Predicates: conditions}
	if field, active := activeSoftDeleteField(descriptor, withDeleted); active {
		shape.SoftDeleteColumn = field.ColumnName()
	}
	return shape, nil
}

func buildMutationShapePredicates(descriptor *model.Descriptor, predicates []predicate) ([]queryshape.MutationPredicate, error) {
	conditions := make([]queryshape.MutationPredicate, len(predicates))
	for index, current := range predicates {
		operator, err := queryShapePredicateOperator(current.operator)
		if err != nil {
			return nil, err
		}
		condition := queryshape.MutationPredicate{Operator: operator}
		switch current.operator {
		case predicateAnd, predicateOr, predicateNot:
			condition.Children, err = buildMutationShapePredicates(descriptor, current.children)
			if err != nil {
				return nil, err
			}
		default:
			field, exists := descriptor.FieldByGoName(current.field)
			if !exists || field.IsComputed() || current.operator == predicateHasRelation {
				return nil, fmt.Errorf("orm: runtime conditional write field %s.%s is unavailable", descriptor.Name(), current.field)
			}
			condition.Column = field.ColumnName()
			condition.EmptyList = (current.operator == predicateIn || current.operator == predicateNotIn) && len(current.values) == 0
		}
		conditions[index] = condition
	}
	return conditions, nil
}
