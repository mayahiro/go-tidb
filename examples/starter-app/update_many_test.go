package starterapp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"
)

type exampleUpdateExecutor func(context.Context, string, ...any) (sql.Result, error)

func (execute exampleUpdateExecutor) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	return execute(ctx, statement, args...)
}

func TestUpdateClipGenrePriorities(t *testing.T) {
	t.Parallel()
	values := []*ClipGenre{{ID: 7, ClipID: 10, GenreID: 20, Priority: 1}, {ID: 8, ClipID: 10, GenreID: 21, Priority: 2}}
	wantSQL := "UPDATE `clip_genres` SET `priority` = CASE `id` WHEN ? THEN ? WHEN ? THEN ? ELSE `priority` END WHERE `id` IN (?, ?)"
	wantArgs := []any{int64(7), int64(1), int64(8), int64(2), int64(7), int64(8)}
	calls := 0
	ctx := context.Background()
	executor := exampleUpdateExecutor(func(got context.Context, statement string, args ...any) (sql.Result, error) {
		calls++
		if got != ctx || statement != wantSQL || !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("unexpected priority update: %s %#v", statement, args)
		}
		return driver.RowsAffected(2), nil
	})
	if affected, err := UpdateClipGenrePriorities(ctx, executor, values); err != nil || affected != 2 || calls != 1 {
		t.Fatalf("affected=%d calls=%d error=%v", affected, calls, err)
	}
	if values[0].ID != 7 || values[0].ClipID != 10 || values[0].GenreID != 20 || values[1].ID != 8 {
		t.Fatal("priority update changed edge identity")
	}
	if affected, err := UpdateClipGenrePriorities(ctx, executor, nil); err != nil || affected != 0 || calls != 1 {
		t.Fatalf("empty input affected=%d calls=%d error=%v", affected, calls, err)
	}
}
