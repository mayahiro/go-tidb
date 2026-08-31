package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDebugCapturesOneMultiStatementOperation(t *testing.T) {
	t.Parallel()

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	report, err := Debug(context.Background(), func(ctx context.Context) error {
		if _, execErr := RawExec(ctx, executor, "UPDATE counters SET value = value + 1 WHERE id = ?", int64(7)); execErr != nil {
			return execErr
		}
		_, execErr := RawExec(ctx, executor, "DELETE FROM counters WHERE id = ?", int64(8))
		return execErr
	})
	if err != nil {
		t.Fatalf("Debug() error = %v", err)
	}
	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.calls)
	}
	if report.StartedAt.IsZero() || report.Duration < 0 || report.StatementDuration < 0 || report.Duration < report.StatementDuration {
		t.Fatalf("Debug() timing = %#v", report)
	}
	if len(report.Statements) != 2 {
		t.Fatalf("Debug() statements = %#v, want 2", report.Statements)
	}
	wantOperations := []StatementOperation{StatementUpdate, StatementDelete}
	for index, event := range report.Statements {
		if event.Operation != wantOperations[index] || event.ArgumentCount != 1 || event.Arguments != nil {
			t.Fatalf("Debug() statement %d = %#v", index, event)
		}
		if !event.RowsAffectedKnown || event.RowsAffected != 1 || event.Error != nil {
			t.Fatalf("Debug() statement result %d = %#v", index, event)
		}
	}
	if !strings.HasPrefix(report.Statements[0].SQL, "UPDATE ") || !strings.HasPrefix(report.Statements[1].SQL, "DELETE ") {
		t.Fatalf("Debug() SQL = %q, %q", report.Statements[0].SQL, report.Statements[1].SQL)
	}
}

func TestDebugReturnsCompletedReportWithCallbackError(t *testing.T) {
	t.Parallel()

	callbackFailure := errors.New("callback failure")
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	report, err := Debug(context.Background(), func(ctx context.Context) error {
		if _, execErr := RawExec(ctx, executor, "UPDATE counters SET value = ?", int64(2)); execErr != nil {
			return execErr
		}
		return callbackFailure
	})
	if err != callbackFailure {
		t.Fatalf("Debug() error = %v, want callback failure identity", err)
	}
	if len(report.Statements) != 1 || report.Statements[0].Error != nil {
		t.Fatalf("Debug() report = %#v", report)
	}
}

