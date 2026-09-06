package orm

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkUpdateMany compares the same primary-key values and updated fields
// through individual Updates and UpdateMany. The executor has no database I/O
// and does not call Valuer, so these numbers cover compiler costs only.
func BenchmarkUpdateMany(b *testing.B) {
	ctx := context.Background()
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	for _, count := range []int{1, 25, 100, 1000, 3*(maxMutationParameters/17) + 1} {
		values := writeBenchmarkRows(count)
		pointers := make([]*writeBenchmarkRow, count)
		for i := range values {
			pointers[i] = &values[i]
		}
		for _, selected := range []bool{true, false} {
			var fields []string
			if selected {
				fields = []string{"V0", "V2"}
			}
			for _, mode := range []string{"loop", "values", "pointers"} {
				b.Run(fmt.Sprintf("rows_%d/selected_%t/%s", count, selected, mode), func(b *testing.B) {
					execute := func() error {
						switch mode {
						case "loop":
							for i := range values {
								if _, err := Update(&values[i], fields...).Exec(ctx, executor); err != nil {
									return err
								}
							}
							return nil
						case "values":
							_, err := UpdateMany(values, fields...).Exec(ctx, executor)
							return err
						default:
							_, err := UpdateMany(pointers, fields...).Exec(ctx, executor)
							return err
						}
					}
					if err := execute(); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						if err := execute(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
