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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, test.arguments...)
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
			"analyze",
			"baseline",
			"lint",
			"Additional commands:\n  version",
			"Options:",
			"-V, --version",
		} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("arguments %q: stdout = %q, want substring %q", arguments, stdout, want)
			}
		}
		if strings.Contains(stdout, "\n  check") {
			t.Fatalf("arguments %q: stdout still lists removed check command: %q", arguments, stdout)
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
		{name: "removed check command", arguments: []string{"check"}, code: cli.CodeUnknownCommand},
		{name: "removed generate command", arguments: []string{"generate"}, code: cli.CodeUnknownCommand},
		{name: "extra version argument", arguments: []string{"version", "extra"}, code: cli.CodeUnknownCommand},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := runApplication(t, test.arguments...)
			if got, want := result.Status(), exitUsage; got != want {
				t.Fatalf("status = %d, want %d", got, want)
			}
			if got := result.Stdout(); len(got) != 0 {
				t.Fatalf("stdout = %q, want empty", got)
			}
			stderr := string(result.Stderr())
			if want := "error[" + string(test.code) + "]"; !strings.Contains(stderr, want) {
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
		{name: "diagnostic failure", status: exitDiagnosticFailure, want: 1},
		{name: "usage", status: exitUsage, want: 2},
		{name: "internal error", status: exitInternalError, want: 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.status != test.want {
				t.Fatalf("status = %d, want %d", test.status, test.want)
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
	return runApplicationAt(t, "/", nil, arguments...)
}

func runApplicationWithInput(t *testing.T, input []byte, arguments ...string) clitest.Result {
	t.Helper()
	return runApplicationAt(t, "/", input, arguments...)
}

func runApplicationAt(t *testing.T, directory string, input []byte, arguments ...string) clitest.Result {
	t.Helper()

	driver := clitest.New(application("dev")).
		Arguments(normalizeArguments(arguments)...).
		Policy(runtimePolicy()).
		CurrentDirectory(directory)
	if input != nil {
		driver.Stdin(input)
	}
	result, err := driver.Run()
	if err != nil {
		t.Fatalf("run application: %v", err)
	}
	return result
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
