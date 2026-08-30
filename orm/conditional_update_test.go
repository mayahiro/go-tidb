package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type conditionalUpdateModel struct {
	model.Meta `tidbgo:"table=channel_leases"`
	ChannelID  int64 `tidbgo:",pk"`
	LockOwner  *string
	LockUntil  *time.Time
	RetryCount int64
	LastError  *string
	Label      string
	RowCount   int64 `tidbgo:"row_count,computed"`
}

type conditionalCustomUpdateModel struct {
	model.Meta `tidbgo:"table=account_balances"`
	ID         int64 `tidbgo:",pk"`
	Balance    mutationValue
}

type conditionalScannerOnlyValue struct{}

func (*conditionalScannerOnlyValue) Scan(any) error {
	return nil
}

type conditionalScannerOnlyUpdateModel struct {
	model.Meta `tidbgo:"table=read_only_values"`
	ID         int64 `tidbgo:",pk"`
	Value      conditionalScannerOnlyValue
}

func TestUpdateWhereBuildsAssignmentsAndScalarPredicates(t *testing.T) {
	t.Parallel()

	leaseUntil := time.Date(2026, time.August, 30, 10, 5, 0, 0, time.UTC)
	now := leaseUntil.Add(-time.Minute)
	var lastError *string
	sqlText, arguments, err := UpdateWhere[conditionalUpdateModel](
		Set("LockOwner", "worker-a"),
		Set("LockUntil", leaseUntil),
		Set("LastError", lastError),
		Increment("RetryCount", int64(1)),
	).Where(
		Equal("ChannelID", int64(7)),
		Or(IsNull("LockUntil"), LessThanOrEqual("LockUntil", now)),
	).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "UPDATE `channel_leases` SET `lock_owner` = ?, `lock_until` = ?, `last_error` = ?, `retry_count` = `retry_count` + ? WHERE `channel_id` = ? AND (`lock_until` IS NULL OR `lock_until` <= ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	wantArguments := []any{"worker-a", leaseUntil, lastError, int64(1), int64(7), now}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", arguments, wantArguments)
	}
}

func TestUpdateWhereBuildDoesNotInvokeCustomValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	delta := mutationValue{calls: &calls, text: "1.25"}
	sqlText, arguments, err := UpdateWhere[conditionalCustomUpdateModel](
		Increment("Balance", delta),
	).Where(Equal("ID", int64(9))).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("driver.Valuer calls = %d, want 0", calls)
	}
	if sqlText != "UPDATE `account_balances` SET `balance` = `balance` + ? WHERE `id` = ?" {
		t.Fatalf("SQL = %q", sqlText)
	}
	if len(arguments) != 2 || !reflect.DeepEqual(arguments[0], delta) || arguments[1] != int64(9) {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestUpdateWhereExecutesAndEmitsUpdateObservation(t *testing.T) {
	t.Parallel()

	var events []StatementEvent
	ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
		events = append(events, event)
	}, IncludeStatementArguments())
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
	affected, err := UpdateWhere[conditionalUpdateModel](
		Set("LockOwner", "worker-b"),
	).Where(In("ChannelID", []int64{7, 8})).Exec(ctx, executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || executor.calls != 1 {
		t.Fatalf("affected = %d, calls = %d", affected, executor.calls)
	}
	if executor.query != "UPDATE `channel_leases` SET `lock_owner` = ? WHERE `channel_id` IN (?, ?)" {
		t.Fatalf("executor SQL = %q", executor.query)
	}
	wantArguments := []any{"worker-b", int64(7), int64(8)}
	if !reflect.DeepEqual(executor.arguments, wantArguments) {
		t.Fatalf("executor arguments = %#v, want %#v", executor.arguments, wantArguments)
	}
	if len(events) != 1 || events[0].Operation != StatementUpdate || !events[0].RowsAffectedKnown || events[0].RowsAffected != 2 || !reflect.DeepEqual(events[0].Arguments, wantArguments) {
		t.Fatalf("events = %#v", events)
	}
}

func TestUpdateWhereRejectsUnsafeOrInvalidInput(t *testing.T) {
	t.Parallel()

	validPredicate := Equal("ChannelID", int64(1))
	tests := []struct {
		name  string
		build func() error
		want  string
	}{
		{name: "nil query", build: func() error {
			var query *UpdateWhereQuery[conditionalUpdateModel]
			_, _, err := query.Build()
			return err
		}, want: "nil conditional UPDATE"},
		{name: "missing assignment", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel]().Where(validPredicate).Build()
			return err
		}, want: "at least one assignment"},
		{name: "missing predicate", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("LockOwner", "worker")).Build()
			return err
		}, want: "at least one predicate"},
		{name: "duplicate assignment", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("LockOwner", "a"), Set("LockOwner", "b")).Where(validPredicate).Build()
			return err
		}, want: "repeats field"},
		{name: "unknown assignment field", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("Missing", 1)).Where(validPredicate).Build()
			return err
		}, want: "not a mapped scalar field"},
		{name: "primary-key assignment", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("ChannelID", int64(2))).Where(validPredicate).Build()
			return err
		}, want: "primary-key field"},
		{name: "computed assignment", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("RowCount", int64(2))).Where(validPredicate).Build()
			return err
		}, want: "computed"},
		{name: "scanner-only assignment", build: func() error {
			_, _, err := UpdateWhere[conditionalScannerOnlyUpdateModel](Set("Value", "write")).Where(Equal("ID", int64(1))).Build()
			return err
		}, want: "cannot be used as a database argument"},
		{name: "increment non-numeric field", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Increment("Label", 1)).Where(validPredicate).Build()
			return err
		}, want: "must be numeric"},
		{name: "nil increment", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Increment("RetryCount", nil)).Where(validPredicate).Build()
			return err
		}, want: "non-nil delta"},
		{name: "unknown assignment operator", build: func() error {
			invalid := Assignment{value: assignment{operator: 99, field: "LockOwner", value: "worker"}}
			_, _, err := UpdateWhere[conditionalUpdateModel](invalid).Where(validPredicate).Build()
			return err
		}, want: "unknown assignment operator"},
		{name: "relation predicate", build: func() error {
			_, _, err := UpdateWhere[conditionalUpdateModel](Set("LockOwner", "worker")).Where(Has("Missing")).Build()
			return err
		}, want: "does not support relation predicates"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.build()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestUpdateWhereRejectsStatementsAboveTiDBPlaceholderLimit(t *testing.T) {
	t.Parallel()

	channelIDs := make([]int64, maxMutationParameters)
	_, _, err := UpdateWhere[conditionalUpdateModel](
		Set("LockOwner", "worker"),
	).Where(In("ChannelID", channelIDs)).Build()
	if err == nil || !strings.Contains(err.Error(), "65536 placeholders") {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestUpdateWhereReportsExecutionAndResultErrors(t *testing.T) {
	t.Parallel()

	failure := errors.New("conditional update failure")
	query := UpdateWhere[conditionalUpdateModel](Set("LockOwner", "worker")).Where(Equal("ChannelID", int64(1)))
	if _, err := query.Exec(context.Background(), &recordingExecExecutor{err: failure}); !errors.Is(err, failure) {
		t.Fatalf("execution error = %v", err)
	}
	rowsFailure := errors.New("conditional update rows failure")
	if _, err := query.Exec(context.Background(), &recordingExecExecutor{result: mutationResult{rowsErr: rowsFailure}}); !errors.Is(err, rowsFailure) {
		t.Fatalf("rows error = %v", err)
	}
	if _, err := query.Exec(context.Background(), &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil result error = %v", err)
	}
}
