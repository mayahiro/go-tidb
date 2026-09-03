package relationtopn

import (
	"testing"

	"github.com/mayahiro/go-tidb/internal/queryshape"
)

func TestDecideUsesStableCompilerOrder(t *testing.T) {
	t.Parallel()

	base := Facts{
		CandidateCount:        1,
		Relation:              "Links",
		Direct:                true,
		RootPredicateCount:    1,
		MetadataKnown:         true,
		SourceIsRootPrimary:   true,
		OrderMatchesSourceKey: true,
		UniquePerRoot:         true,
	}
	tests := []struct {
		name   string
		change func(*Facts)
		want   queryshape.CompilerRewrite
		reason string
	}{
		{name: "optimized", want: queryshape.CompilerRewriteRelationTopN},
		{name: "multiple", change: func(facts *Facts) { facts.CandidateCount = 2 }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonMultipleCollections},
		{name: "nested", change: func(facts *Facts) { facts.Direct = false }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonNestedCollection},
		{name: "seek", change: func(facts *Facts) { facts.SeekAfter = true }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonSeekAfter},
		{name: "root predicate", change: func(facts *Facts) { facts.RootPredicateCount = 2 }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonRootPredicate},
		{name: "soft delete", change: func(facts *Facts) { facts.RootSoftDelete = true }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonRootSoftDelete},
		{name: "source key", change: func(facts *Facts) { facts.SourceIsRootPrimary = false }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonSourceKey},
		{name: "order", change: func(facts *Facts) { facts.OrderMatchesSourceKey = false }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonOrder},
		{name: "uniqueness", change: func(facts *Facts) { facts.UniquePerRoot = false }, want: queryshape.CompilerRewriteRelationTopNFallback, reason: ReasonTargetUniqueness},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			facts := base
			if test.change != nil {
				test.change(&facts)
			}
			result := Decide(facts)
			if !result.Complete || result.Decision.Rewrite != test.want || result.Decision.Relation != "Links" || result.Decision.Reason != test.reason {
				t.Fatalf("Decide() = %#v, want rewrite %q and reason %q", result, test.want, test.reason)
			}
		})
	}
}

func TestDecideWaitsOnlyForMetadataFacts(t *testing.T) {
	t.Parallel()

	result := Decide(Facts{
		CandidateCount:     1,
		Relation:           "Links",
		Direct:             true,
		RootPredicateCount: 1,
	})
	if result.Complete || result.Decision != (queryshape.CompilerDecision{}) {
		t.Fatalf("Decide() = %#v, want incomplete", result)
	}
}
