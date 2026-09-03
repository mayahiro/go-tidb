package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSelectQueryCountCountsPredicatesWithoutProjectionOrOrder(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(7)}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID"))

	count, err := query.Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 7 {
		t.Fatalf("Count() = %d, want 7", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `scan_model` WHERE `name` = ?"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	if state.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", state.closeCalls)
	}
}

func TestSelectQueryCountUsesRelationOnlyAssociationWhenCardinalityIsProven(t *testing.T) {
	t.Parallel()

	compiled, err := Query[relationTopNVideo]().
		Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		compileCount()
	if err != nil {
		t.Fatalf("compileCount() error = %v", err)
	}
	wantSQL := "SELECT COUNT(*) FROM `relation_topn_video_genres` WHERE `genre_id` = ?"
	if compiled.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", compiled.sql, wantSQL)
	}
	if got, want := compiled.arguments, []any{int64(7)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountUsesRelationOnlyJunctionWhenTargetKeyIsFixed(t *testing.T) {
	t.Parallel()

	compiled, err := Query[preloadUser]().
		Where(Has("Roles", Equal("ID", uint64(7)))).
		compileCount()
	if err != nil {
		t.Fatalf("compileCount() error = %v", err)
	}
	wantSQL := "SELECT COUNT(*) FROM `preload_user_roles` WHERE `role_id` = ?"
	if compiled.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", compiled.sql, wantSQL)
	}
	if got, want := compiled.arguments, []any{uint64(7)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRelationOnlyCountPreservesTargetSoftDeleteScope(t *testing.T) {
	t.Parallel()

	compiled, err := Query[relationTopNSoftVideo]().
		WithDeleted().
		Where(Has("Links", Equal("GenreID", int64(7)))).
		compileCount()
	if err != nil {
		t.Fatalf("compileCount() error = %v", err)
	}
	wantSQL := "SELECT COUNT(*) FROM `relation_topn_soft_links` WHERE `deleted_at` IS NULL AND `genre_id` = ?"
	if compiled.sql != wantSQL {
		t.Fatalf("SQL = %q, want %q", compiled.sql, wantSQL)
	}
	if got, want := compiled.arguments, []any{int64(7)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryRelationOnlyCountValidatesTargetPredicates(t *testing.T) {
	t.Parallel()

	compiled, err := Query[relationTopNVideo]().
		Where(Has("VideoGenres", Equal("GenreID", nil))).
		compileCount()
	if err == nil || !strings.Contains(err.Error(), "COUNT relation predicate relationTopNVideo.VideoGenres") || !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("compileCount() = %#v, %v, want COUNT relation predicate validation error", compiled, err)
	}
}

func TestSelectQueryRelationOnlyCountFallsBackWhenProofIsIncomplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query interface {
			compileCount() (compiledCount, error)
		}
		associationTable string
	}{
		{
			name: "pagination",
			query: Query[relationTopNVideo]().
				Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
				Limit(20),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "keyset",
			query: Query[relationTopNVideo]().
				Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
				OrderBy(Desc("ID")).
				SeekAfter(int64(100)),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "root predicate",
			query: Query[relationTopNVideo]().
				Where(Equal("Title", "video"), Has("VideoGenres", Equal("GenreID", int64(7)))),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "nested relation predicate",
			query: Query[relationTopNVideo]().
				Where(And(Has("VideoGenres", Equal("GenreID", int64(7))), Equal("Title", "video"))),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "multiple collection predicates",
			query: Query[relationTopNVideo]().
				Where(
					Has("VideoGenres", Equal("GenreID", int64(7))),
					Has("VideoGenres", Equal("GenreID", int64(8))),
				),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "incomplete candidate unique key",
			query: Query[relationTopNVideo]().
				Where(Has("VideoGenres")),
			associationTable: "relation_topn_video_genres",
		},
		{
			name: "undeclared candidate unique key",
			query: Query[relationTopNUnprovenVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))),
			associationTable: "relation_topn_unproven_links",
		},
		{
			name: "active root soft delete",
			query: Query[relationTopNSoftVideo]().
				Where(Has("Links", Equal("GenreID", int64(7)))),
			associationTable: "relation_topn_soft_links",
		},
		{
			name: "many-to-many predicate requires target join",
			query: Query[preloadUser]().
				Where(Has("Roles", Equal("Name", "admin"))),
			associationTable: "preload_user_roles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, err := tt.query.compileCount()
			if err != nil {
				t.Fatalf("compileCount() error = %v", err)
			}
			if !strings.Contains(compiled.sql, "EXISTS (") {
				t.Fatalf("SQL = %q, want root EXISTS fallback", compiled.sql)
			}
			if !strings.Contains(compiled.sql, "`"+tt.associationTable+"`") {
				t.Fatalf("SQL = %q, want association table %s", compiled.sql, tt.associationTable)
			}
		})
	}
}

