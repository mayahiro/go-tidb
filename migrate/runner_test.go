package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The fixture models database state independently of the runner. SHOW CREATE
// reads that state, while ALTER effects are explicit test-owned definitions.
type memoryDatabase struct {
	mu           sync.Mutex
	tables       map[string]string
	kinds        map[string]string
	replicas     [][]driver.Value
	effects      map[string]func()
	events       []Event
	managed      bool
	owner        *memoryConn
	ddlCount     int
	failSQL      string
	failProgress bool
	failRelease  bool
	onDDL        func(string)
}
type memoryConnector struct{ db *memoryDatabase }

func (c memoryConnector) Connect(context.Context) (driver.Conn, error) {
	return &memoryConn{db: c.db}, nil
}
func (c memoryConnector) Driver() driver.Driver { return memoryDriver{c.db} }

type memoryDriver struct{ db *memoryDatabase }

func (d memoryDriver) Open(string) (driver.Conn, error) { return &memoryConn{db: d.db}, nil }

type memoryConn struct {
	db          *memoryDatabase
	pending     []Event
	transaction bool
}

func (c *memoryConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (c *memoryConn) Close() error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.db.owner == c {
		c.db.owner = nil
	}
	return nil
}
func (c *memoryConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *memoryConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.pending = append([]Event(nil), c.db.events...)
	c.transaction = true
	return memoryTx{c}, nil
}

type memoryTx struct{ c *memoryConn }

func (tx memoryTx) Commit() error {
	tx.c.db.mu.Lock()
	defer tx.c.db.mu.Unlock()
	tx.c.db.events = tx.c.pending
	tx.c.transaction = false
	return nil
}
func (tx memoryTx) Rollback() error { tx.c.transaction = false; tx.c.pending = nil; return nil }
func (c *memoryConn) journal() *[]Event {
	if c.transaction {
		return &c.pending
	}
	return &c.db.events
}

type memoryResult int64

func (r memoryResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r memoryResult) RowsAffected() (int64, error) { return 1, nil }

type memoryRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *memoryRows) Columns() []string { return r.columns }
func (r *memoryRows) Close() error      { return nil }
func (r *memoryRows) Next(dest []driver.Value) error {
	if r.index == len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}
func memoryData(width int, rows ...[]driver.Value) driver.Rows {
	columns := make([]string, width)
	for i := range columns {
		columns[i] = fmt.Sprint(i)
	}
	return &memoryRows{columns: columns, rows: rows}
}

