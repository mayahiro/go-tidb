package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type relationSyncRoot struct {
	model.Meta `tidbgo:"table=tidbgo_it_sync_roots"`
	ID         int64              `tidbgo:",pk"`
	Targets    []relationSyncRoot `tidbgo:"many_to_many,through=tidbgo_it_sync_pairs,source=ID:s,target=t:ID"`
}

type relationSyncPair struct {
	model.Meta `tidbgo:"table=tidbgo_it_sync_pairs"`
	S          int64 `tidbgo:",pk"`
	T          int64 `tidbgo:",pk"`
}

type relationSyncEdge struct {
	model.Meta `tidbgo:"table=tidbgo_it_sync_edges"`
	ID         int64 `tidbgo:",pk,auto_random"`
	S          int64 `tidbgo:",unique=pair"`
	T          int64 `tidbgo:",unique=pair"`
	V0         int64
	V1         string
}

// All candidates take the same parent lock and run inside one caller-owned
// pessimistic transaction. Only the synchronization strategy differs.
func BenchmarkTiDBCloudStarterRelationSync(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected relation synchronization benchmark")
	}
	config := parseTestDSN(b, dsn)
	b.Logf("relation sync transport: interpolateParams=%t clientFoundRows=%t", config.InterpolateParams, config.ClientFoundRows)
	b.Log("Statement-ServerRU includes SELECT and DML, but excludes BEGIN/COMMIT; it is not total transaction or billing RU")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	database := openTestDatabase(b, dsn)
	verifyConnectedTarget(b, ctx, database, dsn)
	installRelationSyncFixture(b, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve relation sync connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close relation sync connection: %s", redact.Error(err, dsn))
		}
	})
	for _, count := range []int{10, 100} {
		for _, payload := range []bool{false, true} {
			for _, change := range []string{"unchanged", "partial", "all", "payload"} {
				if change == "payload" && !payload {
					continue
				}
				for _, strategy := range []string{"replace", "set_based", "read_diff"} {
					b.Run(fmt.Sprintf("rows_%d/payload_%t/%s/%s", count, payload, change, strategy), func(b *testing.B) {
						benchmarkRelationSyncCase(b, ctx, connection, dsn, count, payload, change, strategy)
					})
				}
			}
		}
	}
}

func installRelationSyncFixture(t testing.TB, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()
	var mode string
	var autocommit bool
	if err := database.QueryRowContext(ctx, "SELECT @@tidb_txn_mode, @@autocommit").Scan(&mode, &autocommit); err != nil {
		fatalDatabaseError(t, dsn, "read relation sync transaction settings", err)
	}
	if mode != "pessimistic" || !autocommit {
		t.Fatal("relation sync comparison requires pessimistic transactions and session autocommit; settings are not changed by the test")
	}
	for _, table := range []fixtureTable{
		{name: "tidbgo_it_sync_roots", create: "CREATE TABLE tidbgo_it_sync_roots (id BIGINT NOT NULL PRIMARY KEY)", drop: "DROP TABLE tidbgo_it_sync_roots"},
		{name: "tidbgo_it_sync_pairs", create: "CREATE TABLE tidbgo_it_sync_pairs (s BIGINT NOT NULL, t BIGINT NOT NULL, PRIMARY KEY (s, t))", drop: "DROP TABLE tidbgo_it_sync_pairs"},
		{name: "tidbgo_it_sync_edges", create: "CREATE TABLE tidbgo_it_sync_edges (id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY, s BIGINT NOT NULL, t BIGINT NOT NULL, v0 BIGINT NOT NULL, v1 VARCHAR(128) NOT NULL, UNIQUE KEY pair (s, t))", drop: "DROP TABLE tidbgo_it_sync_edges"},
	} {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create relation sync table; a pre-existing table is never removed", err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := database.ExecContext(cleanup, table.drop); err != nil {
				t.Errorf("drop relation sync table %s: %s", table.name, redact.Error(err, dsn))
			}
		})
	}
	roots := make([]relationSyncRoot, 202)
	for i := range roots {
		roots[i].ID = int64(i + 1)
	}
	if _, err := orm.InsertMany(roots).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "seed relation sync roots", err)
	}
}

