package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Direction selects forward or reverse SQL execution.
type Direction string

const (
	// Up applies pending versions in ascending order.
	Up Direction = "up"
	// Down reverses applied versions in descending order.
	Down Direction = "down"
)

var (
	// ErrDirty means an interrupted operation needs inspection and explicit repair.
	ErrDirty = errors.New("migrate: interrupted migration requires explicit repair")
	// ErrDrift means the live structure differs from the last recorded structure.
	ErrDrift = errors.New("migrate: live schema differs from migration history")
	// ErrChecksum means recorded migration SQL differs from local files.
	ErrChecksum = errors.New("migrate: migration history and SQL files disagree")
	// ErrUnmanaged means an existing database needs init and baseline adoption.
	ErrUnmanaged = errors.New("migrate: nonempty database has no history; use init and baseline")
	// ErrSnapshot means database work completed but the snapshot file was not updated.
	ErrSnapshot = errors.New("migrate: database operation completed but schema snapshot was not updated; run dump")
	// ErrLocked means another cooperating migration owns the database lock.
	ErrLocked = errors.New("migrate: could not acquire the database migration lock")
)

// Config selects SQL input and snapshot output locations. Empty paths default
// to migrations and schema.sql. The snapshot must be outside the migrations
// directory. LockTimeout defaults to 30 seconds and must be 1..3600 seconds.
type Config struct {
	Directory   string
	SchemaFile  string
	LockTimeout time.Duration
}

// Runner performs explicit deployment operations on a caller-owned pool. It
// never closes that pool or imports a protocol driver. Use a dedicated tooling
// connection configuration with TLS, autocommit, and default quoting semantics.
type Runner struct {
	db     *sql.DB
	config Config
}

// New validates configuration without opening a connection or creating files.
func New(db *sql.DB, config Config) (*Runner, error) {
	if db == nil {
		return nil, fmt.Errorf("migrate: database is required")
	}
	if config.Directory == "" {
		config.Directory = "migrations"
	}
	if config.SchemaFile == "" {
		config.SchemaFile = "schema.sql"
	}
	var err error
	config.Directory, err = resolveLocation(config.Directory)
	if err != nil {
		return nil, err
	}
	config.SchemaFile, err = filepath.Abs(config.SchemaFile)
	if err != nil {
		return nil, err
	}
	parent, err := resolveLocation(filepath.Dir(config.SchemaFile))
	if err != nil {
		return nil, err
	}
	config.SchemaFile = filepath.Join(parent, filepath.Base(config.SchemaFile))
	rel, err := filepath.Rel(config.Directory, config.SchemaFile)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, fmt.Errorf("migrate: schema output must be outside the migration directory")
	}
	if config.LockTimeout == 0 {
		config.LockTimeout = 30 * time.Second
	}
	if config.LockTimeout < time.Second || config.LockTimeout > time.Hour || config.LockTimeout%time.Second != 0 {
		return nil, fmt.Errorf("migrate: lock timeout must be an integer number of seconds from 1 to 3600")
	}
	return &Runner{db, config}, nil
}

// Result describes confirmed progress even when an operation returns an error.
// SnapshotUpdated is independent of database success. Dirty signals that SQL
// execution may have partially completed and must not be blindly retried.
type Result struct {
	Database        string  `json:"database"`
	Version         int64   `json:"version"`
	Completed       []int64 `json:"completed"`
	SnapshotUpdated bool    `json:"snapshot_updated"`
	Dirty           bool    `json:"dirty"`
}

// OperationError locates a failed operation without including SQL or server
// error text. Unwrap provides the original cause for controlled diagnostics.
type OperationError struct {
	Phase     string
	Version   int64
	Statement int
	Cause     error
}

// Error describes the phase and position without disclosing the underlying error.
func (e *OperationError) Error() string {
	return fmt.Sprintf("migrate: %s failed (version=%d, statement=%d); inspect the operation state", e.Phase, e.Version, e.Statement)
}

// Unwrap returns the underlying error; it may contain server-supplied data.
func (e *OperationError) Unwrap() error { return e.Cause }

