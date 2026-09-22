package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cli "github.com/mayahiro/nagicli-go"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/referencecheck"
	"github.com/mayahiro/go-tidb/internal/sourcecheck"
)

func auditCommand() *cli.Command {
	return cli.NewCommand("audit").ID("audit").About("Check declared logical references for physical orphan rows using read-only SQL").
		Argument(cli.Positional("audit-path").Parser(cli.StringParser()).Help("Go source file or directory (default: current directory)")).
		Option(cli.ValueOption("audit-schema").Long("schema").Parser(cli.StringParser()).Help("Required SQL snapshot for the target database")).
		Option(cli.Flag("audit-dry-run").Long("dry-run").Help("Print the reference plan and SELECT statements without connecting")).
		Option(cli.Flag("audit-json").Long("json").Help("Write structured audit results")).
		Option(cli.ValueOption("audit-relation").Long("relation").Repeated().Parser(cli.StringParser()).Help("Select a relation name from the dry-run plan; repeat to select more")).
		Option(cli.ValueOption("audit-timeout").Long("timeout").Parser(cli.CustomParser("DURATION", time.ParseDuration)).Help("Deadline for the whole database audit (default: 30s, maximum: 1h)")).
		Option(cli.ValueOption("audit-dsn-env").Long("dsn-env").Parser(cli.StringParser()).Help("Verified TLS MySQL DSN environment variable (default: TIDBGO_DSN)")).
		Handle(runAudit)
}

type auditProbe struct {
	referencecheck.Reference
	AlsoDeclaredBy []string `json:"also_declared_by,omitempty"`
	SQL            string   `json:"sql"`
}

type auditOutput struct {
	DryRun      bool                    `json:"dry_run"`
	Complete    bool                    `json:"complete"`
	Statistics  sourcecheck.Statistics  `json:"statistics"`
	Diagnostics []check.Diagnostic      `json:"diagnostics"`
	Plan        []auditProbe            `json:"plan"`
	Results     []referencecheck.Result `json:"results"`
}

func runAudit(c *cli.Context, in *cli.Invocation) (cli.Outcome, error) {
	catalog, present, diagnostic := schemaFromOption(c, in, "audit-schema")
	if diagnostic != nil {
		return cli.Outcome{}, diagnostic
	}
	if !present {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "audit requires --schema")
	}
	timeout, ok := cli.ValueAs[time.Duration](in, "audit-timeout")
	if !ok {
		timeout = 30 * time.Second
	}
	if timeout <= 0 || timeout > time.Hour {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "audit timeout must be positive and no greater than 1h")
	}
	input := migrationValue(in, "audit-path", ".")
	analysis, err := sourcecheck.AnalyzePath(migrationPath(c, input), sourcecheck.WithSchema(catalog))
	if err != nil {
		return cli.Outcome{}, lintInputDiagnostic(input, err).WithTarget(cli.ArgumentTarget("audit-path"))
	}
	dryRun, _ := in.Flag("audit-dry-run")
	output := auditOutput{DryRun: dryRun, Statistics: analysis.Statistics, Diagnostics: []check.Diagnostic{}, Plan: []auditProbe{}, Results: []referencecheck.Result{}}
	blocked := analysis.Statistics.UncertainSchemaModels != 0 || analysis.Statistics.UncertainSchemaRelations != 0
	for _, d := range analysis.Diagnostics {
		// Query tuning diagnostics are unrelated to the reference audit plan.
		if strings.HasPrefix(d.Code, "CMP") || strings.HasPrefix(d.Code, "REF") || d.Code == "SRC002" || d.Code == "SRC003" {
			output.Diagnostics = append(output.Diagnostics, d)
			blocked = blocked || d.Severity == check.SeverityError
		}
	}
	var selected []string
	for _, value := range in.ParsedValues("audit-relation") {
		if name, ok := value.Typed().(string); ok {
			selected = append(selected, name)
		}
	}
	plan, err := auditPlan(analysis.References, selected)
	if err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error())
	}
	output.Plan = plan
	if len(plan) == 0 {
		blocked = true
	}
	if blocked {
		output.Diagnostics = append(output.Diagnostics, check.Diagnostic{
			Code: "REF006", Severity: check.SeverityError, Title: "Reference audit is incomplete",
			Message:    "Resolve schema errors and uncertain mappings, and include at least one declared relation before auditing data",
			Suggestion: "Run lint with the same source and schema; dry-run output lists the resolved portion only",
		})
	}
	status := cli.StatusSuccess
	if blocked {
		status = exitDiagnosticFailure
	}
	if !dryRun && !blocked {
		envName := migrationValue(in, "audit-dsn-env", "TIDBGO_DSN")
		dsn, ok := c.Environment(envName)
		if !ok || dsn == "" {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, "set the audit DSN environment variable before connecting")
		}
		db, err := openToolDatabase(dsn, "audit")
		if err != nil {
			return cli.Outcome{}, cli.NewDiagnostic(cli.CodeInvalidValue, err.Error())
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(c.Cancellation(), timeout)
		defer cancel()
		status = executeAudit(ctx, db, &output)
	}
	if err := writeAudit(c, in, output); err != nil {
		return cli.Outcome{}, cli.NewDiagnostic(cli.CodeIOError, "write audit output failed")
	}
	return cli.NewOutcome(status), nil
}

