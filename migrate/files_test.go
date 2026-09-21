package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeSQL(t *testing.T, dir, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSQLBoundaries(t *testing.T) {
	input := "-- header ;\nCREATE TABLE `semi;colon` (s VARCHAR(50) DEFAULT 'a;''bc', b VARBINARY(5) DEFAULT X'FF'); /* regular ; */\n" +
		"INSERT INTO `semi;colon` VALUES ('-- ;', X'FF'); # trailing ;\nALTER TABLE `semi;colon` ADD COLUMN id BIGINT /*T![auto_rand] AUTO_RANDOM(5) */;"
	statements, err := splitSQL(input)
	if err != nil || len(statements) != 3 {
		t.Fatalf("split = %#v, %v", statements, err)
	}
	for _, invalid := range []string{
		"CREATE TABLE t (s TEXT DEFAULT 'unterminated);", "CREATE TABLE t (id INT); /* unfinished",
		"USE production;", "BEGIN;", "SET autocommit=0;", "DELIMITER $$", "CREATE DATABASE other;", "CREATE VIEW v AS SELECT 1;",
		"/*! SET autocommit=0 */;", "CREATE TABLE t (id INT) /*T! ; DROP TABLE t */;", "DELETE FROM `_tidbgo_migrations`;",
		"INSERT INTO t SELECT RELEASE_LOCK('x');", "CREATE TABLE t (id INT)\x00",
		"INSERT INTO t /*! SELECT RELEASE_ALL_LOCKS() */;", "TRUNCATE /*! TABLE `_tidbgo_migrations` */;",
	} {
		if _, err := splitSQL(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestSnapshotPreservesSchemaSemantics(t *testing.T) {
	ddl := "CREATE TABLE `t` (\n `id` bigint NOT NULL AUTO_INCREMENT,\n `amount` decimal(15,4) DEFAULT 1.25,\n `bits` bit(2) DEFAULT b'10',\n `raw` varbinary(2) DEFAULT X'ABCD',\n `s` varchar(30) DEFAULT 'AUTO_INCREMENT=7; X',\n PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=123 DEFAULT CHARSET=utf8mb4 /*T![auto_rand_base] AUTO_RANDOM_BASE=456 */"
	got, err := normalizedDDL(ddl)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"decimal(15,4) DEFAULT 1.25", "DEFAULT b'10'", "DEFAULT X'ABCD'", "DEFAULT 'AUTO_INCREMENT=7; X'", "NOT NULL AUTO_INCREMENT", "CHARSET=utf8mb4"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q in %s", want, got)
		}
	}
	if strings.Contains(got, "AUTO_INCREMENT=123") || strings.Contains(got, "AUTO_RANDOM_BASE") {
		t.Fatal(got)
	}
	hash, err := snapshotHash(ddl)
	if err != nil {
		t.Fatal(err)
	}
	changedCounter := strings.Replace(ddl, "AUTO_INCREMENT=123", "AUTO_INCREMENT=789", 1)
	if other, _ := snapshotHash(changedCounter); hash != other {
		t.Fatal("allocator changed structure hash")
	}
	for _, change := range []string{strings.Replace(ddl, "15,4", "15,3", 1), strings.Replace(ddl, "1.25", "1.26", 1), strings.Replace(ddl, "utf8mb4", "utf8", 1)} {
		if other, _ := snapshotHash(change); other == hash {
			t.Fatal("structural change ignored")
		}
	}
	if hash2, _ := snapshotHash(got); hash2 != hash {
		t.Fatal("normalization changed structure")
	}
	absentCounters := strings.Replace(ddl, " AUTO_INCREMENT=123", "", 1)
	absentCounters = strings.Replace(absentCounters, " /*T![auto_rand_base] AUTO_RANDOM_BASE=456 */", "", 1)
	if other, err := normalizedDDL(absentCounters); err != nil || other != got {
		t.Fatal("allocator presence changed snapshot whitespace")
	}
}

func TestMigrationSections(t *testing.T) {
	up := "-- header\r\n  -- tidbgo:up\r\nCREATE TABLE t (s TEXT DEFAULT '\n-- tidbgo:down\n');\n/*\n-- tidbgo:down\n*/\n"
	down := "-- tidbgo:down\r\nDROP TABLE t;\r\n-- footer\r\n"
	for _, source := range []string{up, up + down} {
		a, b, err := migrationParts(source)
		if err != nil || a+b != source || a != up {
			t.Fatalf("parts = %q, %q, %v", a, b, err)
		}
	}
	for _, source := range []string{
		"CREATE TABLE t (id INT);",
		"-- tidbgo:up\n-- empty",
		"-- tidbgo:down\nDROP TABLE t;",
		"-- tidbgo:up\n-- tidbgo:down\nDROP TABLE t;",
		"-- tidbgo:up\nCREATE TABLE t (id INT)\n-- tidbgo:down\nDROP TABLE t;",
		"-- tidbgo:up\nCREATE TABLE t (id INT); -- tidbgo:down\nDROP TABLE t;",
		up + "-- tidbgo:up\nDROP TABLE t;",
		up + "-- tidbgo:other\nDROP TABLE t;",
		up + "-- tidbgo:down\n-- empty",
		up + down + down,
		"-- tidbgo:up\nSET autocommit=0;",
		up + "-- tidbgo:down\nBEGIN;",
	} {
		if _, _, err := migrationParts(source); err == nil {
			t.Errorf("invalid sections accepted: %q", source)
		}
	}
}

func TestLoadValidatesTimestampsAndHistoryBytes(t *testing.T) {
	dir := t.TempDir()
	initial := "20260921000000001_initial.sql"
	add := "20260921000000002_add.sql"
	source := migrationSQL("ALTER TABLE t ADD COLUMN x INT", "ALTER TABLE t DROP COLUMN x")
	writeSQL(t, dir, add, source)
	writeSQL(t, dir, initial, migrationSQL("CREATE TABLE t (id INT)", ""))
	files, err := Load(dir)
	if err != nil || len(files) != 2 || files[0].Version != firstVersion || files[0].Down != "" || files[1].Up+files[1].Down != source {
		t.Fatalf("files=%#v err=%v", files, err)
	}
	beforeUp, beforeDown := checksum(files[1].Up), checksum(files[1].Down)
	writeSQL(t, dir, add, "-- header\n"+source+"-- footer\n")
	files, err = Load(dir)
	if err != nil || checksum(files[1].Up) == beforeUp || checksum(files[1].Down) == beforeDown {
		t.Fatalf("comments excluded from checksum: %v", err)
	}
	for _, name := range []string{
		"20260921000000002_duplicate.sql", "20260229000000000_invalid.sql",
		"20260921240000000_invalid.sql", "2026092100000000_short.sql",
		"202609210000000001_long.sql", "20260921000000003_pair.up.sql", "unknown.sql",
	} {
		t.Run(name, func(t *testing.T) {
			writeSQL(t, dir, name, source)
			defer os.Remove(filepath.Join(dir, name))
			if _, err := Load(dir); err == nil {
				t.Fatal("invalid file accepted")
			}
		})
	}
	writeSQL(t, dir, "20240229235959999_leap.sql", source)
	if _, err := Load(dir); err != nil {
		t.Fatalf("valid leap date rejected: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, initial), filepath.Join(dir, "20260921000000003_link.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestCreateUsesUTCMillisecondsAndRejectsCollisions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2027, 1, 1, 8, 59, 59, 999987654, time.FixedZone("JST", 9*60*60))
	source := migrationSQL("CREATE TABLE t (id INT)", "DROP TABLE t")
	m, err := createMigration(dir, "initial", source, now, false)
	if err != nil || m.Version != 20261231235959999 {
		t.Fatalf("UTC millisecond timestamp = %#v, %v", m, err)
	}
	for _, name := range []string{"initial", "different"} {
		if _, err := createMigration(dir, name, source, now, false); err == nil {
			t.Fatal("same-millisecond version accepted")
		}
	}
	if _, err := createMigration(dir, "older", source, now.Add(-time.Second), false); err == nil {
		t.Fatal("clock rollback accepted")
	}
	m, err = createMigration(dir, "next", source, now.Add(time.Millisecond), false)
	if err != nil || m.Version != 20270101000000000 {
		t.Fatalf("calendar rollover = %#v, %v", m, err)
	}
	files, err := Load(dir)
	if err != nil || len(files) != 2 || files[0].Up+files[0].Down != source {
		t.Fatalf("existing file changed: %#v, %v", files, err)
	}
}

func TestCreateSerializesConcurrentNames(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := createMigration(dir, name, migrationSQL("CREATE TABLE t (id INT)", ""), time.Date(2026, 9, 21, 0, 0, 0, 123000000, time.UTC), false)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || successes != 1 || len(entries) != 1 {
		t.Fatalf("concurrent creations = %d, files = %v, err = %v", successes, entries, err)
	}
}

func TestCreateAndAtomicSnapshot(t *testing.T) {
	dir := t.TempDir()
	m, err := Create(filepath.Join(dir, "migrations"), "create_users")
	if err != nil || m.Version <= 0 {
		t.Fatalf("create=%#v,%v", m, err)
	}
	if _, err := Load(filepath.Join(dir, "migrations")); err == nil {
		t.Fatal("unfinished templates accepted")
	}
	if _, err := Create(dir, "../bad"); err == nil {
		t.Fatal("path traversal name accepted")
	}
	path := filepath.Join(dir, "schema.sql")
	if err := writeSnapshot(path, "old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(path, "new"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions changed")
	}
	link := filepath.Join(dir, "link.sql")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(link, "bad"); err == nil {
		t.Fatal("symlink replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatal("target corrupted")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tidbgo-") {
			t.Fatal("temporary file leaked")
		}
	}
}

func TestForeignKeyOrdering(t *testing.T) {
	tables := map[string]tableDDL{"child": {name: "child", dependencies: []string{"parent"}}, "parent": {name: "parent"}}
	ordered, err := orderTables(tables)
	if err != nil || ordered[0].name != "parent" {
		t.Fatalf("order=%#v,%v", ordered, err)
	}
	tables["parent"] = tableDDL{name: "parent", dependencies: []string{"child"}}
	if _, err := orderTables(tables); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, _, err := portableReferences("CREATE TABLE c (FOREIGN KEY (id) REFERENCES other.p(id))", "app"); err == nil {
		t.Fatal("cross-database dependency accepted")
	}
	source := "CREATE TABLE c (a INT REFERENCES `app`.`p`(id), b INT REFERENCES app.q(id), note TEXT DEFAULT 'app.p')"
	portable, deps, err := portableReferences(source, "app")
	if err != nil || portable != "CREATE TABLE c (a INT REFERENCES `p`(id), b INT REFERENCES q(id), note TEXT DEFAULT 'app.p')" || strings.Join(deps, ",") != "p,q" {
		t.Fatalf("own-database references=%q,%v,%v", portable, deps, err)
	}
}

func TestPlanChecksEveryDownBeforeExecution(t *testing.T) {
	migrations := []Migration{{1, "initial", "CREATE TABLE t (id INT);", ""}, {2, "add", "ALTER TABLE t ADD x INT;", "ALTER TABLE t DROP x;"}}
	s := historyState{stack: []int64{1, 2}, hash: "hash"}
	snap := snapshot{Hash: "hash"}
	if _, err := buildPlan(migrations, s, snap, Down, 2); err == nil {
		t.Fatal("irreversible initial migration accepted")
	}
	s.baseline = 1
	if _, err := buildPlan(migrations, s, snap, Down, 2); err == nil {
		t.Fatal("baseline crossed")
	}
	p, err := buildPlan(migrations, s, snap, Down, 1)
	if err != nil || p.TargetVersion != 1 || len(p.Steps) != 1 {
		t.Fatalf("plan=%#v,%v", p, err)
	}
}

func FuzzSQLBoundaries(f *testing.F) {
	for _, s := range []string{"CREATE TABLE t (id INT);", "INSERT INTO t VALUES (';');", "/*T![auto_rand] AUTO_RANDOM(5) */", "'\\"} {
		f.Add(s)
		f.Add("-- tidbgo:up\n" + s + "\n-- tidbgo:down\nDROP TABLE t;")
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 65536 {
			t.Skip()
		}
		_, _, _ = migrationParts(s)
		_, _ = splitSQL(s)
		_, _ = canonicalSQL(s)
		_, _ = normalizedDDL(s)
	})
}
