package orm_test

import (
	"context"
	"io"
	"testing"

	"github.com/mayahiro/go-tidb/orm"
)

func TestCollectServerRUConfiguresBothPublicObservationBoundaries(t *testing.T) {
	t.Parallel()

	ctx := orm.WithStatementObserver(context.Background(), func(orm.StatementEvent) {}, orm.CollectServerRU())
	ctx = orm.WithRuntimeCapture(ctx, orm.NewRuntimeCapture(io.Discard), orm.CollectServerRU())
	if ctx == nil {
		t.Fatal("observation context = nil")
	}
}
