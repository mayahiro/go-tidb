package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

var allQueryBenchmarkSink []scanModel

func BenchmarkSelectQueryAll100Rows(b *testing.B) {
	rows := make([][]driver.Value, 100)
	for index := range rows {
		rows[index] = []driver.Value{int64(index + 1), "Ada"}
	}
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  rows,
	}
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	})
	query := Query[scanModel]().Select("ID", "Name")
	ctx := context.Background()
	var values []scanModel
	var err error

	b.ReportAllocs()
	for b.Loop() {
		values, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	allQueryBenchmarkSink = values
}
