package orm

import (
	"reflect"
	"strings"
	"testing"
)

type compositeKeysetModel struct {
	TenantID uint64 `tidbgo:",pk"`
	ID       uint64 `tidbgo:",pk"`
	Sequence uint64
}

type noPrimaryKeyModel struct {
	ID uint64
}

type valuerKeysetModel struct {
	ID    uint64 `tidbgo:",pk"`
	Value observedValuer
}

func TestCompileSelectSeekAfterPreservesClauseAndArgumentOrder(t *testing.T) {
	t.Parallel()

	compiled, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID", "Name"},
		predicates: []predicate{
			{operator: predicateGreaterThanOrEqual, field: "ID", values: []any{uint64(10)}},
		},
		orderBy: []orderTerm{
			{field: "Name", direction: orderAscending},
			{field: "ID", direction: orderDescending},
		},
		seekAfter: []cursorValue{
			{field: "Name", value: "Ada"},
			{field: "ID", value: uint64(20)},
		},
		pagination: pagination{limit: 100, limitSet: true},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `id`, `name` FROM `scan_model` WHERE `id` >= ? AND (`name` > ? OR (`name` = ? AND (`id` < ?))) ORDER BY `name` ASC, `id` DESC LIMIT ?"
	if got := compiled.statement.sql; got != wantSQL {
		t.Fatalf("SQL = %q, want %q", got, wantSQL)
	}
	if got, want := compiled.arguments, []any{uint64(10), "Ada", "Ada", uint64(20), int64(100)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectSeekAfterPreservesZeroCursorValues(t *testing.T) {
	t.Parallel()

	compiled, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"ID"},
		orderBy: []orderTerm{
			{field: "Name", direction: orderAscending},
			{field: "ID", direction: orderAscending},
		},
		seekAfter: []cursorValue{
			{field: "Name", value: ""},
			{field: "ID", value: uint64(0)},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := compiled.arguments, []any{"", "", uint64(0)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectSeekAfterUsesTiDBNullOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		orderBy   []orderTerm
		cursor    []cursorValue
		wantSQL   string
		wantValue []any
	}{
		{
			name: "ascending after null",
			orderBy: []orderTerm{
				{field: "Nickname", direction: orderAscending},
				{field: "ID", direction: orderAscending},
			},
			cursor: []cursorValue{
				{field: "Nickname", null: true},
				{field: "ID", value: uint64(7)},
			},
			wantSQL:   "SELECT `id` FROM `scan_model` WHERE (`nickname` IS NOT NULL OR (`nickname` IS NULL AND (`id` > ?))) ORDER BY `nickname` ASC, `id` ASC",
			wantValue: []any{uint64(7)},
		},
		{
			name: "descending after value includes null",
			orderBy: []orderTerm{
				{field: "Nickname", direction: orderDescending},
				{field: "ID", direction: orderDescending},
			},
			cursor: []cursorValue{
				{field: "Nickname", value: "M"},
				{field: "ID", value: uint64(7)},
			},
			wantSQL:   "SELECT `id` FROM `scan_model` WHERE ((`nickname` < ? OR `nickname` IS NULL) OR (`nickname` = ? AND (`id` < ?))) ORDER BY `nickname` DESC, `id` DESC",
			wantValue: []any{"M", "M", uint64(7)},
		},
		{
			name: "descending after null remains in null group",
			orderBy: []orderTerm{
				{field: "Nickname", direction: orderDescending},
				{field: "ID", direction: orderDescending},
			},
			cursor: []cursorValue{
				{field: "Nickname", null: true},
				{field: "ID", value: uint64(7)},
			},
			wantSQL:   "SELECT `id` FROM `scan_model` WHERE (`nickname` IS NULL AND (`id` < ?)) ORDER BY `nickname` DESC, `id` DESC",
			wantValue: []any{uint64(7)},
		},
		{
			name: "descending null at final key has no following row",
			orderBy: []orderTerm{
				{field: "ID", direction: orderAscending},
				{field: "Nickname", direction: orderDescending},
			},
			cursor: []cursorValue{
				{field: "ID", value: uint64(7)},
				{field: "Nickname", null: true},
			},
			wantSQL:   "SELECT `id` FROM `scan_model` WHERE (`id` > ? OR (`id` = ? AND (FALSE))) ORDER BY `id` ASC, `nickname` DESC",
			wantValue: []any{uint64(7), uint64(7)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			compiled, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[scanModel](),
				projection: []string{"ID"},
				orderBy:    tt.orderBy,
				seekAfter:  tt.cursor,
			})
			if err != nil {
				t.Fatalf("compileSelect() error = %v", err)
			}
			if got := compiled.statement.sql; got != tt.wantSQL {
				t.Fatalf("SQL = %q, want %q", got, tt.wantSQL)
			}
			if got := compiled.arguments; !reflect.DeepEqual(got, tt.wantValue) {
				t.Fatalf("arguments = %#v, want %#v", got, tt.wantValue)
			}
		})
	}
}

