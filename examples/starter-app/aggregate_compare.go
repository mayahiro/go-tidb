package starterapp

import (
	"context"
	"database/sql"

	"github.com/mayahiro/go-tidb/orm"
)

// CompareOrderTotals measures auto, TiKV, and TiFlash MPP over a fixed order
// fixture. The caller supplies stable data and TiFlash replicas. This explicit
// diagnostic executes 21 SELECTs with RU probes and three separate plan/warning
// pairs; it is not called during ordinary application startup or query loading.
// A complete report can export one variant with WriteCapture for baseline checks.
func CompareOrderTotals(ctx context.Context, db *sql.DB) (orm.AggregateComparison, error) {
	return orm.Aggregate[Order]().
		Select(orm.Field("UserID"), orm.CountAll().As("OrderCount"), orm.Sum("Total").As("Total")).
		GroupBy("UserID").OrderBy(orm.Asc("UserID")).
		Compare(ctx, db, orm.AggregateCompareOptions{Case: "order-totals-fixture-v1"})
}
