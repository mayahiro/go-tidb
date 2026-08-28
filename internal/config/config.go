// Package config loads the repository configuration used by tidbgo commands.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mayahiro/go-tidb/check"
)

const (
	// CurrentVersion is the supported tidbgo.yaml format version.
	CurrentVersion = 1
	// ProfileTiDBCloudStarter is the only deployment profile supported in v0.1.
	ProfileTiDBCloudStarter = "tidb-cloud-starter"
)

// Config is the fully resolved repository configuration.
type Config struct {
	Version     int
	Profile     string
	Schema      SchemaConfig
	Migrations  MigrationConfig
	Runtime     RuntimeConfig
	Lint        LintConfig
	Environment EnvironmentConfig
}

// SchemaConfig controls schema descriptor execution and generated output.
type SchemaConfig struct {
	Command      []string
	GeneratedDir string
}

// MigrationConfig controls versioned migration files and locking.
type MigrationConfig struct {
	Dir         string
	Table       string
	LockName    string
	LockTimeout time.Duration
}

// RuntimeConfig contains deterministic runtime defaults.
type RuntimeConfig struct {
	RelationBatchSize int
	MaxPreloadDepth   int
	ConnMaxLifetime   time.Duration
	MaxOpenConns      int
	MaxIdleConns      int
}

// LintConfig controls the default diagnostic policy and thresholds.
type LintConfig struct {
	FailOn                  check.Severity
	LargeOffsetThreshold    int64
	UnboundedSelectSeverity check.Severity
	FullScanEstRowsWarning  int64
	FullScanEstRowsError    int64
	RURegressionRatio       float64
	RURegressionAbsolute    float64
}

// EnvironmentConfig contains values that must not be stored in tidbgo.yaml.
type EnvironmentConfig struct {
	DSN                 string
	TestDSN             string
	AllowExplainAnalyze bool
}

// Options controls configuration sources. Overrides use dotted field names,
// such as "runtime.max_open_conns". A schema.command override is encoded as a
// JSON string array so argument boundaries remain unambiguous.
type Options struct {
	Path      string
	LookupEnv func(string) (string, bool)
	Overrides map[string]string
}

// Default returns a new configuration populated with documented defaults.
func Default() Config {
	return Config{
		Version: CurrentVersion,
		Profile: ProfileTiDBCloudStarter,
		Schema: SchemaConfig{
			Command:      []string{"go", "run", "./db/schema"},
			GeneratedDir: "./internal/db",
		},
		Migrations: MigrationConfig{
			Dir:         "./db/migrations",
			Table:       "_tidbgo_migrations",
			LockName:    "tidbgo:migrate",
			LockTimeout: 30 * time.Second,
		},
		Runtime: RuntimeConfig{
			RelationBatchSize: 500,
			MaxPreloadDepth:   3,
			ConnMaxLifetime:   5 * time.Minute,
			MaxOpenConns:      10,
			MaxIdleConns:      10,
		},
		Lint: LintConfig{
			FailOn:                  check.SeverityError,
			LargeOffsetThreshold:    10_000,
			UnboundedSelectSeverity: check.SeverityWarning,
			FullScanEstRowsWarning:  1_000,
			FullScanEstRowsError:    100_000,
			RURegressionRatio:       0.30,
			RURegressionAbsolute:    1.0,
		},
	}
}

