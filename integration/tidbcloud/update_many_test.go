package tidbcloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type starterUpdateManyRow struct {
	model.Meta `tidbgo:"table=tidbgo_it_update_many"`
	ID         int64 `tidbgo:",pk,auto_random"`
	V0         int64
	V1         *string
	V2         bool
	V3         starterDecimal
	V4         []byte
	V5         time.Time
	V6         string
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
	Computed   int64      `tidbgo:",computed"`
}

type starterUpdateManyComposite struct {
	model.Meta `tidbgo:"table=tidbgo_it_update_many_composite"`
	K0         int64  `tidbgo:",pk"`
	K1         string `tidbgo:",pk"`
	V0         *string
}

type starterUpdateManyUnsigned struct {
	model.Meta `tidbgo:"table=tidbgo_it_update_many_unsigned"`
	ID         uint64 `tidbgo:",pk"`
	V0         uint64
}

func TestTiDBCloudStarterUpdateMany(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected bulk UPDATE test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	tables := []fixtureTable{
		{name: "tidbgo_it_update_many", create: "CREATE TABLE tidbgo_it_update_many (id BIGINT PRIMARY KEY AUTO_RANDOM, v0 BIGINT NOT NULL, v1 VARCHAR(64) NULL, v2 BOOLEAN NOT NULL, v3 DECIMAL(30,6) NOT NULL, v4 BLOB NULL, v5 DATETIME(6) NOT NULL, v6 JSON NOT NULL, deleted_at DATETIME(6) NULL, computed BIGINT AS (v0 * 2) STORED, UNIQUE KEY unique_v0 (v0))", drop: "DROP TABLE tidbgo_it_update_many"},
		{name: "tidbgo_it_update_many_composite", create: "CREATE TABLE tidbgo_it_update_many_composite (k0 BIGINT NOT NULL, k1 VARCHAR(64) COLLATE utf8mb4_bin NOT NULL, v0 VARCHAR(64) NULL, PRIMARY KEY (k0, k1))", drop: "DROP TABLE tidbgo_it_update_many_composite"},
		{name: "tidbgo_it_update_many_unsigned", create: "CREATE TABLE tidbgo_it_update_many_unsigned (id BIGINT UNSIGNED PRIMARY KEY, v0 BIGINT UNSIGNED NOT NULL)", drop: "DROP TABLE tidbgo_it_update_many_unsigned"},
	}
	var created []fixtureTable
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := database.ExecContext(cleanup, created[i].drop); err != nil {
				t.Errorf("drop bulk UPDATE fixture %s: %s", created[i].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(t, dsn, "create bulk UPDATE fixture; pre-existing tables are never removed", err)
		}
		created = append(created, table)
	}
	for _, interpolate := range []bool{false, true} {
		for _, foundRows := range []bool{false, true} {
			t.Run(fmt.Sprintf("interpolate_%t/found_rows_%t", interpolate, foundRows), func(t *testing.T) {
				config := parseTestDSN(t, dsn)
				config.ParseTime, config.InterpolateParams, config.ClientFoundRows = true, interpolate, foundRows
				currentDSN := config.FormatDSN()
				executor := openTestDatabase(t, currentDSN)
				testUpdateManyValues(t, ctx, executor, currentDSN, foundRows)
				testUpdateManyCompositeAndUnsigned(t, ctx, executor, currentDSN)
			})
		}
	}
}

