package orm

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type softDeleteVideo struct {
	model.Meta `tidbgo:"table=soft_delete_videos"`
	ID         int64 `tidbgo:",pk"`
	ChannelID  int64
	Title      string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type softDeletePointerVideo struct {
	model.Meta `tidbgo:"table=soft_delete_pointer_videos"`
	ID         int64      `tidbgo:",pk"`
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

type softDeleteChannel struct {
	model.Meta `tidbgo:"table=soft_delete_channels"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	DeletedAt  time.Time         `tidbgo:",soft_delete"`
	Videos     []softDeleteVideo `tidbgo:"has_many,join=ID:ChannelID"`
	Tags       []softDeleteTag   `tidbgo:"many_to_many,through=soft_delete_channel_tags,source=ID:channel_id,target=tag_id:ID"`
}

type softDeleteTag struct {
	model.Meta `tidbgo:"table=soft_delete_tags"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type softDeleteWatch struct {
	model.Meta `tidbgo:"table=soft_delete_watches"`
	ID         int64 `tidbgo:",pk"`
	VideoID    int64
	DeletedAt  time.Time        `tidbgo:",soft_delete"`
	Video      *softDeleteVideo `tidbgo:"belongs_to,join=VideoID:ID"`
}

func TestSoftDeleteSelectFiltersRootRowsUnlessIncluded(t *testing.T) {
	t.Parallel()

	defaultSQL, arguments, err := Query[softDeleteVideo]().Where(Equal("Title", "demo")).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantDefault := "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `deleted_at` IS NULL AND `title` = ?"
	if defaultSQL != wantDefault || !reflect.DeepEqual(arguments, []any{"demo"}) {
		t.Fatalf("Build() = %q, %#v, want %q, [demo]", defaultSQL, arguments, wantDefault)
	}

	withDeletedSQL, arguments, err := Query[softDeleteVideo]().WithDeleted().Where(Equal("Title", "demo")).Build()
	if err != nil {
		t.Fatalf("WithDeleted().Build() error = %v", err)
	}
	wantWithDeleted := "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `title` = ?"
	if withDeletedSQL != wantWithDeleted || !reflect.DeepEqual(arguments, []any{"demo"}) {
		t.Fatalf("WithDeleted().Build() = %q, %#v, want %q, [demo]", withDeletedSQL, arguments, wantWithDeleted)
	}
	onlyDeletedSQL, arguments, err := Query[softDeleteVideo]().WithDeleted().Where(IsNotNull("DeletedAt")).Build()
	if err != nil {
		t.Fatalf("only-deleted Build() error = %v", err)
	}
	wantOnlyDeleted := "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `deleted_at` IS NOT NULL"
	if onlyDeletedSQL != wantOnlyDeleted || len(arguments) != 0 {
		t.Fatalf("only-deleted Build() = %q, %#v, want %q", onlyDeletedSQL, arguments, wantOnlyDeleted)
	}

	count, err := Query[softDeleteVideo]().compileCount()
	if err != nil || count.sql != "SELECT COUNT(*) FROM `soft_delete_videos` WHERE `deleted_at` IS NULL" {
		t.Fatalf("compileCount() = %#v, %v", count, err)
	}
	exists, err := Query[softDeleteVideo]().compileExists()
	if err != nil || exists.sql != "SELECT 1 FROM `soft_delete_videos` WHERE `deleted_at` IS NULL LIMIT ?" || !reflect.DeepEqual(exists.arguments, []any{int64(1)}) {
		t.Fatalf("compileExists() = %#v, %v", exists, err)
	}

	if _, _, err := Query[scanModel]().WithDeleted().Build(); err == nil || !strings.Contains(err.Error(), "requires a soft-delete field") {
		t.Fatalf("plain WithDeleted().Build() error = %v", err)
	}
}

func TestSoftDeleteSelectScansNullIntoValueAndPointerTime(t *testing.T) {
	deletedAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		state *allTestState
		query func(*allTestState) error
	}{
		{
			name: "value",
			state: &allTestState{
				columns: []string{"id", "deleted_at"},
				values:  [][]driver.Value{{int64(1), nil}, {int64(2), deletedAt}},
			},
			query: func(state *allTestState) error {
				database := openAllTestDB(t, state)
				values, err := Query[softDeleteVideo]().Select("ID", "DeletedAt").WithDeleted().All(context.Background(), database)
				if err != nil {
					return err
				}
				if len(values) != 2 || !values[0].DeletedAt.IsZero() || !values[1].DeletedAt.Equal(deletedAt) {
					t.Fatalf("value rows = %#v", values)
				}
				return nil
			},
		},
		{
			name: "pointer",
			state: &allTestState{
				columns: []string{"id", "deleted_at"},
				values:  [][]driver.Value{{int64(1), nil}, {int64(2), deletedAt}},
			},
			query: func(state *allTestState) error {
				database := openAllTestDB(t, state)
				values, err := Query[softDeletePointerVideo]().WithDeleted().All(context.Background(), database)
				if err != nil {
					return err
				}
				if len(values) != 2 || values[0].DeletedAt != nil || values[1].DeletedAt == nil || !values[1].DeletedAt.Equal(deletedAt) {
					t.Fatalf("pointer rows = %#v", values)
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.query(tt.state); err != nil {
				t.Fatalf("All() error = %v", err)
			}
		})
	}
}

func TestSoftDeletePreloadScopesRootAndRelationFilters(t *testing.T) {
	t.Parallel()

	defaultSQL, _, err := Query[softDeleteWatch]().Preload("Video").Build()
	if err != nil {
		t.Fatalf("Preload().Build() error = %v", err)
	}
	wantDefault := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`video_id`, `tidbgo_t0`.`deleted_at`, `tidbgo_t1`.`id`, `tidbgo_t1`.`channel_id`, `tidbgo_t1`.`title`, `tidbgo_t1`.`deleted_at` FROM `soft_delete_watches` AS `tidbgo_t0` LEFT JOIN `soft_delete_videos` AS `tidbgo_t1` ON (`tidbgo_t0`.`video_id` = `tidbgo_t1`.`id` AND `tidbgo_t1`.`deleted_at` IS NULL) WHERE `tidbgo_t0`.`deleted_at` IS NULL"
	if defaultSQL != wantDefault {
		t.Fatalf("Preload().Build() SQL = %q, want %q", defaultSQL, wantDefault)
	}

	allSQL, _, err := Query[softDeleteWatch]().WithDeleted().Preload("Video", PreloadWithDeleted()).Build()
	if err != nil {
		t.Fatalf("WithDeleted preload Build() error = %v", err)
	}
	wantAll := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`video_id`, `tidbgo_t0`.`deleted_at`, `tidbgo_t1`.`id`, `tidbgo_t1`.`channel_id`, `tidbgo_t1`.`title`, `tidbgo_t1`.`deleted_at` FROM `soft_delete_watches` AS `tidbgo_t0` LEFT JOIN `soft_delete_videos` AS `tidbgo_t1` ON (`tidbgo_t0`.`video_id` = `tidbgo_t1`.`id`)"
	if allSQL != wantAll {
		t.Fatalf("WithDeleted preload SQL = %q, want %q", allSQL, wantAll)
	}

	if _, _, err := Query[preloadUser]().Preload("Orders", PreloadWithDeleted()).Build(); err == nil || !strings.Contains(err.Error(), "requires a soft-delete field") {
		t.Fatalf("plain PreloadWithDeleted().Build() error = %v", err)
	}
}

func TestSoftDeleteInlinePreloadScansNullTimes(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{{
			columns: []string{"id", "video_id", "deleted_at", "video_id", "channel_id", "title", "video_deleted_at"},
			values:  [][]driver.Value{{int64(1), int64(10), nil, int64(10), int64(2), "active", nil}},
		}},
	}
	database := openPreloadTestDB(t, state)

	values, err := Query[softDeleteWatch]().Preload("Video").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(values) != 1 || !values[0].DeletedAt.IsZero() || values[0].Video == nil || !values[0].Video.DeletedAt.IsZero() {
		t.Fatalf("values = %#v", values)
	}
}

func TestSoftDeletePreloadFiltersSecondaryDirectAndManyToManyQueries(t *testing.T) {
	deletedAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		query     *SelectQuery[softDeleteChannel]
		responses []*preloadTestResponse
		wantSQL   string
		check     func([]softDeleteChannel)
	}{
		{
			name:  "has many active only",
			query: Query[softDeleteChannel]().Select("ID").Preload("Videos").Limit(1),
			responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
				{columns: []string{"id", "channel_id", "title", "deleted_at"}, values: [][]driver.Value{{int64(10), int64(1), "active", nil}}},
			},
			wantSQL: "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `deleted_at` IS NULL AND `channel_id` IN (?)",
			check: func(values []softDeleteChannel) {
				if len(values) != 1 || len(values[0].Videos) != 1 || !values[0].Videos[0].DeletedAt.IsZero() {
					t.Fatalf("active videos = %#v", values)
				}
			},
		},
		{
			name:  "has many active root key batch",
			query: Query[softDeleteChannel]().Select("ID").Preload("Videos"),
			responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
				{columns: []string{"id", "channel_id", "title", "deleted_at"}, values: [][]driver.Value{{int64(10), int64(1), "active", nil}}},
			},
			wantSQL: "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `deleted_at` IS NULL AND `channel_id` IN (?)",
			check: func(values []softDeleteChannel) {
				if len(values) != 1 || len(values[0].Videos) != 1 || values[0].Videos[0].ChannelID != 1 {
					t.Fatalf("all-source videos = %#v", values)
				}
			},
		},
		{
			name:  "has many all root rows",
			query: Query[softDeleteChannel]().WithDeleted().Select("ID").Preload("Videos"),
			responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}, {int64(2)}}},
				{columns: []string{"id", "channel_id", "title", "deleted_at"}, values: [][]driver.Value{{int64(10), int64(1), "active", nil}}},
			},
			wantSQL: "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `deleted_at` IS NULL",
			check: func(values []softDeleteChannel) {
				if len(values) != 2 || len(values[0].Videos) != 1 || len(values[1].Videos) != 0 {
					t.Fatalf("all-root videos = %#v", values)
				}
			},
		},
		{
			name:  "has many with deleted",
			query: Query[softDeleteChannel]().Select("ID").Preload("Videos", PreloadWithDeleted()).Limit(1),
			responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
				{columns: []string{"id", "channel_id", "title", "deleted_at"}, values: [][]driver.Value{{int64(11), int64(1), "deleted", deletedAt}}},
			},
			wantSQL: "SELECT `id`, `channel_id`, `title`, `deleted_at` FROM `soft_delete_videos` WHERE `channel_id` IN (?)",
			check: func(values []softDeleteChannel) {
				if len(values) != 1 || len(values[0].Videos) != 1 || !values[0].Videos[0].DeletedAt.Equal(deletedAt) {
					t.Fatalf("all videos = %#v", values)
				}
			},
		},
		{
			name:  "many to many active only",
			query: Query[softDeleteChannel]().Select("ID").Preload("Tags").Limit(1),
			responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
				{columns: []string{"channel_id", "id", "name", "deleted_at"}, values: [][]driver.Value{{int64(1), int64(20), "tag", nil}}},
			},
			wantSQL: "SELECT `j`.`channel_id`, `t`.`id`, `t`.`name`, `t`.`deleted_at` FROM `soft_delete_channel_tags` AS `j` JOIN `soft_delete_tags` AS `t` ON (`t`.`id` = `j`.`tag_id`) WHERE `t`.`deleted_at` IS NULL AND `j`.`channel_id` IN (?)",
			check: func(values []softDeleteChannel) {
				if len(values) != 1 || len(values[0].Tags) != 1 || values[0].Tags[0].Name != "tag" {
					t.Fatalf("active tags = %#v", values)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &preloadTestState{record: true, responses: tt.responses}
			database := openPreloadTestDB(t, state)
			values, err := tt.query.All(context.Background(), database)
			if err != nil {
				t.Fatalf("All() error = %v", err)
			}
			tt.check(values)
			calls := preloadCalls(state)
			if len(calls) != 2 || calls[1].query != tt.wantSQL {
				t.Fatalf("calls = %#v, want second SQL %q", calls, tt.wantSQL)
			}
		})
	}
}

