package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
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

// This opt-in experiment compares physical SELECT shapes. It does not assert
// an RU threshold or establish that one shape is preferable for all data sets.
func TestTiDBCloudStarterOrderedListSQLShapes(t *testing.T) {
	if os.Getenv("TIDBGO_TEST_ORDERED_LIST") != "1" {
		t.Skip("TIDBGO_TEST_ORDERED_LIST=1 is required for the ordered list SQL comparison")
	}
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected ordered list comparison")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	installOrderedListFixture(t, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve ordered list connection", err)
	}
	defer connection.Close()
	var version string
	if err := connection.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		fatalDatabaseError(t, dsn, "read ordered list server version", err)
	}
	t.Logf("server=%s", version)
	for _, tc := range []orderedListCase{
		{name: "first_50", owner: 1, limit: 50},
		{name: "first_10", owner: 1, limit: 10},
		{name: "first_100", owner: 1, limit: 100},
		{name: "ascending", owner: 1, limit: 50, ascending: true},
		{name: "deep_offset", owner: 1, limit: 50, offset: 9000},
		{name: "large_limit", owner: 1, limit: 5000},
		{name: "few_matches", owner: 3, limit: 50},
		{name: "empty", owner: 4, limit: 50},
		{name: "unindexed_filter", owner: 1, limit: 50, filterTarget: true},
		{name: "missing_index", owner: 1, limit: 50, missingIndex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shapes := []string{"default", "force_index", "keys_first", "compiler"}
			if tc.missingIndex {
				if _, err := connection.ExecContext(ctx, "ALTER TABLE tidbgo_it_ordered_links DROP INDEX owner_order"); err != nil {
					fatalDatabaseError(t, dsn, "remove index from the newly created comparison fixture", err)
				}
				shapes = []string{"default", "keys_first", "compiler"}
			}
			var reference []orderedListRow
			costs, durations := make([][]float64, len(shapes)), make([][]float64, len(shapes))
			for sample := range 4 {
				for position := range len(shapes) {
					mode := (sample + position) % len(shapes)
					query, args := orderedListSQL(t, shapes[mode], tc)
					start := time.Now()
					var rows []orderedListRow
					if shapes[mode] == "compiler" {
						rows = readCompiledOrderedList(t, ctx, connection, dsn, tc)
					} else {
						rows = readOrderedList(t, ctx, connection, dsn, query, args)
					}
					elapsed := time.Since(start)
					ru, err := orm.LastServerRU(ctx, connection)
					if err != nil {
						fatalDatabaseError(t, dsn, "read ordered list RU", err)
					}
					if sample == 0 && position == 0 {
						reference = rows
						verifyOrderedListRows(t, rows, tc)
					}
					if !reflect.DeepEqual(rows, reference) {
						t.Fatalf("shape=%s returned different IDs, order, or values", shapes[mode])
					}
					if sample > 0 {
						costs[mode] = append(costs[mode], ru)
						durations[mode] = append(durations[mode], float64(elapsed)/float64(time.Millisecond))
						t.Logf("sample=%d shape=%s ServerRU=%.6f elapsed_ms=%.3f", sample, shapes[mode], ru, float64(elapsed)/float64(time.Millisecond))
					}
				}
			}
			for mode, shape := range shapes {
				slices.Sort(costs[mode])
				slices.Sort(durations[mode])
				t.Logf("shape=%s rows=%d median_ServerRU=%.6f median_ms=%.3f", shape, len(reference), costs[mode][1], durations[mode][1])
				query, args := orderedListSQL(t, shape, tc)
				plan := explainOrderedList(t, ctx, connection, dsn, query, args)
				requireNoTiDBWarnings(t, ctx, connection, dsn, "ordered list "+shape)
				for _, row := range plan {
					t.Logf("shape=%s plan=%s estRows=%.2f actRows=%d access=%s info=%s", shape, row.ID, row.EstRows, row.ActRows, row.AccessObject, row.OperatorInfo)
				}
			}
		})
	}
}

type orderedListCase struct {
	name         string
	owner        int64
	limit        int
	offset       int
	filterTarget bool
	missingIndex bool
	ascending    bool
}

type orderedListRow struct {
	ID        int64
	OwnerID   int64
	TargetID  int64
	AddedAt   time.Time
	RelatedID *int64
	Title     *string
	Payload   *string
}

type orderedListLinkModel struct {
	model.Meta `tidbgo:"table=tidbgo_it_ordered_links"`
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64
	TargetID   int64
	AddedAt    time.Time
	Target     *orderedListTargetModel `tidbgo:"belongs_to"`
}

