package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"testing"
	"time"
)

type writeBenchmarkScalar struct {
	text string
}

func (*writeBenchmarkScalar) Value() (driver.Value, error) {
	panic("offline write benchmark must not call Value")
}

type writeBenchmarkRow struct {
	ID int64 `tidbgo:",pk,auto_random"`
	V0 int64
	V1 uint64
	V2 bool
	V3 string
	V4 []byte
	V5 time.Time
	V6 *int64
	V7 writeBenchmarkScalar
}

func writeBenchmarkRows(count int) []writeBenchmarkRow {
	values := make([]writeBenchmarkRow, count)
	for i := range values {
		values[i] = writeBenchmarkRow{
			ID: int64(i + 1000), V0: int64(i + 2000), V1: uint64(i + 3000),
			V2: i%2 == 0, V3: fmt.Sprintf("value-%08x", i),
			V4: []byte(fmt.Sprintf(`{"v":%d}`, i)),
			V5: time.Date(2026, time.September, 5, 0, 0, i%60, 0, time.UTC),
			V7: writeBenchmarkScalar{text: "12.34"},
		}
		if i%3 != 0 {
			values[i].V6 = &values[i].V0
		}
	}
	return values
}

func BenchmarkMutationWrite(b *testing.B) {
	value := writeBenchmarkRows(1)[0]
	for _, test := range []struct {
		name  string
		build func() (string, []any, error)
	}{
		{"insert", func() (string, []any, error) { return Insert(&value).Build() }},
		{"upsert", func() (string, []any, error) { return Upsert(&value).Build() }},
		{"upsert_selected", func() (string, []any, error) { return Upsert(&value, "V0", "V2").Build() }},
		{"update", func() (string, []any, error) { return Update(&value).Build() }},
		{"update_selected", func() (string, []any, error) { return Update(&value, "V0", "V2").Build() }},
		{"delete", func() (string, []any, error) { return Delete(&value).Build() }},
	} {
		b.Run(test.name, func(b *testing.B) {
			if _, _, err := test.build(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				sqlText, arguments, err := test.build()
				if err != nil {
					b.Fatal(err)
				}
				mutationBenchmarkSQLSink = sqlText
				mutationBenchmarkArgsSink = arguments
			}
		})
	}

	for _, count := range []int{100, 3*(maxMutationParameters/8) + 7} {
		values := writeBenchmarkRows(count)
		pointers := make([]*writeBenchmarkRow, count)
		for i := range values {
			pointers[i] = &values[i]
		}
		executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
		ctx := context.Background()
		for _, test := range []struct {
			name string
			exec func() (int64, error)
		}{
			{"insert_values", func() (int64, error) { return InsertMany(values).Exec(ctx, executor) }},
			{"insert_pointers", func() (int64, error) { return InsertMany(pointers).Exec(ctx, executor) }},
			{"upsert_values", func() (int64, error) { return UpsertMany(values).Exec(ctx, executor) }},
			{"upsert_pointers", func() (int64, error) { return UpsertMany(pointers).Exec(ctx, executor) }},
		} {
			b.Run(fmt.Sprintf("%s/rows_%d", test.name, count), func(b *testing.B) {
				if _, err := test.exec(); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					affected, err := test.exec()
					if err != nil {
						b.Fatal(err)
					}
					mutationBenchmarkAffectedSink = affected
				}
			})
		}
	}
}
