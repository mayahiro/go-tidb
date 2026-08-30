package orm

import (
	"reflect"
	"testing"
)

func BenchmarkCompileSelectStringPatterns(b *testing.B) {
	query := selectQuery{
		modelType: reflect.TypeFor[scanBenchmarkModel](),
		predicates: []predicate{
			{operator: predicateContains, field: "Name", values: []any{"50%_! discount"}},
			{operator: predicateHasPrefix, field: "Name", values: []any{"TiDB"}},
			{operator: predicateHasSuffix, field: "Name", values: []any{"Cloud"}},
		},
	}
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
