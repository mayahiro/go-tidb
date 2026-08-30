package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

var terminalQueryBenchmarkSink scanModel

func BenchmarkSelectQueryFirstOneRow(b *testing.B) {
	benchmarkSelectQueryOneRow(b, false)
}

func BenchmarkSelectQueryOnlyOneRow(b *testing.B) {
	benchmarkSelectQueryOneRow(b, true)
}

func benchmarkSelectQueryOneRow(b *testing.B, only bool) {
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  [][]driver.Value{{int64(1), "Ada"}},
	}
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	})
	query := Query[scanModel]().Select("ID", "Name")
	ctx := context.Background()
	var value scanModel
	var err error

	b.ReportAllocs()
	for b.Loop() {
		if only {
			value, err = query.Only(ctx, database)
		} else {
			value, err = query.First(ctx, database)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
	terminalQueryBenchmarkSink = value
}