func relationSyncValues(count int, change string) []relationSyncEdge {
	values := make([]relationSyncEdge, count)
	for i := range values {
		values[i] = relationSyncEdge{S: 1, T: int64(i + 3), V0: int64(i), V1: strings.Repeat("x", 32)}
		if change == "all" || (change == "partial" && i < max(1, count/10)) {
			values[i].T += int64(count)
		}
		if (change == "partial" && i >= max(1, count/10) && i < max(2, count/5)) || (change == "payload" && i < max(1, count/10)) {
			values[i].V0++
		}
	}
	return values
}

func relationSyncTargets(values []relationSyncEdge) []int64 {
	keys := make([]int64, len(values))
	for i := range values {
		keys[i] = values[i].T
	}
	return keys
}

func executeRelationSyncCandidate(ctx context.Context, tx *sql.Tx, values []relationSyncEdge, payload bool, strategy string) error {
	if _, err := orm.Raw[relationSyncRoot]("SELECT id FROM tidbgo_it_sync_roots WHERE id = ? FOR UPDATE", int64(1)).Only(ctx, tx); err != nil {
		return err
	}
	keys := relationSyncTargets(values)
	if strategy == "read_diff" {
		if payload {
			existing, err := orm.Raw[relationSyncEdge]("SELECT t, v0, v1 FROM tidbgo_it_sync_edges WHERE s = ? FOR UPDATE", int64(1)).All(ctx, tx)
			if err != nil {
				return err
			}
			removed, changed := relationSyncDiff(existing, values)
			if len(removed) != 0 {
				if _, err := orm.DeleteWhere[relationSyncEdge](orm.Equal("S", int64(1)), orm.In("T", removed)).Exec(ctx, tx); err != nil {
					return err
				}
			}
			if len(changed) != 0 {
				_, err = orm.UpsertMany(changed, "V0", "V1").Exec(ctx, tx)
			}
			return err
		}
		existing, err := orm.Raw[relationSyncPair]("SELECT t FROM tidbgo_it_sync_pairs WHERE s = ? FOR UPDATE", int64(1)).All(ctx, tx)
		if err != nil {
			return err
		}
		previous := make([]relationSyncEdge, len(existing))
		for i := range existing {
			previous[i].T = existing[i].T
		}
		current := make([]relationSyncEdge, len(values))
		for i := range values {
			current[i].T = values[i].T
		}
		removed, changed := relationSyncDiff(previous, current)
		if _, err := orm.RemoveRelation[relationSyncRoot]("Targets", int64(1), removed...).Exec(ctx, tx); err != nil {
			return err
		}
		_, err = orm.AddRelation[relationSyncRoot]("Targets", int64(1), relationSyncTargets(changed)...).Exec(ctx, tx)
		return err
	}
	if strategy != "replace" && strategy != "set_based" {
		return fmt.Errorf("unknown relation sync candidate %q", strategy)
	}
	predicates := []orm.Predicate{orm.Equal("S", int64(1))}
	if strategy == "set_based" && len(keys) != 0 {
		predicates = append(predicates, orm.NotIn("T", keys))
	}
	if payload {
		if _, err := orm.DeleteWhere[relationSyncEdge](predicates...).Exec(ctx, tx); err != nil {
			return err
		}
		if strategy == "set_based" {
			_, err := orm.UpsertMany(values, "V0", "V1").Exec(ctx, tx)
			return err
		}
		_, err := orm.InsertMany(values).Exec(ctx, tx)
		return err
	}
	if _, err := orm.DeleteWhere[relationSyncPair](predicates...).Exec(ctx, tx); err != nil {
		return err
	}
	insert := orm.AddRelation[relationSyncRoot]("Targets", int64(1), keys...)
	if strategy == "set_based" {
		insert.IgnoreExisting()
	}
	_, err := insert.Exec(ctx, tx)
	return err
}