func (c *memoryConn) QueryContext(_ context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	switch {
	case strings.HasPrefix(q, "SELECT DATABASE()"):
		return memoryData(4, []driver.Value{"fixture", "8.5-TiDB", "STRICT_TRANS_TABLES", int64(1)}), nil
	case strings.HasPrefix(q, "SELECT GET_LOCK"):
		if c.db.owner != nil && c.db.owner != c {
			return memoryData(1, []driver.Value{int64(0)}), nil
		}
		c.db.owner = c
		return memoryData(1, []driver.Value{int64(1)}), nil
	case strings.HasPrefix(q, "SELECT RELEASE_LOCK"):
		if c.db.failRelease {
			return nil, fmt.Errorf("lost release acknowledgement")
		}
		if c.db.owner != c {
			return memoryData(1, []driver.Value{int64(0)}), nil
		}
		c.db.owner = nil
		return memoryData(1, []driver.Value{int64(1)}), nil
	case strings.HasPrefix(q, "SELECT TABLE_NAME, TABLE_TYPE"):
		var names []string
		for name := range c.db.tables {
			names = append(names, name)
		}
		sort.Strings(names)
		var rows [][]driver.Value
		for _, name := range names {
			kind := c.db.kinds[name]
			if kind == "" {
				kind = "BASE TABLE"
			}
			rows = append(rows, []driver.Value{name, kind, ""})
		}
		if c.db.managed {
			rows = append(rows, []driver.Value{historyTable, "BASE TABLE", historyMarker})
		}
		return memoryData(3, rows...), nil
	case strings.HasPrefix(q, "SHOW CREATE TABLE"):
		parts := strings.Split(q, "`")
		name := parts[len(parts)-2]
		ddl, ok := c.db.tables[name]
		if !ok {
			return nil, fmt.Errorf("table missing")
		}
		return memoryData(2, []driver.Value{name, ddl}), nil
	case strings.HasPrefix(q, "SELECT TABLE_NAME, REPLICA_COUNT"):
		return memoryData(3, c.db.replicas...), nil
	case strings.HasPrefix(q, "SELECT id, version"):
		var rows [][]driver.Value
		for _, e := range *c.journal() {
			rows = append(rows, []driver.Value{e.ID, e.Version, e.Name, e.UpChecksum, e.DownChecksum, e.Direction, e.State, int64(e.StatementIndex), int64(e.StatementTotal), e.SchemaBefore, e.SchemaAfter})
		}
		return memoryData(11, rows...), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}

func (c *memoryConn) ExecContext(_ context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	// Ignore leading SQL comments, including the section directive, in this fixture.
	if ts, err := tokens(q); err == nil && len(ts) > 0 {
		q = q[ts[0].start:]
	}
	internal := strings.Contains(q, "`"+historyTable+"`")
	if !internal && c.db.onDDL != nil {
		c.db.onDDL(q)
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if q == createHistorySQL {
		if c.db.managed {
			return nil, fmt.Errorf("history already exists")
		}
		c.db.managed = true
		return memoryResult(1), nil
	}
	if strings.HasPrefix(q, "INSERT INTO `_tidbgo_migrations`") {
		e := Event{ID: int64(len(*c.journal()) + 1), Version: a[0].Value.(int64), Name: a[1].Value.(string), UpChecksum: a[2].Value.(string), DownChecksum: a[3].Value.(string), Direction: a[4].Value.(string), State: a[5].Value.(string), StatementTotal: int(a[6].Value.(int64)), SchemaBefore: a[7].Value.(string), SchemaAfter: a[8].Value.(string)}
		*c.journal() = append(*c.journal(), e)
		return memoryResult(e.ID), nil
	}
	if strings.HasPrefix(q, "UPDATE `_tidbgo_migrations`") {
		if strings.Contains(q, "SET state = 'resolved'") {
			id := a[0].Value.(int64)
			(*c.journal())[id-1].State = "resolved"
			return memoryResult(1), nil
		}
		if c.db.failProgress && a[0].Value == "running" {
			c.db.failProgress = false
			return nil, fmt.Errorf("lost progress acknowledgement")
		}
		id := a[4].Value.(int64)
		e := &(*c.journal())[id-1]
		e.State = a[0].Value.(string)
		e.StatementIndex = int(a[1].Value.(int64))
		e.SchemaAfter = a[2].Value.(string)
		return memoryResult(1), nil
	}
	c.db.ddlCount++
	if q == c.db.failSQL {
		return nil, fmt.Errorf("injected SQL failure containing confidential data")
	}
	if effect, ok := c.db.effects[q]; ok {
		effect()
		return memoryResult(1), nil
	}
	return nil, fmt.Errorf("unexpected DDL: %s", q)
}

const firstVersion int64 = 20260921000000001
const secondVersion int64 = 20260921000000002

const createUsers = "CREATE TABLE `users` (`id` INT NOT NULL PRIMARY KEY)"
const withName = "CREATE TABLE `users` (`id` INT NOT NULL PRIMARY KEY, `name` VARCHAR(50))"
const addName = "ALTER TABLE `users` ADD COLUMN `name` VARCHAR(50)"
const dropName = "ALTER TABLE `users` DROP COLUMN `name`"

func fixtureRunner(t *testing.T) (*Runner, *memoryDatabase, string) {
	t.Helper()
	dir := t.TempDir()
	migrations := filepath.Join(dir, "migrations")
	if err := os.Mkdir(migrations, 0755); err != nil {
		t.Fatal(err)
	}
	f := &memoryDatabase{tables: map[string]string{}, effects: map[string]func(){}}
	f.effects[createUsers] = func() { f.tables["users"] = createUsers }
	f.effects["DROP TABLE `users`"] = func() { delete(f.tables, "users") }
	f.effects[addName] = func() { f.tables["users"] = withName }
	f.effects[dropName] = func() { f.tables["users"] = createUsers }
	db := sql.OpenDB(memoryConnector{f})
	t.Cleanup(func() { db.Close() })
	r, err := New(db, Config{Directory: migrations, SchemaFile: filepath.Join(dir, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	return r, f, migrations
}
func installMigrations(t *testing.T, dir string) {
	writeSQL(t, dir, "20260921000000001_create_users.sql", migrationSQL(createUsers, "DROP TABLE `users`"))
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(addName, dropName))
}
func migrationSQL(up, down string) string {
	source := "-- tidbgo:up\n" + up + ";\n"
	if down != "" {
		source += "\n-- tidbgo:down\n" + down + ";\n"
	}
	return source
}
func readSnapshot(t *testing.T, r *Runner) string {
	t.Helper()
	data, err := os.ReadFile(r.config.SchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunnerUpDownAndReapply(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	p, err := r.Plan(ctx, Up, 0)
	if err != nil || p.TargetVersion != secondVersion || !p.CreatesHistory || f.managed || f.ddlCount != 0 {
		t.Fatalf("plan=%#v,%v", p, err)
	}
	result, err := r.Apply(ctx, Up, 0)
	if err != nil || result.Version != secondVersion || !result.SnapshotUpdated || result.Dirty {
		t.Fatalf("up=%#v,%v", result, err)
	}
	latest := readSnapshot(t, r)
	if !strings.Contains(latest, "`name`") {
		t.Fatal(latest)
	}
	result, err = r.Apply(ctx, Down, 1)
	if err != nil || result.Version != firstVersion || strings.Contains(readSnapshot(t, r), "`name`") {
		t.Fatalf("down=%#v,%v", result, err)
	}
	result, err = r.Apply(ctx, Up, 0)
	if err != nil || result.Version != secondVersion || readSnapshot(t, r) != latest {
		t.Fatalf("re-up=%#v,%v", result, err)
	}
	status, err := r.Status(ctx)
	if err != nil || status.Dirty || status.Drift || len(status.Events) != 4 {
		t.Fatalf("status=%#v,%v", status, err)
	}
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(addName, dropName)+"-- changed\n")
	count := f.ddlCount
	if _, err := r.Apply(ctx, Down, 1); !errors.Is(err, ErrChecksum) || f.ddlCount != count {
		t.Fatalf("checksum=%v", err)
	}
}

func TestRunnerAdoptsExistingDatabaseWithoutDDL(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	// First adoption must also work when the default directory does not exist.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	f.tables["users"] = createUsers
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 0); !errors.Is(err, ErrUnmanaged) {
		t.Fatal(err)
	}
	if _, err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if f.managed || f.ddlCount != 0 {
		t.Fatal("init modified database")
	}
	initial := readSnapshot(t, r)
	files, err := Load(dir)
	if err != nil || len(files) != 1 || files[0].Down != "" {
		t.Fatalf("initial migration = %#v, %v", files, err)
	}
	baselineVersion := files[0].Version
	if err := os.Remove(r.config.SchemaFile); err != nil {
		t.Fatal(err)
	}
	f.tables["users"] = withName
	if _, err := r.Baseline(ctx); !errors.Is(err, ErrDrift) {
		t.Fatal(err)
	}
	if f.managed {
		t.Fatal("mismatch created history")
	}
	f.tables["users"] = createUsers
	result, err := r.Baseline(ctx)
	if err != nil || result.Version != baselineVersion || !result.SnapshotUpdated || f.ddlCount != 0 || readSnapshot(t, r) != initial {
		t.Fatalf("baseline=%#v,%v", result, err)
	}
	if _, err := r.Apply(ctx, Down, 1); err == nil {
		t.Fatal("baseline reversed")
	}
	writeSQL(t, dir, "99990101000000000_add.sql", migrationSQL(addName, dropName))
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(ctx, Down, 1); err != nil {
		t.Fatal(err)
	}
	if f.tables["users"] != createUsers {
		t.Fatal("adopted table changed after reversal")
	}
}

func TestRunnerRejectsOutputDirectoryAlias(t *testing.T) {
	r, _, dir := fixtureRunner(t)
	alias := filepath.Join(filepath.Dir(dir), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for _, config := range []Config{
		{Directory: dir, SchemaFile: filepath.Join(alias, "20260921000000001_initial.sql")},
		{Directory: alias, SchemaFile: filepath.Join(dir, "nested", "schema.sql")},
	} {
		if _, err := New(r.db, config); err == nil {
			t.Fatal("output alias allowed overwriting migration SQL")
		}
	}
}

func TestRunnerRepairsInterruptedDown(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored_applied_%t", restored), func(t *testing.T) {
			r, f, dir := fixtureRunner(t)
			installMigrations(t, dir)
			ctx := context.Background()
			if _, err := r.Apply(ctx, Up, 0); err != nil {
				t.Fatal(err)
			}
			f.failProgress = true
			result, err := r.Apply(ctx, Down, 1)
			if err == nil || !result.Dirty || result.Version != secondVersion || f.tables["users"] != createUsers {
				t.Fatalf("interrupted down=%#v,%v", result, err)
			}
			if _, err := r.Apply(ctx, Down, 1); !errors.Is(err, ErrDirty) {
				t.Fatalf("repeated down=%v", err)
			}
			if restored {
				f.tables["users"] = withName
			}
			if _, err := r.Dump(ctx); err != nil {
				t.Fatal(err)
			}
			count := f.ddlCount
			result, err = r.Repair(ctx, RepairOptions{Version: secondVersion, Applied: restored, ExpectedSchema: r.config.SchemaFile, Reason: "Independently verified the down result"})
			want := firstVersion
			if restored {
				want = secondVersion
			}
			if err != nil || result.Dirty || result.Version != want || f.ddlCount != count {
				t.Fatalf("repair=%#v,%v", result, err)
			}
			status, err := r.Status(ctx)
			if err != nil || status.Version != want || status.Dirty || status.Drift {
				t.Fatalf("status=%#v,%v", status, err)
			}
			if restored {
				_, err = r.Apply(ctx, Down, 1)
			} else {
				_, err = r.Apply(ctx, Up, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerStopsOnPartialFailureAndRequiresRepair(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 1); err != nil {
		t.Fatal(err)
	}
	failing := "ALTER TABLE `users` ADD COLUMN broken unsupported_type"
	f.failSQL = failing
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(addName+";\n"+failing, dropName))
	result, err := r.Apply(ctx, Up, 0)
	if err == nil || !result.Dirty || result.Version != firstVersion {
		t.Fatalf("partial=%#v,%v", result, err)
	}
	if strings.Contains(err.Error(), "confidential") {
		t.Fatal("server error leaked")
	}
	if f.events[1].State != "failed" || f.events[1].StatementIndex != 1 {
		t.Fatalf("event=%#v", f.events[1])
	}
	count := f.ddlCount
	if _, err := r.Apply(ctx, Up, 0); !errors.Is(err, ErrDirty) || f.ddlCount != count {
		t.Fatalf("retry=%v", err)
	}
	if _, err := r.Dump(ctx); err != nil {
		t.Fatal(err)
	}
	options := RepairOptions{Version: secondVersion, ExpectedSchema: r.config.SchemaFile, Reason: "Verified and manually reversed the partial operation"}
	if _, err := r.Repair(ctx, options); !errors.Is(err, ErrDrift) {
		t.Fatalf("accepted partial structure as reverted: %v", err)
	}
	f.tables["users"] = createUsers
	if _, err := r.Dump(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = r.Repair(ctx, options)
	if err != nil || result.Dirty || result.Version != firstVersion || f.ddlCount != count {
		t.Fatalf("repair=%#v,%v", result, err)
	}
	status, err := r.Status(ctx)
	if err != nil || status.Dirty || status.Drift || status.Events[1].State != "resolved" || status.Events[2].Direction != "repair" {
		t.Fatalf("status=%#v,%v", status, err)
	}
}

func TestRunnerRepairsLostProgressAcknowledgement(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 1); err != nil {
		t.Fatal(err)
	}
	f.failProgress = true
	result, err := r.Apply(ctx, Up, 0)
	if err == nil || !result.Dirty || f.tables["users"] != withName {
		t.Fatalf("result=%#v,%v", result, err)
	}
	if _, err := r.Dump(ctx); err != nil {
		t.Fatal(err)
	}
	result, err = r.Repair(ctx, RepairOptions{Version: secondVersion, Applied: true, ExpectedSchema: r.config.SchemaFile, Reason: "Checked schema, data, and DDL completion"})
	if err != nil || result.Version != secondVersion || result.Dirty {
		t.Fatalf("repair=%#v,%v", result, err)
	}
	if _, err := r.Apply(ctx, Down, 1); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerDistinguishesSnapshotFailure(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	f.onDDL = func(q string) {
		if q == createUsers {
			if err := os.Mkdir(r.config.SchemaFile, 0755); err != nil {
				t.Error(err)
			}
		}
	}
	result, err := r.Apply(context.Background(), Up, 1)
	if !errors.Is(err, ErrSnapshot) || result.Version != firstVersion || result.Dirty || result.SnapshotUpdated {
		t.Fatalf("result=%#v,%v", result, err)
	}
	count := f.ddlCount
	f.onDDL = nil
	if err := os.Remove(r.config.SchemaFile); err != nil {
		t.Fatal(err)
	}
	result, err = r.Dump(context.Background())
	if err != nil || !result.SnapshotUpdated || f.ddlCount != count {
		t.Fatalf("dump=%#v,%v", result, err)
	}
}

func TestRunnerSerializesIndependentClients(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	db := sql.OpenDB(memoryConnector{f})
	defer db.Close()
	other, err := New(db, r.config)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.onDDL = func(q string) {
		if q == createUsers {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, err := r.Apply(context.Background(), Up, 1); done <- err }()
	<-entered
	_, err = other.Apply(context.Background(), Up, 1)
	if !errors.Is(err, ErrLocked) {
		t.Errorf("concurrent=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRejectsLiveDriftBeforeDDL(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	if _, err := r.Apply(context.Background(), Up, 1); err != nil {
		t.Fatal(err)
	}
	f.tables["users"] = withName
	count := f.ddlCount
	if _, err := r.Apply(context.Background(), Up, 0); !errors.Is(err, ErrDrift) || f.ddlCount != count {
		t.Fatalf("drift=%v", err)
	}
	status, err := r.Status(context.Background())
	if err != nil || !status.Drift {
		t.Fatalf("status=%#v,%v", status, err)
	}
}

func TestDumpRejectsUnsupportedObjectsWithoutReplacingSnapshot(t *testing.T) {
	for _, kind := range []string{"VIEW", "SEQUENCE"} {
		t.Run(kind, func(t *testing.T) {
			r, f, _ := fixtureRunner(t)
			f.tables["users"] = createUsers
			if _, err := r.Dump(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := readSnapshot(t, r)
			f.tables["unknown"] = "unrepresentable"
			f.kinds = map[string]string{"unknown": kind}
			if _, err := r.Dump(context.Background()); err == nil || readSnapshot(t, r) != before || f.managed {
				t.Fatalf("unsupported snapshot was accepted or modified: %v", err)
			}
		})
	}
}

func TestDumpCapturesTiFlashAndDetectsReplicaDrift(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 1); err != nil {
		t.Fatal(err)
	}
	f.replicas = [][]driver.Value{{"users", int64(2), ""}}
	status, err := r.Status(ctx)
	if err != nil || !status.Drift {
		t.Fatalf("replica drift=%#v,%v", status, err)
	}
	if _, err := r.Dump(ctx); err != nil {
		t.Fatal(err)
	}
	before := readSnapshot(t, r)
	if !strings.Contains(before, "ALTER TABLE `users` SET TIFLASH REPLICA 2;") {
		t.Fatal("replica setting missing from dump")
	}
	f.replicas[0][2] = "zone"
	if _, err := r.Dump(ctx); err == nil || readSnapshot(t, r) != before {
		t.Fatalf("unsupported replica settings accepted: %v", err)
	}
}

func TestRunnerDiscardsConnectionAfterFailedLockRelease(t *testing.T) {
	r, f, _ := fixtureRunner(t)
	f.failRelease = true
	result, err := r.Dump(context.Background())
	var operation *OperationError
	if !errors.As(err, &operation) || operation.Phase != "release lock" || !result.SnapshotUpdated {
		t.Fatalf("release=%#v,%v", result, err)
	}
	if r.db.Stats().OpenConnections != 0 || f.owner != nil {
		t.Fatal("a potentially locked connection was returned to the pool")
	}
	f.failRelease = false
	if _, err := r.Dump(context.Background()); err != nil {
		t.Fatal(err)
	}
}
