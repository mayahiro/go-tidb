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

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

func TestRuntimeCaptureRecordsTypedQueryWithoutBindValues(t *testing.T) {
	const secret = "private-runtime-value"
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  [][]driver.Value{{int64(1), "Ada"}, {int64(2), "Grace"}},
	}
	database := openAllTestDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	var parentEvent StatementEvent
	parent := WithStatementObserver(context.Background(), func(event StatementEvent) {
		parentEvent = event
	}, IncludeStatementArguments())
	ctx := WithRuntimeCapture(parent, capture)

	values, err := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("Name", secret)).
		Limit(20).
		All(ctx, database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("All() values = %#v, want two", values)
	}
	if got := parentEvent.Arguments; !reflect.DeepEqual(got, []any{secret, int64(20)}) {
		t.Fatalf("parent arguments = %#v, want original values", got)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("runtime artifact exposed bind value: %s", output.String())
	}

	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 {
		t.Fatalf("runtime records = %#v, want one", records)
	}
	record := records[0]
	if record.Source != runtimecapture.SourceTypedSelect || record.Terminal != "all" || record.Model != "scanModel" {
		t.Fatalf("runtime source = %#v", record)
	}
	if !strings.HasPrefix(record.Fingerprint, "q1:") || record.ScopeID != 1 || record.Sequence != 1 {
		t.Fatalf("runtime identity = %#v", record)
	}
	if record.ArgumentCount != 2 || !record.RowsReturnedKnown || record.RowsReturned != 2 || record.Query == nil {
		t.Fatalf("runtime result = %#v", record)
	}
	if !record.Query.Limit.Set || record.Query.Limit.Value != 0 {
		t.Fatalf("runtime limit = %#v, want presence without value", record.Query.Limit)
	}
	if got, want := record.Query.Projection, []string{"id", "name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime projection = %#v, want %#v", got, want)
	}
	if err := capture.Err(); err != nil {
		t.Fatalf("RuntimeCapture.Err() = %v", err)
	}
}

func TestRuntimeCaptureRecordsPreloadExecutionWithoutApplicationEstimate(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{
				columns: []string{"id", "email"},
				values:  [][]driver.Value{{int64(1), "ada@example.com"}, {int64(1), "duplicate@example.com"}, {int64(2), "grace@example.com"}},
			},
			{
				columns: []string{"user_id", "id", "name"},
				values:  [][]driver.Value{{int64(1), int64(10), "admin"}, {int64(2), int64(20), "reader"}},
			},
		},
	}
	database := openPreloadTestDB(t, state)
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)

	users, err := Query[preloadUser]().Select("ID", "Email").Preload("Roles").All(ctx, database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("All() users = %#v, want three", users)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 2 {
		t.Fatalf("runtime records = %#v, want root and preload", records)
	}
	if records[0].Query == nil || len(records[0].Query.Preloads) != 1 || records[0].Query.Preloads[0].Path != "Roles" {
		t.Fatalf("root query shape = %#v", records[0].Query)
	}
	preload := records[1]
	if preload.Source != runtimecapture.SourcePreload || preload.Relation != "Roles" || preload.Batch == nil {
		t.Fatalf("preload record = %#v", preload)
	}
	if preload.Batch.Count != 1 || preload.Batch.Index != 1 || preload.Batch.TotalRows != 2 || !preload.Batch.LoadAll {
		t.Fatalf("preload batch = %#v", preload.Batch)
	}
	if !preload.RowsReturnedKnown || preload.RowsReturned != 2 {
		t.Fatalf("preload returned rows = %#v", preload)
	}
}

func TestRuntimeCaptureRecordsAutomaticBulkSplitWithoutStatementCountCall(t *testing.T) {
	values := make([]bulkMutationModel, maxMutationParameters/2+1)
	for index := range values {
		values[index] = bulkMutationModel{ID: int64(index + 1), Value: int64(index + 10)}
	}
	executor := &bulkRecordingExecExecutor{}
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)

	affected, err := InsertMany(values).Exec(ctx, executor)
	if err != nil {
		t.Fatalf("InsertMany().Exec() error = %v", err)
	}
	if affected != int64(len(values)) {
		t.Fatalf("affected = %d, want %d", affected, len(values))
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 2 {
		t.Fatalf("runtime record count = %d, want two", len(records))
	}
	for index, record := range records {
		if record.Source != runtimecapture.SourceTypedMutation || record.Terminal != "insert_many" || record.Batch == nil {
			t.Fatalf("runtime bulk record %d = %#v", index, record)
		}
		if record.Batch.Index != index+1 || record.Batch.Count != 2 || record.Batch.TotalRows != len(values) {
			t.Fatalf("runtime bulk batch %d = %#v", index, record.Batch)
		}
		if record.Batch.Group != records[0].Batch.Group {
			t.Fatalf("runtime bulk groups = %d, %d", records[0].Batch.Group, record.Batch.Group)
		}
	}
	if records[0].Batch.Rows != maxMutationParameters/2 || records[1].Batch.Rows != 1 {
		t.Fatalf("runtime bulk rows = %d, %d", records[0].Batch.Rows, records[1].Batch.Rows)
	}
}