// This comparator is deliberately fixture-specific, not a public Go-value
// equality contract for arbitrary collations, SQL types, or custom scalars.
func relationSyncDiff(existing, desired []relationSyncEdge) ([]int64, []relationSyncEdge) {
	previous := make(map[int64]relationSyncEdge, len(existing))
	for _, row := range existing {
		previous[row.T] = row
	}
	var changed []relationSyncEdge
	for _, row := range desired {
		old, exists := previous[row.T]
		if !exists || old.V0 != row.V0 || old.V1 != row.V1 {
			changed = append(changed, row)
		}
		delete(previous, row.T)
	}
	var removed []int64
	for _, row := range existing {
		if _, exists := previous[row.T]; exists {
			removed = append(removed, row.T)
		}
	}
	return removed, changed
}

func benchmarkRelationSyncCase(b *testing.B, ctx context.Context, connection *sql.Conn, dsn string, count int, payload bool, change, strategy string) {
	values := relationSyncValues(count, change)
	execute := func(ctx context.Context) error {
		return orm.Transaction(ctx, connection, func(tx *sql.Tx) error {
			return executeRelationSyncCandidate(ctx, tx, values, payload, strategy)
		})
	}
	verify := func(seed map[int64]int64) {
		b.Helper()
		if err := verifyRelationSyncResult(ctx, connection, values, payload, strategy != "replace", seed); err != nil {
			fatalDatabaseError(b, dsn, "verify relation synchronization", err)
		}
	}
	prepare := func() map[int64]int64 {
		b.Helper()
		seed, err := prepareRelationSync(ctx, connection, count, payload)
		if err != nil {
			fatalDatabaseError(b, dsn, "reset relation synchronization", err)
		}
		return seed
	}
	seed := prepare()
	if err := execute(ctx); err != nil {
		fatalDatabaseError(b, dsn, "warm relation synchronization", err)
	}
	verify(seed)
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for range b.N {
		prepare()
		b.StartTimer()
		err := execute(ctx)
		b.StopTimer()
		if err != nil {
			fatalDatabaseError(b, dsn, "execute relation synchronization", err)
		}
	}
	var totalRU float64
	var statements, writes int
	for range 3 {
		seed := prepare()
		metrics := relationSyncObservation{}
		observed := orm.WithStatementObserver(ctx, metrics.observe, orm.CollectServerRU())
		if err := execute(observed); err != nil {
			fatalDatabaseError(b, dsn, "sample relation synchronization", err)
		}
		wantStatements, wantWrites := 3, 2
		if strategy == "read_diff" {
			wantStatements, wantWrites = 4, 2
			if change == "unchanged" {
				wantStatements, wantWrites = 2, 0
			} else if change == "payload" {
				wantStatements, wantWrites = 3, 1
			}
		}
		if metrics.err != nil || metrics.begins != 1 || metrics.commits != 1 || metrics.statements != wantStatements || metrics.writes != wantWrites {
			b.Fatalf("invalid relation sync observations: %+v", metrics)
		}
		totalRU += metrics.ru
		statements, writes = metrics.statements, metrics.writes
		verify(seed)
	}
	b.ReportMetric(totalRU/3, "Statement-ServerRU/op")
	b.ReportMetric(float64(statements), "statements/op")
	b.ReportMetric(float64(writes), "writes/op")
	b.ReportMetric(2, "tx-controls/op")
}

type relationSyncObservation struct {
	ru         float64
	statements int
	writes     int
	begins     int
	commits    int
	err        error
}

