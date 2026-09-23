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
	"time"
)

// The fixture models database state independently of the runner. SHOW CREATE
// reads that state, while ALTER effects are explicit test-owned definitions.
type memoryDatabase struct {
	mu            sync.Mutex
	tables        map[string]string
	kinds         map[string]string
	replicas      [][]driver.Value
	effects       map[string]func()
	versions      map[string]time.Time
	recordCount   int64
	managed       bool
	owner         *memoryConn
	ddlCount      int
	failSQL       string
	failRecord    bool
	loseRecordAck bool
	loseSQLAck    bool
	failRelease   bool
	onDDL         func(string)
}
type memoryConnector struct{ db *memoryDatabase }

func (c memoryConnector) Connect(context.Context) (driver.Conn, error) {
	return &memoryConn{db: c.db}, nil
}
func (c memoryConnector) Driver() driver.Driver { return memoryDriver{c.db} }

type memoryDriver struct{ db *memoryDatabase }

func (d memoryDriver) Open(string) (driver.Conn, error) { return &memoryConn{db: d.db}, nil }

type memoryConn struct{ db *memoryDatabase }

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
	return nil, fmt.Errorf("unexpected transaction")
}

type memoryResult int64

func (r memoryResult) LastInsertId() (int64, error) {
	return 0, fmt.Errorf("migration state has no generated ID")
}
func (r memoryResult) RowsAffected() (int64, error) { return int64(r), nil }

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
		return memoryData(5, []driver.Value{"fixture", "8.5-TiDB", "STRICT_TRANS_TABLES", int64(1), "+00:00"}), nil
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
	case strings.HasPrefix(q, "SELECT version, DATE_FORMAT"):
		var versions []string
		for version := range c.db.versions {
			versions = append(versions, version)
		}
		sort.Slice(versions, func(i, j int) bool {
			a, b := c.db.versions[versions[i]], c.db.versions[versions[j]]
			if a.Equal(b) {
				return versions[i] < versions[j]
			}
			return a.Before(b)
		})
		var rows [][]driver.Value
		for _, version := range versions {
			rows = append(rows, []driver.Value{version, c.db.versions[version].UTC().Format(recordTimeLayout)})
		}
		return memoryData(2, rows...), nil
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
	if strings.HasPrefix(q, "INSERT INTO `_tidbgo_migrations`") || strings.HasPrefix(q, "DELETE FROM `_tidbgo_migrations`") {
		if c.db.failRecord {
			c.db.failRecord = false
			return nil, fmt.Errorf("injected version write failure")
		}
		if c.db.versions == nil {
			c.db.versions = make(map[string]time.Time)
		}
		if strings.HasPrefix(q, "INSERT") {
			for i := 0; i < len(a); i++ {
				if _, exists := c.db.versions[a[i].Value.(string)]; exists {
					return nil, fmt.Errorf("duplicate version")
				}
			}
			for i := 0; i < len(a); i++ {
				c.db.recordCount++
				c.db.versions[a[i].Value.(string)] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(c.db.recordCount) * time.Millisecond)
			}
		} else {
			version := a[0].Value.(string)
			_, exists := c.db.versions[version]
			if !exists {
				return memoryResult(0), nil
			}
			delete(c.db.versions, version)
		}
		if c.db.loseRecordAck {
			c.db.loseRecordAck = false
			return nil, fmt.Errorf("lost version write acknowledgement")
		}
		return memoryResult(1), nil
	}
	c.db.ddlCount++
	if q == c.db.failSQL {
		return nil, fmt.Errorf("injected SQL failure containing confidential data")
	}
	if effect, ok := c.db.effects[q]; ok {
		effect()
		if c.db.loseSQLAck {
			c.db.loseSQLAck = false
			return nil, fmt.Errorf("lost SQL acknowledgement")
		}
		return memoryResult(1), nil
	}
	return nil, fmt.Errorf("unexpected DDL: %s", q)
}