type orderedListTargetModel struct {
	model.Meta `tidbgo:"table=tidbgo_it_ordered_targets"`
	ID         int64 `tidbgo:",pk"`
	Title      string
	Payload    string
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func orderedListQuery(tc orderedListCase) *orm.SelectQuery[orderedListLinkModel] {
	query := orm.Query[orderedListLinkModel]().Preload("Target").
		Where(orm.Equal("OwnerID", tc.owner)).
		Limit(int64(tc.limit)).Offset(int64(tc.offset))
	if tc.ascending {
		query.OrderBy(orm.Asc("AddedAt"), orm.Asc("ID"))
	} else {
		query.OrderBy(orm.Desc("AddedAt"), orm.Desc("ID"))
	}
	if tc.filterTarget {
		query.Where(orm.LessThan("TargetID", int64(5)))
	}
	return query
}

func orderedListSQL(t *testing.T, shape string, tc orderedListCase) (string, []any) {
	t.Helper()
	if shape == "compiler" {
		statement, args, err := orderedListQuery(tc).Build()
		if err != nil {
			t.Fatal(err)
		}
		wantRewrite := tc.offset == 0 && tc.limit <= 100 && !tc.filterTarget
		if strings.Contains(statement, " STRAIGHT_JOIN ") != wantRewrite {
			t.Fatalf("unexpected root page compiler decision for %s", tc.name)
		}
		return statement, args
	}
	index := ""
	if shape == "force_index" {
		index = " FORCE INDEX (owner_order)"
	}
	filter := "l.owner_id = ?"
	if tc.filterTarget {
		filter += " AND l.target_id < 5"
	}
	const projection = "SELECT l.id, l.owner_id, l.target_id, l.added_at, t.id AS related_id, t.title, t.payload FROM "
	const table = "tidbgo_it_ordered_links"
	const related = " LEFT JOIN tidbgo_it_ordered_targets t ON t.id = l.target_id AND t.deleted_at IS NULL"
	order := " ORDER BY l.added_at DESC, l.id DESC"
	if tc.ascending {
		order = " ORDER BY l.added_at ASC, l.id ASC"
	}
	const page = " LIMIT ? OFFSET ?"
	args := []any{tc.owner, tc.limit, tc.offset}
	if shape == "keys_first" {
		return projection + "(SELECT l.id FROM " + table + " l" + index + " WHERE " + filter + order + page + ") k STRAIGHT_JOIN " + table + " l ON l.id = k.id" + related + order, args
	}
	return projection + table + " l" + index + related + " WHERE " + filter + order + page, args
}

func readOrderedList(t *testing.T, ctx context.Context, connection *sql.Conn, dsn, query string, args []any) []orderedListRow {
	t.Helper()
	rows, err := orm.Raw[orderedListRow](query, args...).All(ctx, connection)
	if err != nil {
		fatalDatabaseError(t, dsn, "read ordered list", err)
	}
	return rows
}

func readCompiledOrderedList(t *testing.T, ctx context.Context, connection *sql.Conn, dsn string, tc orderedListCase) []orderedListRow {
	t.Helper()
	links, err := orderedListQuery(tc).All(ctx, connection)
	if err != nil {
		fatalDatabaseError(t, dsn, "execute compiled ordered list", err)
	}
	rows := make([]orderedListRow, len(links))
	for i, link := range links {
		rows[i] = orderedListRow{ID: link.ID, OwnerID: link.OwnerID, TargetID: link.TargetID, AddedAt: link.AddedAt}
		if link.Target != nil {
			rows[i].RelatedID = &link.Target.ID
			rows[i].Title = &link.Target.Title
			rows[i].Payload = &link.Target.Payload
		}
	}
	return rows
}

func verifyOrderedListRows(t *testing.T, rows []orderedListRow, tc orderedListCase) {
	t.Helper()
	var ids []int64
	for id := int64(12000); id > 0; id-- {
		owner := int64(1)
		if id > 10826 {
			owner = 2
		}
		if id > 11990 {
			owner = 3
		}
		if owner == tc.owner && (!tc.filterTarget || id%601+1 < 5) {
			ids = append(ids, id)
		}
	}
	if tc.ascending {
		slices.Reverse(ids)
	}
	start := min(tc.offset, len(ids))
	ids = ids[start:min(start+tc.limit, len(ids))]
	if len(rows) != len(ids) {
		t.Fatalf("page rows=%d want=%d", len(rows), len(ids))
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, row := range rows {
		id := ids[i]
		target := id%601 + 1
		addedAt := stamp.Add(time.Duration(id/4) * time.Second)
		if row.ID != id || row.OwnerID != tc.owner || row.TargetID != target || row.AddedAt.Format(time.DateTime) != addedAt.Format(time.DateTime) {
			t.Fatalf("row %d differs from the fixture reference", i)
		}
		if target == 601 || target%7 == 0 {
			if row.RelatedID != nil || row.Title != nil || row.Payload != nil {
				t.Fatalf("row %d must preserve a missing or deleted target as NULL", i)
			}
		} else if row.RelatedID == nil || *row.RelatedID != target || row.Title == nil || *row.Title != fmt.Sprintf("title-%d", target) || row.Payload == nil || *row.Payload != strings.Repeat("x", 512) {
			t.Fatalf("row %d has incorrect related values", i)
		}
	}
}

func explainOrderedList(t *testing.T, ctx context.Context, connection *sql.Conn, dsn, query string, args []any) orm.ExplainAnalyzePlan {
	t.Helper()
	rows, err := connection.QueryContext(ctx, "EXPLAIN ANALYZE "+query, args...)
	if err != nil {
		fatalDatabaseError(t, dsn, "explain ordered list", err)
	}
	defer rows.Close()
	var plan orm.ExplainAnalyzePlan
	for rows.Next() {
		var row orm.ExplainAnalyzeRow
		if err := rows.Scan(&row.ID, &row.EstRows, &row.ActRows, &row.Task, &row.AccessObject, &row.ExecutionInfo, &row.OperatorInfo, &row.Memory, &row.Disk); err != nil {
			fatalDatabaseError(t, dsn, "scan ordered list plan", err)
		}
		plan = append(plan, row)
	}
	if err := rows.Err(); err != nil {
		fatalDatabaseError(t, dsn, "finish ordered list plan", err)
	}
	return plan
}

func installOrderedListFixture(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()
	tables := []fixtureTable{
		{name: "tidbgo_it_ordered_links", create: "CREATE TABLE tidbgo_it_ordered_links (id BIGINT PRIMARY KEY CLUSTERED, owner_id BIGINT NOT NULL, target_id BIGINT NOT NULL, added_at DATETIME NOT NULL, KEY owner_order (owner_id,added_at,id))", drop: "DROP TABLE tidbgo_it_ordered_links"},
		{name: "tidbgo_it_ordered_targets", create: "CREATE TABLE tidbgo_it_ordered_targets (id BIGINT PRIMARY KEY CLUSTERED, title VARCHAR(64) NOT NULL, payload TEXT NOT NULL, deleted_at DATETIME NULL)", drop: "DROP TABLE tidbgo_it_ordered_targets"},
	}
	var created []fixtureTable
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := database.ExecContext(cleanup, created[i].drop); err != nil {
				t.Errorf("drop ordered list fixture %s: %s", created[i].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create ordered list fixture; existing tables are never removed", err)
		}
		created = append(created, table)
	}
	for batch := range 12 {
		var insert strings.Builder
		insert.WriteString("INSERT INTO tidbgo_it_ordered_links VALUES ")
		for i := range 1000 {
			id, owner := batch*1000+i+1, 1
			if id > 10826 {
				owner = 2
			}
			if id > 11990 {
				owner = 3
			}
			if i > 0 {
				insert.WriteByte(',')
			}
			fmt.Fprintf(&insert, "(%d,%d,%d,DATE_ADD('2026-01-01', INTERVAL %d SECOND))", id, owner, id%601+1, id/4)
		}
		if _, err := database.ExecContext(ctx, insert.String()); err != nil {
			fatalDatabaseError(t, dsn, "seed ordered list links", err)
		}
	}
	var insert strings.Builder
	insert.WriteString("INSERT INTO tidbgo_it_ordered_targets VALUES ")
	for id := 1; id <= 600; id++ {
		if id > 1 {
			insert.WriteByte(',')
		}
		deleted := "NULL"
		if id%7 == 0 {
			deleted = "'2026-01-01'"
		}
		fmt.Fprintf(&insert, "(%d,'title-%d',REPEAT('x',512),%s)", id, id, deleted)
	}
	if _, err := database.ExecContext(ctx, insert.String()); err != nil {
		fatalDatabaseError(t, dsn, "seed ordered list targets", err)
	}
}
