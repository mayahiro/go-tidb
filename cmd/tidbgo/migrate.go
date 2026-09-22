package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/migrate"
)

func migrateCommand() *cli.Command {
	root := cli.NewCommand("migrate").ID("migrate").About("Manage versioned SQL migrations and the current database schema snapshot").RequireSubcommand()
	for _, action := range []string{"new", "lint", "init", "baseline", "status", "plan", "up", "down", "dump"} {
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
		if action == "lint" {
			command.Option(cli.ValueOption("migration-schema").Long("schema").Parser(cli.StringParser()).Help("Prior SQL snapshot; requires --file and --direction")).
				Option(cli.ValueOption("migration-file").Long("file").Repeated().Parser(cli.StringParser()).Help("Filename in --dir; repeat in execution order for schema checks")).
				Option(cli.ValueOption("migration-direction").Long("direction").Parser(cli.StringParser()).Help("up or down; default without --schema: both"))
		}
		if action == "up" || action == "down" || action == "plan" {
			command.Option(cli.ValueOption("migration-steps").Long("steps").Parser(cli.CustomParser("COUNT", strconv.Atoi)).Help("Versions to execute; default: all for up, one for down"))
		}
		if action == "plan" {
			command.Option(cli.ValueOption("migration-direction").Long("direction").Parser(cli.StringParser()).Help("up or down (default: up)"))
		}
		command.Handle(func(c *cli.Context, in *cli.Invocation) (cli.Outcome, error) { return runMigrate(c, in, action) })
		root.Subcommand(command)
	}
	return root
}

func migrationHelp(action string) string {
	return map[string]string{
		"new":      "Create one offline SQL template with a UTC millisecond prefix in its filename",
		"lint":     "Check existence guards and optional prior-schema consistency offline",
		"init":     "Capture an existing database into one initial SQL file without Down",
		"baseline": "Record a matching initial file without executing its SQL",
		"status":   "List recorded applied versions and local pending migrations",
		"plan":     "Preview exact up/down SQL without applying it",
		"up":       "Apply pending SQL and refresh schema.sql from the database",
		"down":     "Run reverse SQL and refresh schema.sql from the database",
		"dump":     "Refresh schema.sql without changing database objects or changing applied records",
	}[action]
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
	var dsn string
	var executionLog *migrationLog
	defer func() {
		if executionLog != nil {
			_ = executionLog.close()
		}
	}()
	if action == "new" {
		name := migrationValue(in, "migration-name", "")
		output, operationErr = migrate.Create(directory, name)
	} else if action == "lint" {
		options := migrate.LintOptions{Direction: migrate.Direction(migrationValue(in, "migration-direction", ""))}
		if path := migrationValue(in, "migration-schema", ""); path != "" {
			options.SchemaFile = migrationPath(c, path)
		}
		for _, value := range in.ParsedValues("migration-file") {
			name, _ := value.Typed().(string)
			options.Versions = append(options.Versions, strings.TrimSuffix(name, ".sql"))
		}
		output, operationErr = migrate.Lint(directory, options)
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
		envName := migrationValue(in, "migration-dsn-env", "TIDBGO_DSN")
		dsn, ok = c.Environment(envName)
		if !ok || dsn == "" {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "set the migration DSN environment variable before connecting")
		}
		db, err := openMigrationDatabase(dsn)
		if err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error())
		}
		defer db.Close()
		config := migrate.Config{Directory: directory, SchemaFile: schemaFile, LockTimeout: lockTimeout}
		if action == "up" || action == "down" || action == "baseline" {
			executionLog, err = newMigrationLog(filepath.Join(c.CurrentDirectory(), "log", "tidbgo"), c.Stderr(), dsn)
			if err != nil {
				return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "prepare migration log: "+err.Error())
			}
			config.OnEvent = executionLog.event
		}
		runner, err := migrate.New(db, config)
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
		}
	}
	if executionLog != nil {
		operationErr = errors.Join(operationErr, executionLog.finish(operationErr))
		if result, ok := output.(migrate.Result); ok {
			output = migrationRunResult{Result: result, LogFile: executionLog.path}
		}
	}
	jsonOutput, _ := in.Flag("migration-json")
	var writeErr error
	if operationErr != nil {
		switch output.(type) {
		case migrate.Result, migrationRunResult:
		default:
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
		if _, err := fmt.Fprintln(c.Stderr(), safeMigrationError(operationErr, dsn)); err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write migration error failed")
		}
		return cli.NewOutcome(exitDiagnosticFailure), nil
	}
	if result, ok := output.(migrate.LintResult); ok && result.HasErrors() {
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

func migrationSecrets(dsn string) []string {
	secrets := []string{dsn}
	if config, err := mysql.ParseDSN(dsn); err == nil {
		secrets = append(secrets, config.Passwd)
	}
	return secrets
}

func safeMigrationError(err error, dsn string) string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		messages := make([]string, 0, len(joined.Unwrap()))
		for _, cause := range joined.Unwrap() {
			messages = append(messages, safeMigrationError(cause, dsn))
		}
		return strings.Join(messages, "; ")
	}
	var operation *migrate.OperationError
	if errors.As(err, &operation) {
		if operation.Cause != nil {
			return operation.Error() + "; cause=" + safeMigrationError(operation.Cause, dsn)
		}
		return operation.Error()
	}
	var server *mysql.MySQLError
	if errors.As(err, &server) {
		state := strings.Trim(string(server.SQLState[:]), "\x00")
		return fmt.Sprintf("database error %d (SQLSTATE %q): %s", server.Number, state, strconv.Quote(redact.String(server.Message, migrationSecrets(dsn)...)))
	}
	return strconv.Quote(redact.Error(err, migrationSecrets(dsn)...))
}