const firstVersion = "20260921000000001_create_users"
const secondVersion = "20260921000000002_add_name"

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
	if err != nil || result.Version != secondVersion || !result.SnapshotUpdated {
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
	if err != nil || len(f.versions) != 2 {
		t.Fatalf("status=%#v,%v", status, err)
	}
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(addName, dropName)+"-- changed\n")
	count := f.ddlCount
	if _, err := r.Apply(ctx, Down, 1); err != nil || f.ddlCount != count+1 {
		t.Fatalf("edited applied file could not be reversed: %v", err)
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
	if _, err := r.Baseline(ctx); err == nil {
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

func TestRunnerEditsAndReappliesRevertedVersion(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(ctx, Down, 1); err != nil {
		t.Fatal(err)
	}
	const revised = "ALTER TABLE `users` ADD COLUMN `name` VARCHAR(100)"
	const revisedTable = "CREATE TABLE `users` (`id` INT NOT NULL PRIMARY KEY, `name` VARCHAR(100))"
	f.effects[revised] = func() { f.tables["users"] = revisedTable }
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(revised, dropName))
	for i := 0; i < 2; i++ {
		if _, err := r.Apply(ctx, Up, 0); err != nil {
			t.Fatalf("reapply revised SQL: %v", err)
		}
		if !strings.Contains(readSnapshot(t, r), "VARCHAR(100)") {
			t.Fatal("revised SQL was not executed")
		}
		if _, err := r.Status(ctx); err != nil {
			t.Fatalf("status after revised SQL: %v", err)
		}
		if _, err := r.Apply(ctx, Down, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "20260921000000002_add_name.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Status(ctx); err != nil {
		t.Fatalf("removed reverted file blocked status: %v", err)
	}
	// A previously reverted high timestamp must not prevent a lower pending
	// timestamp after the current version from being applied.
	writeSQL(t, dir, "20260921000000003_add_name.sql", migrationSQL(addName, dropName))
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(ctx, Down, 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "20260921000000003_add_name.sql")); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(revised, dropName))
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatalf("old highest timestamp constrained pending files: %v", err)
	}
	// A newly added earlier filename remains pending even after later files run.
	writeSQL(t, dir, "20260921000000000_earlier.sql", migrationSQL(createUsers, "DROP TABLE users"))
	if plan, err := r.Plan(ctx, Up, 0); err != nil || len(plan.Steps) != 1 || plan.Steps[0].Version != "20260921000000000_earlier" {
		t.Fatalf("earlier pending file omitted: %#v, %v", plan, err)
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

func TestRunnerAllowsManualRecoveryOfUncertainOutcomes(t *testing.T) {
	for _, direction := range []Direction{Up, Down} {
		for _, failure := range []string{"SQL acknowledgement", "version write", "version acknowledgement"} {
			t.Run(string(direction)+"/"+failure, func(t *testing.T) {
				r, f, dir := fixtureRunner(t)
				installMigrations(t, dir)
				ctx := context.Background()
				steps := 1
				if direction == Down {
					steps = 2
				}
				if _, err := r.Apply(ctx, Up, steps); err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "SQL acknowledgement":
					f.loseSQLAck = true
				case "version write":
					f.failRecord = true
				case "version acknowledgement":
					f.loseRecordAck = true
				}
				result, err := r.Apply(ctx, direction, 1)
				if err == nil || result.SnapshotUpdated {
					t.Fatalf("uncertain result=%#v,%v", result, err)
				}
				wantSQL, wantVersion := withName, secondVersion
				if direction == Down {
					wantSQL, wantVersion = createUsers, firstVersion
				}
				if f.tables["users"] != wantSQL {
					t.Fatal("SQL outcome was not independently observable")
				}
				// The operator checks the SQL outcome and directly corrects the version row.
				if direction == Up {
					f.versions[secondVersion] = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
				} else {
					delete(f.versions, secondVersion)
				}
				status, err := r.Status(ctx)
				if err != nil || status.Version != wantVersion {
					t.Fatalf("status=%#v,%v", status, err)
				}
				opposite := Down
				if direction == Down {
					opposite = Up
				}
				if _, err := r.Apply(ctx, opposite, 1); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestRunnerKeepsOnlySuccessfulVersionsAfterSQLFailure(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	f.failSQL = addName
	result, err := r.Apply(ctx, Up, 0)
	if err == nil || result.Version != firstVersion || len(result.Completed) != 1 || result.SnapshotUpdated {
		t.Fatalf("failure=%#v,%v", result, err)
	}
	if strings.Contains(err.Error(), "confidential") || len(f.versions) != 1 || f.tables["users"] != createUsers {
		t.Fatal("failed SQL changed recorded versions or disclosed its raw cause")
	}
	// The operator verifies the database, corrects the file, and retries.
	const fixed = "ALTER TABLE `users` ADD COLUMN `name` VARCHAR(100)"
	f.effects[fixed] = func() { f.tables["users"] = withName }
	writeSQL(t, dir, "20260921000000002_add_name.sql", migrationSQL(fixed, dropName))
	result, err = r.Apply(ctx, Up, 0)
	if err != nil || result.Version != secondVersion || len(f.versions) != 2 {
		t.Fatalf("retry corrected SQL=%#v,%v", result, err)
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
	if !errors.Is(err, ErrSnapshot) || result.Version != firstVersion || result.SnapshotUpdated {
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

func TestRunnerDoesNotLockFilesOrCheckLiveDrift(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 1); err != nil {
		t.Fatal(err)
	}
	f.tables["users"] = withName
	writeSQL(t, dir, "20260921000000001_create_users.sql", migrationSQL(createUsers, "DROP TABLE users")+"-- edited\n")
	plan, err := r.Plan(ctx, Up, 0)
	if err != nil || plan.CurrentVersion != firstVersion || len(plan.Steps) != 1 {
		t.Fatalf("plan=%#v,%v", plan, err)
	}
	status, err := r.Status(ctx)
	if err != nil || status.Version != firstVersion {
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

func TestDumpCapturesTiFlash(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 1); err != nil {
		t.Fatal(err)
	}
	f.replicas = [][]driver.Value{{"users", int64(2), ""}}
	status, err := r.Status(ctx)
	if err != nil || status.Version != firstVersion {
		t.Fatalf("status with updated replicas=%#v,%v", status, err)
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

func TestRunnerLogsPartialSQLAndStops(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	writeSQL(t, dir, "001_setup.sql", migrationSQL(createUsers+"; "+addName+"; INSERT INTO users VALUES (2)", ""))
	writeSQL(t, dir, "002_later.sql", migrationSQL("INSERT INTO users VALUES (3)", ""))
	f.failSQL = addName
	var events []Event
	r.config.OnEvent = func(e Event) error {
		if e.State == "started" && e.Phase == "SQL" && e.Statement == 1 && f.ddlCount != 0 {
			t.Fatal("SQL ran before its start was reported")
		}
		events = append(events, e)
		return nil
	}
	result, err := r.Apply(context.Background(), Up, 0)
	if err == nil || result.Version != "" || len(result.Completed) != 0 || len(f.versions) != 0 || f.ddlCount != 2 {
		t.Fatalf("partial result: %#v %v", result, err)
	}
	var states []string
	for _, e := range events {
		if e.Phase == "SQL" {
			states = append(states, fmt.Sprintf("%s:%d:%s", e.Version, e.Statement, e.State))
			if e.SQL == "" || e.Time.IsZero() {
				t.Fatal("missing SQL or timestamp")
			}
		}
	}
	want := "001_setup:1:started,001_setup:1:succeeded,001_setup:2:started,001_setup:2:error,001_setup:3:unexecuted,002_later:1:unexecuted"
	if strings.Join(states, ",") != want {
		t.Fatalf("events: %v", states)
	}
	// Human recovery restores the schema and fixes the failing SQL file.
	delete(f.tables, "users")
	writeSQL(t, dir, "001_setup.sql", migrationSQL(createUsers+"; "+addName, dropName+"; DROP TABLE `users`"))
	if err := os.Remove(filepath.Join(dir, "002_later.sql")); err != nil {
		t.Fatal(err)
	}
	f.failSQL = ""
	r.config.OnEvent = nil
	if _, err := r.Apply(context.Background(), Up, 0); err != nil {
		t.Fatal(err)
	}
	f.failSQL = "DROP TABLE `users`"
	result, err = r.Apply(context.Background(), Down, 1)
	if err == nil || result.Version != "001_setup" || len(f.versions) != 1 || f.tables["users"] != createUsers {
		t.Fatalf("partial down: %#v %v", result, err)
	}
}

func TestRunnerUsesApplicationTimeForDown(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	installMigrations(t, dir)
	ctx := context.Background()
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	const older = "000_add_age"
	const add = "ALTER TABLE users ADD COLUMN age INT"
	f.effects[add] = func() {
		f.tables["users"] = "CREATE TABLE users (id INT NOT NULL PRIMARY KEY, name VARCHAR(50), age INT)"
	}
	f.effects["ALTER TABLE users DROP COLUMN age"] = func() { f.tables["users"] = withName }
	writeSQL(t, dir, older+".sql", migrationSQL(add, "ALTER TABLE users DROP COLUMN age"))
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	plan, err := r.Plan(ctx, Down, 1)
	if err != nil || len(plan.Steps) != 1 || plan.Steps[0].Version != older {
		t.Fatalf("application order: %#v %v", plan, err)
	}
	if result, err := r.Apply(ctx, Down, 1); err != nil || result.Version != secondVersion {
		t.Fatalf("down older: %#v %v", result, err)
	}
	if _, exists := f.versions[older]; exists {
		t.Fatal("down record retained")
	}
	if err := os.Remove(filepath.Join(dir, older+".sql")); err != nil {
		t.Fatal(err)
	}
	firstApplied := f.versions[secondVersion]
	if _, err := r.Apply(ctx, Down, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(ctx, Up, 0); err != nil {
		t.Fatal(err)
	}
	if !f.versions[secondVersion].After(firstApplied) {
		t.Fatal("re-up did not get a new registration time")
	}
	f.versions[firstVersion] = f.versions[secondVersion]
	plan, err = r.Plan(ctx, Down, 1)
	if err != nil || len(plan.Steps) != 1 || plan.Steps[0].Version != secondVersion {
		t.Fatalf("filename tie breaker: %#v %v", plan, err)
	}
}

func TestRunnerProgressErrorsPreserveConfirmedRecordOutcome(t *testing.T) {
	for _, phase := range []string{"SQL start", "record success"} {
		t.Run(phase, func(t *testing.T) {
			r, f, dir := fixtureRunner(t)
			installMigrations(t, dir)
			r.config.OnEvent = func(e Event) error {
				if phase == "SQL start" && e.Phase == "SQL" && e.State == "started" || phase == "record success" && e.Phase == "record applied version" && e.State == "succeeded" {
					return errors.New("log unavailable")
				}
				return nil
			}
			result, err := r.Apply(context.Background(), Up, 1)
			if err == nil {
				t.Fatal("progress error ignored")
			}
			if phase == "SQL start" {
				if f.ddlCount != 0 || len(f.versions) != 0 {
					t.Fatal("SQL ran after logging failed")
				}
			} else if result.Version != firstVersion || len(result.Completed) != 1 || len(f.versions) != 1 {
				t.Fatalf("confirmed record lost: %#v", result)
			}
		})
	}
}

func TestInitProducesOneIrreversibleFileForMultipleTables(t *testing.T) {
	r, f, dir := fixtureRunner(t)
	f.tables["users"] = createUsers
	f.tables["items"] = "CREATE TABLE items (id INT)"
	if _, err := r.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := Load(dir)
	if err != nil || len(files) != 1 || files[0].Down != "" {
		t.Fatalf("initial: %#v %v", files, err)
	}
	statements, err := migrationStatements(files[0], Up)
	if err != nil || len(statements) != 2 {
		t.Fatalf("initial SQL: %#v %v", statements, err)
	}
	if _, err := r.Baseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.versions) != 1 || f.ddlCount != 0 {
		t.Fatal("baseline executed application SQL")
	}
	if _, err := r.Apply(context.Background(), Down, 1); err == nil || len(f.versions) != 1 {
		t.Fatal("initial record was removed")
	}
	if _, err := r.Apply(context.Background(), Up, 0); err != nil || f.ddlCount != 0 {
		t.Fatalf("initial re-executed: %v", err)
	}
}
