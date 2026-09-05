package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"
)

type mutationPlanValue string

func (mutationPlanValue) Value() (driver.Value, error) {
	panic("Build must not call Value")
}

type mutationPlanPointer string

func (*mutationPlanPointer) Value() (driver.Value, error) {
	panic("Build must not call Value")
}

// MutationPlanEmbedded exercises exported embedded field traversal.
type MutationPlanEmbedded struct {
	Text    string
	Value   mutationPlanValue
	Address mutationPlanPointer
}

type mutationPlanRow struct {
	ID int64 `tidbgo:",pk"`
	*MutationPlanEmbedded
	Pointer   *mutationPlanPointer
	Deep      **mutationPlanPointer
	Bytes     []byte
	DeletedAt time.Time `tidbgo:",soft_delete"`
}

func TestMutationPlansPreserveCustomArgumentsAndNullableFields(t *testing.T) {
	t.Parallel()
	for _, embeddedNil := range []bool{false, true} {
		row := mutationPlanRow{ID: 1000}
		want := []any{int64(1000), nil, nil, nil, nil, nil, nil, nil}
		if !embeddedNil {
			row.MutationPlanEmbedded = &MutationPlanEmbedded{Text: "first", Value: "value", Address: "address"}
			row.Pointer = &row.Address
			row.Deep = &row.Pointer
			row.Bytes = []byte("bytes")
			want = []any{row.ID, row.Text, row.Value, &row.Address, row.Pointer, row.Deep, row.Bytes, nil}
		}
		for name, build := range map[string]func() (string, []any, error){
			"insert":          Insert(&row).Build,
			"upsert":          Upsert(&row).Build,
			"insert_values":   InsertMany([]mutationPlanRow{row}).Build,
			"insert_pointers": InsertMany([]*mutationPlanRow{&row}).Build,
			"upsert_values":   UpsertMany([]mutationPlanRow{row}).Build,
			"upsert_pointers": UpsertMany([]*mutationPlanRow{&row}).Build,
		} {
			_, got, err := build()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s embeddedNil=%t: arguments = %#v, error = %v, want %#v", name, embeddedNil, got, err, want)
			}
			for i := range got {
				if reflect.TypeOf(got[i]) != reflect.TypeOf(want[i]) {
					t.Fatalf("%s argument %d has type %T, want %T", name, i, got[i], want[i])
				}
			}
		}
		_, got, err := Update(&row, "Value", "Address", "Pointer", "Deep", "DeletedAt").Build()
		updateWant := []any{want[2], want[3], want[4], want[5], nil, row.ID}
		if err != nil || !reflect.DeepEqual(got, updateWant) {
			t.Fatalf("update embeddedNil=%t: arguments = %#v, error = %v, want %#v", embeddedNil, got, err, updateWant)
		}
	}
}

func TestMutationPlansReadCurrentValuesOnEveryBuild(t *testing.T) {
	t.Parallel()
	row := mutationPlanRow{ID: 1000, MutationPlanEmbedded: &MutationPlanEmbedded{Text: "first"}}
	query := Upsert(&row)
	firstSQL, first, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	row.Text = "second"
	row.Pointer = &row.Address
	row.DeletedAt = time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)
	secondSQL, second, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	if firstSQL != secondSQL || first[1] != "first" || first[4] != nil || first[7] != nil || second[1] != "second" || second[4] != row.Pointer || second[7] != row.DeletedAt {
		t.Fatalf("repeated Build results = (%s, %#v), (%s, %#v)", firstSQL, first, secondSQL, second)
	}
}

type retainedMutationCall struct {
	sql  string
	args []any
}

type retainingMutationExecutor struct {
	calls []retainedMutationCall
}

func (executor *retainingMutationExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	executor.calls = append(executor.calls, retainedMutationCall{sql: query, args: args})
	return mutationResult{rowsAffected: int64(len(args) / 2)}, nil
}

func TestBulkMutationRepeatedFullBatchesKeepIndependentArguments(t *testing.T) {
	const fullBatch = maxMutationParameters / 2
	values := make([]bulkMutationModel, 2*fullBatch+3)
	for i := range values {
		values[i] = bulkMutationModel{ID: int64(i + 1000), Value: int64(i + 2000)}
	}
	for _, upsert := range []bool{false, true} {
		executor := &retainingMutationExecutor{}
		var affected int64
		var err error
		if upsert {
			affected, err = UpsertMany(values).Exec(context.Background(), executor)
		} else {
			affected, err = InsertMany(values).Exec(context.Background(), executor)
		}
		if err != nil || affected != int64(len(values)) || len(executor.calls) != 3 {
			t.Fatalf("upsert=%t: affected=%d, statements=%d, error=%v", upsert, affected, len(executor.calls), err)
		}
		for batch, call := range executor.calls {
			start := batch * fullBatch
			end := min(start+fullBatch, len(values))
			if strings.Count(call.sql, "?") != 2*(end-start) || len(call.args) != 2*(end-start) {
				t.Fatalf("batch %d has the wrong placeholder or argument count", batch)
			}
			if strings.Contains(call.sql, " ON DUPLICATE KEY UPDATE ") != upsert {
				t.Fatalf("batch %d has the wrong operation", batch)
			}
			for i := start; i < end; i++ {
				if call.args[2*(i-start)] != values[i].ID || call.args[2*(i-start)+1] != values[i].Value {
					t.Fatalf("upsert=%t batch=%d row=%d: retained arguments changed", upsert, batch, i)
				}
			}
		}
	}
}
