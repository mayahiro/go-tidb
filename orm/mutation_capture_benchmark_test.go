package orm

import (
	"context"
	"testing"
	"time"
)

// Capture includes bind-free metadata construction, JSON encoding, and a
// discard writer. All modes exclude driver conversion and database work.
func BenchmarkConditionalMutationObservation(b *testing.B) {
	for _, operation := range []string{"update", "delete"} {
		b.Run(operation, func(b *testing.B) {
			for _, mode := range []string{"none", "observer", "capture"} {
				b.Run(mode, func(b *testing.B) {
					ctx := context.Background()
					switch mode {
					case "observer":
						ctx = WithStatementObserver(ctx, func(StatementEvent) {})
					case "capture":
						ctx = WithRuntimeCapture(ctx, NewRuntimeCapture(discardRuntimeCaptureWriter{}))
					}
					now := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)
					conditions := []Predicate{
						Equal("ChannelID", int64(7)),
						Or(IsNull("LockUntil"), LessThanOrEqual("LockUntil", now)),
					}
					update := UpdateWhere[conditionalUpdateModel](Set("LockOwner", "worker"), Increment("RetryCount", int64(1))).Where(conditions...)
					deletion := DeleteWhere[conditionalUpdateModel](conditions...)
					executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
					b.ReportAllocs()
					for b.Loop() {
						var err error
						if operation == "update" {
							_, err = update.Exec(ctx, executor)
						} else {
							_, err = deletion.Exec(ctx, executor)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
