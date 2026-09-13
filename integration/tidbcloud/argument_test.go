package tidbcloud

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

const argumentTimeLayout = "2006-01-02 15:04:05.000000"

// This application-selected representation deliberately supplies a wall-clock
// string through driver.Valuer rather than a native time.Time argument.
type argumentWallTime string

func (value argumentWallTime) Value() (driver.Value, error) {
	return string(value), nil
}

func (value *argumentWallTime) Scan(source any) error {
	switch source := source.(type) {
	case time.Time:
		*value = argumentWallTime(source.Format(argumentTimeLayout))
	case []byte:
		*value = argumentWallTime(source)
	case string:
		*value = argumentWallTime(source)
	default:
		return fmt.Errorf("unsupported wall-clock source %T", source)
	}
	return nil
}

type starterArgumentRow struct {
	model.Meta `tidbgo:"table=tidbgo_it_arguments"`
	ID         int64 `tidbgo:",pk"`
	Text       string
	DateTime   time.Time
	StampedAt  time.Time
	OptionalAt *time.Time
	WallTime   argumentWallTime
}

func TestTiDBCloudStarterArguments(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected argument tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	database := openTestDatabase(t, dsn)
	verifyConnectedTarget(t, ctx, database, dsn)
	const create = "CREATE TABLE tidbgo_it_arguments (id BIGINT PRIMARY KEY, text TEXT COLLATE utf8mb4_bin NOT NULL, date_time DATETIME(6) NOT NULL, stamped_at TIMESTAMP(6) NOT NULL, optional_at DATETIME(6) NULL, wall_time DATETIME(6) NOT NULL)"
	if _, err := database.ExecContext(ctx, create); err != nil {
		fatalDatabaseError(t, dsn, "create argument fixture; pre-existing tables are never removed", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := database.ExecContext(cleanup, "DROP TABLE tidbgo_it_arguments"); err != nil {
			t.Errorf("drop argument fixture: %s", redact.Error(err, dsn))
		}
	})
	jst, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{time.UTC, jst} {
		for _, interpolate := range []bool{false, true} {
			t.Run(fmt.Sprintf("loc_%s/interpolate_%t", loc, interpolate), func(t *testing.T) {
				config := parseTestDSN(t, dsn)
				config.Loc, config.ParseTime, config.InterpolateParams = loc, true, interpolate
				if err := config.Apply(mysql.TimeTruncate(0)); err != nil {
					t.Fatal(err)
				}
				currentDSN := config.FormatDSN()
				db := openTestDatabase(t, currentDSN)
				connection, err := db.Conn(ctx)
				if err != nil {
					fatalDatabaseError(t, currentDSN, "reserve argument test connection", err)
				}
				defer connection.Close()
				for _, session := range []struct {
					name, statement string
					loc             *time.Location
				}{
					{name: "UTC", statement: "SET time_zone = '+00:00'", loc: time.UTC},
					{name: "JST", statement: "SET time_zone = '+09:00'", loc: jst},
				} {
					for _, inputLoc := range []*time.Location{time.UTC, jst} {
						t.Run(fmt.Sprintf("session_%s/input_%s", session.name, inputLoc), func(t *testing.T) {
							// The same instant crosses a date boundary between UTC and JST.
							at := time.Date(2026, 9, 13, 0, 30, 0, 123456000, jst).In(inputLoc)
							testArgumentValues(t, ctx, connection, currentDSN, at, loc, session.loc, session.statement)
						})
					}
				}
			})
		}
	}
}

