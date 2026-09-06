package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

type updateManyExecFunc func(context.Context, string, ...any) (sql.Result, error)

func (execute updateManyExecFunc) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return execute(ctx, query, args...)
}

type bulkUpdateSoftRow struct {
	ID        int64 `tidbgo:",pk"`
	Value     string
	DeletedAt time.Time `tidbgo:",soft_delete"`
}

func TestUpdateManyBuild(t *testing.T) {
	t.Parallel()
	rows := []bulkMutationModel{{ID: 3, Value: 10}, {ID: 7, Value: 20}}
	wantSQL := "UPDATE `bulk_mutation_models` SET `value` = CASE `id` WHEN ? THEN ? WHEN ? THEN ? ELSE `value` END WHERE `id` IN (?, ?)"
	wantArgs := []any{int64(3), int64(10), int64(7), int64(20), int64(3), int64(7)}
	for _, query := range []interface {
		Build() (string, []any, error)
	}{UpdateMany(rows), UpdateMany([]*bulkMutationModel{&rows[0], &rows[1]}, "Value")} {
		statement, args, err := query.Build()
		if err != nil || statement != wantSQL || !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("Build = %s, %#v, %v", statement, args, err)
		}
		if strings.Count(statement, "?") != len(args) {
			t.Fatal("placeholder count does not match arguments")
		}
	}
	statement, args, err := UpdateMany(rows[:1], "Value").Build()
	single, singleArgs, singleErr := Update(&rows[0], "Value").Build()
	if err != nil || singleErr != nil || statement != single || !reflect.DeepEqual(args, singleArgs) {
		t.Fatalf("one-row UPDATE differs from Update: %s %#v %v", statement, args, err)
	}
}

func TestUpdateManyCompositePrimaryKey(t *testing.T) {
	t.Parallel()
	rows := []compositeMutationModel{{TenantID: 1, UserID: 2, Role: "a"}, {TenantID: 2, UserID: 1, Role: "b"}}
	statement, args, err := UpdateMany(rows, "Role").Build()
	wantSQL := "UPDATE `memberships` SET `role` = CASE WHEN `tenant_id` = ? AND `user_id` = ? THEN ? WHEN `tenant_id` = ? AND `user_id` = ? THEN ? ELSE `role` END WHERE (`tenant_id`, `user_id`) IN ((?, ?), (?, ?))"
	wantArgs := []any{int64(1), int64(2), "a", int64(2), int64(1), "b", int64(1), int64(2), int64(2), int64(1)}
	if err != nil || statement != wantSQL || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("Build = %s, %#v, %v", statement, args, err)
	}
}

func TestUpdateManyNullValuerAndGeneratedFields(t *testing.T) {
	t.Parallel()
	calls := 0
	name := "nickname"
	rows := []mutationModel{
		{ID: 3, Name: "a", Nickname: &name, Amount: mutationValue{calls: &calls, text: "1.25"}},
		{ID: 7, Name: "b", Amount: mutationValue{calls: &calls, text: "2.50"}},
	}
	before := append([]mutationModel(nil), rows...)
	statement, args, err := UpdateMany(rows).Build()
	wantArgs := []any{
		int64(3), "a", int64(7), "b",
		int64(3), &name, int64(7), nil,
		int64(3), rows[0].Amount, int64(7), rows[1].Amount,
		int64(3), int64(7),
	}
	if err != nil || !reflect.DeepEqual(args, wantArgs) || strings.Contains(statement, "`count`") || strings.Contains(statement, "SET `id`") || calls != 0 {
		t.Fatalf("Build = %s, %#v, %v, Valuer calls=%d", statement, args, err, calls)
	}
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1, lastIDErr: errors.New("LastInsertId must not be called")}}
	if affected, err := UpdateMany(rows).Exec(context.Background(), executor); err != nil || affected != 1 || calls != 0 {
		t.Fatalf("Exec affected=%d error=%v Valuer calls=%d", affected, err, calls)
	}
	if !reflect.DeepEqual(rows, before) {
		t.Fatal("bulk UPDATE changed input models")
	}
}

func TestUpdateManySoftDeleteScopeAndRestore(t *testing.T) {
	t.Parallel()
	rows := []bulkUpdateSoftRow{{ID: 1, Value: "a"}, {ID: 2, Value: "b"}}
	statement, _, err := UpdateMany(rows, "Value").Build()
	if err != nil || !strings.HasSuffix(statement, " AND `deleted_at` IS NULL") {
		t.Fatalf("active scope: %s, %v", statement, err)
	}
	statement, args, err := UpdateMany(rows, "DeletedAt").WithDeleted().Build()
	if err != nil || strings.Contains(statement, " IS NULL") || !reflect.DeepEqual(args, []any{int64(1), nil, int64(2), nil, int64(1), int64(2)}) {
		t.Fatalf("restore: %s, %#v, %v", statement, args, err)
	}
}

func TestUpdateManyRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	type nullableKey struct {
		ID    *int64 `tidbgo:",pk"`
		Value int64
	}
	one := &mutationModel{ID: 1}
	var nilQuery *UpdateManyQuery[mutationModel]
	for _, test := range []struct {
		name  string
		query interface {
			Build() (string, []any, error)
			Exec(context.Context, ExecExecutor) (int64, error)
		}
		want string
	}{
		{"nil query", nilQuery.WithDeleted(), "nil bulk UPDATE"},
		{"scalar", UpdateMany([]int{1}), "struct or pointer"},
		{"pointer depth", UpdateMany([]**mutationModel{&one}), "struct or pointer"},
		{"nil row", UpdateMany([]*mutationModel{one, nil}), "row 1"},
		{"no primary key", UpdateMany([]mutationWithoutPrimaryKey{{}}), "primary key"},
		{"only primary key", UpdateMany([]mutationOnlyAutoRandom{{ID: 1}}), "no writable"},
		{"unknown field", UpdateMany([]mutationModel{*one}, "Missing"), "not a mapped scalar"},
		{"primary key field", UpdateMany([]mutationModel{*one}, "ID"), "primary-key"},
		{"computed field", UpdateMany([]mutationModel{*one}, "Count"), "computed"},
		{"repeated field", UpdateMany([]mutationModel{*one}, "Name", "Name"), "repeats field"},
		{"nil key", UpdateMany([]nullableKey{{}}), "must not be nil"},
		{"duplicate key", UpdateMany([]mutationModel{*one, *one}), "row 1 repeats the primary key of row 0"},
		{"no deletion field", UpdateMany([]mutationModel{*one}).WithDeleted(), "soft-delete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := test.query.Build(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want %q", err, test.want)
			}
			executor := &recordingExecExecutor{}
			if _, err := test.query.Exec(context.Background(), executor); err == nil || !strings.Contains(err.Error(), test.want) || executor.calls != 0 {
				t.Fatalf("Exec error = %v, calls=%d, want %q", err, executor.calls, test.want)
			}
		})
	}
	if _, err := UpdateMany([]mutationModel{*one}).Exec(nil, &recordingExecExecutor{}); err == nil {
		t.Fatal("nil context accepted")
	}
	var executor *recordingExecExecutor
	if _, err := UpdateMany([]mutationModel{*one}).Exec(context.Background(), executor); err == nil {
		t.Fatal("typed nil executor accepted")
	}
}

func TestUpdateManyEmptyInput(t *testing.T) {
	t.Parallel()
	for _, query := range []interface {
		Build() (string, []any, error)
		Exec(context.Context, ExecExecutor) (int64, error)
	}{UpdateMany([]mutationModel(nil)), UpdateMany([]*mutationModel{})} {
		statement, args, err := query.Build()
		if err != nil || statement != "" || args != nil {
			t.Fatalf("empty Build = %s, %#v, %v", statement, args, err)
		}
		executor := &recordingExecExecutor{}
		if affected, err := query.Exec(context.Background(), executor); err != nil || affected != 0 || executor.calls != 0 {
			t.Fatalf("empty Exec: affected=%d error=%v calls=%d", affected, err, executor.calls)
		}
	}
}

