package orm

import (
	"reflect"
	"testing"
	"time"
)

type keysetBenchmarkModel struct {
	ID        uint64 `tidbgo:",pk"`
	CreatedAt time.Time
	Name      string
}

func BenchmarkCompileSelectSeekAfter(b *testing.B) {
	createdAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	query := selectQuery{
		modelType: reflect.TypeFor[keysetBenchmarkModel](),
		orderBy: []orderTerm{
			{field: "CreatedAt", direction: orderDescending},
			{field: "ID", direction: orderDescending},
		},
		seekAfter: []cursorValue{
			{field: "CreatedAt", value: createdAt},
			{field: "ID", value: uint64(1000)},
		},
		pagination: pagination{limit: 100, limitSet: true},
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
