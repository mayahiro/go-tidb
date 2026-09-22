package tidbcloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/migrate"
)

// This test owns the whole empty, dedicated test schema for its duration. It
// never runs against an existing application schema or alongside other suites.
func TestTiDBCloudStarterMigrations(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_MIGRATE") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_MIGRATE=1 for migrations in an empty dedicated test database")
	}
	config := parseTestDSN(t, dsn)
	if !validTestDatabaseName(config.DBName) {
		t.Fatal("migration test DSN must select a dedicated tidbgo_test_ database")
	}
	if config.TLS == nil || config.TLS.InsecureSkipVerify || config.AllowFallbackToPlaintext {
		t.Fatal("migration tests require verified TLS")
	}
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	var objects int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()").Scan(&objects); err != nil {
		fatalDatabaseError(t, dsn, "inspect migration test database", err)
	}
	if objects != 0 {
		t.Fatal("refusing migration fixture: dedicated test database must contain no tables, views, or sequences")
	}
	const firstVersion = "20260921000000001_accounts"
	const secondVersion = "20260921000000002_label"
	const thirdVersion = "20260921000000003_partial"
	const table = "tidbgo_it_migration_accounts"
	const initial = "CREATE TABLE `" + table + "` (`id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, `amount` DECIMAL(18,6) NOT NULL DEFAULT 1.250000, `note` VARCHAR(80) DEFAULT 'a;quoted')"
	const add = "ALTER TABLE `" + table + "` ADD COLUMN `label` VARCHAR(50)"
	const reverse = "ALTER TABLE `" + table + "` DROP COLUMN `label`"
	// The initial emptiness check precedes every creation. Only these two names
	// are ever removed, and both belong exclusively to this test.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, name := range []string{table, "_tidbgo_migrations"} {
			if _, err := db.ExecContext(cleanup, "DROP TABLE IF EXISTS `"+name+"`"); err != nil {
				t.Errorf("cleanup owned migration fixture: %s", redact.Error(err, dsn))
			}
		}
	})
	var events []migrate.Event
	makeRunner := func() (*migrate.Runner, string, string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "migrations")
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		schema := filepath.Join(dir, "schema.sql")
		r, err := migrate.New(db, migrate.Config{Directory: path, SchemaFile: schema, OnEvent: func(event migrate.Event) error { events = append(events, event); return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return r, path, schema
	}
	write := func(dir, name, up, down string) {
		t.Helper()
		source := "-- tidbgo:up\n" + up + ";\n"
		if down != "" {
			source += "\n-- tidbgo:down\n" + down + ";\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	check := func(operation string, err error) {
		t.Helper()
		if err != nil {
			fatalDatabaseError(t, dsn, operation, err)
		}
	}
	r, dir, schema := makeRunner()
	write(dir, firstVersion+".sql", initial, "DROP TABLE `"+table+"`")
	write(dir, secondVersion+".sql", add, reverse)
	plan, err := r.Plan(ctx, migrate.Up, 0)
	check("plan initial migrations", err)
	if len(plan.Steps) != 2 {
		t.Fatal("wrong planned version count")
	}
	result, err := r.Apply(ctx, migrate.Up, 0)
	check("apply initial migrations", err)
	if result.Version != secondVersion || !result.SnapshotUpdated {
		t.Fatalf("unexpected migration result: %#v", result)
	}
	beforeInsert := read(schema)
	if _, err := db.ExecContext(ctx, "INSERT INTO `"+table+"` (amount) VALUES (?)", "27.123456"); err != nil {
		check("seed migration data", err)
	}
	_, err = r.Dump(ctx)
	check("dump after data insert", err)
	latest := read(schema)
	if latest != beforeInsert {
		t.Fatal("ordinary inserts changed the structural snapshot")
	}
	if !strings.Contains(latest, "decimal(18,6)") || !strings.Contains(latest, "`label`") {
		t.Fatal("snapshot lost precision or column")
	}
	_, err = r.Apply(ctx, migrate.Down, 1)
	check("down label", err)
	if strings.Contains(read(schema), "`label`") {
		t.Fatal("down did not refresh schema.sql")
	}
	var amount string
	check("read retained decimal data", db.QueryRowContext(ctx, "SELECT amount FROM `"+table+"`").Scan(&amount))
	if amount != "27.123456" {
		t.Fatal("data changed")
	}
	_, err = r.Apply(ctx, migrate.Up, 0)
	check("reapply label", err)
	if read(schema) != latest {
		t.Fatal("re-up snapshot differs")
	}
	// A reverted file may be corrected and reapplied without old-file constraints.
	_, err = r.Apply(ctx, migrate.Down, 1)
	check("reverse before SQL edit", err)
	write(dir, secondVersion+".sql", "ALTER TABLE `"+table+"` ADD COLUMN `label` VARCHAR(100)", reverse)
	_, err = r.Apply(ctx, migrate.Up, 0)
	check("apply corrected SQL", err)
	if !strings.Contains(read(schema), "varchar(100)") {
		t.Fatal("corrected column was not applied")
	}
	var columnCount int
	check("inspect version columns", db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='_tidbgo_migrations'").Scan(&columnCount))
	if columnCount != 2 {
		t.Fatal("management table does not have exactly two columns")
	}
	write(dir, thirdVersion+".sql", "ALTER TABLE `"+table+"` ADD COLUMN `partial` INT; ALTER TABLE `"+table+"` ADD COLUMN `partial` INT; ALTER TABLE `"+table+"` ADD COLUMN `pending_marker` INT", "ALTER TABLE `"+table+"` DROP COLUMN IF EXISTS `partial`; ALTER TABLE `"+table+"` DROP COLUMN IF EXISTS `pending_marker`")
	events = nil
	result, err = r.Apply(ctx, migrate.Up, 0)
	if err == nil || result.Version != secondVersion || result.SnapshotUpdated {
		t.Fatal("partial failure was not reported")
	}
	var applied int
	check("check partial version is absent", db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `_tidbgo_migrations` WHERE version=?", thirdVersion).Scan(&applied))
	if applied != 0 {
		t.Fatal("partial up recorded as applied")
	}
	var states []string
	for _, event := range events {
		if event.Phase == "SQL" && event.State != "started" {
			states = append(states, fmt.Sprintf("%d:%s", event.Statement, event.State))
		}
	}
	if strings.Join(states, ",") != "1:succeeded,2:error,3:unexecuted" {
		t.Fatalf("unexpected execution events: %v", states)
	}
	check("check unsent SQL", db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='pending_marker'", table).Scan(&columnCount))
	if columnCount != 0 {
		t.Fatal("SQL after failure was executed")
	}
	// The operator checks the partial state and supplies guarded corrected SQL.
	correctedUp := "ALTER TABLE `" + table + "` ADD COLUMN IF NOT EXISTS `partial` INT; ALTER TABLE `" + table + "` ADD COLUMN IF NOT EXISTS `pending_marker` INT"
	correctedDown := "ALTER TABLE `" + table + "` DROP COLUMN IF EXISTS `partial`; ALTER TABLE `" + table + "` DROP COLUMN IF EXISTS `pending_marker`"
	write(dir, thirdVersion+".sql", correctedUp, correctedDown)
	_, err = r.Apply(ctx, migrate.Up, 0)
	check("retry corrected partial SQL", err)
	write(dir, thirdVersion+".sql", correctedUp, "ALTER TABLE `"+table+"` DROP COLUMN IF EXISTS `partial`; ALTER TABLE `"+table+"` DROP COLUMN `absent_marker`")
	_, err = r.Apply(ctx, migrate.Down, 1)
	if err == nil {
		t.Fatal("expected partial down failure")
	}
	check("check partial down keeps version", db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `_tidbgo_migrations` WHERE version=?", thirdVersion).Scan(&applied))
	if applied != 1 {
		t.Fatal("failed down removed its record")
	}
	write(dir, thirdVersion+".sql", correctedUp, correctedDown)
	_, err = r.Apply(ctx, migrate.Down, 1)
	check("complete corrected down", err)
	_, err = r.Apply(ctx, migrate.Up, 0)
	check("reapply after corrected down", err)
	_, err = r.Apply(ctx, migrate.Down, 1)
	check("reverse completed partial fixture", err)
	status, err := r.Status(ctx)
	check("inspect applied records", err)
	if status.Version != secondVersion {
		t.Fatal("incorrect latest applied record")
	}
	// Finish this independently owned history and then exercise adoption of the
	// retained application table and data from a different migration directory.
	_, err = db.ExecContext(ctx, "DROP TABLE `_tidbgo_migrations`")
	check("reset owned test history", err)
	adopt, adoptDir, adoptSchema := makeRunner()
	_, err = adopt.Init(ctx)
	check("capture existing fixture", err)
	files, err := migrate.Load(adoptDir)
	check("load captured initial migration", err)
	if len(files) != 1 || files[0].Down != "" {
		t.Fatal("expected one irreversible initial migration")
	}
	baselineVersion := files[0].Version
	captured := read(adoptSchema)
	check("remove snapshot to verify baseline regeneration", os.Remove(adoptSchema))
	result, err = adopt.Baseline(ctx)
	check("adopt existing fixture", err)
	if result.Version != baselineVersion || !result.SnapshotUpdated || read(adoptSchema) != captured {
		t.Fatal("wrong baseline")
	}
	if _, err := adopt.Apply(ctx, migrate.Down, 1); err == nil {
		t.Fatal("adoption baseline was reversible")
	}
	write(adoptDir, "99990101000000000_note.sql", "ALTER TABLE `"+table+"` ADD COLUMN `extra` INT", "ALTER TABLE `"+table+"` DROP COLUMN `extra`")
	before := read(adoptSchema)
	_, err = adopt.Apply(ctx, migrate.Up, 0)
	check("up adopted fixture", err)
	_, err = adopt.Apply(ctx, migrate.Down, 1)
	check("down adopted fixture", err)
	if before != read(adoptSchema) {
		t.Fatal("adopted schema did not return to its original structure")
	}
	check("read adopted decimal data", db.QueryRowContext(ctx, "SELECT amount FROM `"+table+"`").Scan(&amount))
	if amount != "27.123456" {
		t.Fatal("adoption changed data")
	}
	// Rebuild only this owned fixture to verify the captured initial SQL is also
	// usable as the initial migration of an empty database.
	for _, name := range []string{table, "_tidbgo_migrations"} {
		_, err = db.ExecContext(ctx, "DROP TABLE `"+name+"`")
		check("reset owned fixture for initial SQL replay", err)
	}
	result, err = adopt.Apply(ctx, migrate.Up, 1)
	check("replay captured initial SQL into empty database", err)
	if result.Version != baselineVersion || read(adoptSchema) != before {
		t.Fatal("captured SQL did not reproduce the adopted structure")
	}
}

// TestTiDBCloudStarterMigrationTarget performs only read-only target checks.
// It is safe to run before explicitly enabling the DDL verification suite.
func TestTiDBCloudStarterMigrationTarget(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("set TIDBGO_TEST_DSN for read-only target verification")
	}
	config := parseTestDSN(t, dsn)
	if !validTestDatabaseName(config.DBName) || config.TLS == nil || config.TLS.InsecureSkipVerify || config.AllowFallbackToPlaintext {
		t.Fatal("expected a dedicated tidbgo_test_ database with verified TLS")
	}
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	var objects int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()").Scan(&objects); err != nil {
		fatalDatabaseError(t, dsn, "inspect migration target", err)
	}
	t.Logf("verified TLS; database=%s; TiDB identity verified; schema objects=%d; no DDL executed", config.DBName, objects)
	if objects != 0 {
		t.Fatal("migration DDL verification requires an empty dedicated database")
	}
}
