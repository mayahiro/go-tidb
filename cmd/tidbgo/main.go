// Command tidbgo provides development tools for TiDB Cloud Starter projects.
package main

import (
	"fmt"
	"os"

	cli "github.com/mayahiro/nagicli-go"
)

var version = "dev"

const (
	exitCheckFailure  cli.ExitStatus = 1
	exitUsage         cli.ExitStatus = 2
	exitInternalError cli.ExitStatus = 5

	versionActionCommand = "version-action"
)

func main() {
	program := "tidbgo"
	if len(os.Args) > 0 {
		program = os.Args[0]
	}
	os.Args = append([]string{program}, normalizeArguments(os.Args[1:])...)

	status, err := application(version).RunProcessWithPolicy(runtimePolicy())
	if err != nil {
		fmt.Fprintf(os.Stderr, "tidbgo: %v\n", err)
		status = exitInternalError
	}
	os.Exit(int(status))
}

func application(toolVersion string) *cli.Command {
	return cli.NewCommand("tidbgo").
		ID("tidbgo-root").
		About("Development tools for TiDB Cloud Starter").
		Version(toolVersion).
		RequireSubcommand().
		HelpSection(
			cli.NewHelpSection("additional-commands", "Additional commands").
				Entry("version", "Print the tidbgo version"),
		).
		Subcommand(checkCommand()).
		Subcommand(
			cli.NewCommand(versionActionCommand).
				ID("version-command").
				Hidden().
				Handle(func(context *cli.Context, _ *cli.Invocation) (cli.Outcome, error) {
					if _, err := fmt.Fprintf(context.Stdout(), "tidbgo %s\n", toolVersion); err != nil {
						return cli.Outcome{}, cli.NewDiagnostic(
							cli.CodeIOError,
							"write version: "+err.Error(),
						)
					}
					return cli.Success(), nil
				}),
		)
}

func normalizeArguments(arguments []string) []string {
	normalized := append([]string(nil), arguments...)
	if len(normalized) == 1 && normalized[0] == "version" {
		normalized[0] = versionActionCommand
	}
	return normalized
}

func runtimePolicy() cli.RuntimePolicy {
	exitCodes := cli.DefaultExitCodePolicy().
		WithStatus(cli.CategorySpecification, exitInternalError).
		WithStatus(cli.CategoryUsage, exitUsage).
		WithStatus(cli.CategoryExecution, exitInternalError).
		WithStatus(cli.CategoryIO, exitInternalError)
	return cli.DefaultRuntimePolicy().WithExitCodePolicy(exitCodes)
}
