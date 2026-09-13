package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type argumentModel struct {
	model.Meta `tidbgo:"table=argument_rows"`
	ID         int64 `tidbgo:",pk"`
	Text       string
	At         time.Time
	OptionalAt *time.Time
	CustomAt   mutationValue
}

type argumentMutation interface {
	Build() (string, []any, error)
	Exec(context.Context, ExecExecutor) (int64, error)
}

// Use database/sql's real argument conversion, including pointer and Valuer
// handling, while recording what reaches the driver without a network.
type argumentTestConnector struct{ allTestConnector }

func (c *argumentTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &argumentTestConn{allTestConn: &allTestConn{state: c.state}}, nil
}

type argumentTestConn struct{ *allTestConn }

func (c *argumentTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.query = query
	c.state.arguments = append([]driver.NamedValue(nil), args...)
	return driver.RowsAffected(1), nil
}

func openArgumentTestDB(t *testing.T, state *allTestState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&argumentTestConnector{allTestConnector{state: state}})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return db
}

func TestMutationPreservesTemporalAndStringArguments(t *testing.T) {
	t.Parallel()
	jst := time.FixedZone("JST", 9*60*60)
	at := time.Date(2026, 9, 13, 0, 30, 0, 123456789, jst)
	for _, input := range []struct {
		name string
		at   time.Time
	}{
		{name: "JST", at: at},
		{name: "UTC_previous_day", at: at.UTC()},
		{name: "ordinary_zero_time", at: time.Time{}},
	} {
		for _, optional := range []*time.Time{nil, &input.at} {
			for _, operation := range []string{"insert", "update", "update_where", "raw_exec"} {
				name := input.name + "/" + operation + "/null"
				if optional != nil {
					name = input.name + "/" + operation + "/pointer"
				}
				t.Run(name, func(t *testing.T) {
					calls := 0
					value := argumentModel{
						ID: 7, Text: "O'Reilly\\path? 100%_\x00日本語", At: input.at,
						OptionalAt: optional,
						CustomAt:   mutationValue{calls: &calls, text: "2026-09-13 00:30:00.123456"},
					}
					var query argumentMutation
					wantSQL := "UPDATE `argument_rows` SET `text` = ?, `at` = ?, `optional_at` = ?, `custom_at` = ? WHERE `id` = ?"
					wantArgs := []any{value.Text, value.At, value.OptionalAt, value.CustomAt, value.ID}
					switch operation {
					case "insert", "raw_exec":
						query = Insert(&value)
						wantSQL = "INSERT INTO `argument_rows` (`id`, `text`, `at`, `optional_at`, `custom_at`) VALUES (?, ?, ?, ?, ?)"
						wantArgs = []any{value.ID, value.Text, value.At, value.OptionalAt, value.CustomAt}
					case "update":
						query = Update(&value)
					case "update_where":
						query = UpdateWhere[argumentModel](
							Set("Text", value.Text), Set("At", value.At),
							Set("OptionalAt", value.OptionalAt), Set("CustomAt", value.CustomAt),
						).Where(Equal("ID", value.ID))
					}
					sqlText, args, err := query.Build()
					if err != nil {
						t.Fatal(err)
					}
					// A nil pointer field is represented as nil by mutation builders.
					if optional == nil {
						if operation == "insert" || operation == "raw_exec" {
							wantArgs[3] = nil
						} else if operation == "update" {
							wantArgs[2] = nil
						}
					}
					if sqlText != wantSQL || !reflect.DeepEqual(args, wantArgs) || calls != 0 {
						t.Fatalf("Build = (%q, %#v), Valuer calls = %d; want (%q, %#v), no calls", sqlText, args, calls, wantSQL, wantArgs)
					}
					state := &allTestState{}
					db := openArgumentTestDB(t, state)
					var affected int64
					if operation == "raw_exec" {
						affected, err = RawExec(context.Background(), db, wantSQL, wantArgs...)
					} else {
						affected, err = query.Exec(context.Background(), db)
					}
					if err != nil || affected != 1 || calls != 1 {
						t.Fatalf("Exec = (%d, %v), Valuer calls = %d; want (1, nil), one call", affected, err, calls)
					}
					var optionalValue any
					if optional != nil {
						optionalValue = *optional
					}
					wantDriver := []any{value.Text, value.At, optionalValue, value.CustomAt.text, value.ID}
					if operation == "insert" || operation == "raw_exec" {
						wantDriver = []any{value.ID, value.Text, value.At, optionalValue, value.CustomAt.text}
					}
					requireDriverArguments(t, state, wantSQL, wantDriver)
				})
			}
		}
	}
}

func TestQueryPreservesTemporalAndStringArguments(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 13, 0, 0, 0, 123456789, time.FixedZone("JST", 9*60*60))
	lower, upper := at.Add(-time.Hour), at.Add(time.Hour)
	text := "O'Reilly\\path? 100%_\x00日本語"
	for _, raw := range []bool{false, true} {
		name := "typed"
		if raw {
			name = "raw"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			custom := mutationValue{calls: &calls, text: "2026-09-13 00:00:00.123456"}
			wantSQL := "SELECT `id` FROM `argument_rows` WHERE `text` = ? AND `at` BETWEEN ? AND ? AND `custom_at` = ? AND `optional_at` IS NULL"
			wantArgs := []any{text, lower, upper, custom}
			query := Query[argumentModel]().Select("ID").Where(
				Equal("Text", text), Between("At", lower, upper),
				Equal("CustomAt", custom), IsNull("OptionalAt"),
			)
			state := &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(7)}}}
			db := openArgumentTestDB(t, state)
			sqlText, args, err := query.Build()
			if raw {
				sqlText, args, err = Raw[argumentModel](wantSQL, wantArgs...).Build()
			}
			if err != nil || sqlText != wantSQL || !reflect.DeepEqual(args, wantArgs) || calls != 0 {
				t.Fatalf("Build = (%q, %#v, %v), Valuer calls = %d", sqlText, args, err, calls)
			}
			var values []argumentModel
			if raw {
				values, err = Raw[argumentModel](wantSQL, wantArgs...).All(context.Background(), db)
			} else {
				values, err = query.All(context.Background(), db)
			}
			if err != nil || len(values) != 1 || values[0].ID != 7 || calls != 1 {
				t.Fatalf("All = (%#v, %v), Valuer calls = %d", values, err, calls)
			}
			requireDriverArguments(t, state, wantSQL, []any{text, lower, upper, custom.text})
		})
	}
}

func requireDriverArguments(t *testing.T, state *allTestState, wantSQL string, want []any) {
	t.Helper()
	if state.query != wantSQL || len(state.arguments) != len(want) {
		t.Fatalf("driver received (%q, %#v), want (%q, %#v)", state.query, state.arguments, wantSQL, want)
	}
	for i, arg := range state.arguments {
		if arg.Name != "" || arg.Ordinal != i+1 || !reflect.DeepEqual(arg.Value, want[i]) {
			t.Fatalf("driver argument %d = %#v, want unnamed ordinal %d with value %#v", i, arg, i+1, want[i])
		}
		if at, ok := want[i].(time.Time); ok {
			if got, ok := arg.Value.(time.Time); !ok || got != at || got.Location() != at.Location() {
				t.Fatalf("time argument %d changed its type, value, precision, or location", i)
			}
		}
	}
}
