package orm

import (
	"reflect"
	"testing"
)

var selectBenchmarkSink compiledSelect

func BenchmarkCompileSelectDefaultCached(b *testing.B) {
	modelType := reflect.TypeFor[scanBenchmarkModel]()
	query := selectQuery{modelType: modelType}
	if _, err := compileSelect(&query); err != nil {
		b.Fatal(err)
	}
	var statement compiledSelect
	var err error

	b.ReportAllocs()
	for b.Loop() {
		statement, err = compileSelect(&query)
		if err != nil {
			b.Fatal(err)
		}
	}
	selectBenchmarkSink = statement
}

func BenchmarkCompileSelectProjection(b *testing.B) {
	modelType := reflect.TypeFor[scanBenchmarkModel]()
	query := selectQuery{
		modelType:  modelType,
		projection: []string{"Name", "ID"},
	}
	var statement compiledSelect
	var err error

	b.ReportAllocs()
	for b.Loop() {
		statement, err = compileSelect(&query)
		if err != nil {
			b.Fatal(err)
		}
	}
	selectBenchmarkSink = statement
}
