package tidbcloud

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type starterViaParent struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_parents"`
	ID         int64              `tidbgo:",pk"`
	Edges      []starterViaEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []starterViaTarget `tidbgo:"many_to_many,via=Edges.Target"`
}
type starterViaEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	TargetID   *int64
	Priority   int64
	DeletedAt  *time.Time        `tidbgo:",soft_delete"`
	Target     *starterViaTarget `tidbgo:"belongs_to"`
}
type starterViaTarget struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_targets"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func TestTiDBCloudStarterViaPreload(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected via preload test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	tables := []fixtureTable{
		{name: "tidbgo_it_via_parents", create: "CREATE TABLE tidbgo_it_via_parents (id BIGINT PRIMARY KEY)", drop: "DROP TABLE tidbgo_it_via_parents"},
		{name: "tidbgo_it_via_targets", create: "CREATE TABLE tidbgo_it_via_targets (id BIGINT PRIMARY KEY, name VARCHAR(20) NOT NULL, deleted_at DATETIME NULL)", drop: "DROP TABLE tidbgo_it_via_targets"},
		{name: "tidbgo_it_via_edges", create: "CREATE TABLE tidbgo_it_via_edges (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, target_id BIGINT NULL, priority BIGINT NOT NULL, deleted_at DATETIME NULL, KEY parent_priority (parent_id, priority))", drop: "DROP TABLE tidbgo_it_via_edges"},
	}
	var created []fixtureTable
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for index := len(created) - 1; index >= 0; index-- {
			if _, err := database.ExecContext(cleanupCtx, created[index].drop); err != nil {
				t.Errorf("drop via fixture %s: %s", created[index].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create via fixture; pre-existing tables are never removed", err)
		}
		created = append(created, table)
	}
	for _, statement := range []string{
		"INSERT INTO tidbgo_it_via_parents VALUES (1), (2)",
		"INSERT INTO tidbgo_it_via_targets VALUES (11, 'a', NULL), (12, 'b', NULL), (13, 'deleted', '2026-01-01'), (14, 'd', NULL)",
		"INSERT INTO tidbgo_it_via_edges VALUES (1,1,11,20,NULL), (2,1,12,10,NULL), (3,1,11,30,NULL), (4,1,13,0,NULL), (5,1,14,0,'2026-01-01'), (6,1,999,0,NULL), (7,1,NULL,0,NULL), (8,2,14,1,NULL), (9,999,11,0,NULL)",
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			fatalDatabaseError(t, dsn, "seed via fixture", err)
		}
	}
	for _, bounded := range []bool{false, true} {
		for _, deleted := range []bool{false, true} {
			options := []orm.PreloadOption{orm.PreloadFields("ID", "Name"), orm.PreloadOrderBy(orm.Asc("Edges.Priority"), orm.Asc("ID"))}
			if deleted {
				options = append(options, orm.PreloadWithDeleted())
			}
			query := orm.Query[starterViaParent]().OrderBy(orm.Asc("ID")).Preload("Targets", options...)
			if bounded {
				query.Where(orm.In("ID", []int64{1, 2}))
			}
			statements := 0
			executor := orm.Observe(database, func(_ orm.StatementEvent) { statements++ })
			parents, err := query.All(ctx, executor)
			if err != nil {
				fatalDatabaseError(t, dsn, "read via targets", err)
			}
			want := []int64{12, 11, 11}
			if deleted {
				want = []int64{13, 14, 12, 11, 11}
			}
			if len(parents) != 2 || statements != 2 {
				t.Fatalf("parents/statements = %d/%d", len(parents), statements)
			}
			var ids []int64
			for _, target := range parents[0].Targets {
				ids = append(ids, target.ID)
			}
			if !reflect.DeepEqual(ids, want) || len(parents[0].Edges) != 0 || len(parents[1].Targets) != 1 || parents[1].Targets[0].ID != 14 {
				t.Fatalf("bounded=%t deleted=%t: IDs = %v", bounded, deleted, ids)
			}
		}
	}
	count, err := orm.Query[starterViaParent]().Where(orm.Has("Targets", orm.Equal("ID", int64(11)))).Count(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "count via parents with duplicate and orphan edges", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	for _, id := range []int64{13, 14} {
		count, err := orm.Query[starterViaParent]().Where(orm.Equal("ID", int64(1)), orm.Has("Targets", orm.Equal("ID", id))).Count(ctx, database)
		if err != nil {
			fatalDatabaseError(t, dsn, "count via parents with deleted edge or target", err)
		}
		if count != 0 {
			t.Fatalf("deleted target %d count = %d", id, count)
		}
	}
}
