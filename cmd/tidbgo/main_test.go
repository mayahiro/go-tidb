package main

import (
	"strings"
	"testing"

	cli "github.com/mayahiro/nagicli-go"
	"github.com/mayahiro/nagicli-go/clitest"
)

func TestApplicationValid(t *testing.T) {
	t.Parallel()

	if err := application("dev").Validate(); err != nil {
		t.Fatalf("application().Validate() error = %v", err)
	}
}

func TestApplicationVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
	}{
		{name: "command", arguments: []string{"version"}},
		{name: "long option", arguments: []string{"--version"}},
		{name: "short option", arguments: []string{"-V"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, tt.arguments...)
			if got, want := result.Status(), cli.StatusSuccess; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got, want := string(result.Stdout()), "tidbgo dev\n"; got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
			if got := result.Stderr(); len(got) != 0 {
				t.Fatalf("stderr = %q, want empty", got)
			}
		})
	}
}

func TestApplicationHelp(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		result := runApplication(t, arguments...)
		if got, want := result.Status(), cli.StatusSuccess; got != want {
			t.Fatalf("arguments %q: status = %d, want %d", arguments, got, want)
		}
		stdout := string(result.Stdout())
		for _, want := range []string{
			"Usage:\n  tidbgo [OPTIONS] <COMMAND>",
			"Commands:\n  version",
			"Options:",
			"-V, --version",
		} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("arguments %q: stdout = %q, want substring %q", arguments, stdout, want)
			}
		}
		if got := result.Stderr(); len(got) != 0 {
			t.Fatalf("arguments %q: stderr = %q, want empty", arguments, got)
		}
	}
}

func TestApplicationUsageError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
		code      cli.DiagnosticCode
	}{
		{name: "no command", code: cli.CodeMissingSubcommand},
		{name: "unknown command", arguments: []string{"unknown"}, code: cli.CodeUnknownCommand},
		{name: "extra version argument", arguments: []string{"version", "extra"}, code: cli.CodeUnknownCommand},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, tt.arguments...)
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got := result.Stdout(); len(got) != 0 {
				t.Fatalf("stdout = %q, want empty", got)
			}
			stderr := string(result.Stderr())
			if want := "error[" + string(tt.code) + "]"; !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, want)
			}
			if want := "usage: tidbgo [OPTIONS] <COMMAND>"; !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, want)
			}
		})
	}
}

func TestExitStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status cli.ExitStatus
		want   cli.ExitStatus
	}{
		{name: "success", status: cli.StatusSuccess, want: 0},
		{name: "lint failure", status: exitLintFailure, want: 1},
		{name: "usage", status: exitUsage, want: 2},
		{name: "migration", status: exitMigration, want: 3},
		{name: "connection", status: exitConnection, want: 4},
		{name: "internal error", status: exitInternalError, want: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.status != tt.want {
				t.Fatalf("status = %d, want %d", tt.status, tt.want)
			}
		})
	}

	policy := runtimePolicy()
	for _, code := range []cli.DiagnosticCode{
		cli.CodeInvalidSpecification,
		cli.CodeHandlerError,
		cli.CodeIOError,
	} {
		diagnostic := cli.NewDiagnostic(code, "test failure")
		if got := policy.StatusForDiagnostic(diagnostic); got != exitInternalError {
			t.Fatalf("diagnostic %q status = %d, want %d", code, got, exitInternalError)
		}
	}
}

func runApplication(t *testing.T, arguments ...string) clitest.Result {
	t.Helper()

	result, err := clitest.New(application("dev")).
		Arguments(normalizeArguments(arguments)...).
		Policy(runtimePolicy()).
		Run()
	if err != nil {
		t.Fatalf("run application: %v", err)
	}
	return result
}
