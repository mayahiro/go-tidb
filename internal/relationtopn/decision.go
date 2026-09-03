// Package relationtopn owns the representation-independent decision rules
// for the relation-first TopN compiler rewrite.
package relationtopn

import "github.com/mayahiro/go-tidb/internal/queryshape"

const (
	ReasonMultipleCollections = "the query contains more than one collection Has predicate"
	ReasonNestedCollection    = "the collection Has predicate is nested in a logical group"
	ReasonManyToMany          = "the relation uses a many-to-many junction whose pair uniqueness is not available to the offline query compiler"
	ReasonSeekAfter           = "the query uses SeekAfter, which is not yet supported by the relation-first TopN compiler"
	ReasonRootPredicate       = "the query contains a root predicate that must be evaluated before LIMIT"
	ReasonRootSoftDelete      = "the root default soft-delete scope must be evaluated before LIMIT"
	ReasonSourceKey           = "the relation source key is not the complete root primary key"
	ReasonOrder               = "ORDER BY does not exactly match the relation source key"
	ReasonTargetUniqueness    = "the relation target primary key does not prove at most one matching row per root"
)

// Facts contains facts extracted from either runtime query values or Go
// source. A missing metadata fact is represented by MetadataKnown=false so a
// caller can stop without guessing after all representation-independent
// structural rules have been evaluated.
type Facts struct {
	CandidateCount        int
	Relation              string
	Direct                bool
	HasMany               bool
	SeekAfter             bool
	RootPredicateCount    int
	RootSoftDelete        bool
	MetadataKnown         bool
	SourceIsRootPrimary   bool
	OrderMatchesSourceKey bool
	UniquePerRoot         bool
}

// Result reports whether all facts needed for a deterministic compiler
// decision were available.
type Result struct {
	Decision queryshape.CompilerDecision
	Complete bool
}

// Outcome is one compact intermediate compiler outcome.
type Outcome uint8

const (
	OutcomeNeedsMetadata Outcome = iota
	OutcomeNone
	OutcomeOptimized
	OutcomeMultipleCollections
	OutcomeNestedCollection
	OutcomeManyToMany
	OutcomeSeekAfter
	OutcomeRootPredicate
	OutcomeRootSoftDelete
	OutcomeSourceKey
	OutcomeOrder
	OutcomeTargetUniqueness
)

var outcomeReasons = [...]string{
	OutcomeMultipleCollections: ReasonMultipleCollections,
	OutcomeNestedCollection:    ReasonNestedCollection,
	OutcomeManyToMany:          ReasonManyToMany,
	OutcomeSeekAfter:           ReasonSeekAfter,
	OutcomeRootPredicate:       ReasonRootPredicate,
	OutcomeRootSoftDelete:      ReasonRootSoftDelete,
	OutcomeSourceKey:           ReasonSourceKey,
	OutcomeOrder:               ReasonOrder,
	OutcomeTargetUniqueness:    ReasonTargetUniqueness,
}

// Decide applies relation-first TopN rules in compiler order.
func Decide(facts Facts) Result {
	outcome := DecideStructural(
		facts.CandidateCount,
		facts.Direct,
		facts.HasMany,
		facts.SeekAfter,
		facts.RootPredicateCount,
		facts.RootSoftDelete,
	)
	if outcome == OutcomeNeedsMetadata {
		if !facts.MetadataKnown {
			return Result{}
		}
		outcome = DecideMetadata(facts.SourceIsRootPrimary, facts.OrderMatchesSourceKey, facts.UniquePerRoot)
	}
	return Result{Decision: Decision(outcome, facts.Relation), Complete: true}
}

// DecideStructural applies rules that do not require resolved relation-key
// metadata.
func DecideStructural(candidateCount int, direct, hasMany, seekAfter bool, rootPredicateCount int, rootSoftDelete bool) Outcome {
	if candidateCount == 0 {
		return OutcomeNone
	}
	if candidateCount != 1 {
		return OutcomeMultipleCollections
	}
	if !direct {
		return OutcomeNestedCollection
	}
	if !hasMany {
		return OutcomeManyToMany
	}
	if seekAfter {
		return OutcomeSeekAfter
	}
	if rootPredicateCount != 1 {
		return OutcomeRootPredicate
	}
	if rootSoftDelete {
		return OutcomeRootSoftDelete
	}
	return OutcomeNeedsMetadata
}

// DecideMetadata completes a structurally eligible decision with resolved
// relation-key metadata.
func DecideMetadata(sourceIsRootPrimary, orderMatchesSourceKey, uniquePerRoot bool) Outcome {
	if !sourceIsRootPrimary {
		return OutcomeSourceKey
	}
	if !orderMatchesSourceKey {
		return OutcomeOrder
	}
	if !uniquePerRoot {
		return OutcomeTargetUniqueness
	}
	return OutcomeOptimized
}

// Decision converts one complete outcome to the neutral captured form.
func Decision(outcome Outcome, relation string) queryshape.CompilerDecision {
	if outcome == OutcomeNone {
		return queryshape.CompilerDecision{Rewrite: queryshape.CompilerRewriteNone}
	}
	if outcome == OutcomeOptimized {
		return queryshape.CompilerDecision{Rewrite: queryshape.CompilerRewriteRelationTopN, Relation: relation}
	}
	return queryshape.CompilerDecision{
		Rewrite:  queryshape.CompilerRewriteRelationTopNFallback,
		Relation: relation,
		Reason:   outcomeReasons[outcome],
	}
}
