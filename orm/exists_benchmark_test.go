package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

var existsQueryBenchmarkSink bool

func BenchmarkSelectQueryExistsTrue(b *testing.B) {
	benchmarkSelectQueryExists(b, true)
}

func BenchmarkSelectQueryExistsFalse(b *testing.B) {
	benchmarkSelectQueryExists(b, false)
}

func benchmarkSelectQueryExists(b *testing.B, found bool) {
	state := &allTestState{columns: []string{"exists"}}
	if found {
		state.values = [][]driver.Value{{int64(1)}}
	}
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	})
	query := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID"))
	ctx := context.Background()
	var exists bool
	var err error

	b.ReportAllocs()
	for b.Loop() {
		exists, err = query.Exists(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	if exists != found {
		b.Fatalf("Exists() = %t, want %t", exists, found)
	}
	existsQueryBenchmarkSink = exists
}