// Load resolves defaults, an optional file, environment variables, and CLI
// overrides in that order. Later sources take precedence over earlier ones.
func Load(options Options) (Config, error) {
	resolved := Default()

	if options.Path != "" {
		data, err := os.ReadFile(options.Path)
		if err != nil {
			return Config{}, fmt.Errorf("read configuration %q: %w", options.Path, err)
		}

		values, err := parseYAML(data)
		if err != nil {
			return Config{}, fmt.Errorf("parse configuration %q: %w", options.Path, err)
		}
		if err := applyValues(&resolved, values); err != nil {
			return Config{}, fmt.Errorf("load configuration %q: %w", options.Path, err)
		}
	}

	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	for _, field := range fields {
		if field.env == "" {
			continue
		}
		value, ok := lookupEnv(field.env)
		if !ok {
			continue
		}
		raw, err := rawFromText(field.key, value)
		if err != nil {
			return Config{}, fmt.Errorf("load environment variable %s: %w", field.env, err)
		}
		if err := field.apply(&resolved, raw); err != nil {
			return Config{}, fmt.Errorf("load environment variable %s: %w", field.env, err)
		}
	}

	if len(options.Overrides) > 0 {
		keys := make([]string, 0, len(options.Overrides))
		for key := range options.Overrides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			field, ok := fieldByKey[key]
			if !ok || !field.allowOverride {
				return Config{}, fmt.Errorf("load CLI override: unsupported field %q", key)
			}
			raw, err := rawFromText(key, options.Overrides[key])
			if err != nil {
				return Config{}, fmt.Errorf("load CLI override %q: %w", key, err)
			}
			if err := field.apply(&resolved, raw); err != nil {
				return Config{}, fmt.Errorf("load CLI override %q: %w", key, err)
			}
		}
	}

	if err := validate(resolved); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return resolved, nil
}

type rawValue struct {
	scalar string
	items  []string
	isList bool
}

func (v rawValue) requireScalar(key string) (string, error) {
	if v.isList {
		return "", fmt.Errorf("%s must be a scalar", key)
	}
	return v.scalar, nil
}

func (v rawValue) requireList(key string) ([]string, error) {
	if !v.isList {
		return nil, fmt.Errorf("%s must be a list", key)
	}
	return append([]string(nil), v.items...), nil
}

type fieldSpec struct {
	key           string
	env           string
	allowFile     bool
	allowOverride bool
	apply         func(*Config, rawValue) error
}

var fields = []fieldSpec{
	intField("version", "", true, false, func(config *Config, value int) { config.Version = value }),
	stringField("profile", "TIDBGO_PROFILE", true, true, func(config *Config, value string) { config.Profile = value }),
	listField("schema.command", "TIDBGO_SCHEMA_COMMAND", true, true, func(config *Config, value []string) { config.Schema.Command = value }),
	stringField("schema.generated_dir", "TIDBGO_SCHEMA_GENERATED_DIR", true, true, func(config *Config, value string) { config.Schema.GeneratedDir = value }),
	stringField("migrations.dir", "TIDBGO_MIGRATIONS_DIR", true, true, func(config *Config, value string) { config.Migrations.Dir = value }),
	stringField("migrations.table", "TIDBGO_MIGRATIONS_TABLE", true, true, func(config *Config, value string) { config.Migrations.Table = value }),
	stringField("migrations.lock_name", "TIDBGO_MIGRATIONS_LOCK_NAME", true, true, func(config *Config, value string) { config.Migrations.LockName = value }),
	durationField("migrations.lock_timeout", "TIDBGO_MIGRATIONS_LOCK_TIMEOUT", true, true, func(config *Config, value time.Duration) { config.Migrations.LockTimeout = value }),
	intField("runtime.relation_batch_size", "TIDBGO_RUNTIME_RELATION_BATCH_SIZE", true, true, func(config *Config, value int) { config.Runtime.RelationBatchSize = value }),
	intField("runtime.max_preload_depth", "TIDBGO_RUNTIME_MAX_PRELOAD_DEPTH", true, true, func(config *Config, value int) { config.Runtime.MaxPreloadDepth = value }),
	durationField("runtime.conn_max_lifetime", "TIDBGO_RUNTIME_CONN_MAX_LIFETIME", true, true, func(config *Config, value time.Duration) { config.Runtime.ConnMaxLifetime = value }),
	intField("runtime.max_open_conns", "TIDBGO_RUNTIME_MAX_OPEN_CONNS", true, true, func(config *Config, value int) { config.Runtime.MaxOpenConns = value }),
	intField("runtime.max_idle_conns", "TIDBGO_RUNTIME_MAX_IDLE_CONNS", true, true, func(config *Config, value int) { config.Runtime.MaxIdleConns = value }),
	severityField("lint.fail_on", "TIDBGO_LINT_FAIL_ON", true, true, func(config *Config, value check.Severity) { config.Lint.FailOn = value }),
	int64Field("lint.large_offset_threshold", "TIDBGO_LINT_LARGE_OFFSET_THRESHOLD", true, true, func(config *Config, value int64) { config.Lint.LargeOffsetThreshold = value }),
	severityField("lint.unbounded_select_severity", "TIDBGO_LINT_UNBOUNDED_SELECT_SEVERITY", true, true, func(config *Config, value check.Severity) { config.Lint.UnboundedSelectSeverity = value }),
	int64Field("lint.full_scan_est_rows_warning", "TIDBGO_LINT_FULL_SCAN_EST_ROWS_WARNING", true, true, func(config *Config, value int64) { config.Lint.FullScanEstRowsWarning = value }),
	int64Field("lint.full_scan_est_rows_error", "TIDBGO_LINT_FULL_SCAN_EST_ROWS_ERROR", true, true, func(config *Config, value int64) { config.Lint.FullScanEstRowsError = value }),
	floatField("lint.ru_regression_ratio", "TIDBGO_LINT_RU_REGRESSION_RATIO", true, true, func(config *Config, value float64) { config.Lint.RURegressionRatio = value }),
	floatField("lint.ru_regression_absolute", "TIDBGO_LINT_RU_REGRESSION_ABSOLUTE", true, true, func(config *Config, value float64) { config.Lint.RURegressionAbsolute = value }),
	stringField("dsn", "TIDBGO_DSN", false, true, func(config *Config, value string) { config.Environment.DSN = value }),
	stringField("test_dsn", "TIDBGO_TEST_DSN", false, true, func(config *Config, value string) { config.Environment.TestDSN = value }),
	boolField("allow_explain_analyze", "TIDBGO_ALLOW_EXPLAIN_ANALYZE", false, true, func(config *Config, value bool) { config.Environment.AllowExplainAnalyze = value }),
}