type session struct {
	conn     *sql.Conn
	database string
}

func (r *Runner) session(ctx context.Context, run func(session) error) (err error) {
	if r == nil || r.db == nil || ctx == nil {
		return fmt.Errorf("migrate: runner and context are required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return &OperationError{Phase: "connect", Cause: err}
	}
	defer conn.Close()
	var database sql.NullString
	var version, mode string
	var autocommit int
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE(), VERSION(), @@SESSION.sql_mode, @@SESSION.autocommit").Scan(&database, &version, &mode, &autocommit); err != nil {
		return &OperationError{Phase: "inspect connection", Cause: err}
	}
	if !database.Valid || database.String == "" || !strings.Contains(strings.ToLower(version), "tidb") {
		return fmt.Errorf("migrate: select a TiDB database before running migrations")
	}
	for _, item := range strings.Split(mode, ",") {
		if item == "ANSI_QUOTES" || item == "NO_BACKSLASH_ESCAPES" {
			return fmt.Errorf("migrate: ANSI_QUOTES and NO_BACKSLASH_ESCAPES are not supported")
		}
	}
	if autocommit != 1 {
		return fmt.Errorf("migrate: migration connections require autocommit=1")
	}
	lock := "tidbgo:" + checksum(strings.ToLower(database.String))[:48]
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lock, int64(r.config.LockTimeout/time.Second)).Scan(&acquired); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return &OperationError{Phase: "acquire lock", Cause: err}
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return ErrLocked
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		releaseErr := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", lock).Scan(&released)
		if releaseErr != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			if releaseErr == nil {
				releaseErr = fmt.Errorf("lock ownership was lost")
			}
			err = errors.Join(err, &OperationError{Phase: "release lock", Cause: releaseErr})
		}
	}()
	return run(session{conn, database.String})
}

func (r *Runner) load() ([]Migration, error) {
	migrations, err := Load(r.config.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return migrations, err
}

func (r *Runner) output(s snapshot, result *Result) error {
	if err := writeSnapshot(r.config.SchemaFile, s.SQL); err != nil {
		return errors.Join(ErrSnapshot, &OperationError{Phase: "write snapshot", Cause: err})
	}
	result.SnapshotUpdated = true
	return nil
}

// Dump writes the actual database structure, including when history is dirty.
// It never repairs history or treats the snapshot as proof of migration success.
func (r *Runner) Dump(ctx context.Context) (result Result, err error) {
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		snap, err := capture(ctx, s.conn, s.database)
		if err != nil {
			return &OperationError{Phase: "read snapshot", Cause: err}
		}
		if snap.managed {
			events, err := readEvents(ctx, s.conn)
			if err != nil {
				return err
			}
			state, err := replay(events)
			if err != nil {
				return err
			}
			result.Version, result.Dirty = state.version(), state.dirty != nil
		}
		return r.output(snap, &result)
	})
	return result, err
}

// Init captures an unmanaged existing database into a UTC millisecond timestamped
// initial migration and schema.sql. It does not modify database objects or
// register history. Review the SQL before Baseline. The initial version has
// an up section only and cannot be reversed.
func (r *Runner) Init(ctx context.Context) (result Result, err error) {
	err = r.session(ctx, func(s session) error {
		result.Database = s.database
		migrations, err := r.load()
		if err != nil {
			return err
		}
		if len(migrations) != 0 {
			return fmt.Errorf("migrate: init requires an empty migration directory")
		}
		snap, err := capture(ctx, s.conn, s.database)
		if err != nil {
			return &OperationError{Phase: "read initial schema", Cause: err}
		}
		if snap.managed {
			return fmt.Errorf("migrate: database already has migration history")
		}
		if snap.tables == 0 {
			return fmt.Errorf("migrate: empty database; create a migration with new")
		}
		if _, err := createMigration(r.config.Directory, "initial", "-- tidbgo:up\n"+snap.SQL, time.Now(), true); err != nil {
			return err
		}
		return r.output(snap, &result)
	})
	return result, err
}
