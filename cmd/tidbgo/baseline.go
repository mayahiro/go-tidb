package main

import (
	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

const baselineInputID = "runtime-input"

func baselineCommand() *cli.Command {
	return cli.NewCommand("baseline").
		ID("baseline-command").
		About("Create a versioned ServerRU baseline from a runtime capture").
		Argument(
			cli.Positional(baselineInputID).
				Parser(cli.StringParser()).
				Help("Runtime capture JSON Lines file, or - for standard input"),
		).
		Handle(runBaseline)
}

func runBaseline(context *cli.Context, invocation *cli.Invocation) (cli.Outcome, error) {
	reader, closeInput, diagnostic := openRuntimeCaptureInput(context, invocation, baselineInputID)
	if diagnostic != nil {
		return cli.Outcome{}, diagnostic
	}
	analysis, err := runtimecapture.AnalyzeReader(reader)
	if closeErr := closeInput(); err == nil && closeErr != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "close runtime capture input: "+closeErr.Error())
	}
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.ArgumentTarget(baselineInputID))
	}
	baseline, err := runtimecapture.NewServerRUBaseline(analysis)
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).
			WithTarget(cli.ArgumentTarget(baselineInputID))
	}
	if err := runtimecapture.EncodeServerRUBaseline(context.Stdout(), baseline); err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write ServerRU baseline: "+err.Error())
	}
	return cli.Success(), nil
}
