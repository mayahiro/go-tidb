package tidbcloud

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type starterViaCompilerParent struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_parents"`
	ID         int64                      `tidbgo:",pk"`
	Edges      []starterViaCompilerEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []starterViaCompilerTarget `tidbgo:"many_to_many,via=Edges.Target"`
}

type starterViaCompilerEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_edges"`
	ID         int64  `tidbgo:",pk"`
	ParentID   *int64 `tidbgo:",unique=pair"`
	TargetID   *int64 `tidbgo:",unique=pair"`
	V0         int64
	DeletedAt  *time.Time                `tidbgo:",soft_delete"`
	Target     *starterViaCompilerTarget `tidbgo:"belongs_to"`
}

type starterViaCompilerTarget struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_targets"`
	ID         int64  `tidbgo:",pk"`
	V0         string `tidbgo:",unique=v0"`
}

type starterViaScopedParent struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_parents"`
	ID         int64                    `tidbgo:",pk"`
	DeletedAt  *time.Time               `tidbgo:",soft_delete"`
	Edges      []starterViaScopedEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []starterViaScopedTarget `tidbgo:"many_to_many,via=Edges.Target"`
}

type starterViaScopedEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_edges"`
	ID         int64  `tidbgo:",pk"`
	ParentID   *int64 `tidbgo:",unique=pair"`
	TargetID   *int64 `tidbgo:",unique=pair"`
	V0         int64
	DeletedAt  *time.Time              `tidbgo:",soft_delete"`
	Target     *starterViaScopedTarget `tidbgo:"belongs_to"`
}