type migrationRunResult struct {
	migrate.Result
	LogFile string `json:"log_file"`
}

func writeMigrationText(c *cli.Context, output any) error {
	switch value := output.(type) {
	case migrate.LintResult:
		if _, err := fmt.Fprintf(c.Stdout(), "versions=%d statements=%d unverified=%d schema_checked=%t execution_checked=false\n", value.Versions, value.Statements, value.Unverified, value.SchemaChecked); err != nil {
			return err
		}
		for _, issue := range value.Issues {
			if _, err := fmt.Fprintf(c.Stdout(), "%s %s.sql %s statement=%d: %s\n", issue.Severity, issue.Version, issue.Direction, issue.Statement, issue.Message); err != nil {
				return err
			}
		}
		return nil
	case migrate.Plan:
		if _, err := fmt.Fprintf(c.Stdout(), "database=%s %s: version %s -> %s creates_history=%t\n", value.Database, value.Direction, value.CurrentVersion, value.TargetVersion, value.CreatesHistory); err != nil {
			return err
		}
		for _, step := range value.Steps {
			if _, err := fmt.Fprintf(c.Stdout(), "file %s.sql\n", step.Version); err != nil {
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
		if _, err := fmt.Fprintf(c.Stdout(), "database=%s version=%s managed=%t\n", value.Database, value.Version, value.Managed); err != nil {
			return err
		}
		for _, m := range value.Migrations {
			if _, err := fmt.Fprintf(c.Stdout(), "%s %s created_at=%v reversible=%t\n", m.Version, m.State, m.CreatedAt, m.Reversible); err != nil {
				return err
			}
		}
		return nil
	case migrationRunResult:
		if err := writeMigrationText(c, value.Result); err != nil {
			return err
		}
		_, err := fmt.Fprintf(c.Stdout(), "log_file=%s\n", value.LogFile)
		return err
	case migrate.Result:
		_, err := fmt.Fprintf(c.Stdout(), "database=%s version=%s completed=%v snapshot_updated=%t\n", value.Database, value.Version, value.Completed, value.SnapshotUpdated)
		return err
	case migrate.Migration:
		_, err := fmt.Fprintf(c.Stdout(), "created %s.sql\n", value.Version)
		return err
	default:
		return json.NewEncoder(c.Stdout()).Encode(output)
	}
}