func TestSoftDeleteRelationPredicateExcludesDeletedTargets(t *testing.T) {
	t.Parallel()

	directSQL, _, err := Query[softDeleteChannel]().Where(Has("Videos", Equal("Title", "demo"))).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, fragment := range []string{
		"`tidbgo_r0`.`deleted_at` IS NULL",
		"`tidbgo_r1`.`deleted_at` IS NULL",
		"`tidbgo_r1`.`title` = ?",
	} {
		if !strings.Contains(directSQL, fragment) {
			t.Fatalf("direct SQL = %q, want fragment %q", directSQL, fragment)
		}
	}

	manyToManySQL, _, err := Query[softDeleteChannel]().Where(Has("Tags", Equal("Name", "featured"))).Build()
	if err != nil {
		t.Fatalf("many-to-many Build() error = %v", err)
	}
	for _, fragment := range []string{
		"JOIN `soft_delete_tags` AS `tidbgo_r1`",
		"`tidbgo_r1`.`deleted_at` IS NULL",
		"`tidbgo_r1`.`name` = ?",
	} {
		if !strings.Contains(manyToManySQL, fragment) {
			t.Fatalf("many-to-many SQL = %q, want fragment %q", manyToManySQL, fragment)
		}
	}
}

func TestSoftDeleteMutationsUseNullAndActiveRowGuards(t *testing.T) {
	t.Parallel()

	value := softDeleteVideo{ID: 7, ChannelID: 3, Title: "demo"}
	insertSQL, arguments, err := Insert(&value).Build()
	if err != nil {
		t.Fatalf("Insert().Build() error = %v", err)
	}
	wantInsert := "INSERT INTO `soft_delete_videos` (`id`, `channel_id`, `title`, `deleted_at`) VALUES (?, ?, ?, ?)"
	if insertSQL != wantInsert || len(arguments) != 4 || arguments[3] != nil {
		t.Fatalf("Insert().Build() = %q, %#v", insertSQL, arguments)
	}

	upsertSQL, arguments, err := Upsert(&value).Build()
	if err != nil {
		t.Fatalf("Upsert().Build() error = %v", err)
	}
	if !strings.Contains(upsertSQL, "`deleted_at` = VALUES(`deleted_at`)") || len(arguments) != 4 || arguments[3] != nil {
		t.Fatalf("Upsert().Build() = %q, %#v", upsertSQL, arguments)
	}
	bulkUpsertSQL, arguments, err := UpsertMany([]softDeleteVideo{value, {ID: 8, ChannelID: 3, Title: "next"}}).Build()
	if err != nil {
		t.Fatalf("UpsertMany().Build() error = %v", err)
	}
	if !strings.Contains(bulkUpsertSQL, "`deleted_at` = VALUES(`deleted_at`)") || len(arguments) != 8 || arguments[3] != nil || arguments[7] != nil {
		t.Fatalf("UpsertMany().Build() = %q, %#v", bulkUpsertSQL, arguments)
	}

	updateSQL, arguments, err := Update(&value).Build()
	if err != nil {
		t.Fatalf("Update().Build() error = %v", err)
	}
	wantUpdate := "UPDATE `soft_delete_videos` SET `channel_id` = ?, `title` = ?, `deleted_at` = ? WHERE `id` = ? AND `deleted_at` IS NULL"
	if updateSQL != wantUpdate || len(arguments) != 4 || arguments[2] != nil || arguments[3] != int64(7) {
		t.Fatalf("Update().Build() = %q, %#v, want %q", updateSQL, arguments, wantUpdate)
	}

	restoreSQL, arguments, err := Update(&value, "DeletedAt").WithDeleted().Build()
	if err != nil {
		t.Fatalf("Update().WithDeleted().Build() error = %v", err)
	}
	if restoreSQL != "UPDATE `soft_delete_videos` SET `deleted_at` = ? WHERE `id` = ?" || len(arguments) != 2 || arguments[0] != nil {
		t.Fatalf("Update().WithDeleted().Build() = %q, %#v", restoreSQL, arguments)
	}

	conditionalSQL, arguments, err := UpdateWhere[softDeleteVideo](Set("Title", "new")).Where(Equal("ChannelID", int64(3))).Build()
	if err != nil {
		t.Fatalf("UpdateWhere().Build() error = %v", err)
	}
	wantConditional := "UPDATE `soft_delete_videos` SET `title` = ? WHERE `channel_id` = ? AND `deleted_at` IS NULL"
	if conditionalSQL != wantConditional || !reflect.DeepEqual(arguments, []any{"new", int64(3)}) {
		t.Fatalf("UpdateWhere().Build() = %q, %#v", conditionalSQL, arguments)
	}

	restoreManySQL, arguments, err := UpdateWhere[softDeleteVideo](Set("DeletedAt", time.Time{})).WithDeleted().Where(Equal("ChannelID", int64(3))).Build()
	if err != nil {
		t.Fatalf("restore UpdateWhere().Build() error = %v", err)
	}
	if restoreManySQL != "UPDATE `soft_delete_videos` SET `deleted_at` = ? WHERE `channel_id` = ?" || len(arguments) != 2 || arguments[0] != nil {
		t.Fatalf("restore UpdateWhere().Build() = %q, %#v", restoreManySQL, arguments)
	}
}

