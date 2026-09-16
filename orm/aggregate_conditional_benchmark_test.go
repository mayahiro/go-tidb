package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
)

// BenchmarkAggregateConditional compares identical conditional SQL and result
// types through ScanAll, typed Raw, and database/sql. The driver does not
// evaluate predicates or SQL aggregation.
func BenchmarkAggregateConditional(b *testing.B) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			q := Aggregate[aggregateConditionalSource]().Select(Field("ShopID"), CountIf(Or(Equal("Status", "paid"), IsNull("Status"))).As("Count"), SumIf("Amount", Between("ShopID", int64(1), int64(5))).As("Total")).GroupBy("ShopID").Having(GreaterThan("Count", int64(0))).OrderBy(Asc("ShopID"))
			statement, args, err := q.Build()
			if err != nil {
				b.Fatal(err)
			}
			state := &allTestState{columns: []string{"ShopID", "Count", "Total"}, values: make([][]driver.Value, count)}
			want := make([]aggregateBenchmarkResult, count)
			for i := range state.values {
				state.values[i] = []driver.Value{int64(i), int64(2), int64(42)}
				want[i] = aggregateBenchmarkResult{ShopID: int64(i), Count: 2, Total: 42}
			}
			db := sql.OpenDB(&allTestConnector{state: state})
			b.Cleanup(func() { _ = db.Close() })
			ctx := context.Background()
			raw := Raw[aggregateBenchmarkResult](statement, args...)
			for _, method := range []string{"aggregate", "raw", "database_sql"} {
				b.Run(method, func(b *testing.B) {
					read := func() ([]aggregateBenchmarkResult, error) {
						switch method {
						case "aggregate":
							var result []aggregateBenchmarkResult
							err := q.ScanAll(ctx, db, &result)
							return result, err
						case "raw":
							return raw.All(ctx, db)
						default:
							rows, err := db.QueryContext(ctx, statement, args...)
							if err != nil {
								return nil, err
							}
							values := make([]aggregateBenchmarkResult, 0)
							for rows.Next() {
								values = append(values, aggregateBenchmarkResult{})
								target := &values[len(values)-1]
								if err := rows.Scan(&target.ShopID, &target.Count, &target.Total); err != nil {
									_ = rows.Close()
									return nil, err
								}
							}
							if err := rows.Err(); err != nil {
								_ = rows.Close()
								return nil, err
							}
							return values, rows.Close()
						}
					}
					if got, err := read(); err != nil || !reflect.DeepEqual(got, want) {
						b.Fatalf("results differ: %v", err)
					}
					b.ReportAllocs()
					for b.Loop() {
						value, err := read()
						if err != nil {
							b.Fatal(err)
						}
						scanAllBenchmarkSink = value
					}
				})
			}
		})
	}
}
