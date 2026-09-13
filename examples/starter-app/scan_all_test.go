package starterapp

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type projectionExampleExecutor func(context.Context, string, ...any) (*sql.Rows, error)

func (execute projectionExampleExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return execute(ctx, query, args...)
}

func TestPartialVideoResultsKeepSourceScope(t *testing.T) {
	failure := errors.New("fixture query failure")
	for _, summary := range []bool{false, true} {
		calls := 0
		executor := projectionExampleExecutor(func(_ context.Context, query string, args ...any) (*sql.Rows, error) {
			calls++
			projection := "`id`"
			if summary {
				projection += ", `title`"
			}
			want := "SELECT " + projection + " FROM `videos` WHERE `deleted_at` IS NULL ORDER BY `id` ASC"
			if query != want || len(args) != 0 {
				t.Fatalf("query = %s; args = %#v", query, args)
			}
			return nil, failure
		})
		var err error
		if summary {
			_, err = ListVideoSummaries(context.Background(), executor)
		} else {
			_, err = ListVideoIDs(context.Background(), executor)
		}
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("error = %v; calls = %d", err, calls)
		}
	}
}
