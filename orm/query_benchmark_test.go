package orm

import (
	"testing"
	"time"
)

var (
	queryBenchmarkSQLSink       string
	queryBenchmarkArgumentsSink []any
)

func BenchmarkSelectQueryBuildDefault(b *testing.B) {
	query := Query[keysetBenchmarkModel]()
	if _, _, err := query.Build(); err != nil {
		b.Fatal(err)
	}
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
	queryBenchmarkSQLSink = sqlText
	queryBenchmarkArgumentsSink = arguments
}

func BenchmarkSelectQueryBuildSeekAfter(b *testing.B) {
	createdAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	query := Query[keysetBenchmarkModel]().
		OrderBy(Desc("CreatedAt"), Desc("ID")).
		SeekAfter(createdAt, uint64(1000)).
		Limit(100)
	if _, _, err := query.Build(); err != nil {
		b.Fatal(err)
	}
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
	queryBenchmarkSQLSink = sqlText
	queryBenchmarkArgumentsSink = arguments
}

func BenchmarkSelectQueryConstructAndBuild(b *testing.B) {
	createdAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = Query[keysetBenchmarkModel]().
			Where(GreaterThan("ID", uint64(10))).
			OrderBy(Desc("CreatedAt"), Desc("ID")).
			SeekAfter(createdAt, uint64(1000)).
			Limit(100).
			Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	queryBenchmarkSQLSink = sqlText
	queryBenchmarkArgumentsSink = arguments
}
