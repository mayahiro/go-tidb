package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

type scanAllChannel struct {
	model.Meta `tidbgo:"table=scan_all_channels"`
	ID         int64  `tidbgo:",pk"`
	YouTubeID  string `tidbgo:"external_id"`
	Title      string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

type scanAllIdentity struct {
	YouTubeID string `tidbgo:"ignored_column"`
	Extra     string
	ID        int64 `tidbgo:"-"`
}

// ScanAllIdentityFields exercises exported, promoted destination fields.
type ScanAllIdentityFields struct {
	ID int64
}

type scanAllEmbeddedIdentity struct {
	*ScanAllIdentityFields
	YouTubeID string
}

type scanAllPrivateFields struct{ ID int64 }
type scanAllAmbiguousFields struct {
	ScanAllIdentityFields
	scanAllPrivateFields
}
type scanAllRecursivePointer *scanAllRecursivePointer
type scanAllNamedID int64
type scanAllNamedSlice []scanAllNamedID

func TestScanAllMapsSourceGoNamesAndIgnoresDestinationTags(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "external_id"},
		values:  [][]driver.Value{{int64(1), "channel-1"}, {int64(2), "channel-2"}},
	}
	db := openAllTestDB(t, state)
	q := Query[scanAllChannel]().Select("ID", "YouTubeID")
	original := []scanAllIdentity{{ID: 99, Extra: "keep old storage"}}
	values := original
	if err := q.ScanAll(context.Background(), db, &values); err != nil {
		t.Fatal(err)
	}
	want := []scanAllIdentity{{ID: 1, YouTubeID: "channel-1"}, {ID: 2, YouTubeID: "channel-2"}}
	if !reflect.DeepEqual(values, want) || original[0].ID != 99 || original[0].Extra != "keep old storage" {
		t.Fatalf("values = %#v; old storage = %#v", values, original)
	}
	if state.query != "SELECT `id`, `external_id` FROM `scan_all_channels` WHERE `deleted_at` IS NULL" {
		t.Fatalf("SQL = %s", state.query)
	}
	var pointers []*scanAllEmbeddedIdentity
	if err := q.ScanAll(context.Background(), db, &pointers); err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 2 || pointers[0].ID != 1 || pointers[1].ID != 2 || pointers[0].ScanAllIdentityFields == pointers[1].ScanAllIdentityFields {
		t.Fatalf("pointer/embedded values = %#v", pointers)
	}
}

