package main

import (
	"fmt"
	"os"
	"path/filepath"

	cli "github.com/mayahiro/nagicli-go"

	physicalschema "github.com/mayahiro/go-tidb/schema"
)

func schemaFromOption(
	context *cli.Context,
	invocation *cli.Invocation,
	optionID string,
) (*physicalschema.Catalog, bool, *cli.Diagnostic) {
	input, present := cli.ValueAs[string](invocation, optionID)
	if !present {
		return nil, false, nil
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(context.CurrentDirectory(), path)
	}
	schemaSQL, err := os.ReadFile(path)
	if err != nil {
		return nil, false, cli.NewDiagnostic(
			cli.CodeIOError,
			fmt.Sprintf("read schema snapshot %q: %s", input, inputOpenErrorReason(err)),
		).WithTarget(cli.OptionTarget(optionID))
	}
	catalog, err := physicalschema.Parse(string(schemaSQL))
	if err != nil {
		return nil, false, cli.NewDiagnostic(cli.CodeInvalidValue, "parse schema snapshot: "+err.Error()).
			WithTarget(cli.OptionTarget(optionID))
	}
	return catalog, true, nil
}