func TestSoftDeleteDeleteUsesServerTimestampWithoutMutatingModel(t *testing.T) {
	t.Parallel()

	value := softDeleteVideo{ID: 7, Title: "demo"}
	sqlText, arguments, err := Delete(&value).Build()
	if err != nil {
		t.Fatalf("Delete().Build() error = %v", err)
	}
	wantSQL := "UPDATE `soft_delete_videos` SET `deleted_at` = CURRENT_TIMESTAMP(6) WHERE `id` = ? AND `deleted_at` IS NULL"
	if sqlText != wantSQL || !reflect.DeepEqual(arguments, []any{int64(7)}) {
		t.Fatalf("Delete().Build() = %q, %#v, want %q, [7]", sqlText, arguments, wantSQL)
	}

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(current StatementEvent) {
		event = current
	})
	affected, err := Delete(&value).Exec(ctx, executor)
	if err != nil {
		t.Fatalf("Delete().Exec() error = %v", err)
	}
	if affected != 1 || executor.query != wantSQL || !value.DeletedAt.IsZero() || event.Operation != StatementUpdate {
		t.Fatalf("Delete().Exec() affected = %d, executor = %#v, value = %#v", affected, executor, value)
	}

	whereSQL, arguments, err := DeleteWhere[softDeleteVideo](Equal("ChannelID", int64(3))).Build()
	if err != nil {
		t.Fatalf("DeleteWhere().Build() error = %v", err)
	}
	wantWhere := "UPDATE `soft_delete_videos` SET `deleted_at` = CURRENT_TIMESTAMP(6) WHERE `channel_id` = ? AND `deleted_at` IS NULL"
	if whereSQL != wantWhere || !reflect.DeepEqual(arguments, []any{int64(3)}) {
		t.Fatalf("DeleteWhere().Build() = %q, %#v, want %q, [3]", whereSQL, arguments, wantWhere)
	}
}

