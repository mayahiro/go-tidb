package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/check"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	got, err := Load(Options{LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestExampleMatchesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "tidbgo.yaml.example")
	got, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Fatalf("example config = %#v, want defaults %#v", got, want)
	}
}

func TestLoadDocumentedConfiguration(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
version: 1
profile: tidb-cloud-starter

schema:
  command:
    - go
    - run
    - ./db/schema
  generated_dir: ./generated/db

migrations:
  dir: ./migrations
  table: migrations
  lock_name: example:migrate
  lock_timeout: 45s

runtime:
  relation_batch_size: 250
  max_preload_depth: 2
  conn_max_lifetime: 4m
  max_open_conns: 8
  max_idle_conns: 6

lint:
  fail_on: warning
  large_offset_threshold: 5000
  unbounded_select_severity: info
  full_scan_est_rows_warning: 500
  full_scan_est_rows_error: 50000
  ru_regression_ratio: 0.25
  ru_regression_absolute: 0.5
`)

	got, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !reflect.DeepEqual(got.Schema.Command, []string{"go", "run", "./db/schema"}) {
		t.Fatalf("Schema.Command = %#v", got.Schema.Command)
	}
	if got.Schema.GeneratedDir != "./generated/db" {
		t.Fatalf("Schema.GeneratedDir = %q", got.Schema.GeneratedDir)
	}
	if got.Migrations.Dir != "./migrations" || got.Migrations.Table != "migrations" {
		t.Fatalf("Migrations = %#v", got.Migrations)
	}
	if got.Migrations.LockTimeout != 45*time.Second {
		t.Fatalf("Migrations.LockTimeout = %s", got.Migrations.LockTimeout)
	}
	if got.Runtime.RelationBatchSize != 250 || got.Runtime.MaxPreloadDepth != 2 {
		t.Fatalf("Runtime = %#v", got.Runtime)
	}
	if got.Runtime.ConnMaxLifetime != 4*time.Minute || got.Runtime.MaxOpenConns != 8 || got.Runtime.MaxIdleConns != 6 {
		t.Fatalf("Runtime = %#v", got.Runtime)
	}
	if got.Lint.FailOn != check.SeverityWarning || got.Lint.UnboundedSelectSeverity != check.SeverityInfo {
		t.Fatalf("Lint severities = %#v", got.Lint)
	}
	if got.Lint.LargeOffsetThreshold != 5000 || got.Lint.FullScanEstRowsWarning != 500 || got.Lint.FullScanEstRowsError != 50000 {
		t.Fatalf("Lint thresholds = %#v", got.Lint)
	}
	if got.Lint.RURegressionRatio != 0.25 || got.Lint.RURegressionAbsolute != 0.5 {
		t.Fatalf("Lint RU thresholds = %#v", got.Lint)
	}
}

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
runtime:
  max_open_conns: 2
  max_idle_conns: 2
`)
	environment := map[string]string{
		"TIDBGO_RUNTIME_MAX_OPEN_CONNS": "5",
		"TIDBGO_DSN":                    "file-user:environment-secret@tcp(example.invalid:4000)/app",
	}
	lookup := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}

	got, err := Load(Options{
		Path:      path,
		LookupEnv: lookup,
		Overrides: map[string]string{
			"runtime.max_open_conns": "8",
			"dsn":                    "cli-user:cli-secret@tcp(example.invalid:4000)/app",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Runtime.MaxOpenConns != 8 {
		t.Fatalf("MaxOpenConns = %d, want CLI value 8", got.Runtime.MaxOpenConns)
	}
	if got.Runtime.MaxIdleConns != 2 {
		t.Fatalf("MaxIdleConns = %d, want file value 2", got.Runtime.MaxIdleConns)
	}
	if !strings.Contains(got.Environment.DSN, "cli-secret") {
		t.Fatalf("DSN did not use the CLI source")
	}
}

func TestLoadEnvironmentOverridesFile(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
runtime:
  max_open_conns: 2
  max_idle_conns: 2
`)
	got, err := Load(Options{
		Path: path,
		LookupEnv: func(key string) (string, bool) {
			if key == "TIDBGO_RUNTIME_MAX_OPEN_CONNS" {
				return "5", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Runtime.MaxOpenConns != 5 {
		t.Fatalf("MaxOpenConns = %d, want environment value 5", got.Runtime.MaxOpenConns)
	}
}

func TestLoadRejectsSecretInFileWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	const secret = "do-not-print-this-password"
	path := writeConfig(t, "dsn: "+secret+"\n")
	_, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported field error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error disclosed the configured secret: %v", err)
	}
}

func TestLoadRejectsUnknownAndInvalidFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown", content: "unknown: value\n"},
		{name: "wrong profile", content: "profile: mysql\n"},
		{name: "negative batch", content: "runtime:\n  relation_batch_size: -1\n"},
		{name: "invalid duration", content: "runtime:\n  conn_max_lifetime: forever\n"},
		{name: "empty command", content: "schema:\n  command:\n"},
		{name: "unsafe migration table", content: "migrations:\n  table: migrations;drop\n"},
		{name: "long migration table", content: "migrations:\n  table: abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklm\n"},
		{name: "non-finite RU ratio", content: "lint:\n  ru_regression_ratio: NaN\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, tt.content)
			if _, err := Load(Options{Path: path, LookupEnv: emptyEnvironment}); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

func TestLoadSchemaCommandEnvironmentUsesJSONArray(t *testing.T) {
	t.Parallel()

	got, err := Load(Options{LookupEnv: func(key string) (string, bool) {
		if key == "TIDBGO_SCHEMA_COMMAND" {
			return `["go","run","./custom/schema"]`, true
		}
		return "", false
	}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got.Schema.Command, []string{"go", "run", "./custom/schema"}) {
		t.Fatalf("Schema.Command = %#v", got.Schema.Command)
	}
}

func TestLoadExplainAnalyzeOptIn(t *testing.T) {
	t.Parallel()

	got, err := Load(Options{LookupEnv: func(key string) (string, bool) {
		if key == "TIDBGO_ALLOW_EXPLAIN_ANALYZE" {
			return "1", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.Environment.AllowExplainAnalyze {
		t.Fatal("AllowExplainAnalyze = false, want true")
	}
}

func TestYAMLQuotedScalarsAndComments(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
# repository configuration
profile: "tidb-cloud-starter" # supported profile
schema:
  generated_dir: "./generated"
migrations:
  lock_name: 'tidbgo:#migration'
`)
	got, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Migrations.LockName != "tidbgo:#migration" {
		t.Fatalf("LockName = %q", got.Migrations.LockName)
	}
	if got.Schema.GeneratedDir != "./generated" {
		t.Fatalf("GeneratedDir = %q, want %q", got.Schema.GeneratedDir, "./generated")
	}
}

func TestYAMLEmptyOrCommentOnlyUsesDefaults(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "# repository configuration\n"} {
		path := writeConfig(t, content)
		got, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if want := Default(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Load() = %#v, want %#v", got, want)
		}
	}
}

