package tidbcloud

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mayahiro/go-tidb/orm"
)

func TestRelationSyncDiff(t *testing.T) {
	before := []relationSyncEdge{{S: 1, T: 3, V0: 1, V1: "a"}, {S: 1, T: 4, V0: 2, V1: "b"}, {S: 1, T: 5, V0: 3, V1: "c"}}
	for _, test := range []struct {
		name    string
		after   []relationSyncEdge
		removed []int64
		changed []relationSyncEdge
	}{
		{name: "unchanged", after: before},
		{name: "empty", removed: []int64{3, 4, 5}},
		{name: "reordered", after: []relationSyncEdge{before[2], before[0], before[1]}},
		{name: "partial", after: []relationSyncEdge{before[0], {S: 1, T: 4, V0: 2, V1: "new"}, {S: 1, T: 6}}, removed: []int64{5}, changed: []relationSyncEdge{{S: 1, T: 4, V0: 2, V1: "new"}, {S: 1, T: 6}}},
		{name: "all", after: []relationSyncEdge{{S: 1, T: 7}}, removed: []int64{3, 4, 5}, changed: []relationSyncEdge{{S: 1, T: 7}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			removed, changed := relationSyncDiff(before, test.after)
			if !reflect.DeepEqual(removed, test.removed) || !reflect.DeepEqual(changed, test.changed) {
				t.Fatalf("diff = %v, %v; want %v, %v", removed, changed, test.removed, test.changed)
			}
		})
	}
}

func TestRelationSyncObservation(t *testing.T) {
	known := &orm.ServerRUObservation{Known: true, Value: 2.5}
	metrics := relationSyncObservation{}
	for _, operation := range []orm.StatementOperation{orm.StatementBegin, orm.StatementSelect, orm.StatementDelete, orm.StatementUpsert, orm.StatementCommit} {
		metrics.observe(orm.StatementEvent{Operation: operation, ServerRU: known})
	}
	if metrics.err != nil || metrics.ru != 7.5 || metrics.statements != 3 || metrics.writes != 2 || metrics.begins != 1 || metrics.commits != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	for _, event := range []orm.StatementEvent{
		{Operation: orm.StatementSelect},
		{Operation: orm.StatementDelete, ServerRU: &orm.ServerRUObservation{Known: false}},
		{Operation: orm.StatementSelect, ServerRU: &orm.ServerRUObservation{Known: true, Error: errors.New("probe")}},
		{Operation: orm.StatementInsert, Error: errors.New("statement")},
		{Operation: orm.StatementRollback},
	} {
		metrics := relationSyncObservation{}
		metrics.observe(orm.StatementEvent{Operation: orm.StatementBegin})
		metrics.observe(event)
		metrics.observe(orm.StatementEvent{Operation: orm.StatementSelect, ServerRU: known})
		if metrics.err == nil || metrics.ru != 0 {
			t.Fatalf("invalid event accepted: %+v", metrics)
		}
	}
	for _, operations := range [][]orm.StatementOperation{
		{orm.StatementCommit},
		{orm.StatementSelect},
		{orm.StatementBegin, orm.StatementBegin},
		{orm.StatementBegin, orm.StatementCommit},
		{orm.StatementBegin, orm.StatementSelect, orm.StatementCommit, orm.StatementDelete},
	} {
		metrics := relationSyncObservation{}
		for _, operation := range operations {
			metrics.observe(orm.StatementEvent{Operation: operation, ServerRU: known})
		}
		if metrics.err == nil {
			t.Fatalf("invalid event order accepted: %v", operations)
		}
	}
}