func testArgumentValues(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string, at time.Time, loc, sessionLoc *time.Location, sessionSQL string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "DELETE FROM tidbgo_it_arguments"); err != nil {
		fatalDatabaseError(t, dsn, "reset owned argument fixture", err)
	}
	const insert = "INSERT INTO tidbgo_it_arguments (id, text, date_time, stamped_at, optional_at, wall_time) VALUES (?, ?, ?, ?, ?, ?)"
	const update = "UPDATE tidbgo_it_arguments SET text = ?, date_time = ?, stamped_at = ?, optional_at = ?, wall_time = ? WHERE id = ?"
	for _, phase := range []string{"insert", "update"} {
		t.Run(phase, func(t *testing.T) {
			if _, err := conn.ExecContext(ctx, sessionSQL); err != nil {
				fatalDatabaseError(t, dsn, "set owned argument session timezone", err)
			}
			value := starterArgumentRow{
				ID: 1, Text: "O'Reilly\\path? 100%_\x00日本語", DateTime: at, StampedAt: at,
				OptionalAt: &at, WallTime: argumentWallTime(at.Format(argumentTimeLayout)),
			}
			if phase == "update" {
				value.Text = "updated 'quote'\\?\x00日本語"
				value.DateTime = at.Add(25 * time.Hour)
				value.StampedAt = value.DateTime
				value.WallTime = argumentWallTime(value.DateTime.Format(argumentTimeLayout))
				value.OptionalAt = nil
			}
			for _, id := range []int64{1, 2, 3} {
				value.ID = id
				query := insert
				args := []any{id, value.Text, value.DateTime, value.StampedAt, value.OptionalAt, value.WallTime}
				if phase == "update" {
					query = update
					args = []any{value.Text, value.DateTime, value.StampedAt, value.OptionalAt, value.WallTime, id}
				}
				var affected int64
				var err error
				switch id {
				case 1:
					if phase == "insert" {
						affected, err = orm.Insert(&value).Exec(ctx, conn)
					} else {
						affected, err = orm.Update(&value).Exec(ctx, conn)
					}
				case 2:
					var result sql.Result
					result, err = conn.ExecContext(ctx, query, args...)
					if err == nil {
						affected, err = result.RowsAffected()
					}
				case 3:
					affected, err = orm.RawExec(ctx, conn, query, args...)
				}
				if err != nil {
					fatalDatabaseError(t, dsn, "write argument fixture", err)
				}
				if affected != 1 {
					t.Fatalf("id %d: affected rows = %d, want 1", id, affected)
				}
			}
			// Adjacent microseconds must not match the exact inclusive range.
			for i, delta := range []time.Duration{-time.Microsecond, time.Microsecond} {
				boundary := value.DateTime.Add(delta)
				query := insert
				args := []any{int64(i + 4), value.Text, boundary, boundary, nil, value.WallTime}
				if phase == "update" {
					query = update
					args = []any{value.Text, boundary, boundary, nil, value.WallTime, int64(i + 4)}
				}
				if _, err := conn.ExecContext(ctx, query, args...); err != nil {
					fatalDatabaseError(t, dsn, "write adjacent argument fixture values", err)
				}
			}
			requireArgumentRows(t, ctx, conn, dsn, value, loc)
			requireArgumentRanges(t, ctx, conn, dsn, value)
			requireStoredArgumentTimes(t, ctx, conn, dsn, value, loc, sessionLoc)
		})
	}
}

func requireArgumentRows(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string, value starterArgumentRow, loc *time.Location) {
	t.Helper()
	const selectSQL = "SELECT id, text, date_time, stamped_at, optional_at, wall_time FROM tidbgo_it_arguments WHERE id <= ? ORDER BY id"
	rows, err := conn.QueryContext(ctx, selectSQL, int64(3))
	if err != nil {
		fatalDatabaseError(t, dsn, "read direct argument rows", err)
	}
	var direct []starterArgumentRow
	for rows.Next() {
		var row starterArgumentRow
		if err := rows.Scan(&row.ID, &row.Text, &row.DateTime, &row.StampedAt, &row.OptionalAt, &row.WallTime); err != nil {
			_ = rows.Close()
			fatalDatabaseError(t, dsn, "scan direct argument row", err)
		}
		direct = append(direct, row)
	}
	if err := rows.Close(); err != nil {
		fatalDatabaseError(t, dsn, "close direct argument rows", err)
	}
	if err := rows.Err(); err != nil {
		fatalDatabaseError(t, dsn, "iterate direct argument rows", err)
	}
	typed, err := orm.Query[starterArgumentRow]().Where(orm.LessThanOrEqual("ID", int64(3))).OrderBy(orm.Asc("ID")).All(ctx, conn)
	if err != nil {
		fatalDatabaseError(t, dsn, "read typed argument rows", err)
	}
	raw, err := orm.Raw[starterArgumentRow](selectSQL, int64(3)).All(ctx, conn)
	if err != nil {
		fatalDatabaseError(t, dsn, "read raw argument rows", err)
	}
	if len(direct) != 3 || !reflect.DeepEqual(typed, direct) || !reflect.DeepEqual(raw, direct) {
		t.Fatalf("typed, raw, and direct argument rows differ: typed=%#v raw=%#v direct=%#v", typed, raw, direct)
	}
	for i, row := range direct {
		want := value
		want.ID = int64(i + 1)
		if row.DateTime.Location().String() != loc.String() || row.StampedAt.Location().String() != loc.String() ||
			(row.OptionalAt != nil && row.OptionalAt.Location().String() != loc.String()) {
			t.Fatalf("row %d did not use driver location %s", row.ID, loc)
		}
		// ParseDSN may load a separate Location instance with the same name.
		// Compare instants independently of the location object's identity.
		row.DateTime, row.StampedAt = row.DateTime.UTC(), row.StampedAt.UTC()
		want.DateTime, want.StampedAt = value.DateTime.UTC(), value.StampedAt.UTC()
		if row.OptionalAt != nil {
			optional := row.OptionalAt.UTC()
			row.OptionalAt = &optional
		}
		if value.OptionalAt != nil {
			optional := value.OptionalAt.UTC()
			want.OptionalAt = &optional
		}
		if !reflect.DeepEqual(row, want) {
			t.Fatalf("row = %#v, want %#v with driver location %s", row, want, loc)
		}
	}
}

