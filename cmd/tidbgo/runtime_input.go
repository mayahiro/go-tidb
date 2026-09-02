package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cli "github.com/mayahiro/nagicli-go"
)

func openRuntimeCaptureInput(context *cli.Context, invocation *cli.Invocation, argumentID string) (io.Reader, func() error, *cli.Diagnostic) {
	input, present := cli.ValueAs[string](invocation, argumentID)
	if !present || input == "-" {
		return context.Stdin(), func() error { return nil }, nil
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(context.CurrentDirectory(), path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, cli.NewDiagnostic(
			cli.CodeIOError,
			fmt.Sprintf("open runtime capture input %q: %s", input, inputOpenErrorReason(err)),
		).WithTarget(cli.ArgumentTarget(argumentID))
	}
	return file, file.Close, nil
}

func inputOpenErrorReason(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "file does not exist"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	default:
		return "open failed"
	}
}
