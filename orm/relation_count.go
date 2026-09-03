package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

type relationCountPlan struct {
	predicate    predicate
	metadata     *relationTopNMetadata
	junctionOnly bool
}

func compileRelationCount(descriptor *model.Descriptor, selection *selectQuery) (compiledCount, bool, error) {
	plan, optimized, err := analyzeRelationCount(descriptor, selection)
	if err != nil || !optimized {
		return compiledCount{}, optimized, err
	}

	metadata := plan.metadata
	associationTable := metadata.target.TableName()
	if metadata.junction != nil {
		associationTable = metadata.junction.tableName
	}
	argumentCount, sqlCapacity := predicateCompileCapacity(plan.predicate.children)
	if predicatesHaveRelation(plan.predicate.children) {
		sqlCapacity += relationPredicateExtraSQLCapacity(metadata.target, plan.predicate.children)
	}
	softDeleteField, filterSoftDeleted := metadata.target.SoftDeleteField()
	if filterSoftDeleted {
		sqlCapacity += len(softDeleteField.ColumnName()) + len("`` IS NULL")
	}
	sqlCapacity += len("SELECT COUNT(*) FROM `` WHERE ") + len(associationTable)

	var query strings.Builder
	query.Grow(sqlCapacity)
	query.WriteString("SELECT COUNT(*) FROM ")
	writeQuotedIdentifier(&query, associationTable)

	var arguments []any
	if argumentCount != 0 {
		arguments = make([]any, 0, argumentCount)
	}
	predicates := predicateCompiler{
		descriptor: metadata.target,
		query:      &query,
		arguments:  arguments,
		operation:  "COUNT",
	}
	wroteWhere := false
	if filterSoftDeleted {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, "", softDeleteField.ColumnName())
		wroteWhere = true
	}
	for index := range plan.predicate.children {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		if plan.junctionOnly {
			err = predicates.writeRelationTopNJunctionPredicate(plan.predicate.children[index], metadata)
		} else {
			err = predicates.write(plan.predicate.children[index])
		}
		if err != nil {
			return compiledCount{}, false, fmt.Errorf("orm: COUNT relation predicate %s.%s: %w", descriptor.Name(), metadata.relationName, err)
		}
	}

	return compiledCount{
		modelName: descriptor.Name(),
		sql:       query.String(),
		arguments: predicates.arguments,
	}, true, nil
}

func analyzeRelationCount(descriptor *model.Descriptor, selection *selectQuery) (relationCountPlan, bool, error) {
	if selection.pagination.limitSet || selection.pagination.offsetSet || selection.seekAfter != nil {
		return relationCountPlan{}, false, nil
	}
	if _, filterSoftDeleted := activeSoftDeleteField(descriptor, selection.withDeleted); filterSoftDeleted {
		return relationCountPlan{}, false, nil
	}

	search := rootCollectionHasSearch{}
	for index := range selection.predicates {
		collectRootCollectionHas(descriptor, selection.predicates[index], true, &search)
	}
	if search.count != 1 || !search.first.direct || len(selection.predicates) != 1 {
		return relationCountPlan{}, false, nil
	}

	metadata, err := relationTopNMetadataFor(descriptor, search.first.relation)
	if err != nil {
		return relationCountPlan{}, false, err
	}
	if !metadata.sourceIsRootPrimaryKey || !relationTopNUniquePerRoot(metadata, search.first.predicate.children) {
		return relationCountPlan{}, false, nil
	}

	junctionOnly := metadata.junction != nil
	if junctionOnly && !relationTopNCanFilterJunction(metadata, search.first.predicate.children) {
		return relationCountPlan{}, false, nil
	}
	return relationCountPlan{
		predicate:    search.first.predicate,
		metadata:     metadata,
		junctionOnly: junctionOnly,
	}, true, nil
}
