package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type preloadManyToManyBenchmarkUser struct {
	model.Meta `tidbgo:"table=preload_users"`
	ID         uint64        `tidbgo:",pk"`
	Roles      []preloadRole `tidbgo:"many_to_many,through=preload_user_roles,source=ID:user_id,target=role_id:ID"`
}

var (
	preloadQueryBenchmarkSink      []preloadUser
	preloadManyToManyBenchmarkSink []preloadManyToManyBenchmarkUser
	preloadRelationGraphSink       preloadGraph
	preloadBuildSQLSink            string
	preloadBuildArgsSink           []any
)

func BenchmarkSelectQueryPreloadRelationGraphThreeStatements(b *testing.B) {
	state := &preloadTestState{
		repeat: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{
					"id", "node_a_id", "node_b_id", "node_c_id",
					"joined_node_a_id", "node_a_value",
					"joined_node_b_id", "node_b_value",
					"joined_node_c_id", "node_c_value",
					"detail_a_id", "detail_a_value", "detail_a_graph_id",
					"detail_b_id", "detail_b_value", "detail_b_graph_id",
				},
				values: [][]driver.Value{{
					int64(1), int64(10), int64(20), int64(30),
					int64(10), []byte("a"),
					int64(20), []byte("b"),
					int64(30), []byte("c"),
					int64(40), []byte("detail-a"), int64(1),
					int64(50), []byte("detail-b"), int64(1),
				}},
			},
			{
				columns: []string{"graph_id", "id", "value", "joined_node_id", "node_value"},
				values: [][]driver.Value{
					{int64(1), int64(60), []byte("tag-a"), int64(90), []byte("tag-node-a")},
					{int64(1), int64(61), []byte("tag-b"), int64(91), []byte("tag-node-b")},
					{int64(1), int64(62), []byte("tag-c"), int64(92), []byte("tag-node-c")},
				},
			},
			{
				columns: []string{"id", "node_id", "graph_id", "joined_node_id", "node_value"},
				values: [][]driver.Value{
					{int64(70), int64(80), int64(1), int64(80), []byte("child-node-a")},
					{int64(71), int64(81), int64(1), int64(81), []byte("child-node-b")},
					{int64(72), int64(82), int64(1), int64(82), []byte("child-node-c")},
				},
			},
		},
	}
	database := openPreloadTestDB(b, state)
	query := relationGraphPreloadQuery()
	ctx := context.Background()
	var graph preloadGraph
	var err error

	b.ReportAllocs()
	for b.Loop() {
		graph, err = query.Only(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadRelationGraphSink = graph
}

func BenchmarkSelectQueryBuildPreloadRelationGraph(b *testing.B) {
	query := relationGraphPreloadQuery()
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadBuildSQLSink = sqlText
	preloadBuildArgsSink = arguments
}

func BenchmarkSelectQueryPreloadHasMany100Parents300Children(b *testing.B) {
	parentRows := make([][]driver.Value, 100)
	for index := range parentRows {
		parentRows[index] = []driver.Value{int64(index + 1)}
	}
	childRows := make([][]driver.Value, 300)
	for index := range childRows {
		childRows[index] = []driver.Value{
			int64(index + 1),
			int64(index%100 + 1),
			"10.00",
		}
	}
	state := &preloadTestState{
		repeat: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: parentRows},
			{columns: []string{"id", "user_id", "total"}, values: childRows},
		},
	}
	database := openPreloadTestDB(b, state)
	query := Query[preloadUser]().Select("ID").Preload("Orders")
	ctx := context.Background()
	var users []preloadUser
	var err error

	b.ReportAllocs()
	for b.Loop() {
		users, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadQueryBenchmarkSink = users
}

