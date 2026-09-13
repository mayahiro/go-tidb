package tidbcloud

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type starterProjectionChannel struct {
	model.Meta `tidbgo:"table=tidbgo_it_projection_channels"`
	ID         int64  `tidbgo:",pk"`
	YouTubeID  string `tidbgo:"external_id"`
	Title      string
	DeletedAt  time.Time               `tidbgo:",soft_delete"`
	Links      []starterProjectionLink `tidbgo:"has_many,join=ID:ChannelID"`
}

type starterProjectionLink struct {
	model.Meta `tidbgo:"table=tidbgo_it_projection_links"`
	ID         int64 `tidbgo:",pk"`
	ChannelID  int64
}

type starterProjectionIdentity struct {
	YouTubeID string
	ID        int64
}

func TestTiDBCloudStarterScanAll(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected ScanAll tests")
	}
	config := parseTestDSN(t, dsn)
	config.ParseTime = true
	dsn = config.FormatDSN()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, db, dsn)
	for _, table := range []struct{ create, drop string }{
		{"CREATE TABLE tidbgo_it_projection_channels (id BIGINT PRIMARY KEY, external_id VARCHAR(64) NOT NULL, title VARCHAR(64) NOT NULL, deleted_at DATETIME(6) NULL)", "DROP TABLE tidbgo_it_projection_channels"},
		{"CREATE TABLE tidbgo_it_projection_links (id BIGINT PRIMARY KEY, channel_id BIGINT NOT NULL, KEY channel_idx (channel_id))", "DROP TABLE tidbgo_it_projection_links"},
	} {
		if _, err := db.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create ScanAll fixture; pre-existing tables are never removed", err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := db.ExecContext(cleanup, table.drop); err != nil {
				t.Errorf("drop owned ScanAll fixture: %s", redact.Error(err, dsn))
			}
		})
	}
	for _, insert := range []string{
		"INSERT INTO tidbgo_it_projection_channels VALUES (1, 'one', 'first', NULL), (2, 'two', 'second', NULL), (3, 'three', 'deleted', '2026-09-14 12:00:00'), (4, 'four', 'no link', NULL)",
		"INSERT INTO tidbgo_it_projection_links VALUES (1, 1), (2, 2), (3, 3)",
	} {
		if _, err := db.ExecContext(ctx, insert); err != nil {
			fatalDatabaseError(t, dsn, "insert ScanAll fixture", err)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve ScanAll connection", err)
	}
	defer conn.Close()
	for _, tc := range []struct {
		name string
		q    *orm.SelectQuery[starterProjectionChannel]
		ids  []int64
	}{
		{"active", orm.Query[starterProjectionChannel](), []int64{4, 2, 1}},
		{"with deleted", orm.Query[starterProjectionChannel]().WithDeleted(), []int64{4, 3, 2, 1}},
		{"offset and index", orm.Query[starterProjectionChannel]().ForceIndex("PRIMARY").Limit(1).Offset(1), []int64{2}},
		{"cursor and index", orm.Query[starterProjectionChannel]().ForceIndex("PRIMARY").SeekAfter(int64(3)).Limit(1), []int64{2}},
		{"relation", orm.Query[starterProjectionChannel]().Where(orm.Has("Links")).Limit(2), []int64{2, 1}},
		{"empty", orm.Query[starterProjectionChannel]().Where(orm.Equal("ID", int64(99))), []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.q.Select("ID", "YouTubeID").OrderBy(orm.Desc("ID"))
			reference, err := q.All(ctx, conn)
			if err != nil {
				fatalDatabaseError(t, dsn, "read model rows", err)
			}
			want := make([]starterProjectionIdentity, len(reference))
			ids := make([]int64, len(reference))
			for i, row := range reference {
				want[i] = starterProjectionIdentity{ID: row.ID, YouTubeID: row.YouTubeID}
				ids[i] = row.ID
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("IDs = %v, want %v", ids, tc.ids)
			}
			var events []orm.StatementEvent
			observed := orm.WithStatementObserver(ctx, func(event orm.StatementEvent) { events = append(events, event) }, orm.CollectServerRU())
			var values []starterProjectionIdentity
			if err := q.ScanAll(observed, conn, &values); err != nil {
				fatalDatabaseError(t, dsn, "scan DTO rows", err)
			}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("DTO rows = %#v, want %#v", values, want)
			}
			query, _, err := q.Build()
			if err != nil || len(events) != 1 || events[0].SQL != query || events[0].Error != nil || events[0].ServerRU == nil || !events[0].ServerRU.Known || events[0].ServerRU.Error != nil {
				t.Fatal("ScanAll must observe the source SQL and collect same-session ServerRU")
			}
		})
	}
	t.Run("scalars and nullable times", func(t *testing.T) {
		var ids []int64
		if err := orm.Query[starterProjectionChannel]().Select("ID").OrderBy(orm.Asc("ID")).ScanAll(ctx, conn, &ids); err != nil {
			fatalDatabaseError(t, dsn, "scan ID slice", err)
		}
		if !reflect.DeepEqual(ids, []int64{1, 2, 4}) {
			t.Fatalf("IDs = %v", ids)
		}
		var names []string
		if err := orm.Query[starterProjectionChannel]().Select("YouTubeID").OrderBy(orm.Asc("ID")).ScanAll(ctx, conn, &names); err != nil {
			fatalDatabaseError(t, dsn, "scan string slice", err)
		}
		if !reflect.DeepEqual(names, []string{"one", "two", "four"}) {
			t.Fatalf("names = %v", names)
		}
		var times []struct{ DeletedAt sql.NullTime }
		q := orm.Query[starterProjectionChannel]().Select("DeletedAt").WithDeleted().OrderBy(orm.Asc("ID"))
		if err := q.ScanAll(ctx, conn, &times); err != nil {
			fatalDatabaseError(t, dsn, "scan nullable DTO times", err)
		}
		var native []time.Time
		if err := q.ScanAll(ctx, conn, &native); err != nil {
			fatalDatabaseError(t, dsn, "scan soft-delete times", err)
		}
		if len(times) != 4 || len(native) != 4 || !native[0].IsZero() || !times[2].DeletedAt.Valid || native[2] != times[2].DeletedAt.Time || times[0].DeletedAt.Valid {
			t.Fatalf("nullable times = %#v; native = %#v", times, native)
		}
	})
}