type starterViaScopedTarget struct {
	model.Meta `tidbgo:"table=tidbgo_it_via_compiler_targets"`
	ID         int64      `tidbgo:",pk"`
	V0         string     `tidbgo:",unique=v0"`
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func TestTiDBCloudStarterViaCompiler(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected via compiler test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	tables := []fixtureTable{
		{name: "tidbgo_it_via_compiler_parents", create: "CREATE TABLE tidbgo_it_via_compiler_parents (id BIGINT PRIMARY KEY, deleted_at DATETIME NULL)", drop: "DROP TABLE tidbgo_it_via_compiler_parents"},
		{name: "tidbgo_it_via_compiler_targets", create: "CREATE TABLE tidbgo_it_via_compiler_targets (id BIGINT PRIMARY KEY, v0 VARCHAR(20) NOT NULL UNIQUE, deleted_at DATETIME NULL)", drop: "DROP TABLE tidbgo_it_via_compiler_targets"},
		{name: "tidbgo_it_via_compiler_edges", create: "CREATE TABLE tidbgo_it_via_compiler_edges (id BIGINT PRIMARY KEY, parent_id BIGINT NULL, target_id BIGINT NULL, v0 BIGINT NOT NULL, deleted_at DATETIME NULL, UNIQUE KEY pair_key (parent_id,target_id), KEY target_scope_parent (target_id,deleted_at,parent_id))", drop: "DROP TABLE tidbgo_it_via_compiler_edges"},
	}
	var created []fixtureTable
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := database.ExecContext(cleanup, created[i].drop); err != nil {
				t.Errorf("drop via compiler fixture %s: %s", created[i].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create via compiler fixture; pre-existing tables are never removed", err)
		}
		created = append(created, table)
	}
	stamp := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	parents := make([]starterViaScopedParent, 200)
	edges := make([]starterViaCompilerEdge, 200)
	for i := range parents {
		id, targetID := int64(i+1), int64(7)
		if i >= 150 {
			targetID = 8
		}
		parents[i].ID = id
		if id%7 == 0 {
			parents[i].DeletedAt = &stamp
		}
		edges[i] = starterViaCompilerEdge{ID: id, ParentID: &id, TargetID: &targetID, V0: id % 3}
		if id%5 == 0 {
			edges[i].DeletedAt = &stamp
		}
	}
	if _, err := orm.InsertMany(parents).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "seed via compiler parents", err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO tidbgo_it_via_compiler_targets VALUES (7,'v7',NULL),(8,'v8',NULL),(9,'v9','2026-01-01')"); err != nil {
		fatalDatabaseError(t, dsn, "seed via compiler targets", err)
	}
	if _, err := orm.InsertMany(edges).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "seed via compiler edges", err)
	}
	// NULL-containing unique pairs can repeat. Neither shortcut may count
	// these as roots or spend a page slot on an unreachable edge.
	if _, err := database.ExecContext(ctx, "INSERT INTO tidbgo_it_via_compiler_edges VALUES (201,NULL,7,1,NULL),(202,NULL,7,2,NULL),(203,1,NULL,1,NULL),(204,1,NULL,2,NULL),(205,1,9,3,NULL)"); err != nil {
		fatalDatabaseError(t, dsn, "seed nullable and deleted-target edges", err)
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve via compiler connection", err)
	}
	defer connection.Close()
	exists := "EXISTS (SELECT 1 FROM tidbgo_it_via_compiler_edges e JOIN tidbgo_it_via_compiler_targets t ON t.id=e.target_id WHERE e.parent_id=p.id AND e.deleted_at IS NULL AND "
	verifyViaCompilerResults(t, ctx, connection, dsn, func() *orm.SelectQuery[starterViaCompilerParent] {
		return orm.Query[starterViaCompilerParent]().Where(orm.Has("Targets", orm.Equal("ID", int64(7))))
	}, exists+"t.id=?)", []any{int64(7)}, true, true)
	verifyViaCompilerResults(t, ctx, connection, dsn, func() *orm.SelectQuery[starterViaCompilerParent] {
		return orm.Query[starterViaCompilerParent]().Where(orm.Has("Targets", orm.Equal("V0", "v7")))
	}, exists+"t.v0=?)", []any{"v7"}, true, false)
	for _, includeRoot := range []bool{false, true} {
		condition := exists + "t.deleted_at IS NULL AND t.id=?)"
		if !includeRoot {
			condition += " AND p.deleted_at IS NULL"
		}
		verifyViaCompilerResults(t, ctx, connection, dsn, func() *orm.SelectQuery[starterViaScopedParent] {
			query := orm.Query[starterViaScopedParent]().Where(orm.Has("Targets", orm.Equal("ID", int64(7))))
			if includeRoot {
				query.WithDeleted()
			}
			return query
		}, condition, []any{int64(7)}, includeRoot, false)
	}
	verifyViaCompilerResults(t, ctx, connection, dsn, func() *orm.SelectQuery[starterViaScopedParent] {
		return orm.Query[starterViaScopedParent]().WithDeleted().Where(orm.Has("Targets", orm.Equal("ID", int64(9))))
	}, exists+"t.deleted_at IS NULL AND t.id=?)", []any{int64(9)}, true, false)
	query := orm.Query[starterViaCompilerParent]().Where(orm.Has("Targets", orm.Equal("ID", int64(7)))).OrderBy(orm.Desc("ID")).Limit(20)
	plan, err := query.ExplainAnalyze(ctx, connection)
	if err != nil {
		fatalDatabaseError(t, dsn, "explain via compiler result", err)
	}
	requireNoTiDBWarnings(t, ctx, connection, dsn, "via compiler EXPLAIN ANALYZE")
	for _, row := range plan {
		t.Logf("plan %s actRows=%d task=%s table=%s access=%s info=%s", row.ID, row.ActRows, row.Task, row.PhysicalTable, row.AccessObject, row.OperatorInfo)
	}
	compareViaCompilerRU(t, ctx, connection, dsn)
}