func (m *relationSyncObservation) observe(event orm.StatementEvent) {
	if m.err != nil {
		return
	}
	if event.Error != nil {
		m.err = event.Error
		return
	}
	switch event.Operation {
	case orm.StatementBegin:
		if m.begins != 0 || m.statements != 0 || m.commits != 0 {
			m.err = fmt.Errorf("unexpected BEGIN order")
		}
		m.begins++
		return
	case orm.StatementCommit:
		if m.begins != 1 || m.commits != 0 || m.statements == 0 {
			m.err = fmt.Errorf("unexpected COMMIT order")
		}
		m.commits++
		return
	case orm.StatementSelect:
	case orm.StatementInsert, orm.StatementUpsert, orm.StatementDelete:
		m.writes++
	default:
		m.err = fmt.Errorf("unexpected operation %s", event.Operation)
		return
	}
	if m.begins != 1 || m.commits != 0 {
		m.err = fmt.Errorf("statement outside transaction")
		return
	}
	if event.ServerRU == nil || !event.ServerRU.Known || event.ServerRU.Error != nil {
		m.err = fmt.Errorf("missing ServerRU for %s", event.Operation)
		return
	}
	m.ru += event.ServerRU.Value
	m.statements++
}

func prepareRelationSync(ctx context.Context, connection *sql.Conn, count int, payload bool) (map[int64]int64, error) {
	values := relationSyncValues(count, "unchanged")
	if payload {
		if _, err := orm.RawExec(ctx, connection, "DELETE FROM tidbgo_it_sync_edges"); err != nil {
			return nil, err
		}
		values = append(values, relationSyncEdge{S: 2, T: 3, V0: -1, V1: "untouched"})
		if _, err := orm.InsertMany(values).Exec(ctx, connection); err != nil {
			return nil, err
		}
		rows, err := orm.Query[relationSyncEdge]().All(ctx, connection)
		if err != nil {
			return nil, err
		}
		ids := make(map[int64]int64, len(rows))
		for _, row := range rows {
			key := row.T
			if row.S == 2 {
				key = -key
			}
			ids[key] = row.ID
		}
		return ids, nil
	}
	if _, err := orm.RawExec(ctx, connection, "DELETE FROM tidbgo_it_sync_pairs"); err != nil {
		return nil, err
	}
	pairs := make([]relationSyncPair, 0, len(values)+1)
	for _, row := range values {
		pairs = append(pairs, relationSyncPair{S: row.S, T: row.T})
	}
	pairs = append(pairs, relationSyncPair{S: 2, T: 3})
	_, err := orm.InsertMany(pairs).Exec(ctx, connection)
	return nil, err
}

func verifyRelationSyncResult(ctx context.Context, connection *sql.Conn, values []relationSyncEdge, payload, preserveIDs bool, seed map[int64]int64) error {
	if payload {
		rows, err := orm.Query[relationSyncEdge]().OrderBy(orm.Asc("S"), orm.Asc("T")).All(ctx, connection)
		if err != nil {
			return err
		}
		if len(rows) != len(values)+1 {
			return fmt.Errorf("edge count = %d, want %d", len(rows), len(values)+1)
		}
		want := make(map[int64]relationSyncEdge, len(values))
		for _, row := range values {
			if row.ID != 0 {
				return fmt.Errorf("input AUTO_RANDOM ID was changed")
			}
			want[row.T] = row
		}
		for _, row := range rows {
			if row.S == 2 {
				if row.T != 3 || row.V0 != -1 || row.V1 != "untouched" || row.ID != seed[-3] {
					return fmt.Errorf("unrelated source row was changed")
				}
				continue
			}
			expected, exists := want[row.T]
			if !exists || row.S != 1 || row.ID <= 0 || row.V0 != expected.V0 || row.V1 != expected.V1 {
				return fmt.Errorf("unexpected edge or payload for target %d", row.T)
			}
			if preserveIDs && seed[row.T] != 0 && row.ID != seed[row.T] {
				return fmt.Errorf("existing ID changed for target %d", row.T)
			}
		}
		return nil
	}
	rows, err := orm.Query[relationSyncPair]().OrderBy(orm.Asc("S"), orm.Asc("T")).All(ctx, connection)
	if err != nil {
		return err
	}
	want := make(map[relationSyncPair]bool, len(values)+1)
	for _, row := range values {
		want[relationSyncPair{S: 1, T: row.T}] = true
	}
	want[relationSyncPair{S: 2, T: 3}] = true
	actual := make(map[relationSyncPair]bool, len(rows))
	for _, row := range rows {
		actual[row] = true
	}
	if len(rows) != len(want) || !reflect.DeepEqual(actual, want) {
		return fmt.Errorf("pure junction result differs from desired membership")
	}
	return nil
}

