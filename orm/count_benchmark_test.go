package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
)

var countQueryBenchmarkSink int64
var compiledCountBenchmarkSink compiledCount

func BenchmarkSelectQueryCountDirect(b *testing.B) {
	benchmarkSelectQueryCount(b, false)
}

func BenchmarkSelectQueryCountPaginated(b *testing.B) {
	benchmarkSelectQueryCount(b, true)
}

func BenchmarkSelectQueryCompileRelationCount(b *testing.B) {
	query := Query[relationTopNVideo]().
		Where(Has("VideoGenres", Equal("GenreID", int64(7))))
	b.ReportAllocs()
	for b.Loop() {
		compiled, err := query.compileCount()
		if err != nil {
			b.Fatal(err)
		}
		compiledCountBenchmarkSink = compiled
	}
}

func benchmarkSelectQueryCount(b *testing.B, paginated bool) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(42)}},
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
	if paginated {
		query.Limit(100).Offset(20)
	}
	ctx := context.Background()
	var count int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		count, err = query.Count(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	if count != 42 {
		b.Fatalf("Count() = %d, want 42", count)
	}
	countQueryBenchmarkSink = count
}
