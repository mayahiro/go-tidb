package orm

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type viaBenchmarkParent struct {
	model.Meta `tidbgo:"table=via_parents"`
	ID         int64                `tidbgo:",pk"`
	Targets    []viaBenchmarkTarget `tidbgo:"many_to_many,via=Edges.Target"`
	Edges      []viaBenchmarkEdge   `tidbgo:"has_many,join=ID:ParentID"`
}
type viaBenchmarkEdge struct {
	model.Meta `tidbgo:"table=via_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	TargetID   int64
	Priority   int
	Target     *viaBenchmarkTarget `tidbgo:"belongs_to"`
}
type viaBenchmarkTarget struct {
	model.Meta `tidbgo:"table=via_targets"`
	ID         int64 `tidbgo:",pk"`
	Name       string
}

var viaBenchmarkSink []viaBenchmarkParent

func BenchmarkViaPreload100Parents300Targets(b *testing.B) {
	for _, direct := range []bool{true, false} {
		name := "via"
		if !direct {
			name = "edge_then_extract"
		}
		b.Run(name, func(b *testing.B) {
			parents := make([][]driver.Value, 100)
			targets := make([][]driver.Value, 300)
			for index := range parents {
				parents[index] = []driver.Value{int64(index + 1)}
			}
			for index := range targets {
				parentID, targetID := int64(index/3+1), int64(index+1)
				if direct {
					targets[index] = []driver.Value{parentID, targetID, "value"}
				} else {
					targets[index] = []driver.Value{targetID, parentID, targetID, "value"}
				}
			}
			query := Query[viaBenchmarkParent]().Select("ID")
			columns := []string{"parent_id", "id", "name"}
			if direct {
				query.Preload("Targets", PreloadOrderBy(Asc("Edges.Priority"), Asc("ID")))
			} else {
				query.Preload("Edges", PreloadFields("TargetID"), PreloadOrderBy(Asc("Priority"), Asc("TargetID"))).Preload("Edges.Target")
				columns = []string{"target_id", "parent_id", "id", "name"}
			}
			state := &preloadTestState{repeat: true, responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: parents}, {columns: columns, values: targets},
			}}
			database := openPreloadTestDB(b, state)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				values, err := query.All(ctx, database)
				if err != nil {
					b.Fatal(err)
				}
				if !direct {
					for index := range values {
						values[index].Targets = make([]viaBenchmarkTarget, 0, len(values[index].Edges))
						for _, edge := range values[index].Edges {
							if edge.Target != nil {
								values[index].Targets = append(values[index].Targets, *edge.Target)
							}
						}
					}
				}
				if len(values) != 100 || len(values[0].Targets) != 3 || values[99].Targets[2].ID != 300 {
					b.Fatal("incorrect benchmark results")
				}
				viaBenchmarkSink = values
			}
		})
	}
}
