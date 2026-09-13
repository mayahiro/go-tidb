package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type scanAllBenchmarkModel struct {
	ID          int64
	YouTubeID   string
	Title       string
	Description string
	Thumbnail   string
	Subscribers int64
	CreatedAt   time.Time
	Optional    *string
	Amount      scanDecimal
}

type scanAllBenchmarkDTO struct {
	ID        int64
	YouTubeID string
}

type scanAllBenchmarkNullable struct {
	ID       int64
	Optional *string
	Amount   scanDecimal
}

var scanAllBenchmarkSink any

// BenchmarkScanAll compares equal SQL and results through the same fake
// database/sql driver. Generic is a benchmark-only alternative API collector.
func BenchmarkScanAll(b *testing.B) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			b.Run("scalar", func(b *testing.B) {
				benchmarkScanAllResult(b, count, []string{"ID"}, []driver.Value{int64(7)}, func(row scanAllBenchmarkModel) int64 { return row.ID })
			})
			b.Run("dto", func(b *testing.B) {
				benchmarkScanAllResult(b, count, []string{"ID", "YouTubeID"}, []driver.Value{int64(7), "channel"}, func(row scanAllBenchmarkModel) scanAllBenchmarkDTO {
					return scanAllBenchmarkDTO{ID: row.ID, YouTubeID: row.YouTubeID}
				})
			})
			b.Run("nullable", func(b *testing.B) {
				benchmarkScanAllResult(b, count, []string{"ID", "Optional", "Amount"}, []driver.Value{int64(7), "optional", "12.34"}, func(row scanAllBenchmarkModel) scanAllBenchmarkNullable {
					return scanAllBenchmarkNullable{ID: row.ID, Optional: row.Optional, Amount: row.Amount}
				})
			})
			b.Run("wide", func(b *testing.B) {
				benchmarkScanAllResult(b, count, nil, []driver.Value{int64(7), "channel", "title", "description", "thumbnail", int64(10), time.Unix(1, 0).UTC(), "optional", "12.34"}, func(row scanAllBenchmarkModel) scanAllBenchmarkModel { return row })
			})
		})
	}
}

func benchmarkScanAllResult[D any](b *testing.B, count int, projection []string, input []driver.Value, convert func(scanAllBenchmarkModel) D) {
	q := Query[scanAllBenchmarkModel]()
	if projection != nil {
		q.Select(projection...)
	}
	plan, err := scanAllPlanFor(reflect.TypeFor[scanAllBenchmarkModel](), reflect.TypeFor[[]D](), projection)
	if err != nil {
		b.Fatal(err)
	}
	state := &allTestState{columns: plan.statement.scanPlan.columns, values: make([][]driver.Value, count)}
	for i := range state.values {
		state.values[i] = input
	}
	db := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	all := func() ([]D, error) {
		rows, err := q.All(ctx, db)
		if err != nil {
			return nil, err
		}
		if result, ok := any(rows).([]D); ok {
			return result, nil // A full-model result needs no conversion.
		}
		result := make([]D, len(rows))
		for i, row := range rows {
			result[i] = convert(row)
		}
		return result, nil
	}
	want, err := all()
	if err != nil {
		b.Fatal(err)
	}
	for _, method := range []string{"all_map", "scan_all", "generic"} {
		b.Run(method, func(b *testing.B) {
			read := all
			switch method {
			case "scan_all":
				read = func() ([]D, error) {
					var result []D
					err := q.ScanAll(ctx, db, &result)
					return result, err
				}
			case "generic":
				read = func() ([]D, error) { return scanAllGenericBenchmark[D](ctx, db, q) }
			}
			if got, err := read(); err != nil || !reflect.DeepEqual(got, want) {
				b.Fatalf("result mismatch: %v", err)
			}
			var result []D
			b.ReportAllocs()
			for b.Loop() {
				result, err = read()
				if err != nil {
					b.Fatal(err)
				}
			}
			scanAllBenchmarkSink = result
		})
	}
}

// This alternative retains source compilation and diagnostics but returns []D
// with a generic append loop, instead of accepting a destination pointer.
func scanAllGenericBenchmark[D any](ctx context.Context, executor QueryExecutor, q *SelectQuery[scanAllBenchmarkModel]) ([]D, error) {
	plan, err := scanAllPlanFor(q.selection.modelType, reflect.TypeFor[[]D](), q.selection.projection)
	if err != nil {
		return nil, err
	}
	compiled, err := compileSelectFromProjection(plan.source, plan.statement, &q.selection)
	if err != nil {
		return nil, err
	}
	ctx = executorStatementContext(ctx, executor)
	rows, err := queryRows(ctx, executor, compiled, runtimeSelectMetadata(ctx, &q.selection, compiled, "scan_all"))
	if err != nil {
		return nil, err
	}
	if !plan.scalar {
		return collectSelectRows[D](&selectStatement{scanPlan: plan.target}, rows)
	}
	result := make([]D, 0)
	var zero D
	for rows.Next() {
		result = append(result, zero)
		if err := rows.Scan(&result[len(result)-1]); err != nil {
			return nil, closeRowsAfterError(plan.source.Name(), rows, err)
		}
	}
	if err := finishRows(plan.source.Name(), rows); err != nil {
		return nil, err
	}
	return result, nil
}
