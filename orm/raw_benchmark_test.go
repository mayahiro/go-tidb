package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

var rawQueryBenchmarkSink []mutationModel

func BenchmarkRawAll100Rows(b *testing.B) {
	rows := make([][]driver.Value, 100)
	for index := range rows {
		rows[index] = []driver.Value{int64(index + 1), "Ada", int64(index + 10)}
	}
	state := &allTestState{
		columns: []string{"id", "name", "count"},
		values:  rows,
	}
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	})
	query := Raw[mutationModel]("SELECT id, name, COUNT(*) AS count FROM mutation_models GROUP BY id, name")
	ctx := context.Background()
	var values []mutationModel
	var err error

	b.ReportAllocs()
	for b.Loop() {
		values, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	rawQueryBenchmarkSink = values
}
