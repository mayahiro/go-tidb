package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
)

type windowBenchmarkResult struct {
	ShopID   int64 `tidbgo:"ShopID"`
	Total    int64 `tidbgo:"Total"`
	Position int64 `tidbgo:"Position"`
	Running  int64 `tidbgo:"Running"`
}

// BenchmarkAggregateWindow compares identical SQL and result mapping. The fake
// driver isolates client work; connected tests verify actual window semantics.
func BenchmarkAggregateWindow(b *testing.B) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			q := Aggregate[aggregateBenchmarkSource]().Select(Field("ShopID"), Sum("Amount").As("Total")).GroupBy("ShopID").Window(RowNumber().Over(WindowSpec{OrderBy: []OrderTerm{Asc("ShopID")}}).As("Position"), Sum("Total").Over(WindowSpec{OrderBy: []OrderTerm{Asc("ShopID")}, Rows: RowsBetween(UnboundedPreceding, CurrentRow)}).As("Running")).OrderBy(Asc("ShopID"))
			statement, args, err := q.Build()
			if err != nil {
				b.Fatal(err)
			}
			state := &allTestState{columns: []string{"ShopID", "Total", "Position", "Running"}, values: make([][]driver.Value, count)}
			want := make([]windowBenchmarkResult, count)
			for i := range want {
				want[i] = windowBenchmarkResult{int64(i), 2, int64(i + 1), int64(2 * (i + 1))}
				state.values[i] = []driver.Value{int64(i), int64(2), int64(i + 1), int64(2 * (i + 1))}
			}
			db := sql.OpenDB(&allTestConnector{state: state})
			b.Cleanup(func() { db.Close() })
			ctx := context.Background()
			raw := Raw[windowBenchmarkResult](statement, args...)
			for _, method := range []string{"aggregate", "raw", "database_sql"} {
				b.Run(method, func(b *testing.B) {
					read := func() ([]windowBenchmarkResult, error) {
						switch method {
						case "aggregate":
							var values []windowBenchmarkResult
							err := q.ScanAll(ctx, db, &values)
							return values, err
						case "raw":
							return raw.All(ctx, db)
						default:
							rows, err := db.QueryContext(ctx, statement, args...)
							if err != nil {
								return nil, err
							}
							values := make([]windowBenchmarkResult, 0)
							for rows.Next() {
								values = append(values, windowBenchmarkResult{})
								v := &values[len(values)-1]
								if err := rows.Scan(&v.ShopID, &v.Total, &v.Position, &v.Running); err != nil {
									rows.Close()
									return nil, err
								}
							}
							if err := rows.Err(); err != nil {
								rows.Close()
								return nil, err
							}
							return values, rows.Close()
						}
					}
					if got, err := read(); err != nil || !reflect.DeepEqual(got, want) {
						b.Fatal("window benchmark mismatch", err)
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

func BenchmarkAggregateRelatedBuild(b *testing.B) {
	q := Aggregate[aggregateRelationOrder]().Select(Field("User.Active").As("Active"), CountAll().As("Total"), CountIf(Has("User", Equal("Active", true))).As("Matched")).GroupBy("Active")
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := q.Build(); err != nil {
			b.Fatal(err)
		}
	}
}