var fieldByKey = func() map[string]fieldSpec {
	result := make(map[string]fieldSpec, len(fields))
	for _, field := range fields {
		result[field.key] = field
	}
	return result
}()

func applyValues(config *Config, values map[string]rawValue) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		field, ok := fieldByKey[key]
		if !ok || !field.allowFile {
			return fmt.Errorf("unsupported field %q", key)
		}
		if err := field.apply(config, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func rawFromText(key, value string) (rawValue, error) {
	if key != "schema.command" {
		return rawValue{scalar: value}, nil
	}

	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return rawValue{}, errors.New("schema.command must be a JSON string array outside tidbgo.yaml")
	}
	return rawValue{items: items, isList: true}, nil
}

func stringField(key, env string, allowFile, allowOverride bool, set func(*Config, string)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		value, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		set(config, value)
		return nil
	}}
}

func listField(key, env string, allowFile, allowOverride bool, set func(*Config, []string)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		value, err := raw.requireList(key)
		if err != nil {
			return err
		}
		set(config, value)
		return nil
	}}
}

func intField(key, env string, allowFile, allowOverride bool, set func(*Config, int)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value, err := strconv.Atoi(text)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		set(config, value)
		return nil
	}}
}

func int64Field(key, env string, allowFile, allowOverride bool, set func(*Config, int64)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		set(config, value)
		return nil
	}}
}

func floatField(key, env string, allowFile, allowOverride bool, set func(*Config, float64)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("%s must be a number", key)
		}
		set(config, value)
		return nil
	}}
}

func boolField(key, env string, allowFile, allowOverride bool, set func(*Config, bool)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value, err := strconv.ParseBool(text)
		if err != nil {
			return fmt.Errorf("%s must be a boolean", key)
		}
		set(config, value)
		return nil
	}}
}

func durationField(key, env string, allowFile, allowOverride bool, set func(*Config, time.Duration)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("%s must be a Go duration", key)
		}
		set(config, value)
		return nil
	}}
}

func severityField(key, env string, allowFile, allowOverride bool, set func(*Config, check.Severity)) fieldSpec {
	return fieldSpec{key: key, env: env, allowFile: allowFile, allowOverride: allowOverride, apply: func(config *Config, raw rawValue) error {
		text, err := raw.requireScalar(key)
		if err != nil {
			return err
		}
		value := check.Severity(strings.ToLower(text))
		if !validSeverity(value) {
			return fmt.Errorf("%s must be info, warning, or error", key)
		}
		set(config, value)
		return nil
	}}
}