func TestYAMLRejectsUnsupportedOrAmbiguousSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "document marker", content: "---\nprofile: tidb-cloud-starter\n"},
		{name: "flow sequence", content: "schema:\n  command: [go, run, ./db/schema]\n"},
		{name: "flow mapping", content: "runtime: {max_open_conns: 1}\n"},
		{name: "anchor", content: "profile: &profile tidb-cloud-starter\n"},
		{name: "explicit tag", content: "profile: !!str tidb-cloud-starter\n"},
		{name: "non-specific tag", content: "profile: ! tidb-cloud-starter\n"},
		{name: "quoted key", content: "\"profile\": tidb-cloud-starter\n"},
		{name: "multiline", content: "profile: |\n  tidb-cloud-starter\n"},
		{name: "tab indentation", content: "runtime:\n\tmax_open_conns: 1\n"},
		{name: "duplicate key", content: "profile: tidb-cloud-starter\nprofile: tidb-cloud-starter\n"},
		{name: "duplicate mapping", content: "runtime:\n  max_open_conns: 1\nruntime:\n  max_idle_conns: 1\n"},
		{name: "over-indented mapping", content: "runtime:\n    max_open_conns: 1\n"},
		{name: "unknown empty mapping", content: "unknown:\n"},
		{name: "root sequence", content: "- profile\n- tidb-cloud-starter\n"},
		{name: "nested sequence item", content: "schema:\n  command:\n    - go\n    - [run, ./db/schema]\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, tt.content)
			if _, err := Load(Options{Path: path, LookupEnv: emptyEnvironment}); err == nil {
				t.Fatal("Load() error = nil, want syntax error")
			}
		})
	}
}

func TestYAMLErrorsDoNotEchoMalformedSecret(t *testing.T) {
	t.Parallel()

	const secret = "do-not-print-this-password"
	for _, content := range []string{
		"dsn: \"" + secret + "\n",
		"dsn: *" + secret + "\n",
	} {
		path := writeConfig(t, content)
		_, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
		if err == nil {
			t.Fatal("Load() error = nil, want YAML syntax error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Load() error disclosed the configured secret: %v", err)
		}
	}
}

func TestYAMLRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tidbgo.yaml")
	if err := os.WriteFile(path, []byte{'p', 'r', 'o', 'f', 'i', 'l', 'e', ':', ' ', 0xff, '\n'}, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want UTF-8 error")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("Load() error = %v, want UTF-8 error", err)
	}
}

func TestYAMLRejectsExcessiveDepthDuringParsing(t *testing.T) {
	t.Parallel()

	var content strings.Builder
	for depth := 0; depth < maxYAMLDepth+2; depth++ {
		content.WriteString(strings.Repeat("  ", depth))
		content.WriteString("level:\n")
	}

	path := writeConfig(t, content.String())
	_, err := Load(Options{Path: path, LookupEnv: emptyEnvironment})
	if err == nil {
		t.Fatal("Load() error = nil, want YAML depth error")
	}
	if !strings.Contains(err.Error(), "invalid YAML syntax") {
		t.Fatalf("Load() error = %v, want parser rejection", err)
	}
}

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tidbgo.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimPrefix(content, "\n")), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
