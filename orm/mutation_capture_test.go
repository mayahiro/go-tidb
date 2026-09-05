package orm

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

type capturedMutationQuery interface {
	Build() (string, []any, error)
	Exec(context.Context, ExecExecutor) (int64, error)
}

func TestRuntimeCaptureConditionalMutationMetadata(t *testing.T) {
	const secret = "private-mutation-bind-value"
	for _, test := range []struct {
		name       string
		query      capturedMutationQuery
		terminal   string
		operation  string
		softDelete string
		emptyList  bool
	}{
		{"update", UpdateWhere[conditionalUpdateModel](Set("LockOwner", secret)).Where(
			Equal("ChannelID", int64(17)), Or(IsNull("LockUntil"), LessThanOrEqual("LockUntil", time.Unix(1000, 0)))), "update_where", "UPDATE", "", false},
		{"delete", DeleteWhere[conditionalUpdateModel](Equal("LockOwner", secret)), "delete_where", "DELETE", "", false},
		{"empty_in", DeleteWhere[conditionalUpdateModel](In("ChannelID", []int64{})), "delete_where", "DELETE", "", true},
		{"empty_not_in", DeleteWhere[conditionalUpdateModel](NotIn("ChannelID", []int64{})), "delete_where", "DELETE", "", true},
		{"soft_delete", DeleteWhere[softDeleteVideo](Equal("Title", secret)), "delete_where", "UPDATE", "deleted_at", false},
		{"soft_update", UpdateWhere[softDeleteVideo](Set("Title", secret)).Where(Equal("ChannelID", int64(17))), "update_where", "UPDATE", "deleted_at", false},
		{"with_deleted", UpdateWhere[softDeleteVideo](Set("Title", secret)).Where(Equal("ChannelID", int64(17))).WithDeleted(), "update_where", "UPDATE", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantSQL, wantArgs, err := test.query.Build()
			if err != nil {
				t.Fatal(err)
			}
			var artifact bytes.Buffer
			capture := NewRuntimeCapture(&artifact)
			var event StatementEvent
			ctx := WithStatementObserver(context.Background(), func(value StatementEvent) { event = value }, IncludeStatementArguments())
			ctx = WithRuntimeCapture(ctx, capture)
			executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
			rows, err := test.query.Exec(ctx, executor)
			if err != nil || rows != 2 || executor.calls != 1 {
				t.Fatalf("Exec() = %d, %v, calls=%d", rows, err, executor.calls)
			}
			if executor.query != wantSQL || !slices.EqualFunc(executor.arguments, wantArgs, reflect.DeepEqual) || !slices.EqualFunc(event.Arguments, wantArgs, reflect.DeepEqual) {
				t.Fatalf("capture changed SQL/arguments: %q %#v %#v", executor.query, executor.arguments, event.Arguments)
			}
			if strings.Contains(artifact.String(), secret) {
				t.Fatalf("artifact exposed arguments: %s", artifact.String())
			}
			records := decodeRuntimeCaptureForTest(t, &artifact)
			if len(records) != 1 || records[0].Mutation == nil {
				t.Fatalf("records = %#v", records)
			}
			record := records[0]
			if err := record.Validate(); err != nil {
				t.Fatal(err)
			}
			if record.Version != 1 || record.Source != runtimecapture.SourceTypedMutation || record.Terminal != test.terminal || record.Operation != test.operation || record.Query != nil || record.MetadataError != "" {
				t.Fatalf("conditional write identity = %#v", record)
			}
			if record.Fingerprint != runtimecapture.StatementFingerprint(test.operation, wantSQL) {
				t.Fatalf("conditional write changed statement fingerprint: %q", record.Fingerprint)
			}
			if record.Mutation.Model != record.Model || record.Mutation.Table == "" || record.Mutation.SoftDeleteColumn != test.softDelete || record.Mutation.Predicates[0].EmptyList != test.emptyList {
				t.Fatalf("mutation shape = %#v", record.Mutation)
			}
			if test.name == "update" {
				want := []queryshape.MutationPredicate{
					{Operator: queryshape.PredicateEqual, Column: "channel_id"},
					{Operator: queryshape.PredicateOr, Children: []queryshape.MutationPredicate{
						{Operator: queryshape.PredicateIsNull, Column: "lock_until"},
						{Operator: queryshape.PredicateLessThanOrEqual, Column: "lock_until"},
					}},
				}
				if !reflect.DeepEqual(record.Mutation.Predicates, want) {
					t.Fatalf("predicates = %#v, want %#v", record.Mutation.Predicates, want)
				}
			}
			if err := capture.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeCaptureConditionalMutationDoesNotInvokeValuer(t *testing.T) {
	calls := 0
	value := mutationValue{calls: &calls, text: "private-decimal-value"}
	var artifact bytes.Buffer
	capture := NewRuntimeCapture(&artifact)
	ctx := WithRuntimeCapture(context.Background(), capture)
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	if _, err := UpdateWhere[conditionalCustomUpdateModel](Set("Balance", value)).Where(Equal("Balance", value)).Exec(ctx, executor); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || strings.Contains(artifact.String(), value.text) {
		t.Fatalf("capture invoked or exposed custom value: calls=%d artifact=%s", calls, artifact.String())
	}
	if err := capture.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCaptureOmitsMutationShapeForOtherWrites(t *testing.T) {
	value := bulkMutationModel{ID: 1, Value: 2}
	for _, query := range []capturedMutationQuery{Insert(&value), Upsert(&value), Update(&value), Delete(&value), InsertMany([]bulkMutationModel{value})} {
		var artifact bytes.Buffer
		capture := NewRuntimeCapture(&artifact)
		ctx := WithRuntimeCapture(context.Background(), capture)
		executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
		if _, err := query.Exec(ctx, executor); err != nil {
			t.Fatal(err)
		}
		records := decodeRuntimeCaptureForTest(t, &artifact)
		if len(records) != 1 || records[0].Mutation != nil {
			t.Fatalf("unexpected mutation shape: %#v", records)
		}
	}
}

func TestConditionalMutationMetadataFailureDoesNotReplaceResult(t *testing.T) {
	var artifact bytes.Buffer
	capture := NewRuntimeCapture(&artifact)
	ctx := WithRuntimeCapture(context.Background(), capture)
	compiled := compiledMutation{modelName: "Missing", sql: "UPDATE rows SET value = ? WHERE id = ?"}
	observation := beginConditionalMutationObservation(ctx, StatementUpdate, compiled, nil, false, "update_where")
	observation.finish(1, true, nil)
	records := decodeRuntimeCaptureForTest(t, &artifact)
	if len(records) != 1 || records[0].Error != "" || !records[0].RowsAffectedKnown || records[0].RowsAffected != 1 || records[0].Mutation != nil || records[0].MetadataError == "" {
		t.Fatalf("metadata failure replaced result: %#v", records)
	}
	analysis, err := runtimecapture.AnalyzeReader(bytes.NewReader(artifact.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != "RUN001" {
		t.Fatalf("metadata failure was silent: %#v", analysis.Diagnostics)
	}
}

func TestRuntimeCaptureConditionalMutationKeepsServerRUCollection(t *testing.T) {
	state := &serverRUObserverState{serverRU: `{"ru_consumption":1.25}`}
	database := openServerRUObserverDB(t, state)
	var artifact bytes.Buffer
	capture := NewRuntimeCapture(&artifact)
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
	if _, err := UpdateWhere[bulkMutationModel](Set("Value", int64(3))).Where(Equal("ID", int64(1))).Exec(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteWhere[bulkMutationModel](Equal("ID", int64(2))).Exec(ctx, database); err != nil {
		t.Fatal(err)
	}
	records := decodeRuntimeCaptureForTest(t, &artifact)
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	for _, record := range records {
		if record.Mutation == nil || record.ServerRU == nil || !record.ServerRU.Known || record.ServerRU.Value != 1.25 {
			t.Fatalf("missing conditional shape or ServerRU: %#v", record)
		}
	}
	_, _, targetCount, ruCount, _ := state.snapshot()
	if targetCount != 2 || ruCount != 2 {
		t.Fatalf("unexpected DB work: targets=%d RU probes=%d", targetCount, ruCount)
	}
}
