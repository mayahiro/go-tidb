package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type aggregatePeriodBenchmarkResult[K any] struct {
	model.Meta `tidbgo:"table=period_benchmark_results"`
	Period     K     `tidbgo:"Period"`
	Count      int64 `tidbgo:"Count"`
}

// BenchmarkAggregatePeriod compares calendar ScanAll with typed Raw and direct
// database/sql for identical SQL, values, and destination field types.
func BenchmarkAggregatePeriod(b *testing.B) {
	b.Run("date", func(b *testing.B) {
		benchmarkAggregatePeriod(b, Date("CreatedAt"), func(i int) time.Time {
			return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		})
	})
	b.Run("year_month", func(b *testing.B) {
		benchmarkAggregatePeriod(b, YearMonth("CreatedAt"), func(i int) int64 { return int64((2000+i/12)*100 + i%12 + 1) })
	})
}

func benchmarkAggregatePeriod[K any](b *testing.B, expr AggregateExpression, key func(int) K) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			q := Aggregate[aggregatePeriodSource]().Select(expr.As("Period"), CountAll().As("Count")).GroupBy("Period").Having(IsNotNull("Period")).OrderBy(Asc("Period"))
			statement, args, err := q.Build()
			if err != nil {
				b.Fatal(err)
			}
			state := &allTestState{columns: []string{"Period", "Count"}, values: make([][]driver.Value, count)}
			want := make([]aggregatePeriodBenchmarkResult[K], count)
			for i := range state.values {
				value := key(i)
				state.values[i] = []driver.Value{value, int64(2)}
				want[i] = aggregatePeriodBenchmarkResult[K]{Period: value, Count: 2}
			}
			db := sql.OpenDB(&allTestConnector{state: state})
			b.Cleanup(func() { _ = db.Close() })
			ctx := context.Background()
			raw := Raw[aggregatePeriodBenchmarkResult[K]](statement, args...)
			for _, method := range []string{"aggregate", "raw", "database_sql"} {
				b.Run(method, func(b *testing.B) {
					read := func() ([]aggregatePeriodBenchmarkResult[K], error) {
						switch method {
						case "aggregate":
							var values []aggregatePeriodBenchmarkResult[K]
							err := q.ScanAll(ctx, db, &values)
							return values, err
						case "raw":
							return raw.All(ctx, db)
						default:
							rows, err := db.QueryContext(ctx, statement, args...)
							if err != nil {
								return nil, err
							}
							values := make([]aggregatePeriodBenchmarkResult[K], 0)
							for rows.Next() {
								values = append(values, aggregatePeriodBenchmarkResult[K]{})
								target := &values[len(values)-1]
								if err := rows.Scan(&target.Period, &target.Count); err != nil {
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
						b.Fatalf("result mismatch: %v", err)
					}
					b.ReportAllocs()
					for b.Loop() {
						values, err := read()
						if err != nil {
							b.Fatal(err)
						}
						scanAllBenchmarkSink = values
					}
				})
			}
		})
	}
}
