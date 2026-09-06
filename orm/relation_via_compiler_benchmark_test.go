package orm

import (
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type viaTopNParent struct {
	model.Meta `tidbgo:"table=via_topn_parents"`
	ID         int64 `tidbgo:",pk"`
	V0         string
	Edges      []viaTopNEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []viaTopNTarget `tidbgo:"many_to_many,via=Edges.Target"`
}

type viaTopNEdge struct {
	model.Meta `tidbgo:"table=via_topn_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64 `tidbgo:",unique=pair"`
	TargetID   int64 `tidbgo:",unique=pair"`
	V0         int64
	Target     *viaTopNTarget `tidbgo:"belongs_to"`
}

type viaTopNTarget struct {
	model.Meta `tidbgo:"table=via_topn_targets"`
	ID         int64  `tidbgo:",pk"`
	V0         string `tidbgo:",unique=v0"`
}

// BenchmarkViaRelationCompiler measures warmed offline compilation, not
// execution time, driver conversion, or database RU.
func BenchmarkViaRelationCompiler(b *testing.B) {
	for _, terminal := range []string{"list", "count", "fallback"} {
		b.Run(terminal, func(b *testing.B) {
			query := Query[viaTopNParent]().Where(Has("Targets", Equal("ID", int64(7))))
			if terminal != "count" {
				query.OrderBy(Desc("ID")).Limit(20)
			}
			if terminal == "fallback" {
				query.Where(Equal("V0", "value"))
			}
			execute := func() {
				if terminal == "count" {
					compiled, err := query.compileCount()
					if err != nil {
						b.Fatal(err)
					}
					compiledCountBenchmarkSink = compiled
				} else {
					statement, args, err := query.Build()
					if err != nil {
						b.Fatal(err)
					}
					relationTopNSQLSink, relationTopNArgumentsSink = statement, args
				}
			}
			execute()
			b.ReportAllocs()
			for b.Loop() {
				execute()
			}
		})
	}
}