func TestSelectQueryCountReturnsZeroForEmptySet(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(0)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[scanModel]().Limit(0).Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Count() = %d, want 0", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM (SELECT 1 FROM `scan_model` LIMIT ?) AS `tidbgo_count`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{int64(0)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountCountsPaginatedRowsWithoutMutatingBuilder(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(7)}},
	}
	database := openAllTestDB(t, state)
	query := Query[scanModel]().
		Select("ID").
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		Limit(10).
		Offset(3)

	count, err := query.Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 7 {
		t.Fatalf("Count() = %d, want 7", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM (SELECT 1 FROM `scan_model` WHERE `name` = ? LIMIT ? OFFSET ?) AS `tidbgo_count`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(10), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}

	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `id` FROM `scan_model` WHERE `name` = ? ORDER BY `id` DESC LIMIT ? OFFSET ?"; got != want {
		t.Fatalf("Build() SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{"Ada", int64(10), int64(3)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Build() arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountKeepsKeysetPredicateWithoutOrderClause(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(4)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[scanModel]().
		Where(Equal("Name", "Ada")).
		OrderBy(Desc("ID")).
		SeekAfter(uint64(10)).
		Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 4 {
		t.Fatalf("Count() = %d, want 4", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `scan_model` WHERE `name` = ? AND (`id` < ?)"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := namedValues(state.arguments), []any{"Ada", int64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryCountDoesNotRequireReadableProjection(t *testing.T) {
	state := &allTestState{
		columns: []string{"count"},
		values:  [][]driver.Value{{int64(1)}},
	}
	database := openAllTestDB(t, state)

	count, err := Query[writeOnlyModel]().Select("Value").Count(context.Background(), database)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Count() = %d, want 1", count)
	}
	if got, want := state.query, "SELECT COUNT(*) FROM `write_only_model`"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestSelectQueryCountReportsExecutionErrors(t *testing.T) {
	queryFailure := errors.New("query failure")
	iterationFailure := errors.New("iteration failure")
	closeFailure := errors.New("close failure")

	tests := []struct {
		name  string
		state *allTestState
		want  error
	}{
		{name: "query", state: &allTestState{queryErr: queryFailure}, want: queryFailure},
		{name: "no row", state: &allTestState{columns: []string{"count"}}, want: sql.ErrNoRows},
		{name: "iteration", state: &allTestState{columns: []string{"count"}, nextErr: iterationFailure}, want: iterationFailure},
		{name: "scan", state: &allTestState{columns: []string{"count"}, values: [][]driver.Value{{"invalid"}}}},
		{
			name: "close",
			state: &allTestState{
				columns:  []string{"count"},
				values:   [][]driver.Value{{int64(1)}},
				closeErr: closeFailure,
			},
			want: closeFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := openAllTestDB(t, tt.state)
			count, err := Query[scanModel]().Count(context.Background(), database)
			if err == nil {
				t.Fatalf("Count() = %d, error = nil", count)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Count() = %d, error = %v, want errors.Is(_, %v)", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
			if tt.name == "scan" && !strings.Contains(err.Error(), "scan scanModel count") {
				t.Fatalf("Count() error = %v, want scan context", err)
			}
		})
	}
}

func TestSelectQueryCountRejectsInvalidExecutionInputs(t *testing.T) {
	var nilQuery *SelectQuery[scanModel]
	var typedNilExecutor *sql.DB

	tests := []struct {
		name     string
		query    *SelectQuery[scanModel]
		context  context.Context
		executor QueryExecutor
		want     string
	}{
		{name: "nil context", query: Query[scanModel](), executor: nilRowsExecutor{}, want: "nil context"},
		{name: "nil executor", query: Query[scanModel](), context: context.Background(), want: "nil executor"},
		{name: "typed nil executor", query: Query[scanModel](), context: context.Background(), executor: typedNilExecutor, want: "nil executor"},
		{name: "nil query", query: nilQuery, context: context.Background(), executor: nilRowsExecutor{}, want: "nil SELECT query"},
		{name: "nil rows", query: Query[scanModel](), context: context.Background(), executor: nilRowsExecutor{}, want: "executor returned nil rows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := tt.query.Count(tt.context, tt.executor)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Count() = %d, error = %v, want substring %q", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
		})
	}
}

func TestSelectQueryCountRejectsInvalidPagination(t *testing.T) {
	tests := []struct {
		name  string
		query *SelectQuery[scanModel]
		want  string
	}{
		{name: "negative limit", query: Query[scanModel]().Limit(-1), want: "LIMIT must not be negative"},
		{name: "offset without limit", query: Query[scanModel]().Offset(1), want: "OFFSET requires LIMIT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := tt.query.Count(context.Background(), nilRowsExecutor{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Count() = %d, error = %v, want substring %q", count, err, tt.want)
			}
			if count != 0 {
				t.Fatalf("Count() = %d on error, want 0", count)
			}
		})
	}
}

func TestSelectQueryCountRejectsPointerModel(t *testing.T) {
	count, err := Query[*scanModel]().Count(context.Background(), nilRowsExecutor{})
	if err == nil || !strings.Contains(err.Error(), "non-pointer struct") {
		t.Fatalf("Count() = %d, error = %v, want non-pointer struct error", count, err)
	}
	if count != 0 {
		t.Fatalf("Count() = %d on error, want 0", count)
	}
}