func testUpdateManyValues(t *testing.T, ctx context.Context, database *sql.DB, dsn string, foundRows bool) {
	t.Helper()
	if _, err := database.ExecContext(ctx, "DELETE FROM tidbgo_it_update_many"); err != nil {
		fatalDatabaseError(t, dsn, "reset owned bulk UPDATE fixture", err)
	}
	stamp := time.Date(2026, 9, 6, 12, 34, 56, 123456000, time.UTC)
	a, b := "first", "second"
	values := []starterUpdateManyRow{
		{V0: 1, V1: &a, V3: decimal("1.250000"), V4: []byte{0, 1, 255}, V5: stamp, V6: "{}"},
		{V0: 2, V1: &b, V2: true, V3: decimal("2.500000"), V5: stamp, V6: "{}"},
		{V0: 3, V3: decimal("3.750000"), V5: stamp, V6: "{}", DeletedAt: &stamp},
	}
	for i := range values {
		if _, err := orm.Insert(&values[i]).Exec(ctx, database); err != nil {
			fatalDatabaseError(t, dsn, "insert generated-ID bulk UPDATE fixture", err)
		}
	}
	beforeIDs := []int64{values[0].ID, values[1].ID, values[2].ID}
	values[0].V0, values[0].V1, values[0].V2 = 11, nil, true
	values[0].V3, values[0].V4 = decimal("12345678901234567890.123456"), nil
	values[0].V5 = stamp.Add(time.Hour)
	values[0].V6 = `{"a": [1,2]}`
	text := "quote' and 日本語 ?"
	values[1].V0, values[1].V1, values[1].V2 = 12, &text, false
	values[1].V3, values[1].V4 = decimal("-12345678901234567890.123456"), []byte{255, 0, 2}
	values[1].V6 = `{"b": null}`
	values[2].V0 = 13
	missing := values[0]
	missing.ID, missing.V0 = -1, 999
	pointers := []*starterUpdateManyRow{&values[0], &values[1], &values[2], &missing}
	affected, err := orm.UpdateMany(pointers).Exec(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "update native, NULL, custom decimal, bytes and time values", err)
	}
	if affected != 2 || !reflect.DeepEqual(beforeIDs, []int64{values[0].ID, values[1].ID, values[2].ID}) || missing.ID != -1 {
		t.Fatalf("affected=%d or input IDs changed", affected)
	}
	checkUpdateManyRows(t, ctx, database, dsn, values, false)
	affected, err = orm.UpdateMany(pointers).Exec(ctx, database)
	wantUnchanged := int64(0)
	if foundRows {
		wantUnchanged = 2
	}
	if err != nil || affected != wantUnchanged {
		t.Fatalf("unchanged rows=%d want=%d error=%s", affected, wantUnchanged, redact.Error(err, dsn))
	}
	// A non-primary unique conflict is an UPDATE error, not an upsert target.
	conflict := []starterUpdateManyRow{{ID: values[0].ID, V0: 12}, {ID: values[1].ID, V0: 12}}
	if _, err := orm.UpdateMany(conflict, "V0").Exec(ctx, database); err == nil {
		t.Fatal("bulk UPDATE silently accepted a unique conflict")
	}
	checkUpdateManyRows(t, ctx, database, dsn, values, false)
	values[2].DeletedAt = nil
	if _, err := orm.UpdateMany(values, "V0", "DeletedAt").WithDeleted().Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "restore rows through bulk UPDATE", err)
	}
	checkUpdateManyRows(t, ctx, database, dsn, values, true)
	var operations []orm.StatementOperation
	observed := orm.Observe(database, func(event orm.StatementEvent) { operations = append(operations, event.Operation) })
	rollback := errors.New("intentional rollback")
	rollbackText := "must not persist"
	rollbackValues := []starterUpdateManyRow{values[0], values[1]}
	rollbackValues[0].V1, rollbackValues[1].V1 = &rollbackText, &rollbackText
	err = orm.Transaction(ctx, observed, func(tx orm.Executor) error {
		if _, err := orm.UpdateMany(rollbackValues, "V1").Exec(ctx, tx); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || !reflect.DeepEqual(operations, []orm.StatementOperation{orm.StatementBegin, orm.StatementUpdate, orm.StatementRollback}) {
		t.Fatalf("transaction observations=%v error=%s", operations, redact.Error(err, dsn))
	}
	checkUpdateManyRows(t, ctx, database, dsn, values, true)
	operations = nil
	committed := "committed"
	values[0].V1, values[1].V1 = &committed, &committed
	err = orm.Transaction(ctx, observed, func(tx orm.Executor) error {
		_, err := orm.UpdateMany(values[:2], "V1").Exec(ctx, tx)
		return err
	})
	if err != nil || !reflect.DeepEqual(operations, []orm.StatementOperation{orm.StatementBegin, orm.StatementUpdate, orm.StatementCommit}) {
		t.Fatalf("commit observations=%v error=%s", operations, redact.Error(err, dsn))
	}
	checkUpdateManyRows(t, ctx, database, dsn, values, true)
}

func checkUpdateManyRows(t *testing.T, ctx context.Context, database *sql.DB, dsn string, expected []starterUpdateManyRow, restored bool) {
	t.Helper()
	rows, err := orm.Raw[starterUpdateManyRow]("SELECT id, v0, v1, v2, v3, v4, v5, v6, deleted_at, computed FROM tidbgo_it_update_many").All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "read bulk UPDATE results", err)
	}
	if len(rows) != len(expected) {
		t.Fatalf("bulk UPDATE inserted a missing row: rows=%d", len(rows))
	}
	for _, row := range rows {
		matched := false
		for i, want := range expected {
			if row.ID != want.ID {
				continue
			}
			matched = true
			if !restored && i == 2 {
				want.V0 = 3
			}
			want.Computed = want.V0 * 2
			var gotJSON, wantJSON any
			if err := json.Unmarshal([]byte(row.V6), &gotJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(want.V6), &wantJSON); err != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Fatalf("bulk UPDATE changed JSON semantics for fixture row %d", i)
			}
			row.V6 = want.V6
			if !row.V5.Equal(want.V5) {
				t.Fatal("bulk UPDATE changed the time precision")
			}
			row.V5 = want.V5
			if row.DeletedAt != nil && want.DeletedAt != nil && row.DeletedAt.Equal(*want.DeletedAt) {
				row.DeletedAt = want.DeletedAt
			}
			if !reflect.DeepEqual(row, want) {
				t.Fatalf("bulk UPDATE result differs for fixture row %d: %#v != %#v", i, row, want)
			}
			break
		}
		if !matched {
			t.Fatalf("bulk UPDATE returned an unexpected primary key: %d", row.ID)
		}
	}
}