func TestRuntimeCaptureSeparatesRelationMutationModelAndPath(t *testing.T) {
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)

	affected, err := AddRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec(ctx, executor)
	if err != nil || affected != 1 {
		t.Fatalf("AddRelation().Exec() = %d, %v, want 1, nil", affected, err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 {
		t.Fatalf("runtime records = %#v, want one", records)
	}
	record := records[0]
	if record.Source != runtimecapture.SourceTypedMutation || record.Terminal != "relation_insert" || record.Model != "preloadUser" || record.Relation != "preloadUser.Roles" {
		t.Fatalf("relation mutation record = %#v", record)
	}
}

func TestRuntimeCaptureWriterFailureDoesNotReplaceExecutionResult(t *testing.T) {
	want := errors.New("capture write failed")
	capture := NewRuntimeCapture(runtimeCaptureFailingWriter{err: want})
	ctx := WithRuntimeCapture(context.Background(), capture)
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}

	affected, err := RawExec(ctx, executor, "UPDATE counters SET value = ?", int64(2))
	if err != nil || affected != 1 {
		t.Fatalf("RawExec() = %d, %v, want 1, nil", affected, err)
	}
	if err := capture.Err(); !errors.Is(err, want) {
		t.Fatalf("RuntimeCapture.Err() = %v, want %v", err, want)
	}
}

func TestDebugPreservesInheritedRuntimeCapture(t *testing.T) {
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}

	_, err := Debug(ctx, func(debugContext context.Context) error {
		_, execErr := RawExec(debugContext, executor, "DELETE FROM counters WHERE id = ?", int64(7))
		return execErr
	})
	if err != nil {
		t.Fatalf("Debug() error = %v", err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || records[0].Source != runtimecapture.SourceRaw || records[0].ScopeID != 1 {
		t.Fatalf("runtime records = %#v", records)
	}
}

func TestStatementObserverAppliedAfterRuntimeCapturePreservesBoth(t *testing.T) {
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	var observed []StatementEvent
	ctx = WithStatementObserver(ctx, func(event StatementEvent) {
		observed = append(observed, event)
	})
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}

	if _, err := RawExec(ctx, executor, "UPDATE counters SET value = ?", int64(2)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || len(observed) != 1 {
		t.Fatalf("runtime records = %d, observer events = %d, want one each", len(records), len(observed))
	}
}

func TestDerivedRuntimeCaptureReplacesInheritedCapture(t *testing.T) {
	var parentOutput bytes.Buffer
	var childOutput bytes.Buffer
	parentCapture := NewRuntimeCapture(&parentOutput)
	childCapture := NewRuntimeCapture(&childOutput)
	ctx := WithRuntimeCapture(context.Background(), parentCapture)
	ctx = WithRuntimeCapture(ctx, childCapture)
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}

	if _, err := RawExec(ctx, executor, "DELETE FROM counters WHERE id = ?", int64(7)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	if parentOutput.Len() != 0 {
		t.Fatalf("inherited capture output = %q, want empty", parentOutput.String())
	}
	if records := decodeRuntimeCaptureForTest(t, &childOutput); len(records) != 1 {
		t.Fatalf("derived capture records = %#v, want one", records)
	}
}

func TestRuntimeCaptureIsReusableAcrossConcurrentScopes(t *testing.T) {
	const scopeCount = 32
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	errors := make(chan error, scopeCount)
	var wait sync.WaitGroup
	for index := range scopeCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx := WithRuntimeCapture(context.Background(), capture)
			_, err := RawExec(ctx, executor, "UPDATE counters SET value = ?", index)
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("RawExec() error = %v", err)
		}
	}

	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != scopeCount {
		t.Fatalf("runtime records = %d, want %d", len(records), scopeCount)
	}
	scopes := make(map[uint64]struct{}, scopeCount)
	for _, record := range records {
		scopes[record.ScopeID] = struct{}{}
		if record.Sequence != 1 {
			t.Fatalf("scope %d sequence = %d, want 1", record.ScopeID, record.Sequence)
		}
	}
	if len(scopes) != scopeCount {
		t.Fatalf("runtime scopes = %d, want %d", len(scopes), scopeCount)
	}
	if err := capture.Err(); err != nil {
		t.Fatalf("RuntimeCapture.Err() = %v", err)
	}
}

func BenchmarkInsertExecWithRuntimeCapture(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	executor := mutationBenchmarkExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	capture := NewRuntimeCapture(discardRuntimeCaptureWriter{})
	ctx := WithRuntimeCapture(context.Background(), capture)
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkSelectQueryAll100RowsWithRuntimeCapture(b *testing.B) {
	rows := make([][]driver.Value, 100)
	for index := range rows {
		rows[index] = []driver.Value{int64(index + 1), "Ada"}
	}
	state := &allTestState{
		columns: []string{"id", "name"},
		values:  rows,
	}
	database := sql.OpenDB(&allTestConnector{state: state})
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Fatal(err)
		}
	})
	query := Query[scanModel]().Select("ID", "Name")
	capture := NewRuntimeCapture(discardRuntimeCaptureWriter{})
	ctx := WithRuntimeCapture(context.Background(), capture)
	var values []scanModel
	var err error

	b.ReportAllocs()
	for b.Loop() {
		values, err = query.All(ctx, database)
		if err != nil {
			b.Fatal(err)
		}
	}
	allQueryBenchmarkSink = values
}

func decodeRuntimeCaptureForTest(t testing.TB, output *bytes.Buffer) []runtimecapture.Record {
	t.Helper()
	records, err := runtimecapture.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("runtimecapture.Decode() error = %v\nartifact: %s", err, output.String())
	}
	return records
}

type runtimeCaptureFailingWriter struct {
	err error
}

func (writer runtimeCaptureFailingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type discardRuntimeCaptureWriter struct{}

func (discardRuntimeCaptureWriter) Write(value []byte) (int, error) {
	return len(value), nil
}
