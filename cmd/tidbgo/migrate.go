package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/migrate"
)

func migrateCommand() *cli.Command {
	root := cli.NewCommand("migrate").ID("migrate").About("Manage versioned SQL migrations and the current database schema snapshot").RequireSubcommand()
	for _, action := range []string{"new", "lint", "init", "baseline", "status", "plan", "up", "down", "dump", "repair"} {
		command := cli.NewCommand(action).ID("migrate-" + action).About(migrationHelp(action)).
			Option(cli.ValueOption("migration-dir").Long("dir").Parser(cli.StringParser()).Help("Migration directory (default: migrations)")).
			Option(cli.Flag("migration-json").Long("json").Help("Write structured JSON output"))
		if action == "new" {
			command.Argument(cli.Positional("migration-name").Parser(cli.StringParser()).Help("Lowercase migration name"))
		}
		if action != "new" && action != "lint" {
			command.Option(cli.ValueOption("migration-schema").Long("schema").Parser(cli.StringParser()).Help("Current database snapshot output (default: schema.sql)")).
				Option(cli.ValueOption("migration-dsn-env").Long("dsn-env").Parser(cli.StringParser()).Help("Environment variable holding the TLS MySQL DSN (default: TIDBGO_DSN)")).
				Option(cli.ValueOption("migration-timeout").Long("timeout").Parser(cli.CustomParser("DURATION", time.ParseDuration)).Help("Operation deadline (default: 30m)")).
				Option(cli.ValueOption("migration-lock-timeout").Long("lock-timeout").Parser(cli.CustomParser("DURATION", time.ParseDuration)).Help("Lock timeout: whole seconds, 1s..1h (default: 30s)"))
		}
		if action == "up" || action == "down" || action == "plan" {
			command.Option(cli.ValueOption("migration-steps").Long("steps").Parser(cli.CustomParser("COUNT", strconv.Atoi)).Help("Versions to execute; default: all for up, one for down"))
		}
		if action == "plan" {
			command.Option(cli.ValueOption("migration-direction").Long("direction").Parser(cli.StringParser()).Help("up or down (default: up)"))
		}
		if action == "repair" {
			command.Argument(cli.Positional("migration-version").Parser(cli.CustomParser("VERSION", parseMigrationVersion)).Help("Unresolved migration version")).
				Option(cli.ValueOption("migration-state").Long("state").Parser(cli.StringParser()).Help("Confirmed result: applied or reverted")).
				Option(cli.ValueOption("migration-expected").Long("expected-schema").Parser(cli.StringParser()).Help("Reviewed SQL snapshot that must match the current database")).
				Option(cli.ValueOption("migration-reason").Long("reason").Parser(cli.StringParser()).Help("Required audit explanation; independently verify data and server DDL completion"))
		}
		command.Handle(func(c *cli.Context, in *cli.Invocation) (cli.Outcome, error) { return runMigrate(c, in, action) })
		root.Subcommand(command)
	}
	return root
}

func migrationHelp(action string) string {
	return map[string]string{
		"new":      "Create one offline SQL template with up/down sections and a UTC millisecond version",
		"lint":     "Validate migration files offline",
		"init":     "Capture an existing database into an initial up section without changing database objects",
		"baseline": "Adopt the matching initial migration and refresh schema.sql without executing its SQL",
		"status":   "Inspect history, interrupted attempts, and live schema drift",
		"plan":     "Preview exact up/down SQL without applying it",
		"up":       "Apply pending SQL and refresh schema.sql from the database",
		"down":     "Run reverse SQL and refresh schema.sql from the database",
		"dump":     "Refresh schema.sql without changing database objects or repairing history",
		"repair":   "Record an explicitly verified result of an interrupted migration",
	}[action]
}

func parseMigrationVersion(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("expected a positive migration version")
	}
	return v, nil
}
func migrationValue(in *cli.Invocation, key, fallback string) string {
	value, ok := cli.ValueAs[string](in, key)
	if !ok {
		return fallback
	}
	return value
}
func migrationPath(c *cli.Context, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(c.CurrentDirectory(), value)
}