func TestScanAllScalarConversionsAndNulls(t *testing.T) {
	stamp := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	id := int64(7)
	for _, tc := range []struct {
		name, field string
		input       []driver.Value
		destination any
		want        any
	}{
		{"integers", "ID", []driver.Value{int64(7), int64(8)}, new([]int64), []int64{7, 8}},
		{"strings", "YouTubeID", []driver.Value{[]byte("one"), "two"}, new([]string), []string{"one", "two"}},
		{"named", "ID", []driver.Value{int64(7)}, new(scanAllNamedSlice), scanAllNamedSlice{7}},
		{"nullable pointer", "ID", []driver.Value{nil, int64(7)}, new([]*int64), []*int64{nil, &id}},
		{"scanner", "ID", []driver.Value{nil, int64(7)}, new([]sql.NullInt64), []sql.NullInt64{{}, {Int64: 7, Valid: true}}},
		{"custom scanner", "YouTubeID", []driver.Value{"one", "two"}, new([]scanDecimal), []scanDecimal{{text: "one"}, {text: "two"}}},
		{"pointer scanner", "ID", []driver.Value{int64(7), nil}, new([]*sql.NullInt64), []*sql.NullInt64{{Int64: 7, Valid: true}, nil}},
		{"bytes", "YouTubeID", []driver.Value{[]byte("one"), nil}, new([][]byte), [][]byte{[]byte("one"), nil}},
		{"any", "ID", []driver.Value{int64(7), nil}, new([]any), []any{int64(7), nil}},
		{"soft delete", "DeletedAt", []driver.Value{nil, stamp}, new([]time.Time), []time.Time{{}, stamp}},
		{"soft delete pointer", "DeletedAt", []driver.Value{nil, stamp}, new([]*time.Time), []*time.Time{nil, &stamp}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &allTestState{columns: []string{"selected"}}
			for _, value := range tc.input {
				state.values = append(state.values, []driver.Value{value})
			}
			db := openAllTestDB(t, state)
			if err := Query[scanAllChannel]().Select(tc.field).WithDeleted().ScanAll(context.Background(), db, tc.destination); err != nil {
				t.Fatal(err)
			}
			if got := reflect.ValueOf(tc.destination).Elem().Interface(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("values = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestScanAllRejectsInvalidMappingBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		query       *SelectQuery[scanAllChannel]
		destination any
		message     string
	}{
		{"nil", Query[scanAllChannel]().Select("ID"), nil, "pointer to a slice"},
		{"nil pointer", Query[scanAllChannel]().Select("ID"), (*[]int64)(nil), "pointer to a slice"},
		{"slice value", Query[scanAllChannel]().Select("ID"), []int64{}, "pointer to a slice"},
		{"struct pointer", Query[scanAllChannel]().Select("ID"), new(scanAllIdentity), "pointer to a slice"},
		{"multiple scalar columns", Query[scanAllChannel](), new([]int64), "exactly one"},
		{"missing field", Query[scanAllChannel](), new([]scanAllIdentity), "Title"},
		{"SQL name", Query[scanAllChannel]().Select("YouTubeID"), new([]struct{ ExternalID string }), "Go field YouTubeID"},
		{"case mismatch", Query[scanAllChannel]().Select("ID"), new([]struct{ Id int64 }), "Go field ID"},
		{"unsupported element", Query[scanAllChannel]().Select("ID"), new([]map[string]any), "unsupported slice element"},
		{"unsupported field", Query[scanAllChannel]().Select("ID"), new([]struct{ ID chan int }), "unsupported scan type"},
		{"raw bytes", Query[scanAllChannel]().Select("ID"), new([]sql.RawBytes), "unsupported slice element"},
		{"scanner interface", Query[scanAllChannel]().Select("ID"), new([]sql.Scanner), "unsupported slice element"},
		{"recursive pointer", Query[scanAllChannel]().Select("ID"), new([]scanAllRecursivePointer), "recursive pointer"},
		{"ambiguous", Query[scanAllChannel]().Select("ID"), new([]scanAllAmbiguousFields), "unambiguous"},
		{"private path", Query[scanAllChannel]().Select("ID"), new([]struct{ *scanAllPrivateFields }), "unexported embedded path"},
		{"duplicate", Query[scanAllChannel]().Select("ID", "ID"), new([]int64), "repeats field"},
		{"unknown projection", Query[scanAllChannel]().Select("external_id"), new([]string), "external_id"},
		{"empty projection", Query[scanAllChannel]().Select(), new([]string), "at least one"},
		{"invalid index", Query[scanAllChannel]().Select("ID").ForceIndex("unsafe index"), new([]int64), "ForceIndex"},
		{"preload", Query[scanAllChannel]().Select("ID").Preload("Anything"), new([]int64), "does not support Preload"},
		{"nil query", nil, new([]int64), "nil SELECT query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &allTestState{}
			db := openAllTestDB(t, state)
			err := tc.query.ScanAll(context.Background(), db, tc.destination)
			if err == nil || !strings.Contains(err.Error(), tc.message) || state.query != "" {
				t.Fatalf("error = %v; SQL = %s", err, state.query)
			}
		})
	}
}

func TestScanAllCommitsOnlyAfterSuccessfulClose(t *testing.T) {
	failure := errors.New("driver failure")
	for _, tc := range []struct {
		name  string
		state *allTestState
	}{
		{"query", &allTestState{queryErr: failure}},
		{"iteration", &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}, nextErr: failure}},
		{"close", &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}, closeErr: failure}},
		{"scan and close", &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}, {"bad"}}, closeErr: failure}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAllTestDB(t, tc.state)
			original := []int64{99, 98, 97}
			values := original[:1]
			err := Query[scanAllChannel]().Select("ID").ScanAll(context.Background(), db, &values)
			if !errors.Is(err, failure) || !reflect.DeepEqual(values, []int64{99}) || !reflect.DeepEqual(original, []int64{99, 98, 97}) || &values[0] != &original[0] {
				t.Fatalf("error = %v; values = %v; original = %v", err, values, original)
			}
			if tc.name != "query" && tc.state.closeCalls != 1 {
				t.Fatalf("Close calls = %d", tc.state.closeCalls)
			}
		})
	}
	t.Run("empty replaces", func(t *testing.T) {
		db := openAllTestDB(t, &allTestState{columns: []string{"id"}})
		values := []int64{99}
		if err := Query[scanAllChannel]().Select("ID").ScanAll(context.Background(), db, &values); err != nil || values == nil || len(values) != 0 {
			t.Fatalf("values = %#v; error = %v", values, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		db := openAllTestDB(t, &allTestState{columns: []string{"id"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		values := []int64{99}
		if err := Query[scanAllChannel]().Select("ID").ScanAll(ctx, db, &values); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(values, []int64{99}) {
			t.Fatalf("values = %#v; error = %v", values, err)
		}
	})
}

func TestScanAllUsesDestinationScannerCapability(t *testing.T) {
	db := openAllTestDB(t, &allTestState{columns: []string{"value"}, values: [][]driver.Value{{"stored"}}})
	var values []string
	if err := Query[writeOnlyModel]().ScanAll(context.Background(), db, &values); err != nil || !reflect.DeepEqual(values, []string{"stored"}) {
		t.Fatalf("values = %#v; error = %v", values, err)
	}
}

func TestScanAllDefaultProjectionAndValueErrors(t *testing.T) {
	state := &allTestState{columns: []string{"id", "external_id", "title", "deleted_at"}, values: [][]driver.Value{{int64(1), "one", "first", nil}}}
	db := openAllTestDB(t, state)
	var values []struct {
		Title, YouTubeID string
		DeletedAt        time.Time
		ID               int64
	}
	q := Query[scanAllChannel]()
	if err := q.ScanAll(context.Background(), db, &values); err != nil {
		t.Fatal(err)
	}
	sqlText, _, err := q.Build()
	if err != nil || state.query != sqlText || len(values) != 1 || values[0].ID != 1 || values[0].Title != "first" || !values[0].DeletedAt.IsZero() {
		t.Fatalf("values = %#v; SQL = %s; error = %v", values, state.query, err)
	}
	for _, tc := range []struct {
		name        string
		input       driver.Value
		destination any
	}{
		{"overflow", int64(256), new([]uint8)},
		{"NULL integer", nil, new([]int64)},
		{"ordinary NULL time", nil, new([]time.Time)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &allTestState{columns: []string{"id"}, values: [][]driver.Value{{tc.input}}}
			db := openAllTestDB(t, state)
			var event StatementEvent
			var output bytes.Buffer
			ctx := WithStatementObserver(WithRuntimeCapture(context.Background(), NewRuntimeCapture(&output)), func(value StatementEvent) { event = value })
			err := Query[scanAllChannel]().Select("ID").ScanAll(ctx, db, tc.destination)
			if err == nil || event.Error == nil || !reflect.ValueOf(tc.destination).Elem().IsNil() || state.closeCalls != 1 {
				t.Fatalf("error = %v; event = %#v", err, event)
			}
			records := decodeRuntimeCaptureForTest(t, &output)
			if len(records) != 1 || records[0].Model != "scanAllChannel" || records[0].Terminal != "scan_all" || records[0].Error == "" {
				t.Fatalf("records = %#v", records)
			}
		})
	}
}

func TestScanAllOwnsByteSliceResults(t *testing.T) {
	input := []byte("original")
	db := openAllTestDB(t, &allTestState{columns: []string{"external_id"}, values: [][]driver.Value{{input}, {input}}})
	var values [][]byte
	if err := Query[scanAllChannel]().Select("YouTubeID").ScanAll(context.Background(), db, &values); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	values[0][0] = 'Y'
	if string(values[1]) != "original" || string(input) != "Xriginal" {
		t.Fatalf("result aliases another row or driver storage: %q", values)
	}
}

func TestScanAllValidatesExecutionBoundary(t *testing.T) {
	db := openAllTestDB(t, &allTestState{})
	var ids []int64
	var typedNil *sql.DB
	q := Query[scanAllChannel]().Select("ID")
	for _, err := range []error{
		q.ScanAll(nil, db, &ids),
		q.ScanAll(context.Background(), nil, &ids),
		q.ScanAll(context.Background(), typedNil, &ids),
		q.ScanAll(context.Background(), nilRowsExecutor{}, &ids),
		Query[int64]().ScanAll(context.Background(), db, &ids),
		Query[*scanAllChannel]().ScanAll(context.Background(), db, &ids),
	} {
		if err == nil || ids != nil {
			t.Fatalf("invalid execution: values=%v, error=%v", ids, err)
		}
	}
}

func TestScanAllKeepsSQLAndSourceDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *SelectQuery[scanAllChannel]
	}{
		{"default scope", Query[scanAllChannel]().Select("ID", "YouTubeID")},
		{"offset", Query[scanAllChannel]().Select("ID", "YouTubeID").Where(Equal("Title", "private-title")).ForceIndex("PRIMARY").OrderBy(Desc("ID")).Limit(20).Offset(100)},
		{"cursor with deleted", Query[scanAllChannel]().Select("ID", "YouTubeID").WithDeleted().OrderBy(Desc("ID")).SeekAfter(int64(50)).Limit(20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.q
			wantSQL, wantArgs, err := q.Build()
			if err != nil {
				t.Fatal(err)
			}
			state := &allTestState{columns: []string{"id", "external_id"}, values: [][]driver.Value{{int64(1), "one"}}}
			db := openAllTestDB(t, state)
			var output bytes.Buffer
			var event StatementEvent
			ctx := WithStatementObserver(WithRuntimeCapture(context.Background(), NewRuntimeCapture(&output)), func(value StatementEvent) { event = value })
			var values []scanAllIdentity
			if err := q.ScanAll(ctx, db, &values); err != nil {
				t.Fatal(err)
			}
			records := decodeRuntimeCaptureForTest(t, &output)
			if len(records) != 1 {
				t.Fatalf("records = %#v", records)
			}
			record := records[0]
			if record.Fingerprint != queryShapeForTest(t, q).Fingerprint() {
				t.Fatalf("destination changed query fingerprint: %s", record.Fingerprint)
			}
			if record.Source != runtimecapture.SourceTypedSelect || record.Terminal != "scan_all" || record.Model != "scanAllChannel" || record.Query == nil || record.Query.Table != "scan_all_channels" || !reflect.DeepEqual(record.Query.Projection, []string{"id", "external_id"}) || record.Query.ForceIndex != q.selection.forceIndex || record.RowsReturned != 1 || !record.RowsReturnedKnown {
				t.Fatalf("record = %#v", record)
			}
			if event.Error != nil || event.SQL != wantSQL || strings.Contains(output.String(), "private-title") {
				t.Fatalf("event = %#v; capture = %s", event, output.String())
			}
			if state.query != wantSQL || len(state.arguments) != len(wantArgs) {
				t.Fatalf("SQL = %s; arguments = %#v", state.query, state.arguments)
			}
			for i, want := range wantArgs {
				if !reflect.DeepEqual(state.arguments[i].Value, want) {
					t.Fatalf("argument %d = %#v, want %#v", i, state.arguments[i].Value, want)
				}
			}
			if sqlText, args, err := q.Build(); err != nil || sqlText != wantSQL || !reflect.DeepEqual(args, wantArgs) {
				t.Fatal("ScanAll mutated query")
			}
		})
	}
	for _, forceIndex := range []bool{false, true} {
		q := Query[relationTopNVideo]().Select("ID").Where(Has("VideoGenres", Equal("GenreID", int64(7)))).OrderBy(Desc("ID")).Limit(20)
		if forceIndex {
			q.ForceIndex("PRIMARY")
		}
		wantSQL, _, err := q.Build()
		if err != nil {
			t.Fatal(err)
		}
		state := &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}}
		db := openAllTestDB(t, state)
		var values []int64
		if err := q.ScanAll(context.Background(), db, &values); err != nil || state.query != wantSQL {
			t.Fatalf("Has SQL = %s, want %s; error = %v", state.query, wantSQL, err)
		}
	}
}

func TestScanAllConcurrentCachedPlan(t *testing.T) {
	q := Query[scanAllChannel]().Select("ID", "YouTubeID")
	var group sync.WaitGroup
	for range 8 {
		state := &allTestState{columns: []string{"id", "external_id"}, values: [][]driver.Value{{int64(1), "one"}}}
		db := openAllTestDB(t, state)
		group.Go(func() {
			for range 10 {
				var values []scanAllIdentity
				if err := q.ScanAll(context.Background(), db, &values); err != nil || len(values) != 1 || values[0].ID != 1 {
					t.Errorf("values = %#v; error = %v", values, err)
					return
				}
			}
		})
	}
	group.Wait()
}