func TestUpdateManyNativeKeyIdentity(t *testing.T) {
	t.Parallel()
	check := func(query interface{ Build() (string, []any, error) }, duplicate bool) {
		t.Helper()
		_, _, err := query.Build()
		if duplicate && (err == nil || !strings.Contains(err.Error(), "repeats the primary key")) || !duplicate && err != nil {
			t.Fatalf("duplicate=%t error=%v", duplicate, err)
		}
	}
	type stringKey struct {
		ID string `tidbgo:",pk"`
		V  int64
	}
	check(UpdateMany([]stringKey{{ID: "abc"}, {ID: "abc"}}), true)
	type bytesKey struct {
		ID []byte `tidbgo:",pk"`
		V  int64
	}
	check(UpdateMany([]bytesKey{{ID: []byte("abc")}, {ID: []byte("abc")}}), true)
	type uintKey struct {
		ID uint64 `tidbgo:",pk"`
		V  int64
	}
	check(UpdateMany([]uintKey{{ID: math.MaxUint64}, {ID: math.MaxInt64}}), false)
	check(UpdateMany([]uintKey{{ID: math.MaxUint64}, {ID: math.MaxUint64}}), true)
	type floatKey struct {
		ID float64 `tidbgo:",pk"`
		V  int64
	}
	check(UpdateMany([]floatKey{{ID: 0}, {ID: math.Copysign(0, -1)}}), true)
	type timeKey struct {
		ID time.Time `tidbgo:",pk"`
		V  int64
	}
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	check(UpdateMany([]timeKey{{ID: now}, {ID: now}}), true)
	type compound struct {
		K0 string `tidbgo:",pk"`
		K1 string `tidbgo:",pk"`
		V  int64
	}
	check(UpdateMany([]compound{{K0: "a", K1: "bc"}, {K0: "ab", K1: "c"}}), false)
	check(UpdateMany([]compound{{K0: "a", K1: "bc"}, {K0: "a", K1: "bc"}}), true)
	type pointerKey struct {
		ID *int64 `tidbgo:",pk"`
		V  int64
	}
	a, b := int64(1), int64(1)
	check(UpdateMany([]pointerKey{{ID: &a}, {ID: &b}}), true)
	type customKey struct {
		ID mutationValue `tidbgo:",pk"`
		V  int64
	}
	calls := 0
	check(UpdateMany([]customKey{{ID: mutationValue{text: "a", calls: &calls}}, {ID: mutationValue{text: "b", calls: &calls}}}), false)
	if calls != 0 {
		t.Fatal("Build invoked a primary-key Valuer")
	}
}

func TestUpdateManySplitCaptureAndArgumentOwnership(t *testing.T) {
	const batchRows = maxMutationParameters / 3
	values := make([]bulkMutationModel, 2*batchRows+1)
	for i := range values {
		values[i] = bulkMutationModel{ID: int64(i + 1), Value: int64(i + 100)}
	}
	if _, _, err := UpdateMany(values).Build(); err == nil || !strings.Contains(err.Error(), "requires 3 statements") {
		t.Fatalf("oversized Build error = %v", err)
	}
	if _, args, err := UpdateMany(values[:batchRows]).Build(); err != nil || len(args) != maxMutationParameters {
		t.Fatalf("exact boundary arguments=%d error=%v", len(args), err)
	}
	var statements []string
	var arguments [][]any
	executor := updateManyExecFunc(func(_ context.Context, query string, args ...any) (sql.Result, error) {
		statements = append(statements, query)
		arguments = append(arguments, args) // Deliberately retain without copying.
		rows := len(args) / 3
		if len(args) == 2 {
			rows = 1
		}
		return driver.RowsAffected(rows), nil
	})
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	affected, err := UpdateMany(values).Exec(ctx, executor)
	if err != nil || affected != int64(len(values)) || len(statements) != 3 {
		t.Fatalf("affected=%d calls=%d error=%v", affected, len(statements), err)
	}
	if statements[0] != statements[1] || arguments[0][0] != int64(1) || arguments[1][0] != int64(batchRows+1) || arguments[2][1] != int64(len(values)) {
		t.Fatal("batch SQL or retained arguments were corrupted")
	}
	if statements[2] != "UPDATE `bulk_mutation_models` SET `value` = ? WHERE `id` = ?" {
		t.Fatalf("single-row remainder = %s", statements[2])
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	for index, record := range records {
		if err := record.Validate(); err != nil {
			t.Fatal(err)
		}
		if record.Source != runtimecapture.SourceTypedMutation || record.Terminal != "update_many" || record.Operation != "UPDATE" || record.Mutation != nil || record.Batch == nil || record.Batch.Index != index+1 || record.Batch.Count != 3 || record.Batch.TotalRows != len(values) || record.Batch.Group != records[0].Batch.Group {
			t.Fatalf("batch record = %#v", record)
		}
	}
	analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(output.Bytes()))
	if err != nil || len(records) != 3 || len(analysis.Diagnostics) != 0 {
		t.Fatal("automatic UPDATE batches produced unexpected capture diagnostics")
	}
}

func TestUpdateManyValidatesAllKeysBeforeAnyBatch(t *testing.T) {
	t.Parallel()
	values := make([]bulkMutationModel, maxMutationParameters/3+1)
	for i := range values {
		values[i].ID = int64(i + 1)
	}
	values[len(values)-1].ID = values[0].ID
	executor := &recordingExecExecutor{}
	if _, err := UpdateMany(values).Exec(context.Background(), executor); err == nil || !strings.Contains(err.Error(), "repeats the primary key") || executor.calls != 0 {
		t.Fatalf("late duplicate error=%v calls=%d", err, executor.calls)
	}
	pointers := make([]*bulkMutationModel, len(values))
	for i := range pointers {
		pointers[i] = &values[i]
	}
	pointers[len(pointers)-1] = nil
	if _, err := UpdateMany(pointers).Exec(context.Background(), executor); err == nil || !strings.Contains(err.Error(), "is nil") || executor.calls != 0 {
		t.Fatalf("late nil pointer error=%v calls=%d", err, executor.calls)
	}
}

