package main

import (
	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

const workloadID = "runtime-workload"

func workloadOptions(invocation *cli.Invocation) ([]runtimecapture.AnalysisOption, *cli.Diagnostic) {
	name, present := cli.ValueAs[string](invocation, workloadID)
	if !present {
		return nil, nil
	}
	if err := runtimecapture.ValidateWorkloadName(name); err != nil {
		return nil, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error()).WithTarget(cli.OptionTarget(workloadID))
	}
	return []runtimecapture.AnalysisOption{runtimecapture.WithWorkload(name)}, nil
}