func TestCompileSelectSeekAfterAcceptsCompleteCompositePrimaryKey(t *testing.T) {
	t.Parallel()

	compiled, err := compileSelect(&selectQuery{
		modelType: reflect.TypeFor[compositeKeysetModel](),
		orderBy: []orderTerm{
			{field: "Sequence", direction: orderDescending},
			{field: "ID", direction: orderAscending},
			{field: "TenantID", direction: orderAscending},
		},
		seekAfter: []cursorValue{
			{field: "Sequence", value: uint64(30)},
			{field: "ID", value: uint64(20)},
			{field: "TenantID", value: uint64(10)},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	wantSQL := "SELECT `tenant_id`, `id`, `sequence` FROM `composite_keyset_model` WHERE (`sequence` < ? OR (`sequence` = ? AND (`id` > ? OR (`id` = ? AND (`tenant_id` > ?))))) ORDER BY `sequence` DESC, `id` ASC, `tenant_id` ASC"
	if got := compiled.statement.sql; got != wantSQL {
		t.Fatalf("SQL = %q, want %q", got, wantSQL)
	}
	if got, want := compiled.arguments, []any{uint64(30), uint64(30), uint64(20), uint64(20), uint64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectSeekAfterKeepsValuerWithoutExecutingIt(t *testing.T) {
	t.Parallel()

	calls := 0
	value := observedValuer{calls: &calls, text: "12.30"}
	compiled, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[valuerKeysetModel](),
		projection: []string{"ID"},
		orderBy: []orderTerm{
			{field: "Value", direction: orderAscending},
			{field: "ID", direction: orderAscending},
		},
		seekAfter: []cursorValue{
			{field: "Value", value: value},
			{field: "ID", value: uint64(9)},
		},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if got, want := compiled.arguments, []any{value, value, uint64(9)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestCompileSelectRejectsInvalidSeekAfter(t *testing.T) {
	t.Parallel()

	typedNil := (*string)(nil)
	tests := []struct {
		name       string
		modelType  reflect.Type
		orderBy    []orderTerm
		seekAfter  []cursorValue
		pagination pagination
		want       string
	}{
		{
			name:      "empty cursor",
			orderBy:   []orderTerm{{field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{},
			want:      "at least one cursor value",
		},
		{
			name:      "missing order",
			seekAfter: []cursorValue{{field: "ID", value: uint64(1)}},
			want:      "requires ORDER BY",
		},
		{
			name:      "count mismatch",
			orderBy:   []orderTerm{{field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "ID", value: uint64(1)}, {field: "Name", value: "A"}},
			want:      "2 cursor values for 1 ORDER BY fields",
		},
		{
			name:    "field order mismatch",
			orderBy: []orderTerm{{field: "Name", direction: orderAscending}, {field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{
				{field: "ID", value: uint64(1)},
				{field: "Name", value: "A"},
			},
			want: "cursor field 1",
		},
		{
			name:      "unknown field",
			orderBy:   []orderTerm{{field: "Missing", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "Missing", value: uint64(1)}},
			want:      "not a mapped scalar field",
		},
		{
			name:      "ignored field",
			orderBy:   []orderTerm{{field: "Ignored", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "Ignored", value: "x"}},
			want:      "not a mapped scalar field",
		},
		{
			name:    "duplicate order field",
			orderBy: []orderTerm{{field: "ID", direction: orderAscending}, {field: "ID", direction: orderDescending}},
			seekAfter: []cursorValue{
				{field: "ID", value: uint64(2)},
				{field: "ID", value: uint64(1)},
			},
			want: "repeats field",
		},
		{
			name:      "unknown direction",
			orderBy:   []orderTerm{{field: "ID", direction: orderDirection(255)}},
			seekAfter: []cursorValue{{field: "ID", value: uint64(1)}},
			want:      "unknown direction",
		},
		{
			name:      "no declared primary key",
			modelType: reflect.TypeFor[noPrimaryKeyModel](),
			orderBy:   []orderTerm{{field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "ID", value: uint64(1)}},
			want:      "requires a declared primary key",
		},
		{
			name:      "incomplete composite primary key",
			modelType: reflect.TypeFor[compositeKeysetModel](),
			orderBy:   []orderTerm{{field: "TenantID", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "TenantID", value: uint64(1)}},
			want:      "must include primary-key field \"ID\"",
		},
		{
			name:    "field cannot be a database argument",
			orderBy: []orderTerm{{field: "Amount", direction: orderAscending}, {field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{
				{field: "Amount", value: scanDecimal{text: "1.00"}},
				{field: "ID", value: uint64(1)},
			},
			want: "cannot be used as a database argument",
		},
		{
			name:    "typed nil without null state",
			orderBy: []orderTerm{{field: "Nickname", direction: orderAscending}, {field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{
				{field: "Nickname", value: typedNil},
				{field: "ID", value: uint64(1)},
			},
			want: "must not be nil",
		},
		{
			name:    "null cursor contains value",
			orderBy: []orderTerm{{field: "Nickname", direction: orderAscending}, {field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{
				{field: "Nickname", value: "A", null: true},
				{field: "ID", value: uint64(1)},
			},
			want: "must not contain a value",
		},
		{
			name:    "null cursor on non-null field",
			orderBy: []orderTerm{{field: "Name", direction: orderAscending}, {field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{
				{field: "Name", null: true},
				{field: "ID", value: uint64(1)},
			},
			want: "cannot represent NULL",
		},
		{
			name:      "null cursor on primary key",
			orderBy:   []orderTerm{{field: "ID", direction: orderAscending}},
			seekAfter: []cursorValue{{field: "ID", null: true}},
			want:      "cannot represent NULL",
		},
		{
			name:       "offset combined with seek",
			orderBy:    []orderTerm{{field: "ID", direction: orderAscending}},
			seekAfter:  []cursorValue{{field: "ID", value: uint64(1)}},
			pagination: pagination{limit: 10, offset: 10, limitSet: true, offsetSet: true},
			want:       "cannot be combined with OFFSET",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			modelType := tt.modelType
			if modelType == nil {
				modelType = reflect.TypeFor[scanModel]()
			}
			_, err := compileSelect(&selectQuery{
				modelType:  modelType,
				projection: []string{"ID"},
				orderBy:    tt.orderBy,
				seekAfter:  tt.seekAfter,
				pagination: tt.pagination,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileSelect() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
