package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cli "github.com/mayahiro/nagicli-go"

	starterapp "github.com/mayahiro/go-tidb/examples/starter-app"
	"github.com/mayahiro/go-tidb/orm"
)

func TestApplicationAnalyzeRepeatedUpdatesFromRuntimeCapture(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		terminal string
		write    func(context.Context, orm.ExecExecutor) (int64, error)
	}{
		{"primary_key", "update", func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return starterapp.UpdateUserEmail(ctx, executor, &starterapp.User{ID: 1, Email: "private-email@example.com"})
		}},
		{"lease", "update_where", func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			return starterapp.ClaimJobLease(ctx, executor, 1, "private-owner", now, now.Add(time.Minute))
		}},
		{"increment", "update_where", func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return starterapp.FailJobLease(ctx, executor, 1, "private-owner", "private-message")
		}},
		{"restore", "update_where", func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return starterapp.RestoreVideo(ctx, executor, 1)
		}},
		{"multiple_targets", "update_where", func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return orm.UpdateWhere[starterapp.User](orm.Set("Email", "private-email@example.com")).
				Where(orm.In("ID", []int64{1, 2})).Exec(ctx, executor)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, collectRU := range []bool{false, true} {
				connector := &workloadTestConnector{}
				database := sql.OpenDB(connector)
				t.Cleanup(func() {
					if err := database.Close(); err != nil {
						t.Error(err)
					}
				})
				var artifact bytes.Buffer
				capture := orm.NewRuntimeCapture(&artifact)
				var options []orm.RuntimeCaptureOption
				if collectRU {
					options = append(options, orm.CollectServerRU())
				}
				ctx := orm.WithRuntimeCapture(context.Background(), capture, options...)
				for range 2 {
					if rows, err := test.write(ctx, database); err != nil || rows != 1 {
						t.Fatalf("write = %d, %v", rows, err)
					}
				}
				if err := capture.Err(); err != nil {
					t.Fatal(err)
				}
				probes := 0
				ruEvidence := "total=unavailable, samples=0/2, collection_errors=0"
				if collectRU {
					probes, ruEvidence = 2, "total=2.5, samples=2/2, collection_errors=0"
				}
				if connector.targets != 2 || connector.probes != probes || connector.begins != 0 || connector.commits != 0 {
					t.Fatalf("capture changed DB work: %#v", connector)
				}
				if strings.Contains(artifact.String(), "private-") {
					t.Fatalf("capture leaked bind values: %s", artifact.String())
				}
				for _, jsonOutput := range []bool{false, true} {
					args := []string{"analyze"}
					if jsonOutput {
						args = append(args, "--json")
					}
					result := runApplicationWithInput(t, artifact.Bytes(), args...)
					if result.Status() != cli.StatusSuccess || len(result.Stderr()) != 0 {
						t.Fatalf("analyze status=%d stderr=%s", result.Status(), result.Stderr())
					}
					for _, want := range []string{"Repeated UPDATE warrants application review", "terminal: " + test.terminal, "Captured write attempts: 2, reported errors: 0", "Captured statement ServerRU: " + ruEvidence, "repetition does not prove that the calls can be combined", "lease conditions", "intentional retries"} {
						if !strings.Contains(string(result.Stdout()), want) {
							t.Errorf("missing %q in %s", want, result.Stdout())
						}
					}
					if !jsonOutput {
						continue
					}
					var output runtimeAnalysisJSON
					if err := json.Unmarshal(result.Stdout(), &output); err != nil {
						t.Fatal(err)
					}
					if output.Statistics.Statements != 2 || output.Statistics.AuxiliaryStatements != probes || len(output.Diagnostics) != 1 || output.Diagnostics[0].Code != "RUN005" || output.Summary.Warnings != 1 || output.Summary.Errors != 0 {
						t.Fatalf("analysis = %#v", output)
					}
				}
				suppressed := runApplicationWithInput(t, artifact.Bytes(), "analyze", "--json", "--suppress", "RUN005=intentional per-row lease boundary")
				var output runtimeAnalysisJSON
				if err := json.Unmarshal(suppressed.Stdout(), &output); err != nil {
					t.Fatal(err)
				}
				if suppressed.Status() != cli.StatusSuccess || len(suppressed.Stderr()) != 0 || len(output.Diagnostics) != 0 || len(output.Suppressed) != 1 || output.Summary.Suppressed != 1 || output.Summary.Warnings != 0 || output.Suppressed[0].Diagnostic.Code != "RUN005" || output.Suppressed[0].Reason != "intentional per-row lease boundary" {
					t.Fatalf("suppression = %#v, stderr=%s", output, suppressed.Stderr())
				}
			}
		})
	}
}

func TestApplicationAnalyzeRepeatedUpdateExclusions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		separate bool
		write    func(context.Context, orm.ExecExecutor) (int64, error)
	}{
		{"separate_scopes", true, func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return starterapp.UpdateUserEmail(ctx, executor, &starterapp.User{ID: 1, Email: "test@example.com"})
		}},
		{"soft_delete", false, func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return starterapp.DeleteVideo(ctx, executor, &starterapp.Video{ID: 1})
		}},
		{"soft_delete_where", false, func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return orm.DeleteWhere[starterapp.Video](orm.Equal("ID", int64(1))).Exec(ctx, executor)
		}},
		{"raw_update", false, func(ctx context.Context, executor orm.ExecExecutor) (int64, error) {
			return orm.RawExec(ctx, executor, "UPDATE users SET email = ? WHERE id = ?", "test@example.com", int64(1))
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
				if _, err := test.write(ctx, executor); err != nil {
					t.Fatal(err)
				}
			}
			if err := capture.Err(); err != nil {
				t.Fatal(err)
			}
			result := runApplicationWithInput(t, artifact.Bytes(), "analyze", "--json")
			var output runtimeAnalysisJSON
			if err := json.Unmarshal(result.Stdout(), &output); err != nil {
				t.Fatal(err)
			}
			if result.Status() != cli.StatusSuccess || len(output.Diagnostics) != 0 || output.Statistics.Statements != 2 || executor.calls != 2 {
				t.Fatalf("excluded updates: calls=%d analysis=%#v stderr=%s", executor.calls, output, result.Stderr())
			}
		})
	}
}