func TestUpdateManyReadsCurrentValuesWithoutRetainingArguments(t *testing.T) {
	t.Parallel()
	values := []bulkMutationModel{{ID: 1, Value: 10}, {ID: 2, Value: 20}}
	query := UpdateMany(values)
	firstSQL, first, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	values[0].ID, values[0].Value = 3, 30
	secondSQL, second, err := query.Build()
	if err != nil || firstSQL != secondSQL || first[0] != int64(1) || first[1] != int64(10) || second[0] != int64(3) || second[1] != int64(30) {
		t.Fatalf("Build retained stale or shared scalar arguments: %#v %#v %v", first, second, err)
	}
	values[0].ID = values[1].ID
	if _, _, err := query.Build(); err == nil {
		t.Fatal("reused builder did not revalidate current primary keys")
	}
}

func TestUpdateManyPartialFailureAndObservation(t *testing.T) {
	t.Parallel()
	values := make([]bulkMutationModel, maxMutationParameters/3+1)
	for i := range values {
		values[i].ID = int64(i + 1)
	}
	failure := errors.New("deliberate update failure")
	for _, failureMode := range []string{"execute", "result", "nil result"} {
		calls := 0
		executor := updateManyExecFunc(func(context.Context, string, ...any) (sql.Result, error) {
			calls++
			if calls == 1 {
				return driver.RowsAffected(9), nil
			}
			switch failureMode {
			case "execute":
				return nil, failure
			case "result":
				return mutationResult{rowsErr: failure}, nil
			default:
				return nil, nil
			}
		})
		var events []StatementEvent
		ctx := WithStatementObserver(context.Background(), func(event StatementEvent) { events = append(events, event) })
		affected, err := UpdateMany(values).Exec(ctx, executor)
		if affected != 9 || err == nil || !strings.Contains(err.Error(), "batch 2/2 rows [21845:21846]") || calls != 2 {
			t.Fatalf("mode=%s affected=%d error=%v calls=%d", failureMode, affected, err, calls)
		}
		if failureMode != "nil result" && !errors.Is(err, failure) {
			t.Fatalf("wrapped error lost cause: %v", err)
		}
		if len(events) != 2 || events[0].RowsAffected != 9 || events[1].Error == nil || events[1].RowsAffectedKnown || events[1].Arguments != nil || events[1].Operation != StatementUpdate {
			t.Fatalf("mode=%s events=%#v", failureMode, events)
		}
	}
}

func TestUpdateManyRuntimeCaptureKeepsServerRUAndPrivacy(t *testing.T) {
	state := &serverRUObserverState{serverRU: `{"ru_consumption":1.25}`}
	database := openServerRUObserverDB(t, state)
	var artifact bytes.Buffer
	capture := NewRuntimeCapture(&artifact)
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
	for range 2 {
		if _, err := UpdateMany([]mutationModel{{ID: 1, Name: "private-update-value"}, {ID: 2, Name: "other"}}, "Name").Exec(ctx, database); err != nil {
			t.Fatal(err)
		}
	}
	records := decodeRuntimeCaptureForTest(t, &artifact)
	for _, record := range records {
		if record.ServerRU == nil || !record.ServerRU.Known || record.ServerRU.Value != 1.25 || record.Terminal != "update_many" || record.Batch == nil {
			t.Fatalf("bulk UPDATE observation = %#v", record)
		}
	}
	_, _, targets, probes, _ := state.snapshot()
	analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(artifact.Bytes()))
	if err != nil || targets != 2 || probes != 2 || strings.Contains(artifact.String(), "private-update-value") || len(analysis.Diagnostics) != 0 {
		t.Fatalf("targets=%d probes=%d or unexpected capture content", targets, probes)
	}
}

func TestUpdateManySQLCapacity(t *testing.T) {
	t.Parallel()
	single, _ := UpdateMany([]bulkMutationModel{{ID: 1}}).prepare()
	composite, _ := UpdateMany([]compositeMutationModel{{TenantID: 1, UserID: 2}}).prepare()
	soft, _ := UpdateMany([]bulkUpdateSoftRow{{ID: 1}}).prepare()
	for _, plan := range []bulkMutationPlan{single, composite, soft} {
		for _, rows := range []int{2, 5, 100} {
			statement := plan.renderUpdateMany(rows)
			if got := plan.updateManySQLCapacity(rows); got != len(statement) {
				t.Fatalf("%s rows=%d capacity=%d SQL bytes=%d", plan.descriptor.Name(), rows, got, len(statement))
			}
		}
	}
}