// BenchmarkRelationSyncPlanning measures only input preparation, the
// fixture-specific diff, and offline SQL construction, without a driver,
// row scanning, locking, transaction control, or network round trips.
func BenchmarkRelationSyncPlanning(b *testing.B) {
	for _, count := range []int{10, 100} {
		for _, payload := range []bool{false, true} {
			for _, change := range []string{"unchanged", "partial", "all"} {
				existing := relationSyncValues(count, "unchanged")
				desired := relationSyncValues(count, change)
				for _, strategy := range []string{"replace", "set_based", "read_diff"} {
					b.Run(fmt.Sprintf("rows_%d/payload_%t/%s/%s", count, payload, change, strategy), func(b *testing.B) {
						if err := buildRelationSyncCandidate(existing, desired, payload, strategy); err != nil {
							b.Fatal(err)
						}
						b.ReportAllocs()
						for b.Loop() {
							if err := buildRelationSyncCandidate(existing, desired, payload, strategy); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

func buildRelationSyncCandidate(existing, desired []relationSyncEdge, payload bool, strategy string) error {
	// Construct the key slice for all candidates, just as the connected path does.
	keys := relationSyncTargets(desired)
	if strategy == "read_diff" {
		if !payload {
			previous := make([]relationSyncEdge, len(existing))
			current := make([]relationSyncEdge, len(desired))
			for i := range existing {
				previous[i].T = existing[i].T
			}
			for i := range desired {
				current[i].T = desired[i].T
			}
			existing, desired = previous, current
		}
		removed, changed := relationSyncDiff(existing, desired)
		if !payload {
			if _, _, err := orm.RemoveRelation[relationSyncRoot]("Targets", int64(1), removed...).Build(); err != nil {
				return err
			}
			_, _, err := orm.AddRelation[relationSyncRoot]("Targets", int64(1), relationSyncTargets(changed)...).Build()
			return err
		}
		if len(removed) != 0 {
			if _, _, err := orm.DeleteWhere[relationSyncEdge](orm.Equal("S", int64(1)), orm.In("T", removed)).Build(); err != nil {
				return err
			}
		}
		if len(changed) != 0 {
			_, _, err := orm.UpsertMany(changed, "V0", "V1").Build()
			return err
		}
		return nil
	}
	if strategy != "replace" && strategy != "set_based" {
		return fmt.Errorf("unknown relation sync candidate %q", strategy)
	}
	predicates := []orm.Predicate{orm.Equal("S", int64(1))}
	if strategy == "set_based" && len(keys) != 0 {
		predicates = append(predicates, orm.NotIn("T", keys))
	}
	if payload {
		if _, _, err := orm.DeleteWhere[relationSyncEdge](predicates...).Build(); err != nil {
			return err
		}
		if strategy == "set_based" {
			_, _, err := orm.UpsertMany(desired, "V0", "V1").Build()
			return err
		}
		_, _, err := orm.InsertMany(desired).Build()
		return err
	}
	if _, _, err := orm.DeleteWhere[relationSyncPair](predicates...).Build(); err != nil {
		return err
	}
	insert := orm.AddRelation[relationSyncRoot]("Targets", int64(1), keys...)
	if strategy == "set_based" {
		insert.IgnoreExisting()
	}
	_, _, err := insert.Build()
	return err
}
