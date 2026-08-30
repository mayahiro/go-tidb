package orm

import "testing"

func BenchmarkAddRelationBuild100Targets(b *testing.B) {
	targets := make([]uint64, 100)
	for index := range targets {
		targets[index] = uint64(index + 1)
	}
	query := AddRelation[preloadUser]("Roles", uint64(7), targets...)
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
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkAddRelationConstructAndBuild100Targets(b *testing.B) {
	targets := make([]uint64, 100)
	for index := range targets {
		targets[index] = uint64(index + 1)
	}
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = AddRelation[preloadUser]("Roles", uint64(7), targets...).Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkRemoveRelationBuild100Targets(b *testing.B) {
	targets := make([]uint64, 100)
	for index := range targets {
		targets[index] = uint64(index + 1)
	}
	query := RemoveRelation[preloadUser]("Roles", uint64(7), targets...)
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
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkRemoveRelationConstructAndBuild100Targets(b *testing.B) {
	targets := make([]uint64, 100)
	for index := range targets {
		targets[index] = uint64(index + 1)
	}
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = RemoveRelation[preloadUser]("Roles", uint64(7), targets...).Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}
