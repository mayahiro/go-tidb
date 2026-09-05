package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/orm"
)

func TestApplicationAnalyzeRepeatedWritesFromRuntimeCapture(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		operation string
		bulk      string
		write     func(context.Context, orm.ExecExecutor, *starterapp.User) (int64, error)
	}{
		{"insert", "INSERT", "InsertMany", starterapp.InsertUser},
		{"upsert", "UPSERT", "UpsertMany", starterapp.UpsertUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only the job/request boundary is instrumented. Existing example
			// repository functions and their callers need no query registration.
			var artifact bytes.Buffer
			capture := orm.NewRuntimeCapture(&artifact)
			ctx := orm.WithRuntimeCapture(context.Background(), capture)
			executor := &captureWriteExecutor{}
			for range 2 {
				value := starterapp.User{Email: "private-bind-value@example.com"}
				if rows, err := test.write(ctx, executor, &value); err != nil || rows != 1 {
					t.Fatalf("write() = %d, %v", rows, err)
				}
				wantID := int64(0)
				if test.operation == "INSERT" {
					wantID = int64(executor.calls)
				}
				if value.ID != wantID {
					t.Fatalf("ID = %d, want %d", value.ID, wantID)
				}
			}
			if err := capture.Err(); err != nil {
				t.Fatal(err)
			}
			if executor.calls != 2 {
				t.Fatalf("capture added calls: %d", executor.calls)
			}
			if strings.Contains(artifact.String(), "private-bind-value") {
				t.Fatalf("artifact exposed bind values: %s", artifact.String())
			}

			result := runApplicationWithInput(t, artifact.Bytes(), "analyze")
			if result.Status() != cli.StatusSuccess || len(result.Stderr()) != 0 {
				t.Fatalf("analyze() status=%d, stderr=%q", result.Status(), result.Stderr())
			}
			stdout := string(result.Stdout())
			for _, want := range []string{
				"WARNING[RUN004] Repeated single-row write may be batchable",
				"One runtime scope attempted the same typed " + test.operation + " statement 2 times",
				"Captured write attempts: 2, reported errors: 0",
				"Captured statement ServerRU: total=unavailable, samples=0/2, collection_errors=0",
				"Review whether " + test.bulk,
				"runtime: captures=1 scopes=1 statements=2 fingerprints=1 batch_groups=0 split_batches=0",
				"summary: errors=0 warnings=1 info=0 suppressed=0",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want %q", stdout, want)
				}
			}
		})
	}
}

func TestApplicationAnalyzeRepeatedWritesJSONAndSuppression(t *testing.T) {
	t.Parallel()

	first := runtimeCommandRecord(1, "s1:write")
	first.Source, first.Operation, first.Terminal = runtimecapture.SourceTypedMutation, "UPSERT", "upsert"
	first.SQL = "INSERT INTO `users` (`email`) VALUES (?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)"
	first.ServerRU = &runtimecapture.ServerRU{Known: true, Value: 1.25, AuxiliaryStatements: 1}
	second := first
	second.Sequence, second.ServerRU = 2, nil
	input := runtimeCaptureInput(t, first, second)
	for _, suppress := range []bool{false, true} {
		args := []string{"analyze", "--json"}
		if suppress {
			args = append(args, "--suppress", "RUN004=intentional retry boundary")
		}
		result := runApplicationWithInput(t, input, args...)
		if result.Status() != cli.StatusSuccess || len(result.Stderr()) != 0 {
			t.Fatalf("analyze() status=%d, stderr=%q", result.Status(), result.Stderr())
		}
		var output runtimeAnalysisJSON
		if err := json.Unmarshal(result.Stdout(), &output); err != nil {
			t.Fatal(err)
		}
		if output.Statistics.Statements != 2 || len(output.ServerRUByFingerprint) != 1 || output.ServerRUByFingerprint[0].Samples != 1 || output.ServerRUByFingerprint[0].Total != 1.25 {
			t.Fatalf("analysis totals = %#v", output)
		}
		if suppress {
			if len(output.Diagnostics) != 0 || len(output.Suppressed) != 1 || output.Summary.Warnings != 0 || output.Summary.Suppressed != 1 {
				t.Fatalf("suppression = %#v", output)
			}
			if output.Suppressed[0].Reason != "intentional retry boundary" || output.Suppressed[0].Diagnostic.Code != "RUN004" {
				t.Fatalf("suppressed diagnostic = %#v", output.Suppressed[0])
			}
		} else {
			if len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != "RUN004" || len(output.Suppressed) != 0 || output.Summary.Warnings != 1 {
				t.Fatalf("diagnostics = %#v", output)
			}
		}
		if !strings.Contains(string(result.Stdout()), "Captured statement ServerRU: total=1.25, samples=1/2, collection_errors=0") {
			t.Fatalf("missing RU coverage: %s", result.Stdout())
		}
	}
}

func TestApplicationAnalyzeDoesNotCombineWriteScopesOrManyCalls(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		separate bool
		write    func(context.Context, orm.ExecExecutor, *starterapp.User) (int64, error)
	}{
		{"separate_scopes", true, starterapp.InsertUser},
		{"insert_many", false, func(ctx context.Context, executor orm.ExecExecutor, value *starterapp.User) (int64, error) {
			return orm.InsertMany([]*starterapp.User{value}).Exec(ctx, executor)
		}},
		{"upsert_many", false, func(ctx context.Context, executor orm.ExecExecutor, value *starterapp.User) (int64, error) {
			return starterapp.UpsertUsers(ctx, executor, []*starterapp.User{value})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var artifact bytes.Buffer
			capture := orm.NewRuntimeCapture(&artifact)
			ctx := orm.WithRuntimeCapture(context.Background(), capture)
			executor := &captureWriteExecutor{}
			for range 2 {
				if test.separate {
					ctx = orm.WithRuntimeCapture(context.Background(), capture)
				}
				if _, err := test.write(ctx, executor, &starterapp.User{Email: "test@example.com"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := capture.Err(); err != nil {
				t.Fatal(err)
			}
			result := runApplicationWithInput(t, artifact.Bytes(), "analyze", "--json")
			if result.Status() != cli.StatusSuccess {
				t.Fatalf("analyze() status=%d, stderr=%q", result.Status(), result.Stderr())
			}
			var output runtimeAnalysisJSON
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if len(output.Diagnostics) != 0 || output.Statistics.Statements != 2 || executor.calls != 2 {
				t.Fatalf("excluded writes: calls=%d analysis=%#v", executor.calls, output)
			}
		})
	}
}

type captureWriteExecutor struct {
	calls int
}

func (executor *captureWriteExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	executor.calls++
	return captureWriteResult(executor.calls), nil
}

type captureWriteResult int64

func (result captureWriteResult) LastInsertId() (int64, error) { return int64(result), nil }
func (captureWriteResult) RowsAffected() (int64, error)        { return 1, nil }
