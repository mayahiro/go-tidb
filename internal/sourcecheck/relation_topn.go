package sourcecheck

import (
	"github.com/mayahiro/go-tidb/internal/modelmeta"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/internal/relationtopn"
)

type sourceRelationTopNAnalysis struct {
	candidate bool
	exact     bool
	decision  queryshape.CompilerDecision
	relation  sourceResolvedRelation
	predicate sourceHasPredicate
}

func (analyzer *sourceAnalyzer) analyzeSourceRelationTopN(summary sourceQuerySummary) sourceRelationTopNAnalysis {
	pattern := summary.pattern
	if pattern.limit.state != sourceBoundPositive || pattern.order == sourceOrderAbsent || len(pattern.hasPredicates) == 0 {
		return sourceRelationTopNAnalysis{}
	}
	analysis := sourceRelationTopNAnalysis{candidate: true}
	if !pattern.predicatesKnown || !pattern.rootCountKnown || !pattern.orderTermsKnown ||
		pattern.seekAfter == sourceToggleUnknown || pattern.withDeleted == sourceToggleUnknown {
		return analysis
	}

	collectionCount := 0
	for _, predicate := range pattern.hasPredicates {
		if !predicate.relationKnown {
			return analysis
		}
		relation, resolved := analyzer.resolveSourceRelation(summary.model, predicate.relation)
		if !resolved {
			return analysis
		}
		if !relation.collection() {
			continue
		}
		collectionCount++
		if collectionCount == 1 {
			analysis.relation = relation
			analysis.predicate = predicate
		}
	}
	if collectionCount == 0 {
		return sourceRelationTopNAnalysis{}
	}

	model, exists := analyzer.models[summary.model]
	if !exists || model.ambiguous {
		return analysis
	}
	outcome := relationtopn.DecideStructural(
		collectionCount,
		analysis.predicate.direct,
		pattern.seekAfter == sourceTogglePresent,
		pattern.rootPredicateCount,
		model.softDelete && pattern.withDeleted != sourceTogglePresent,
	)
	if outcome != relationtopn.OutcomeNeedsMetadata {
		analysis.exact = true
		analysis.decision = relationtopn.Decision(outcome, analysis.relation.name)
		return analysis
	}

	target, exists := analyzer.models[analysis.relation.target]
	if !exists || target.ambiguous || !sourceModelHasUniqueKey(target) {
		return analysis
	}
	relationFields := analysis.relation.targetFields
	if analysis.relation.kind == modelmeta.RelationManyToMany {
		relationFields = nil
	}
	outcome = relationtopn.DecideMetadata(
		sameSourceFieldNames(analysis.relation.sourceFields, model.primaryFields),
		sourceOrderMatchesFields(pattern.orderTerms, analysis.relation.sourceFields),
		sourceRelationUniquePerRoot(
			target,
			relationFields,
			analysis.predicate.equalityFields,
		),
	)
	analysis.exact = true
	analysis.decision = relationtopn.Decision(outcome, analysis.relation.name)
	return analysis
}

func sourceModelHasUniqueKey(model *sourceModel) bool {
	return len(model.primaryFields) != 0 || len(model.uniqueKeys) != 0
}

func sameSourceFieldNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sourceOrderMatchesFields(order sourceOrderTerms, fields []string) bool {
	if order.count != len(fields) {
		return false
	}
	for index := range fields {
		term := order.at(index)
		if term.field != fields[index] {
			return false
		}
		switch term.direction {
		case queryshape.OrderAscending, queryshape.OrderDescending:
		default:
			return false
		}
	}
	return true
}

func sourceRelationUniquePerRoot(model *sourceModel, relationFields, equalityFields []string) bool {
	if sourceRelationKeyCovered(model.primaryFields, relationFields, equalityFields) {
		return true
	}
	for _, key := range model.uniqueKeys {
		if sourceRelationKeyCovered(key.fields, relationFields, equalityFields) {
			return true
		}
	}
	return false
}

func sourceRelationKeyCovered(keyFields, relationFields, equalityFields []string) bool {
	for _, field := range keyFields {
		if sourceStringExists(relationFields, field) || sourceStringExists(equalityFields, field) {
			continue
		}
		return false
	}
	return len(keyFields) != 0
}

func sourceStringExists(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