func requireArgumentRanges(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string, value starterArgumentRow) {
	t.Helper()
	for _, column := range []struct{ field, sql string }{
		{field: "DateTime", sql: "date_time"}, {field: "StampedAt", sql: "stamped_at"},
	} {
		statement := "SELECT id FROM tidbgo_it_arguments WHERE text = ? AND " + column.sql + " BETWEEN ? AND ? ORDER BY id"
		args := []any{value.Text, value.DateTime, value.DateTime}
		rows, err := conn.QueryContext(ctx, statement, args...)
		if err != nil {
			fatalDatabaseError(t, dsn, "execute direct datetime range", err)
		}
		var direct []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				fatalDatabaseError(t, dsn, "scan direct datetime range", err)
			}
			direct = append(direct, id)
		}
		if err := rows.Close(); err != nil {
			fatalDatabaseError(t, dsn, "close direct datetime range", err)
		}
		if err := rows.Err(); err != nil {
			fatalDatabaseError(t, dsn, "iterate direct datetime range", err)
		}
		typed, err := orm.Query[starterArgumentRow]().Select("ID").Where(
			orm.Equal("Text", value.Text), orm.Between(column.field, value.DateTime, value.DateTime),
		).OrderBy(orm.Asc("ID")).All(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "execute typed datetime range", err)
		}
		raw, err := orm.Raw[starterArgumentRow](statement, args...).All(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "execute raw datetime range", err)
		}
		want := []starterArgumentRow{{ID: 1}, {ID: 2}, {ID: 3}}
		if !reflect.DeepEqual(direct, []int64{1, 2, 3}) || !reflect.DeepEqual(typed, want) || !reflect.DeepEqual(raw, want) {
			t.Fatalf("%s range: direct=%v typed=%#v raw=%#v, want IDs 1, 2, 3", column.sql, direct, typed, raw)
		}
	}
}

func requireStoredArgumentTimes(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string, value starterArgumentRow, loc, sessionLoc *time.Location) {
	t.Helper()
	// Read TIMESTAMP in UTC and cast both columns to text. A round trip through
	// the same driver location alone can hide a mismatched session timezone.
	if _, err := conn.ExecContext(ctx, "SET time_zone = '+00:00'"); err != nil {
		fatalDatabaseError(t, dsn, "inspect stored timestamp in UTC", err)
	}
	wall := value.DateTime.In(loc)
	wantDateTime := wall.Format(argumentTimeLayout)
	wantTimestamp := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), sessionLoc).UTC().Format(argumentTimeLayout)
	for _, id := range []int64{1, 2, 3} {
		var dateTime, stampedAt, wallTime string
		var optional sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT CAST(date_time AS CHAR), CAST(stamped_at AS CHAR), CAST(optional_at AS CHAR), CAST(wall_time AS CHAR) FROM tidbgo_it_arguments WHERE id = ?", id).
			Scan(&dateTime, &stampedAt, &optional, &wallTime)
		if err != nil {
			fatalDatabaseError(t, dsn, "read stored argument representations", err)
		}
		if dateTime != wantDateTime || stampedAt != wantTimestamp || wallTime != string(value.WallTime) {
			t.Fatalf("id %d stored (datetime, timestamp, valuer) = (%q, %q, %q), want (%q, %q, %q)", id, dateTime, stampedAt, wallTime, wantDateTime, wantTimestamp, value.WallTime)
		}
		if optional.Valid != (value.OptionalAt != nil) || (optional.Valid && optional.String != value.OptionalAt.In(loc).Format(argumentTimeLayout)) {
			t.Fatalf("id %d nullable datetime = %#v, want %v in %s", id, optional, value.OptionalAt, loc)
		}
	}
}
