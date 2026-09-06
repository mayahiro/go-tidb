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

type costParent struct {
	model.Meta `tidbgo:"table=tidbgo_it_cost_parents"`
	ID         int64 `tidbgo:",pk"`
	V0         string
	V1         *int64
	Edges      []costEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []costTarget `tidbgo:"many_to_many,via=Edges.Target"`
	Notes      []costNote   `tidbgo:"has_many,join=ID:ParentID"`
}
type costEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_cost_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	TargetID   int64
	Priority   int64
	Target     *costTarget `tidbgo:"belongs_to"`
}
type costTarget struct {
	model.Meta `tidbgo:"table=tidbgo_it_cost_targets"`
	ID         int64 `tidbgo:",pk"`
	V0         string
	V1         *int64
	V2         string
	V3         *string
	V4         int64
	V5         *int64
	V6         string
	V7         *string
	V8         int64
	V9         *int64
	V10        string
	V11        *string
	V12        time.Time
	V13        *time.Time
	V14        string
	V15        []byte
	V16        string
}
type costNote struct {
	model.Meta `tidbgo:"table=tidbgo_it_cost_notes"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	V0         string
}

type costScenario struct {
	name        string
	first, last int64
	narrow      bool
	edges       int
}

func costQuery(s costScenario, via bool) *orm.SelectQuery[costParent] {
	q := orm.Query[costParent]().Where(orm.GreaterThanOrEqual("ID", s.first), orm.LessThanOrEqual("ID", s.last)).
		OrderBy(orm.Asc("ID")).Limit(s.last - s.first + 1).Preload("Notes")
	var fields []orm.PreloadOption
	if s.narrow {
		fields = []orm.PreloadOption{orm.PreloadFields("ID", "V0")}
	}
	if via {
		q.Preload("Targets", append([]orm.PreloadOption{orm.PreloadOrderBy(orm.Asc("Edges.Priority"), orm.Asc("ID"))}, fields...)...)
	} else {
		q.Preload("Edges", orm.PreloadOrderBy(orm.Asc("Priority"), orm.Asc("TargetID"))).Preload("Edges.Target", fields...)
	}
	return q
}

func costRead(ctx context.Context, executor orm.QueryExecutor, s costScenario, via bool) ([]costParent, error) {
	rows, err := costQuery(s, via).All(ctx, executor)
	if err != nil {
		return nil, err
	}
	if !via {
		for i := range rows {
			for _, edge := range rows[i].Edges {
				if edge.Target != nil {
					rows[i].Targets = append(rows[i].Targets, *edge.Target)
				}
			}
			rows[i].Edges = nil
		}
	}
	return rows, nil
}

// TestTiDBCloudStarterViaCost is an opt-in diagnostic, not a latency threshold
// test. It compares paired ORM calls and raw SQL draining, with separate RU
// and observed-statement passes. Raw draining does not hydrate domain models.
func TestTiDBCloudStarterViaCost(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_VIA_COST") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_VIA_COST=1 for the connected via diagnostic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, db, dsn)
	installCostFixture(t, ctx, db, dsn)
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve via diagnostic connection", err)
	}
	defer conn.Close()
	for _, s := range []costScenario{
		{"shared", 1, 20, false, 100}, {"narrow", 1, 20, true, 100},
		{"fanout", 21, 40, false, 2000}, {"empty", 41, 60, false, 0}, {"single", 61, 61, false, 1},
	} {
		t.Run(s.name, func(t *testing.T) {
			var statements [2][]orm.StatementEvent
			var reference []costParent
			for mode := range 2 {
				executor := orm.Observe(conn, func(e orm.StatementEvent) { statements[mode] = append(statements[mode], e) }, orm.IncludeStatementArguments())
				rows, err := costRead(ctx, executor, s, mode == 1)
				if err != nil {
					fatalDatabaseError(t, dsn, "warm via diagnostic", err)
				}
				if len(statements[mode]) != 3 || len(rows) != int(s.last-s.first+1) {
					t.Fatal("unexpected statement or parent count")
				}
				count := 0
				for _, row := range rows {
					count += len(row.Targets)
				}
				if count != s.edges {
					t.Fatalf("targets=%d want=%d", count, s.edges)
				}
				if mode == 1 && !reflect.DeepEqual(reference, rows) {
					t.Fatal("via and edge results differ")
				}
				reference = rows
			}
			var elapsed [4][]float64
			for round := -2; round < 6; round++ {
				for position := range 4 {
					variant := position
					if round%2 != 0 {
						variant = 3 - position
					}
					started := time.Now()
					if variant < 2 {
						rows, err := costRead(ctx, conn, s, variant == 1)
						if err != nil {
							fatalDatabaseError(t, dsn, "measure via ORM", err)
						}
						duration := time.Since(started)
						if !reflect.DeepEqual(reference, rows) {
							t.Fatal("measured ORM results differ")
						}
						if round >= 0 {
							elapsed[variant] = append(elapsed[variant], float64(duration)/float64(time.Millisecond))
						}
					} else {
						counts, err := costDrain(ctx, conn, statements[variant-2])
						if err != nil {
							fatalDatabaseError(t, dsn, "measure raw via SQL", err)
						}
						duration := time.Since(started)
						if counts[0] != len(reference) || counts[1] != len(reference) || counts[2] != s.edges {
							t.Fatalf("raw row counts=%v", counts)
						}
						if round >= 0 {
							elapsed[variant] = append(elapsed[variant], float64(duration)/float64(time.Millisecond))
						}
					}
				}
			}
			for i, name := range []string{"edge_orm", "via_orm", "edge_raw_drain", "via_raw_drain"} {
				t.Logf("%s ms=%v median_ms=%.6f", name, elapsed[i], costMedian(elapsed[i]))
			}
			// Observation is deliberately outside the unobserved latency pass.
			for mode := range 2 {
				var times [3][]float64
				var ru []float64
				for round := range 6 {
					var events []orm.StatementEvent
					options := []orm.StatementObserverOption{}
					if round >= 3 {
						options = append(options, orm.CollectServerRU())
					}
					executor := orm.Observe(conn, func(e orm.StatementEvent) { events = append(events, e) }, options...)
					if _, err := costRead(ctx, executor, s, mode == 1); err != nil {
						fatalDatabaseError(t, dsn, "observe via diagnostic", err)
					}
					if len(events) != 3 {
						t.Fatal("observed statement count changed")
					}
					var total float64
					for i, event := range events {
						if event.SQL != statements[mode][i].SQL {
							t.Fatal("observed SQL changed")
						}
						if round < 3 {
							times[i] = append(times[i], float64(event.Duration)/float64(time.Millisecond))
						} else {
							if event.ServerRU == nil || !event.ServerRU.Known || event.ServerRU.Error != nil {
								t.Fatal("incomplete RU sample")
							}
							total += event.ServerRU.Value
						}
					}
					if round >= 3 {
						ru = append(ru, total)
					}
				}
				t.Logf("via=%t observed_statement_ms=%v DML_RU=%v", mode == 1, times, ru)
				if s.name == "shared" {
					event := statements[mode][2]
					t.Logf("via=%t relation_SQL=%s", mode == 1, event.SQL)
					plan, err := conn.QueryContext(ctx, "EXPLAIN ANALYZE "+event.SQL, event.Arguments...)
					if err != nil {
						fatalDatabaseError(t, dsn, "explain via SQL", err)
					}
					columns, err := plan.Columns()
					if err != nil {
						plan.Close()
						fatalDatabaseError(t, dsn, "read plan columns", err)
					}
					for plan.Next() {
						values := make([]sql.NullString, len(columns))
						dest := make([]any, len(columns))
						for i := range values {
							dest[i] = &values[i]
						}
						if err := plan.Scan(dest...); err != nil {
							plan.Close()
							fatalDatabaseError(t, dsn, "scan via plan", err)
						}
						parts := make([]string, len(values))
						for i := range values {
							parts[i] = values[i].String
						}
						t.Logf("via=%t plan %s", mode == 1, strings.Join(parts, " | "))
					}
					if err := plan.Err(); err != nil {
						plan.Close()
						fatalDatabaseError(t, dsn, "read via plan", err)
					}
					plan.Close()
				}
			}
		})
	}
}

func costDrain(ctx context.Context, conn *sql.Conn, statements []orm.StatementEvent) ([]int, error) {
	counts := make([]int, len(statements))
	for i, statement := range statements {
		rows, err := conn.QueryContext(ctx, statement.SQL, statement.Arguments...)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for j := range values {
			dest[j] = &values[j]
		}
		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			counts[i]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func costMedian(values []float64) float64 {
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return (ordered[(len(ordered)-1)/2] + ordered[len(ordered)/2]) / 2
}

func installCostFixture(t *testing.T, ctx context.Context, db *sql.DB, dsn string) {
	t.Helper()
	tables := []fixtureTable{
		{name: "tidbgo_it_cost_parents", create: "CREATE TABLE tidbgo_it_cost_parents (id BIGINT PRIMARY KEY, v0 VARCHAR(30) NOT NULL, v1 BIGINT NULL)", drop: "DROP TABLE tidbgo_it_cost_parents"},
		{name: "tidbgo_it_cost_targets", create: "CREATE TABLE tidbgo_it_cost_targets (id BIGINT PRIMARY KEY, v0 VARCHAR(100) NOT NULL, v1 BIGINT NULL, v2 VARCHAR(100) NOT NULL, v3 VARCHAR(100) NULL, v4 BIGINT NOT NULL, v5 BIGINT NULL, v6 VARCHAR(100) NOT NULL, v7 VARCHAR(100) NULL, v8 BIGINT NOT NULL, v9 BIGINT NULL, v10 VARCHAR(100) NOT NULL, v11 VARCHAR(100) NULL, v12 DATETIME NOT NULL, v13 DATETIME NULL, v14 VARCHAR(100) NOT NULL, v15 BLOB NOT NULL, v16 VARCHAR(100) NOT NULL)", drop: "DROP TABLE tidbgo_it_cost_targets"},
		{name: "tidbgo_it_cost_edges", create: "CREATE TABLE tidbgo_it_cost_edges (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, target_id BIGINT NOT NULL, priority BIGINT NOT NULL, UNIQUE KEY parent_target (parent_id,target_id))", drop: "DROP TABLE tidbgo_it_cost_edges"},
		{name: "tidbgo_it_cost_notes", create: "CREATE TABLE tidbgo_it_cost_notes (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, v0 VARCHAR(30) NOT NULL, KEY parent (parent_id))", drop: "DROP TABLE tidbgo_it_cost_notes"},
	}
	var created []fixtureTable
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(cleanup, created[i].drop); err != nil {
				t.Errorf("cleanup via cost fixture %s: %s", created[i].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create via cost fixture; existing tables are never removed", err)
		}
		created = append(created, table)
	}
	parents := make([]costParent, 61)
	notes := make([]costNote, 61)
	for i := range parents {
		parents[i] = costParent{ID: int64(i + 1), V0: "parent"}
		notes[i] = costNote{ID: int64(i + 1), ParentID: int64(i + 1), V0: "note"}
	}
	targets := make([]costTarget, 100)
	optional, number, stamp := "optional", int64(10), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range targets {
		targets[i] = costTarget{ID: int64(i + 1), V0: fmt.Sprintf("value-%d", i), V2: "text", V3: &optional, V4: 5, V6: "value", V8: 9, V9: &number, V10: "text", V12: stamp, V13: &stamp, V14: "value", V15: []byte("bytes"), V16: "text"}
	}
	var edges []costEdge
	for i := range 100 {
		edges = append(edges, costEdge{ID: int64(i + 1), ParentID: int64(i/5 + 1), TargetID: int64(i%12 + 1), Priority: int64(i % 3)})
	}
	for i := range 2000 {
		edges = append(edges, costEdge{ID: int64(i + 101), ParentID: int64(i/100 + 21), TargetID: int64(i%100 + 1), Priority: int64(i % 3)})
	}
	edges = append(edges, costEdge{ID: 2101, ParentID: 61, TargetID: 1})
	if _, err := orm.InsertMany(parents).Exec(ctx, db); err != nil {
		fatalDatabaseError(t, dsn, "seed cost parents", err)
	}
	if _, err := orm.InsertMany(targets).Exec(ctx, db); err != nil {
		fatalDatabaseError(t, dsn, "seed cost targets", err)
	}
	if _, err := orm.InsertMany(edges).Exec(ctx, db); err != nil {
		fatalDatabaseError(t, dsn, "seed cost edges", err)
	}
	if _, err := orm.InsertMany(notes).Exec(ctx, db); err != nil {
		fatalDatabaseError(t, dsn, "seed cost notes", err)
	}
}
