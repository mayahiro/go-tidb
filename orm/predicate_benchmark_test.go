package orm

import (
	"reflect"
	"testing"
)

func BenchmarkCompileSelectPredicates(b *testing.B) {
	query := selectQuery{
		modelType: reflect.TypeFor[scanBenchmarkModel](),
		predicates: []predicate{
			{operator: predicateGreaterThanOrEqual, field: "ID", values: []any{uint64(100)}},
			{
				operator: predicateOr,
				children: []predicate{
					{operator: predicateEqual, field: "Name", values: []any{"Ada"}},
					{operator: predicateEqual, field: "Name", values: []any{"Grace"}},
				},
			},
			{operator: predicateIn, field: "ID", values: []any{uint64(101), uint64(102), uint64(103)}},
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

func BenchmarkCompileSelectRelationPredicateDirect(b *testing.B) {
	query := selectQuery{
		modelType: reflect.TypeFor[preloadUser](),
		predicates: []predicate{
			{
				operator: predicateHasRelation,
				field:    "Orders",
				children: []predicate{
					{operator: predicateGreaterThan, field: "Total", values: []any{"10.00"}},
				},
			},
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

func BenchmarkCompileSelectRelationPredicateManyToMany(b *testing.B) {
	query := selectQuery{
		modelType: reflect.TypeFor[preloadUser](),
		predicates: []predicate{
			{
				operator: predicateHasRelation,
				field:    "Roles",
				children: []predicate{
					{operator: predicateEqual, field: "Name", values: []any{"admin"}},
				},
			},
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
