package orm

import (
	"reflect"
	"testing"
)

func BenchmarkCompileSelectOrderPagination(b *testing.B) {
	query := selectQuery{
		modelType: reflect.TypeFor[scanBenchmarkModel](),
		orderBy: []orderTerm{
			{field: "CreatedAt", direction: orderDescending},
			{field: "ID", direction: orderDescending},
		},
		pagination: pagination{
			limit:     100,
			offset:    200,
			limitSet:  true,
			offsetSet: true,
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
