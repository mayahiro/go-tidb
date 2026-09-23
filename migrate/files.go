package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxSQLSize = 16 << 20

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const timestampLayout = "20060102150405.000"

// Migration is one trusted SQL file. Version is its complete filename without
// the .sql extension. Each direction may contain multiple statements. An absent
// Down makes the migration irreversible. SQL is preserved, including comments.
type Migration struct {
	Version string
	Up      string
	Down    string
}

// Load reads .sql files in filename order. Versions contain ASCII letters,
// digits, underscores, dots, and hyphens, starting with a letter or digit.
// Files use -- tidbgo:up and an optional -- tidbgo:down section. Load validates
// file structure and statement boundaries, not SQL grammar or executability.
// Symlinks, empty sections, and session/transaction control are rejected.
func Load(directory string) ([]Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("migrate: read migration directory: %w", err)
	}
	if len(entries) > 20000 {
		return nil, fmt.Errorf("migrate: too many migration files")
	}
	var result []Migration
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".sql")
		if !validVersion(version) {
			return nil, fmt.Errorf("migrate: invalid migration filename %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("migrate: migration files must be regular files")
		}
		data, err := readSQL(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > 128<<20 {
			return nil, fmt.Errorf("migrate: migration files exceed 128 MiB")
		}
		up, down, err := migrationParts(data)
		if err != nil {
			return nil, fmt.Errorf("migrate: %s: %w", entry.Name(), err)
		}
		result = append(result, Migration{Version: version, Up: up, Down: down})
	}
	// Removing .sql can change the order of prefix names such as a.sql/a-b.sql.
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result, nil
}

func readSQL(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("migrate: inspect SQL file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSQLSize {
		return "", fmt.Errorf("migrate: SQL must be a regular file no larger than 16 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) {
		return "", fmt.Errorf("migrate: SQL file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSQLSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSQLSize {
		return "", fmt.Errorf("migrate: SQL file exceeds 16 MiB")
	}
	return string(data), nil
}

// resolveLocation resolves existing parents while allowing paths that Init or
// Create has yet to create. Output-directory aliases must not target SQL input.
func resolveLocation(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		_, err = os.Lstat(path)
		if err == nil {
			path, err = filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = filepath.Dir(path)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		path = filepath.Join(path, missing[i])
	}
	return path, nil
}

func checksum(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Create creates an offline up/down SQL template named with a UTC millisecond
// timestamp and the supplied name. Existing files are never overwritten. Fill
// both sections before loading, or remove Down for an irreversible migration.
func Create(directory, name string) (Migration, error) {
	return createMigration(directory, name, "-- tidbgo:up\n-- Write the forward migration SQL here.\n\n-- tidbgo:down\n-- Write the reverse SQL here, or remove this section if irreversible.\n", time.Now(), false)
}

func createInitialMigration(directory, source string, now time.Time) error {
	_, err := createMigration(directory, "initial", "-- tidbgo:up\n"+source, now, true)
	return err
}

func createMigration(directory, name, source string, now time.Time, empty bool) (Migration, error) {
	if !namePattern.MatchString(name) || len(name) > 128 {
		return Migration{}, fmt.Errorf("migrate: name must start with a lowercase letter and contain only lowercase letters, digits, and underscores (at most 128 bytes)")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return Migration{}, err
	}
	if empty {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return Migration{}, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".sql") {
				return Migration{}, fmt.Errorf("migrate: init requires an empty migration directory")
			}
		}
	}
	version := strings.ReplaceAll(now.UTC().Format(timestampLayout), ".", "") + "_" + name
	m := Migration{Version: version}
	if err := createFile(filepath.Join(directory, filename(m)), source); err != nil {
		return Migration{}, err
	}
	return m, nil
}

func validVersion(version string) bool {
	return len(version) <= 251 && versionPattern.MatchString(version)
}

func filename(m Migration) string { return m.Version + ".sql" }

func createFile(path, source string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = io.WriteString(f, source)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func writeSnapshot(path, source string) (err error) {
	mode := os.FileMode(0644)
	info, statErr := os.Lstat(path)
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("migrate: snapshot output must be a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tidbgo-schema-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = io.WriteString(f, source); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