func BenchmarkExecutePreloadHasMany10000Parents(b *testing.B) {
	const parentCount = 10000

	descriptor, err := model.Describe[preloadUser]()
	if err != nil {
		b.Fatal(err)
	}
	parents := make([]preloadUser, parentCount)
	for index := range parents {
		parents[index].ID = uint64(index + 1)
	}
	b.Run("all_sources", func(b *testing.B) {
		plans, compileErr := compilePreloadPlans(descriptor, []preloadRequest{{path: "Orders"}})
		if compileErr != nil {
			b.Fatal(compileErr)
		}
		plans[0].loadAllSources = true
		state := &preloadTestState{
			repeat: true,
			responses: []*preloadTestResponse{{
				columns: []string{"id", "user_id", "total"},
			}},
		}
		database := openPreloadTestDB(b, state)
		ctx := context.Background()
		b.ReportAllocs()
		b.ReportMetric(1, "relation-statements/op")

		for b.Loop() {
			if executeErr := executePreloads(ctx, database, plans, reflect.ValueOf(parents)); executeErr != nil {
				b.Fatal(executeErr)
			}
		}
		preloadQueryBenchmarkSink = parents
	})

	for _, batchSize := range []int{500, 1000, 2000, 5000, 10000} {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			plans, compileErr := compilePreloadPlans(descriptor, []preloadRequest{{path: "Orders"}})
			if compileErr != nil {
				b.Fatal(compileErr)
			}
			plans[0].batchSize = batchSize
			state := &preloadTestState{
				repeat: true,
				responses: []*preloadTestResponse{{
					columns: []string{"id", "user_id", "total"},
				}},
			}
			database := openPreloadTestDB(b, state)
			ctx := context.Background()
			b.ReportAllocs()
			b.ReportMetric(float64((parentCount+batchSize-1)/batchSize), "relation-statements/op")

			for b.Loop() {
				if executeErr := executePreloads(ctx, database, plans, reflect.ValueOf(parents)); executeErr != nil {
					b.Fatal(executeErr)
				}
			}
			preloadQueryBenchmarkSink = parents
		})
	}
}

func BenchmarkSelectQueryBuildPreloadHasMany(b *testing.B) {
	query := Query[preloadUser]().Select("Email").Preload("Orders")
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadBuildSQLSink = sqlText
	preloadBuildArgsSink = arguments
}

func BenchmarkSelectQueryPreloadManyToMany100Parents300Targets(b *testing.B) {
	parentRows := make([][]driver.Value, 100)
	for index := range parentRows {
		parentRows[index] = []driver.Value{int64(index + 1)}
	}
	targetRows := make([][]driver.Value, 300)
	for index := range targetRows {
		targetRows[index] = []driver.Value{
			int64(index%100 + 1),
			int64(index + 1),
			"role",
		}
	}
	state := &preloadTestState{
		repeat: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: parentRows},
			{columns: []string{"user_id", "id", "name"}, values: targetRows},
		},
	}
	database := openPreloadTestDB(b, state)
	query := Query[preloadManyToManyBenchmarkUser]().Select("ID").Preload("Roles")
	ctx := context.Background()
	var users []preloadManyToManyBenchmarkUser
	var err error

	b.ReportAllocs()
	for b.Loop() {
		users, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadManyToManyBenchmarkSink = users
}

func BenchmarkSelectQueryBuildPreloadManyToMany(b *testing.B) {
	query := Query[preloadManyToManyBenchmarkUser]().Select("ID").Preload("Roles")
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadBuildSQLSink = sqlText
	preloadBuildArgsSink = arguments
}

func BenchmarkSelectQueryPreloadNested100Parents300Children(b *testing.B) {
	parentRows := make([][]driver.Value, 100)
	for index := range parentRows {
		parentRows[index] = []driver.Value{int64(index + 1)}
	}
	childRows := make([][]driver.Value, 300)
	for index := range childRows {
		childRows[index] = []driver.Value{
			int64(index + 1),
			"10.00",
			int64(index%100 + 1),
			int64(index%100 + 1),
			"user@example.test",
		}
	}
	state := &preloadTestState{
		repeat: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: parentRows},
			{columns: []string{"id", "total", "user_id", "joined_user_id", "joined_user_email"}, values: childRows},
		},
	}
	database := openPreloadTestDB(b, state)
	query := Query[preloadUser]().
		Select("ID").
		Preload("Orders", PreloadFields("ID", "Total"), PreloadOrderBy(Desc("ID"))).
		Preload("Orders.User")
	ctx := context.Background()
	var users []preloadUser
	var err error

	b.ReportAllocs()
	for b.Loop() {
		users, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadQueryBenchmarkSink = users
}

func BenchmarkSelectQueryBuildPreloadNestedOptions(b *testing.B) {
	query := Query[preloadUser]().
		Select("ID").
		Preload("Orders", PreloadFields("ID", "Total"), PreloadOrderBy(Desc("ID"))).
		Preload("Orders.User")
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	preloadBuildSQLSink = sqlText
	preloadBuildArgsSink = arguments
}
