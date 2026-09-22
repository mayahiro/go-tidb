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
	"strconv"
	"strings"
	"time"
)

const maxSQLSize = 16 << 20

var filePattern = regexp.MustCompile(`^([0-9]{17})_([a-z][a-z0-9_]*)\.sql$`)
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const timestampLayout = "20060102150405.000"

// Migration is one immutable-by-convention version loaded from a trusted SQL file.
// Version is a UTC timestamp encoded as YYYYMMDDHHMMSSmmm (milliseconds).
// Up and Down partition the original file bytes, including directives and
// comments, so their checksums cover the entire file without normalization.
// An absent Down explicitly makes the version irreversible.
type Migration struct {
	Version int64
	Name    string
	Up      string
	Down    string
}

// Load reads YYYYMMDDHHMMSSmmm_name.sql files with a -- tidbgo:up section
// followed by an optional -- tidbgo:down section. Directives occupy their own
// lines, outside SQL quotes and block comments. Up SQL must end with a semicolon
// before down. It rejects invalid dates, duplicate versions, symlinks, empty
// sections, and session/transaction control statements. It performs
// no database I/O and does not claim full SQL grammar validation.
func Load(directory string) ([]Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("migrate: read migration directory: %w", err)
	}
	if len(entries) > 20000 {
		return nil, fmt.Errorf("migrate: too many migration files")
	}
	versions := make(map[int64]bool)
	var result []Migration
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := filePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migrate: invalid migration filename %q", entry.Name())
		}
		if len(match[2]) > 128 {
			return nil, fmt.Errorf("migrate: migration names cannot exceed 128 bytes")
		}
		version, err := parseVersion(match[1])
		if err != nil {
			return nil, fmt.Errorf("migrate: invalid version in %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("migrate: migration files must be regular files")
		}
		if versions[version] {
			return nil, fmt.Errorf("migrate: duplicate version %d", version)
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
		versions[version] = true
		result = append(result, Migration{Version: version, Name: match[2], Up: up, Down: down})
	}
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

// Create creates one SQL template with up/down sections, using the current UTC
// timestamp to millisecond precision. Fill both sections before running Load,
// or remove the down section to declare an irreversible change. It rejects a
// timestamp at or before the latest local version, including same-millisecond
// collisions. Existing files are never overwritten.
func Create(directory, name string) (Migration, error) {
	return createMigration(directory, name, "-- tidbgo:up\n-- Write the forward migration SQL here.\n\n-- tidbgo:down\n-- Write the reverse SQL here, or remove this section if irreversible.\n", time.Now(), false)
}

func createMigration(directory, name, source string, now time.Time, empty bool) (Migration, error) {
	if !namePattern.MatchString(name) || len(name) > 128 {
		return Migration{}, fmt.Errorf("migrate: name must start with a lowercase letter and contain only lowercase letters, digits, and underscores (at most 128 bytes)")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return Migration{}, err
	}
	// Serialize local writers even when they choose different names for the same
	// millisecond. A leftover lock after a crash must be inspected and removed.
	lock := filepath.Join(directory, ".tidbgo-create.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return Migration{}, fmt.Errorf("migrate: acquire local creation lock: %w", err)
	}
	defer os.Remove(lock)
	migrations, err := Load(directory)
	if err != nil {
		return Migration{}, err
	}
	if empty && len(migrations) > 0 {
		return Migration{}, fmt.Errorf("migrate: init requires an empty migration directory")
	}
	version, err := parseVersion(strings.ReplaceAll(now.UTC().Format(timestampLayout), ".", ""))
	if err != nil {
		return Migration{}, err
	}
	if len(migrations) > 0 && version <= migrations[len(migrations)-1].Version {
		return Migration{}, fmt.Errorf("migrate: UTC timestamp %017d must be later than the latest local version; check the clock or retry after the current millisecond", version)
	}
	m := Migration{Version: version, Name: name}
	if err := createFile(filepath.Join(directory, filename(m)), source); err != nil {
		return Migration{}, err
	}
	return m, nil
}

func parseVersion(value string) (int64, error) {
	if len(value) != 17 {
		return 0, fmt.Errorf("migrate: version must be YYYYMMDDHHMMSSmmm in UTC")
	}
	timestamp, err := time.Parse(timestampLayout, value[:14]+"."+value[14:])
	if err != nil || timestamp.Year() < 1 {
		return 0, fmt.Errorf("migrate: invalid UTC timestamp version %q", value)
	}
	return strconv.ParseInt(value, 10, 64)
}

func filename(m Migration) string {
	return fmt.Sprintf("%017d_%s.sql", m.Version, m.Name)
}
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
