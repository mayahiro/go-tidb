package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type viaCostParent struct {
	model.Meta `tidbgo:"table=via_cost_parents"`
	ID         int64 `tidbgo:",pk"`
	V0         string
	V1         *int64
	Edges      []viaCostEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []viaCostTarget `tidbgo:"many_to_many,via=Edges.Target"`
	Notes      []viaCostNote   `tidbgo:"has_many,join=ID:ParentID"`
}

type viaCostEdge struct {
	model.Meta `tidbgo:"table=via_cost_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	TargetID   int64
	Priority   int64
	Target     *viaCostTarget `tidbgo:"belongs_to"`
}

type viaCostTarget struct {
	model.Meta `tidbgo:"table=via_cost_targets"`
	ID         int64 `tidbgo:",pk"`
	V0         string
	V1         *int64
	V2         string
	V3         *string
	V4         int64
	V5         *int64
	V6         string
	V7         *string
	V8         int64
	V9         *int64
	V10        string
	V11        *string
	V12        time.Time
	V13        *time.Time
	V14        string
	V15        []byte
	V16        string
}

type viaCostNote struct {
	model.Meta `tidbgo:"table=via_cost_notes"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	V0         string
}

// The fixture preserves column widths, NULLs, shared targets and row ordering,
// without depending on an application model or a live database.
func viaCostQueryAndRows(parents, edges, targets int, narrow, via bool) (*SelectQuery[viaCostParent], []*preloadTestResponse) {
	query := Query[viaCostParent]().OrderBy(Asc("ID")).Limit(int64(parents)).Preload("Notes")
	targetColumns := []string{"id", "v0", "v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9", "v10", "v11", "v12", "v13", "v14", "v15", "v16"}
	var fields []PreloadOption
	if narrow {
		targetColumns = targetColumns[:2]
		fields = []PreloadOption{PreloadFields("ID", "V0")}
	}
	if via {
		options := append([]PreloadOption{PreloadOrderBy(Asc("Edges.Priority"), Asc("ID"))}, fields...)
		query.Preload("Targets", options...)
	} else {
		query.Preload("Edges", PreloadOrderBy(Asc("Priority"), Asc("TargetID"))).Preload("Edges.Target", fields...)
	}
	root := &preloadTestResponse{columns: []string{"id", "v0", "v1"}}
	notes := &preloadTestResponse{columns: []string{"id", "parent_id", "v0"}}
	for i := 0; i < parents; i++ {
		root.values = append(root.values, []driver.Value{int64(i + 1), "parent", nil})
		notes.values = append(notes.values, []driver.Value{int64(i + 1), int64(i + 1), "note"})
	}
	columns := []string{"parent_id"}
	if !via {
		columns = []string{"id", "parent_id", "target_id", "priority"}
	}
	relations := &preloadTestResponse{columns: append(columns, targetColumns...)}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Ascending priority is unique here; repeated target IDs still exercise
	// value ownership when targets are shared by multiple edges or parents.
	for i := 0; i < edges; i++ {
		parent, target := int64(i%parents+1), int64(i%targets+1)
		row := []driver.Value{parent}
		if !via {
			row = []driver.Value{int64(i + 1), parent, target, int64(i)}
		}
		values := []driver.Value{target, []byte("value"), nil, []byte("text"), []byte("optional"), int64(5), nil, []byte("value"), nil, int64(9), int64(10), []byte("text"), nil, stamp, stamp, []byte("value"), []byte("bytes"), []byte("text")}
		if narrow {
			values = values[:2]
		}
		relations.values = append(relations.values, append(row, values...))
	}
	return query, []*preloadTestResponse{root, notes, relations}
}

func viaCostRead(query *SelectQuery[viaCostParent], executor QueryExecutor, via bool) ([]viaCostParent, error) {
	values, err := query.All(context.Background(), executor)
	if err != nil {
		return nil, err
	}
	if !via {
		for i := range values {
			for _, edge := range values[i].Edges {
				if edge.Target != nil {
					values[i].Targets = append(values[i].Targets, *edge.Target)
				}
			}
			values[i].Edges = nil
		}
	}
	return values, nil
}

var viaCostSink []viaCostParent

func TestViaCostFixtureResults(t *testing.T) {
	for _, edges := range []int{0, 1, 100} {
		for _, narrow := range []bool{false, true} {
			var reference []viaCostParent
			for _, via := range []bool{false, true} {
				query, responses := viaCostQueryAndRows(20, edges, 12, narrow, via)
				state := &preloadTestState{record: true, responses: responses}
				values, err := viaCostRead(query, openPreloadTestDB(t, state), via)
				if err != nil {
					t.Fatal(err)
				}
				if len(preloadCalls(state)) != 3 {
					t.Fatal("expected three statements")
				}
				if via && !reflect.DeepEqual(values, reference) {
					t.Fatalf("edges=%d narrow=%t results differ", edges, narrow)
				}
				reference = values
			}
		}
	}
}

func BenchmarkViaPreloadCost(b *testing.B) {
	for _, scenario := range []struct {
		name                    string
		parents, edges, targets int
		narrow                  bool
	}{
		{"empty", 20, 0, 12, false},
		{"single", 1, 1, 1, false},
		{"shared", 20, 100, 12, false},
		{"narrow", 20, 100, 12, true},
		{"distinct", 100, 300, 300, false},
		{"fanout", 20, 2000, 12, false},
	} {
		for _, via := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/via_%t", scenario.name, via), func(b *testing.B) {
				query, responses := viaCostQueryAndRows(scenario.parents, scenario.edges, scenario.targets, scenario.narrow, via)
				db := openPreloadTestDB(b, &preloadTestState{repeat: true, responses: responses})
				b.ReportAllocs()
				for b.Loop() {
					values, err := viaCostRead(query, db, via)
					if err != nil {
						b.Fatal(err)
					}
					viaCostSink = values
				}
			})
		}
	}
}

func BenchmarkViaPreloadPointerFallback(b *testing.B) {
	parents := &preloadTestResponse{columns: []string{"id"}}
	targets := &preloadTestResponse{columns: []string{"parent_id", "id", "name"}}
	for i := range 100 {
		parents.values = append(parents.values, []driver.Value{int64(i + 1)})
	}
	for i := range 300 {
		targets.values = append(targets.values, []driver.Value{int64(i/3 + 1), int64(i + 1), "value"})
	}
	db := openPreloadTestDB(b, &preloadTestState{repeat: true, responses: []*preloadTestResponse{parents, targets}})
	query := Query[viaPreloadParent]().Select("ID").Limit(100).Preload("TargetPointers", PreloadFields("ID", "Name"))
	b.ReportAllocs()
	for b.Loop() {
		rows, err := query.All(context.Background(), db)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 100 || len(rows[99].TargetPointers) != 3 || rows[99].TargetPointers[2].ID != 300 {
			b.Fatal("pointer fallback result changed")
		}
	}
}
