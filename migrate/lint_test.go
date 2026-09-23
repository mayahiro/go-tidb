package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintGuardsAndUnverifiedSQL(t *testing.T) {
	dir := t.TempDir()
	writeSQL(t, dir, "001.sql", migrationSQL("CREATE TABLE t (id INT); ALTER TABLE t ADD COLUMN x INT; UPDATE t SET x = 1", "ALTER TABLE t DROP COLUMN IF EXISTS x; DROP TABLE IF EXISTS t"))
	r, err := Lint(dir, LintOptions{})
	if err != nil || r.HasErrors() || r.Statements != 5 || r.Unverified != 1 || r.ExecutionChecked || r.SchemaChecked {
		t.Fatalf("lint: %#v %v", r, err)
	}
	warnings := 0
	for _, issue := range r.Issues {
		if issue.Severity == "warning" {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("warnings: %#v", r.Issues)
	}
}

func TestLintPriorSchemaAndSequentialChanges(t *testing.T) {
	for _, tt := range []struct {
		name, up        string
		errors, unknown bool
	}{
		{"sequence", "ALTER TABLE t ADD COLUMN IF NOT EXISTS x INT; CREATE INDEX IF NOT EXISTS idx_x ON t (x); DROP INDEX IF EXISTS idx_x ON t; ALTER TABLE t DROP COLUMN IF EXISTS x", false, false},
		{"missing column", "CREATE INDEX idx_x ON t (absent)", true, false},
		{"duplicate column", "ALTER TABLE t ADD COLUMN id INT", true, false},
		{"type comparison incomplete", "ALTER TABLE t ADD COLUMN IF NOT EXISTS id TEXT", false, true},
		{"type alias", "ALTER TABLE t ADD COLUMN IF NOT EXISTS id INTEGER", false, true},
		{"nullability mismatch", "ALTER TABLE t ADD COLUMN IF NOT EXISTS id INT NOT NULL", true, false},
		{"skipped details", "ALTER TABLE t ADD COLUMN IF NOT EXISTS id INT", false, true},
		{"missing table", "ALTER TABLE missing ADD COLUMN x INT", true, false},
		{"unknown dependency", "RENAME TABLE t TO other; ALTER TABLE other ADD COLUMN x INT", false, true},
		{"guarded missing drop", "DROP TABLE IF EXISTS missing", false, false},
		{"unsupported alter", "ALTER TABLE t MODIFY COLUMN id BIGINT", false, true},
		{"create from query", "CREATE TABLE copied (id INT) AS SELECT id FROM t", false, true},
		{"quoted keywords", "ALTER TABLE t ADD COLUMN IF NOT EXISTS `index` INT", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(t.TempDir(), "schema.sql")
			if err := os.WriteFile(input, []byte("CREATE TABLE t (id INT DEFAULT NULL);"), 0644); err != nil {
				t.Fatal(err)
			}
			writeSQL(t, dir, "001.sql", migrationSQL(tt.up, ""))
			r, err := Lint(dir, LintOptions{SchemaFile: input, Versions: []string{"001"}, Direction: Up})
			if err != nil || r.HasErrors() != tt.errors || (r.Unverified > 0) != tt.unknown || !r.SchemaChecked {
				t.Fatalf("lint: %#v %v", r, err)
			}
		})
	}
}

func TestLintSnapshotRequiresExplicitTargets(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(input, []byte("-- empty prior database\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, dir, "001.sql", migrationSQL("CREATE TABLE IF NOT EXISTS t (id INT)", "DROP TABLE IF EXISTS t"))
	writeSQL(t, dir, "002.sql", migrationSQL("ALTER TABLE t ADD COLUMN IF NOT EXISTS x INT", "ALTER TABLE t DROP COLUMN IF EXISTS x"))
	for _, options := range []LintOptions{{SchemaFile: input}, {SchemaFile: input, Versions: []string{"001"}}, {Versions: []string{"missing"}}, {Versions: []string{"001", "001"}}} {
		if _, err := Lint(dir, options); err == nil {
			t.Fatal("ambiguous selection accepted")
		}
	}
	r, err := Lint(dir, LintOptions{SchemaFile: input, Versions: []string{"001", "002"}, Direction: Up})
	if err != nil || r.HasErrors() || r.Unverified != 0 {
		t.Fatalf("explicit empty snapshot: %#v %v", r, err)
	}
	if err := os.WriteFile(input, []byte("CREATE TABLE t (id INT, x INT);"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err = Lint(dir, LintOptions{SchemaFile: input, Versions: []string{"002", "001"}, Direction: Down})
	if err != nil || r.HasErrors() {
		t.Fatalf("reverse snapshot: %#v %v", r, err)
	}
}

func TestLintDoesNotTrustIgnoredSnapshotStatements(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(input, []byte("CREATE TABLE t (id INT); ALTER TABLE t ADD COLUMN x INT;"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, dir, "001.sql", migrationSQL("CREATE INDEX idx_x ON t (x)", ""))
	r, err := Lint(dir, LintOptions{SchemaFile: input, Versions: []string{"001"}, Direction: Up})
	if err != nil || r.HasErrors() || r.Unverified != 1 {
		t.Fatalf("unmodeled snapshot effect: %#v %v", r, err)
	}
	if !strings.Contains(r.Issues[0].Message, "schema state is unknown") {
		t.Fatalf("missing scope: %#v", r)
	}
}

func TestLintDoesNotCompareComplexIndexDefinitions(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(input, []byte("CREATE TABLE t (name VARCHAR(50), KEY idx (name(10)));"), 0644); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, dir, "001.sql", migrationSQL("CREATE TABLE IF NOT EXISTS t (name VARCHAR(50), KEY idx (name))", ""))
	r, err := Lint(dir, LintOptions{SchemaFile: input, Versions: []string{"001"}, Direction: Up})
	if err != nil || r.HasErrors() || r.Unverified != 1 {
		t.Fatalf("complex index comparison: %#v %v", r, err)
	}
}