// This small, fixed sample compares the previous hinted EXISTS shape with
// the compiler rewrite. It is not an application benchmark or an RU gate.
func compareViaCompilerRU(t *testing.T, ctx context.Context, connection *sql.Conn, dsn string) {
	t.Helper()
	condition := " FROM tidbgo_it_via_compiler_parents p WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM tidbgo_it_via_compiler_edges e JOIN tidbgo_it_via_compiler_targets t ON t.id=e.target_id WHERE e.parent_id=p.id AND e.deleted_at IS NULL AND t.id=?)"
	for _, terminal := range []string{"count", "list"} {
		var durations, costs [2][]float64
		for sample := range 4 {
			for position := range 2 {
				mode := (sample + position) % 2
				start := time.Now()
				var count int64
				var rows []starterViaCompilerParent
				var err error
				if terminal == "count" {
					if mode == 0 {
						err = connection.QueryRowContext(ctx, "SELECT COUNT(*)"+condition, 7).Scan(&count)
					} else {
						count, err = orm.Query[starterViaCompilerParent]().Where(orm.Has("Targets", orm.Equal("ID", int64(7)))).Count(ctx, connection)
					}
				} else {
					if mode == 0 {
						rows, err = orm.Raw[starterViaCompilerParent]("SELECT p.id"+condition+" ORDER BY p.id DESC LIMIT 20", 7).All(ctx, connection)
					} else {
						rows, err = orm.Query[starterViaCompilerParent]().Select("ID").Where(orm.Has("Targets", orm.Equal("ID", int64(7)))).OrderBy(orm.Desc("ID")).Limit(20).All(ctx, connection)
					}
					count = int64(len(rows))
				}
				elapsed := time.Since(start)
				if err != nil {
					fatalDatabaseError(t, dsn, "measure via compiler "+terminal, err)
				}
				// Read RU immediately on the same pinned connection. Timing
				// excludes this additional round trip and any EXPLAIN work.
				ru, err := orm.LastServerRU(ctx, connection)
				if err != nil {
					fatalDatabaseError(t, dsn, "read via compiler RU", err)
				}
				want := int64(120)
				if terminal == "list" {
					want = 20
				}
				if count != want {
					t.Fatalf("measurement count=%d want=%d", count, want)
				}
				if terminal == "list" {
					wantID := int64(150)
					for _, row := range rows {
						for wantID%5 == 0 {
							wantID--
						}
						if row.ID != wantID {
							t.Fatalf("measurement row ID=%d want=%d", row.ID, wantID)
						}
						wantID--
					}
				}
				if sample != 0 {
					durations[mode] = append(durations[mode], float64(elapsed)/float64(time.Millisecond))
					costs[mode] = append(costs[mode], ru)
				}
			}
		}
		for mode, name := range []string{"hinted_exists", "via_rewrite"} {
			slices.Sort(durations[mode])
			slices.Sort(costs[mode])
			t.Logf("terminal=%s shape=%s median_ms=%.3f median_ServerRU=%.6f samples=3", terminal, name, durations[mode][1], costs[mode][1])
		}
	}
}

func verifyViaCompilerResults[T any](t *testing.T, ctx context.Context, connection *sql.Conn, dsn string, build func() *orm.SelectQuery[T], condition string, args []any, optimizedList, optimizedCount bool) {
	t.Helper()
	var countSQL string
	observed := orm.Observe(connection, func(event orm.StatementEvent) { countSQL = event.SQL })
	count, err := build().Count(ctx, observed)
	if err != nil {
		fatalDatabaseError(t, dsn, "count via compiler fixture", err)
	}
	requireNoTiDBWarnings(t, ctx, connection, dsn, "via compiler COUNT")
	if strings.HasPrefix(countSQL, "SELECT COUNT(*) FROM `tidbgo_it_via_compiler_edges`") != optimizedCount {
		t.Fatalf("unexpected count shape: %s", countSQL)
	}
	var wantCount int64
	if err := connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM tidbgo_it_via_compiler_parents p WHERE "+condition, args...).Scan(&wantCount); err != nil {
		fatalDatabaseError(t, dsn, "count reference EXISTS fixture", err)
	}
	if count != wantCount {
		t.Fatalf("count=%d want=%d", count, wantCount)
	}
	for _, descending := range []bool{false, true} {
		order, sqlOrder := orm.Asc("ID"), "ASC"
		if descending {
			order, sqlOrder = orm.Desc("ID"), "DESC"
		}
		for _, offset := range []int64{0, 1} {
			query := build().Select("ID").OrderBy(order).Limit(2).Offset(offset)
			statement, _, err := query.Build()
			if err != nil || strings.Contains(statement, ") AS `tidbgo_k0` STRAIGHT_JOIN ") != optimizedList {
				t.Fatalf("unexpected list shape: %s error=%v", statement, err)
			}
			actual, err := query.All(ctx, connection)
			if err != nil {
				fatalDatabaseError(t, dsn, "read via compiler fixture", err)
			}
			requireNoTiDBWarnings(t, ctx, connection, dsn, "via compiler SELECT")
			referenceArgs := append(append([]any(nil), args...), int64(2), offset)
			reference, err := orm.Raw[T]("SELECT p.id FROM tidbgo_it_via_compiler_parents p WHERE "+condition+" ORDER BY p.id "+sqlOrder+" LIMIT ? OFFSET ?", referenceArgs...).All(ctx, connection)
			if err != nil {
				fatalDatabaseError(t, dsn, "read reference EXISTS fixture", err)
			}
			if !reflect.DeepEqual(actual, reference) {
				t.Fatalf("result/order differs: actual=%#v reference=%#v", actual, reference)
			}
		}
	}
	t.Logf("model=%T count=%d optimizedList=%t optimizedCount=%t", *new(T), count, optimizedList, optimizedCount)
}