func TestDebugReturnsNonNilEmptyReport(t *testing.T) {
	t.Parallel()

	report, err := Debug(context.Background(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Debug() error = %v", err)
	}
	if report.Statements == nil || len(report.Statements) != 0 || report.StartedAt.IsZero() || report.Duration < 0 || report.StatementDuration != 0 || report.ServerRU != nil {
		t.Fatalf("Debug() report = %#v, want a non-nil empty report", report)
	}
}

func TestDebugStopsCapturingWhenCallbackReturns(t *testing.T) {
	t.Parallel()

	var debugContext context.Context
	report, err := Debug(context.Background(), func(ctx context.Context) error {
		debugContext = ctx
		return nil
	})
	if err != nil {
		t.Fatalf("Debug() error = %v", err)
	}
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	if _, err := RawExec(debugContext, executor, "UPDATE counters SET value = ?", int64(2)); err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	if len(report.Statements) != 0 {
		t.Fatalf("Debug() statements after callback = %#v, want none", report.Statements)
	}
}

func TestDebugComposesInheritedObserverWithIndependentArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		parentArguments       bool
		reportArguments       bool
		wantParentArguments   bool
		wantReportArguments   bool
		mutateParentArguments bool
	}{
		{name: "neither", wantParentArguments: false, wantReportArguments: false},
		{name: "parent only", parentArguments: true, wantParentArguments: true, wantReportArguments: false},
		{name: "report only", reportArguments: true, wantParentArguments: false, wantReportArguments: true},
		{name: "both", parentArguments: true, reportArguments: true, wantParentArguments: true, wantReportArguments: true, mutateParentArguments: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var parentEvent StatementEvent
			parentOptions := []StatementObserverOption(nil)
			if test.parentArguments {
				parentOptions = append(parentOptions, IncludeStatementArguments())
			}
			ctx := WithStatementObserver(context.Background(), func(event StatementEvent) {
				parentEvent = event
				if test.mutateParentArguments && len(event.Arguments) != 0 {
					event.Arguments[0] = "changed@example.test"
				}
			}, parentOptions...)
			reportOptions := []StatementObserverOption(nil)
			if test.reportArguments {
				reportOptions = append(reportOptions, IncludeStatementArguments())
			}
			executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
			report, err := Debug(ctx, func(debugContext context.Context) error {
				_, execErr := RawExec(debugContext, executor, "UPDATE users SET email = ? WHERE id = ?", "ada@example.test", int64(7))
				return execErr
			}, reportOptions...)
			if err != nil {
				t.Fatalf("Debug() error = %v", err)
			}
			if len(report.Statements) != 1 || parentEvent.SQL == "" {
				t.Fatalf("Debug() report = %#v, parent = %#v", report, parentEvent)
			}
			if got := parentEvent.Arguments != nil; got != test.wantParentArguments {
				t.Fatalf("parent arguments present = %v, want %v", got, test.wantParentArguments)
			}
			if got := report.Statements[0].Arguments != nil; got != test.wantReportArguments {
				t.Fatalf("report arguments present = %v, want %v", got, test.wantReportArguments)
			}
			if test.wantReportArguments {
				want := []any{"ada@example.test", int64(7)}
				if !reflect.DeepEqual(report.Statements[0].Arguments, want) {
					t.Fatalf("report arguments = %#v, want %#v", report.Statements[0].Arguments, want)
				}
			}
		})
	}
}

func TestDebugCapturesConcurrentCompletedStatements(t *testing.T) {
	t.Parallel()

	const count = 32
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	report, err := Debug(context.Background(), func(ctx context.Context) error {
		var group sync.WaitGroup
		errorsChannel := make(chan error, count)
		group.Add(count)
		for index := range count {
			go func() {
				defer group.Done()
				_, execErr := RawExec(ctx, executor, "UPDATE counters SET value = value + 1 WHERE id = ?", index)
				errorsChannel <- execErr
			}()
		}
		group.Wait()
		close(errorsChannel)
		for execErr := range errorsChannel {
			if execErr != nil {
				return execErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Debug() error = %v", err)
	}
	if len(report.Statements) != count {
		t.Fatalf("Debug() statement count = %d, want %d", len(report.Statements), count)
	}
	var wantDuration time.Duration
	for _, event := range report.Statements {
		wantDuration += event.Duration
	}
	if report.StatementDuration != wantDuration {
		t.Fatalf("Debug() statement duration = %s, want %s", report.StatementDuration, wantDuration)
	}
}

func TestDebugRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ctx      context.Context
		callback func(context.Context) error
		want     string
	}{
		{name: "nil context", callback: func(context.Context) error { return nil }, want: "nil context"},
		{name: "nil callback", ctx: context.Background(), want: "nil callback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Debug(test.ctx, test.callback)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Debug() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func BenchmarkDebugReportTwoStatements(b *testing.B) {
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	callback := func(ctx context.Context) error {
		if _, err := RawExec(ctx, executor, "UPDATE counters SET value = value + 1 WHERE id = ?", int64(7)); err != nil {
			return err
		}
		_, err := RawExec(ctx, executor, "UPDATE counters SET value = value + 1 WHERE id = ?", int64(8))
		return err
	}
	ctx := context.Background()
	var report DebugReport
	var err error

	b.ReportAllocs()
	for b.Loop() {
		report, err = Debug(ctx, callback)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = int64(len(report.Statements))
}