func TestSoftDeletePointerMutationPreservesExplicitPointerValue(t *testing.T) {
	t.Parallel()

	zero := time.Time{}
	tests := []struct {
		name  string
		value softDeletePointerVideo
		want  any
	}{
		{name: "nil", value: softDeletePointerVideo{ID: 1}, want: nil},
		{name: "explicit zero", value: softDeletePointerVideo{ID: 2, DeletedAt: &zero}, want: &zero},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, arguments, err := Insert(&tt.value).Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if len(arguments) != 2 || !reflect.DeepEqual(arguments[1], tt.want) {
				t.Fatalf("arguments = %#v, want second %#v", arguments, tt.want)
			}
		})
	}

	deleteSQL, arguments, err := Delete(&softDeletePointerVideo{ID: 3}).Build()
	if err != nil {
		t.Fatalf("pointer Delete().Build() error = %v", err)
	}
	wantDelete := "UPDATE `soft_delete_pointer_videos` SET `deleted_at` = CURRENT_TIMESTAMP(6) WHERE `id` = ? AND `deleted_at` IS NULL"
	if deleteSQL != wantDelete || !reflect.DeepEqual(arguments, []any{int64(3)}) {
		t.Fatalf("pointer Delete().Build() = %q, %#v, want %q, [3]", deleteSQL, arguments, wantDelete)
	}
}

func TestSoftDeleteWithDeletedRequiresMappedFieldOnUpdates(t *testing.T) {
	t.Parallel()

	value := mutationModel{ID: 1, Name: "demo"}
	if _, _, err := Update(&value).WithDeleted().Build(); err == nil || !strings.Contains(err.Error(), "requires a soft-delete field") {
		t.Fatalf("plain Update().WithDeleted().Build() error = %v", err)
	}
	if _, _, err := UpdateWhere[mutationModel](Set("Name", "updated")).WithDeleted().Where(Equal("ID", int64(1))).Build(); err == nil || !strings.Contains(err.Error(), "requires a soft-delete field") {
		t.Fatalf("plain UpdateWhere().WithDeleted().Build() error = %v", err)
	}
}