func TestTiDBCloudStarterRelationSyncCandidates(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected relation synchronization tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	installRelationSyncFixture(t, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve relation sync test connection", err)
	}
	defer connection.Close()
	for _, payload := range []bool{false, true} {
		for _, strategy := range []string{"replace", "set_based", "read_diff"} {
			for _, change := range []string{"unchanged", "partial", "all", "empty", "payload"} {
				values := relationSyncValues(10, change)
				if change == "empty" {
					values = nil
				}
				seed, err := prepareRelationSync(ctx, connection, 10, payload)
				if err != nil {
					fatalDatabaseError(t, dsn, "prepare relation sync test", err)
				}
				execute := func() error {
					return orm.Transaction(ctx, connection, func(tx *sql.Tx) error {
						return executeRelationSyncCandidate(ctx, tx, values, payload, strategy)
					})
				}
				for range 2 {
					if err := execute(); err != nil {
						fatalDatabaseError(t, dsn, "repeat relation synchronization", err)
					}
					if err := verifyRelationSyncResult(ctx, connection, values, payload, strategy != "replace", seed); err != nil {
						fatalDatabaseError(t, dsn, "verify repeated relation membership and payload", err)
					}
				}
			}
			seed, err := prepareRelationSync(ctx, connection, 10, payload)
			if err != nil {
				fatalDatabaseError(t, dsn, "prepare relation sync rollback", err)
			}
			rollback := errors.New("rollback synchronization")
			err = orm.Transaction(ctx, connection, func(tx *sql.Tx) error {
				if err := executeRelationSyncCandidate(ctx, tx, relationSyncValues(10, "partial"), payload, strategy); err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				fatalDatabaseError(t, dsn, "roll back synchronization", err)
			}
			if err := verifyRelationSyncResult(ctx, connection, relationSyncValues(10, "unchanged"), payload, true, seed); err != nil {
				fatalDatabaseError(t, dsn, "verify synchronization rollback", err)
			}
		}
	}
	database.SetMaxOpenConns(2)
	second, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "reserve concurrent relation sync connection", err)
	}
	defer second.Close()
	for _, strategy := range []string{"replace", "set_based", "read_diff"} {
		testRelationSyncParentLock(t, ctx, connection, second, dsn, strategy)
	}
}

func testRelationSyncParentLock(t *testing.T, ctx context.Context, first, second *sql.Conn, dsn, strategy string) {
	t.Helper()
	if _, err := prepareRelationSync(ctx, first, 0, true); err != nil {
		fatalDatabaseError(t, dsn, "seed an empty source for the parent lock test", err)
	}
	tx1, err := first.BeginTx(ctx, nil)
	if err != nil {
		fatalDatabaseError(t, dsn, "begin first sync transaction", err)
	}
	defer tx1.Rollback()
	tx2, err := second.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		fatalDatabaseError(t, dsn, "begin second sync transaction", err)
	}
	defer tx2.Rollback()
	// Establish an old snapshot before the first writer inserts any edges.
	if _, err := orm.Query[relationSyncEdge]().Where(orm.Equal("S", int64(1))).All(ctx, tx2); err != nil {
		fatalDatabaseError(t, dsn, "establish the second transaction snapshot", err)
	}
	if err := executeRelationSyncCandidate(ctx, tx1, relationSyncValues(10, "partial"), true, strategy); err != nil {
		fatalDatabaseError(t, dsn, "synchronize in the first transaction", err)
	}
	_, err = orm.Raw[relationSyncRoot]("SELECT id FROM tidbgo_it_sync_roots WHERE id = ? FOR UPDATE NOWAIT", int64(1)).Only(ctx, tx2)
	var lockError *mysql.MySQLError
	if !errors.As(err, &lockError) || lockError.Number != 3572 {
		t.Fatalf("expected parent row lock conflict, got error type %T", err)
	}
	if err := tx1.Commit(); err != nil {
		fatalDatabaseError(t, dsn, "commit the first sync transaction", err)
	}
	snapshot, err := orm.Query[relationSyncEdge]().Where(orm.Equal("S", int64(1))).All(ctx, tx2)
	if err != nil {
		fatalDatabaseError(t, dsn, "verify the older second-transaction snapshot", err)
	}
	if len(snapshot) != 0 {
		t.Fatal("the second transaction did not retain its empty snapshot")
	}
	rows, err := orm.Query[relationSyncEdge]().All(ctx, first)
	if err != nil {
		fatalDatabaseError(t, dsn, "read identities after first commit", err)
	}
	seed := make(map[int64]int64, len(rows))
	for _, row := range rows {
		key := row.T
		if row.S == 2 {
			key = -key
		}
		seed[key] = row.ID
	}
	values := relationSyncValues(10, "all")
	if err := executeRelationSyncCandidate(ctx, tx2, values, true, strategy); err != nil {
		fatalDatabaseError(t, dsn, "synchronize after waiting for the parent", err)
	}
	if err := tx2.Commit(); err != nil {
		fatalDatabaseError(t, dsn, "commit the second sync transaction", err)
	}
	if err := verifyRelationSyncResult(ctx, first, values, true, strategy != "replace", seed); err != nil {
		fatalDatabaseError(t, dsn, "verify current-read synchronization", err)
	}
}
