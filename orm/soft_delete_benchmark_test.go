package orm

import (
	"database/sql"
	"reflect"
	"testing"
)

var softDeleteBenchmarkModelSink softDeleteVideo

func BenchmarkSoftDeleteSelectBuildDefault(b *testing.B) {
	query := Query[softDeleteVideo]()
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

func BenchmarkSoftDeleteSelectBuildWithDeleted(b *testing.B) {
	query := Query[softDeleteVideo]().WithDeleted()
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

func BenchmarkSoftDeleteRowDecoderScanNull(b *testing.B) {
	plan, err := scanPlanForTest(reflect.TypeFor[softDeleteVideo]())
	if err != nil {
		b.Fatal(err)
	}
	decoder := plan.newDecoder()
	row := scanValuesRow{scan: func(destinations []any) error {
		*destinations[0].(*int64) = 1
		*destinations[1].(*int64) = 2
		*destinations[2].(*string) = "demo"
		return destinations[3].(sql.Scanner).Scan(nil)
	}}
	var target softDeleteVideo

	b.ReportAllocs()
	for b.Loop() {
		target = softDeleteVideo{}
		if err := decoder.scan(row, &target); err != nil {
			b.Fatal(err)
		}
	}
	softDeleteBenchmarkModelSink = target
}

func BenchmarkSoftDeleteDeleteBuild(b *testing.B) {
	value := softDeleteVideo{ID: 7}
	query := Delete(&value)
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

func BenchmarkSoftDeletePreloadBuild(b *testing.B) {
	query := Query[softDeleteWatch]().Preload("Video")
	benchmarkSoftDeletePreloadBuild(b, query)
}

func BenchmarkSoftDeletePreloadBuildWithDeleted(b *testing.B) {
	query := Query[softDeleteWatch]().WithDeleted().Preload("Video", PreloadWithDeleted())
	benchmarkSoftDeletePreloadBuild(b, query)
}

func benchmarkSoftDeletePreloadBuild(b *testing.B, query *SelectQuery[softDeleteWatch]) {
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
