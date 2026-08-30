package orm

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectQueryBuildsPublicScalarQueryOffline(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[scanModel]().
		Select("ID", "Name").
		Where(
			GreaterThanOrEqual("ID", uint64(10)),
			Or(Equal("Name", "Ada"), HasPrefix("Name", "Gr")),
			IsNotNull("Nickname"),
		).
		OrderBy(Asc("Name"), Desc("ID")).
		SeekAfter("Ada", uint64(20)).
		Limit(100).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id`, `name` FROM `scan_model` WHERE `id` >= ? AND (`name` = ? OR `name` LIKE ? ESCAPE '!') AND `nickname` IS NOT NULL AND (`name` > ? OR (`name` = ? AND (`id` < ?))) ORDER BY `name` ASC, `id` DESC LIMIT ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	wantArguments := []any{uint64(10), "Ada", "Gr%", "Ada", "Ada", uint64(20), int64(100)}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", arguments, wantArguments)
	}
}

func TestSelectQueryPublicPredicateConstructors(t *testing.T) {
	t.Parallel()

	type namedString string
	tests := []struct {
		name      string
		predicate Predicate
		wantSQL   string
		wantArgs  []any
	}{
		{name: "equal", predicate: Equal("ID", uint64(1)), wantSQL: "`id` = ?", wantArgs: []any{uint64(1)}},
		{name: "not equal", predicate: NotEqual("ID", uint64(1)), wantSQL: "`id` <> ?", wantArgs: []any{uint64(1)}},
		{name: "greater than", predicate: GreaterThan("ID", uint64(1)), wantSQL: "`id` > ?", wantArgs: []any{uint64(1)}},
		{name: "greater than or equal", predicate: GreaterThanOrEqual("ID", uint64(1)), wantSQL: "`id` >= ?", wantArgs: []any{uint64(1)}},
		{name: "less than", predicate: LessThan("ID", uint64(1)), wantSQL: "`id` < ?", wantArgs: []any{uint64(1)}},
		{name: "less than or equal", predicate: LessThanOrEqual("ID", uint64(1)), wantSQL: "`id` <= ?", wantArgs: []any{uint64(1)}},
		{name: "in", predicate: In("ID", []uint64{1, 2}), wantSQL: "`id` IN (?, ?)", wantArgs: []any{uint64(1), uint64(2)}},
		{name: "empty in", predicate: In("ID", []uint64{}), wantSQL: "FALSE"},
		{name: "not in", predicate: NotIn("ID", []uint64{1}), wantSQL: "`id` NOT IN (?)", wantArgs: []any{uint64(1)}},
		{name: "empty not in", predicate: NotIn("ID", []uint64{}), wantSQL: "TRUE"},
		{name: "is null", predicate: IsNull("Nickname"), wantSQL: "`nickname` IS NULL"},
		{name: "is not null", predicate: IsNotNull("Nickname"), wantSQL: "`nickname` IS NOT NULL"},
		{name: "between", predicate: Between("ID", uint64(1), uint64(2)), wantSQL: "`id` BETWEEN ? AND ?", wantArgs: []any{uint64(1), uint64(2)}},
		{name: "contains", predicate: Contains("Name", namedString("a%b")), wantSQL: "`name` LIKE ? ESCAPE '!'", wantArgs: []any{"%a!%b%"}},
		{name: "prefix", predicate: HasPrefix("Name", namedString("a")), wantSQL: "`name` LIKE ? ESCAPE '!'", wantArgs: []any{"a%"}},
		{name: "suffix", predicate: HasSuffix("Name", namedString("a")), wantSQL: "`name` LIKE ? ESCAPE '!'", wantArgs: []any{"%a"}},
		{name: "and", predicate: And(Equal("ID", uint64(1)), Equal("Name", "A")), wantSQL: "(`id` = ? AND `name` = ?)", wantArgs: []any{uint64(1), "A"}},
		{name: "or", predicate: Or(Equal("ID", uint64(1)), Equal("Name", "A")), wantSQL: "(`id` = ? OR `name` = ?)", wantArgs: []any{uint64(1), "A"}},
		{name: "not", predicate: Not(Equal("ID", uint64(1))), wantSQL: "NOT (`id` = ?)", wantArgs: []any{uint64(1)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sqlText, arguments, err := Query[scanModel]().Select("ID").Where(tt.predicate).Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			wantSQL := "SELECT `id` FROM `scan_model` WHERE " + tt.wantSQL
			if sqlText != wantSQL {
				t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
			}
			if !reflect.DeepEqual(arguments, tt.wantArgs) {
				t.Fatalf("arguments = %#v, want %#v", arguments, tt.wantArgs)
			}
		})
	}
}

func TestSelectQuerySeekAfterInfersSQLNullPositionally(t *testing.T) {
	t.Parallel()

	var nickname *string
	sqlText, arguments, err := Query[scanModel]().
		Select("ID").
		OrderBy(Asc("Nickname"), Desc("ID")).
		SeekAfter(nickname, uint64(7)).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id` FROM `scan_model` WHERE (`nickname` IS NOT NULL OR (`nickname` IS NULL AND (`id` < ?))) ORDER BY `nickname` ASC, `id` DESC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{uint64(7)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryBuildDoesNotExecuteValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	value := observedValuer{calls: &calls, text: "12.30"}
	_, arguments, err := Query[valuerKeysetModel]().
		Select("ID").
		Where(Equal("Value", value)).
		OrderBy(Asc("Value"), Asc("ID")).
		SeekAfter(value, uint64(1)).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if got, want := arguments, []any{value, value, value, uint64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRejectsInvalidPublicInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func() error
		want  string
	}{
		{
			name: "pointer model",
			build: func() error {
				_, _, err := Query[*scanModel]().Build()
				return err
			},
			want: "non-pointer struct",
		},
		{
			name: "nil query",
			build: func() error {
				var query *SelectQuery[scanModel]
				_, _, err := query.Build()
				return err
			},
			want: "nil SELECT query",
		},
		{
			name: "empty projection",
			build: func() error {
				_, _, err := Query[scanModel]().Select().Build()
				return err
			},
			want: "at least one",
		},
		{
			name: "zero predicate",
			build: func() error {
				_, _, err := Query[scanModel]().Select("ID").Where(Predicate{}).Build()
				return err
			},
			want: "unknown operator",
		},
		{
			name: "empty seek",
			build: func() error {
				_, _, err := Query[scanModel]().Select("ID").OrderBy(Asc("ID")).SeekAfter().Build()
				return err
			},
			want: "at least one cursor value",
		},
		{
			name: "seek count mismatch",
			build: func() error {
				_, _, err := Query[scanModel]().Select("ID").OrderBy(Asc("ID")).SeekAfter(uint64(1), uint64(2)).Build()
				return err
			},
			want: "2 cursor values for 1 ORDER BY fields",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.build(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