func validate(config Config) error {
	if config.Version != CurrentVersion {
		return fmt.Errorf("version must be %d", CurrentVersion)
	}
	if config.Profile != ProfileTiDBCloudStarter {
		return fmt.Errorf("profile must be %q", ProfileTiDBCloudStarter)
	}
	if len(config.Schema.Command) == 0 {
		return errors.New("schema.command must not be empty")
	}
	for _, argument := range config.Schema.Command {
		if argument == "" {
			return errors.New("schema.command arguments must not be empty")
		}
	}
	if config.Schema.GeneratedDir == "" {
		return errors.New("schema.generated_dir must not be empty")
	}
	if config.Migrations.Dir == "" {
		return errors.New("migrations.dir must not be empty")
	}
	if config.Migrations.Table == "" {
		return errors.New("migrations.table must not be empty")
	}
	if !validIdentifier(config.Migrations.Table) {
		return errors.New("migrations.table must be a simple SQL identifier")
	}
	if len(config.Migrations.Table) > 64 {
		return errors.New("migrations.table must be at most 64 bytes")
	}
	if config.Migrations.LockName == "" {
		return errors.New("migrations.lock_name must not be empty")
	}
	if len(config.Migrations.LockName) > 64 {
		return errors.New("migrations.lock_name must be at most 64 bytes")
	}
	if strings.IndexFunc(config.Migrations.LockName, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0 {
		return errors.New("migrations.lock_name must not contain control characters")
	}
	if config.Migrations.LockTimeout <= 0 {
		return errors.New("migrations.lock_timeout must be positive")
	}
	if config.Runtime.RelationBatchSize <= 0 {
		return errors.New("runtime.relation_batch_size must be positive")
	}
	if config.Runtime.MaxPreloadDepth <= 0 {
		return errors.New("runtime.max_preload_depth must be positive")
	}
	if config.Runtime.ConnMaxLifetime <= 0 {
		return errors.New("runtime.conn_max_lifetime must be positive")
	}
	if config.Runtime.MaxOpenConns < 0 {
		return errors.New("runtime.max_open_conns must not be negative")
	}
	if config.Runtime.MaxIdleConns < 0 {
		return errors.New("runtime.max_idle_conns must not be negative")
	}
	if config.Runtime.MaxOpenConns > 0 && config.Runtime.MaxIdleConns > config.Runtime.MaxOpenConns {
		return errors.New("runtime.max_idle_conns must not exceed runtime.max_open_conns")
	}
	if !validSeverity(config.Lint.FailOn) {
		return errors.New("lint.fail_on is invalid")
	}
	if !validSeverity(config.Lint.UnboundedSelectSeverity) {
		return errors.New("lint.unbounded_select_severity is invalid")
	}
	if config.Lint.LargeOffsetThreshold < 0 {
		return errors.New("lint.large_offset_threshold must not be negative")
	}
	if config.Lint.FullScanEstRowsWarning < 0 || config.Lint.FullScanEstRowsError < 0 {
		return errors.New("lint full scan thresholds must not be negative")
	}
	if config.Lint.FullScanEstRowsWarning > config.Lint.FullScanEstRowsError {
		return errors.New("lint.full_scan_est_rows_warning must not exceed lint.full_scan_est_rows_error")
	}
	if math.IsNaN(config.Lint.RURegressionRatio) || math.IsInf(config.Lint.RURegressionRatio, 0) || config.Lint.RURegressionRatio < 0 {
		return errors.New("lint.ru_regression_ratio must be finite and not negative")
	}
	if math.IsNaN(config.Lint.RURegressionAbsolute) || math.IsInf(config.Lint.RURegressionAbsolute, 0) || config.Lint.RURegressionAbsolute < 0 {
		return errors.New("lint.ru_regression_absolute must be finite and not negative")
	}
	return nil
}

func validIdentifier(value string) bool {
	for index, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || current == '_' || index > 0 && current >= '0' && current <= '9' {
			continue
		}
		return false
	}
	return value != ""
}

func validSeverity(value check.Severity) bool {
	switch value {
	case check.SeverityInfo, check.SeverityWarning, check.SeverityError:
		return true
	default:
		return false
	}
}