func executeAudit(ctx context.Context, db referencecheck.Queryer, output *auditOutput) cli.ExitStatus {
	status := cli.StatusSuccess
	var err error
	refs := make([]referencecheck.Reference, len(output.Plan))
	for i, probe := range output.Plan {
		refs[i] = probe.Reference
	}
	output.Results, err = referencecheck.Run(ctx, db, refs)
	output.Complete = err == nil
	if err != nil {
		status = exitInternalError
		output.Diagnostics = append(output.Diagnostics, check.Diagnostic{
			Code: "REF006", Severity: check.SeverityError, Title: "Reference audit is incomplete",
			Message:    err.Error(),
			Suggestion: "Check connectivity, SELECT privileges, schema freshness, and the deadline; completed probes remain in the results",
		})
	}
	for i, result := range output.Results {
		if result.Checked && result.Orphan {
			if status == cli.StatusSuccess {
				status = exitDiagnosticFailure
			}
			output.Diagnostics = append(output.Diagnostics, check.Diagnostic{
				Code: "REF005", Severity: check.SeverityError, Title: "Orphan reference exists",
				Message:    result.Reference + ": at least one non-NULL child key has no physical parent row",
				Location:   output.Plan[i].Location,
				Suggestion: "Inspect and repair the reference through application policy; audit does not delete or modify rows",
			})
		}
	}
	return status
}

func auditPlan(refs []referencecheck.Reference, selected []string) ([]auditProbe, error) {
	result := []auditProbe{}
	found := make(map[string]bool)
	seen := make(map[string]int)
	for _, ref := range refs {
		include := len(selected) == 0
		for _, name := range selected {
			if ref.Name == name || strings.HasPrefix(ref.Name, name+"#") {
				include = true
				found[name] = true
			}
		}
		if !include {
			continue
		}
		query, err := referencecheck.SQL(ref)
		if err != nil {
			return nil, err
		}
		// Generated identifiers are ASCII-insensitive in the physical catalog.
		key := strings.ToLower(query)
		if i, ok := seen[key]; ok {
			result[i].AlsoDeclaredBy = append(result[i].AlsoDeclaredBy, ref.Name)
			continue
		}
		seen[key] = len(result)
		result = append(result, auditProbe{Reference: ref, SQL: query})
	}
	for _, name := range selected {
		if !found[name] {
			return nil, fmt.Errorf("unknown or unresolved audit relation %q; inspect --dry-run without --relation", name)
		}
	}
	return result, nil
}

func writeAudit(c *cli.Context, in *cli.Invocation, output auditOutput) error {
	jsonOutput, _ := in.Flag("audit-json")
	if jsonOutput {
		return json.NewEncoder(c.Stdout()).Encode(output)
	}
	for _, d := range output.Diagnostics {
		if err := writeTextDiagnostic(c.Stdout(), stringUpper(d.Severity), d, ""); err != nil {
			return err
		}
	}
	for _, probe := range output.Plan {
		if _, err := fmt.Fprintf(c.Stdout(), "reference=%s also_declared_by=%q\n", probe.Name, probe.AlsoDeclaredBy); err != nil {
			return err
		}
		if output.DryRun {
			if _, err := fmt.Fprintf(c.Stdout(), "%s;\n", probe.SQL); err != nil {
				return err
			}
		}
	}
	checked, orphans := 0, 0
	for _, result := range output.Results {
		if result.Checked {
			checked++
		}
		if result.Orphan {
			orphans++
		}
		if _, err := fmt.Fprintf(c.Stdout(), "reference=%s checked=%t orphan=%t\n", result.Reference, result.Checked, result.Orphan); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(c.Stdout(), "audit: dry_run=%t complete=%t references=%d checked=%d orphan_references=%d uncertain_models=%d uncertain_relations=%d\n", output.DryRun, output.Complete, len(output.Plan), checked, orphans, output.Statistics.UncertainSchemaModels, output.Statistics.UncertainSchemaRelations)
	return err
}