func runMigrate(c *cli.Context, in *cli.Invocation, action string) (cli.Outcome, error) {
	directory := migrationPath(c, migrationValue(in, "migration-dir", "migrations"))
	var output any
	var operationErr error
	if action == "new" {
		name := migrationValue(in, "migration-name", "")
		output, operationErr = migrate.Create(directory, name)
	} else if action == "lint" {
		files, err := migrate.Load(directory)
		operationErr = err
		output = struct {
			Versions int `json:"versions"`
		}{len(files)}
	} else {
		schemaFile := migrationPath(c, migrationValue(in, "migration-schema", "schema.sql"))
		timeout, ok := cli.ValueAs[time.Duration](in, "migration-timeout")
		if !ok {
			timeout = 30 * time.Minute
		}
		if timeout <= 0 {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "migration timeout must be positive")
		}
		lockTimeout, hasLockTimeout := cli.ValueAs[time.Duration](in, "migration-lock-timeout")
		if hasLockTimeout && lockTimeout == 0 {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "migration lock timeout must be whole seconds from 1s to 1h")
		}
		direction := migrate.Up
		if action == "down" {
			direction = migrate.Down
		}
		if action == "plan" {
			direction = migrate.Direction(migrationValue(in, "migration-direction", "up"))
		}
		steps, present := cli.ValueAs[int](in, "migration-steps")
		if !present && direction == migrate.Down {
			steps = 1
		}
		if direction != migrate.Up && direction != migrate.Down || steps < 0 || direction == migrate.Down && steps == 0 {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "use direction up or down and a valid step count")
		}
		var repair migrate.RepairOptions
		if action == "repair" {
			repair.Version, _ = cli.ValueAs[int64](in, "migration-version")
			state := migrationValue(in, "migration-state", "")
			repair.Applied = state == "applied"
			expected := migrationValue(in, "migration-expected", "")
			repair.Reason = migrationValue(in, "migration-reason", "")
			if repair.Version <= 0 || state != "applied" && state != "reverted" || expected == "" || repair.Reason == "" {
				return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "repair requires VERSION, --state applied|reverted, --expected-schema, and --reason")
			}
			repair.ExpectedSchema = migrationPath(c, expected)
		}
		envName := migrationValue(in, "migration-dsn-env", "TIDBGO_DSN")
		dsn, ok := c.Environment(envName)
		if !ok || dsn == "" {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "set the migration DSN environment variable before connecting")
		}
		db, err := openMigrationDatabase(dsn)
		if err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error())
		}
		defer db.Close()
		runner, err := migrate.New(db, migrate.Config{Directory: directory, SchemaFile: schemaFile, LockTimeout: lockTimeout})
		if err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error())
		}
		ctx, cancel := context.WithTimeout(c.Cancellation(), timeout)
		defer cancel()
		switch action {
		case "init":
			output, operationErr = runner.Init(ctx)
		case "baseline":
			output, operationErr = runner.Baseline(ctx)
		case "status":
			output, operationErr = runner.Status(ctx)
		case "plan":
			output, operationErr = runner.Plan(ctx, direction, steps)
		case "up", "down":
			output, operationErr = runner.Apply(ctx, direction, steps)
		case "dump":
			output, operationErr = runner.Dump(ctx)
		case "repair":
			output, operationErr = runner.Repair(ctx, repair)
		}
	}
	jsonOutput, _ := in.Flag("migration-json")
	var writeErr error
	if operationErr != nil {
		if _, ok := output.(migrate.Result); !ok {
			output = nil
		}
	}
	if output == nil {
		// Failed offline operations and plans have no successful result to print.
	} else if jsonOutput {
		writeErr = json.NewEncoder(c.Stdout()).Encode(output)
	} else {
		writeErr = writeMigrationText(c, output)
	}
	if writeErr != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write migration output failed")
	}
	if operationErr != nil {
		if _, err := fmt.Fprintln(c.Stderr(), safeMigrationError(operationErr)); err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write migration error failed")
		}
		return cli.NewOutcome(exitDiagnosticFailure), nil
	}
	if status, ok := output.(migrate.Status); ok && (status.Dirty || status.Drift) {
		return cli.NewOutcome(exitDiagnosticFailure), nil
	}
	return cli.Success(), nil
}

func openMigrationDatabase(dsn string) (*sql.DB, error) {
	return openToolDatabase(dsn, "migration")
}

func openToolDatabase(dsn, operation string) (*sql.DB, error) {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid %s DSN", operation)
	}
	if config.DBName == "" || config.Net != "tcp" || config.TLS == nil || config.TLS.InsecureSkipVerify || config.AllowFallbackToPlaintext {
		return nil, fmt.Errorf("%s DSN requires a database and TCP with verified TLS (tls=true)", operation)
	}
	if config.MultiStatements || config.AllowAllFiles || config.AllowOldPasswords || len(config.Params) > 0 {
		return nil, fmt.Errorf("%s DSN cannot enable multiple statements, arbitrary file access, old passwords, or session-variable parameters", operation)
	}
	config.Logger = migrationDriverLogger{}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, fmt.Errorf("invalid %s connection configuration", operation)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(time.Minute)
	return db, nil
}

type migrationDriverLogger struct{}

func (migrationDriverLogger) Print(...any) {}

func safeMigrationError(err error) string {
	var operation *migrate.OperationError
	if errors.As(err, &operation) {
		return err.Error()
	}
	var server *mysql.MySQLError
	if errors.As(err, &server) {
		return fmt.Sprintf("migrate: database error %d; inspect the database before retrying", server.Number)
	}
	return err.Error()
}

func writeMigrationText(c *cli.Context, output any) error {
	switch value := output.(type) {
	case migrate.Plan:
		if _, err := fmt.Fprintf(c.Stdout(), "database=%s %s: version %d -> %d creates_history=%t\n", value.Database, value.Direction, value.CurrentVersion, value.TargetVersion, value.CreatesHistory); err != nil {
			return err
		}
		for _, step := range value.Steps {
			if _, err := fmt.Fprintf(c.Stdout(), "version %d %s\n", step.Version, step.Name); err != nil {
				return err
			}
			for _, sql := range step.Statements {
				if _, err := fmt.Fprintf(c.Stdout(), "%s;\n", sql); err != nil {
					return err
				}
			}
		}
		return nil
	case migrate.Status:
		if _, err := fmt.Fprintf(c.Stdout(), "database=%s version=%d baseline=%d managed=%t dirty=%t drift=%t\n", value.Database, value.Version, value.Baseline, value.Managed, value.Dirty, value.Drift); err != nil {
			return err
		}
		for _, m := range value.Migrations {
			if _, err := fmt.Fprintf(c.Stdout(), "%d %s %s reversible=%t\n", m.Version, m.Name, m.State, m.Reversible); err != nil {
				return err
			}
		}
		return nil
	case migrate.Result:
		_, err := fmt.Fprintf(c.Stdout(), "database=%s version=%d completed=%v snapshot_updated=%t dirty=%t\n", value.Database, value.Version, value.Completed, value.SnapshotUpdated, value.Dirty)
		return err
	case migrate.Migration:
		_, err := fmt.Fprintf(c.Stdout(), "created %017d_%s.sql\n", value.Version, value.Name)
		return err
	default:
		return json.NewEncoder(c.Stdout()).Encode(output)
	}
}