func testUpdateManyCompositeAndUnsigned(t *testing.T, ctx context.Context, database *sql.DB, dsn string) {
	t.Helper()
	for _, statement := range []string{
		"DELETE FROM tidbgo_it_update_many_composite",
		"DELETE FROM tidbgo_it_update_many_unsigned",
		"INSERT INTO tidbgo_it_update_many_composite VALUES (1,'a','before'),(1,'b','before'),(2,'a','before'),(2,'b','before')",
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			fatalDatabaseError(t, dsn, "seed composite bulk UPDATE fixture", err)
		}
	}
	text := "after' ?"
	values := []starterUpdateManyComposite{{K0: 1, K1: "a"}, {K0: 2, K1: "b", V0: &text}, {K0: 3, K1: "missing"}}
	if affected, err := orm.UpdateMany(values).Exec(ctx, database); err != nil || affected != 2 {
		t.Fatalf("composite affected=%d error=%s", affected, redact.Error(err, dsn))
	}
	rows, err := orm.Query[starterUpdateManyComposite]().OrderBy(orm.Asc("K0"), orm.Asc("K1")).All(ctx, database)
	if err != nil {
		fatalDatabaseError(t, dsn, "read composite bulk UPDATE results", err)
	}
	if len(rows) != 4 || rows[0].V0 != nil || rows[3].V0 == nil || *rows[3].V0 != text || rows[1].V0 == nil || *rows[1].V0 != "before" || rows[2].V0 == nil || *rows[2].V0 != "before" {
		t.Fatalf("composite UPDATE crossed key components: %#v", rows)
	}
	unsigned := []starterUpdateManyUnsigned{{ID: math.MaxUint64, V0: 1}, {ID: math.MaxInt64 + 1, V0: 2}}
	if _, err := orm.InsertMany(unsigned).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "seed unsigned bulk UPDATE fixture", err)
	}
	unsigned[0].V0, unsigned[1].V0 = math.MaxUint64, math.MaxInt64+1
	if _, err := orm.UpdateMany(unsigned).Exec(ctx, database); err != nil {
		fatalDatabaseError(t, dsn, "update high-bit unsigned primary keys and values", err)
	}
	got, err := orm.Query[starterUpdateManyUnsigned]().OrderBy(orm.Desc("ID")).All(ctx, database)
	if err != nil || !reflect.DeepEqual(got, unsigned) {
		t.Fatalf("unsigned results=%#v error=%s", got, redact.Error(err, dsn))
	}
}
