package orm

import (
	"context"
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/schema"
)

// VectorPlan contains one explicit vector plan inspection and same-session
// warnings. Planned and Executed have the same provenance as AggregatePlan.
type VectorPlan struct {
	Mode          VectorSearchMode
	Requested     ReadPolicy
	Planned       []ExplainRow
	Executed      ExplainAnalyzePlan
	Warnings      []PlanWarning
	WarningsError error
}

// VectorIndexUsage is evidence from the returned plan, not from index existence.
type VectorIndexUsage string

const (
	// VectorIndexUnknown means the available plan cannot establish index usage.
	VectorIndexUnknown VectorIndexUsage = ""
	// VectorIndexUsed means TiDB reported an annIndex marker on a TiFlash scan.
	VectorIndexUsed VectorIndexUsage = "used"
	// VectorIndexNotUsed means recognized table scans have no annIndex marker.
	VectorIndexNotUsed VectorIndexUsage = "not_used"
)

// Explain inspects the search without executing it and collects same-session
// warnings. It requires *sql.DB, *sql.Conn, *sql.Tx, or Observe wrappers.
func (q *VectorQuery[T]) Explain(ctx context.Context, executor QueryExecutor) (VectorPlan, error) {
	return q.inspectVectorPlan(ctx, executor, false)
}

// ExplainAnalyze explicitly executes the search and collects its runtime plan
// and same-session warnings. It consumes resources and returns no search results.
func (q *VectorQuery[T]) ExplainAnalyze(ctx context.Context, executor QueryExecutor) (VectorPlan, error) {
	return q.inspectVectorPlan(ctx, executor, true)
}

func (q *VectorQuery[T]) inspectVectorPlan(ctx context.Context, executor QueryExecutor, analyze bool) (VectorPlan, error) {
	var resolver planAccessResolver
	c, err := q.compileVector(&resolver)
	if err != nil {
		return VectorPlan{}, err
	}
	p, err := inspectReadPlan(ctx, executor, c, resolver, q.policy, analyze, "vector")
	return VectorPlan{Mode: q.mode, Requested: p.Requested, Planned: p.Planned, Executed: p.Executed, Warnings: p.Warnings, WarningsError: p.WarningsError}, err
}

// IndexUsage reports planned usage for Explain and observed usage for
// ExplainAnalyze. It says nothing about recall or another query execution.
func (p VectorPlan) IndexUsage() VectorIndexUsage {
	used, scan, unknown := false, false, false
	inspect := func(id, task, access, info string) {
		if planAccessObjectTable(access) == "" {
			return
		}
		t := parsePlanTask(task)
		operator := planOperatorName(planOperatorIdentifier(id))
		if t.Known && t.Kind == "root" {
			return
		}
		if t.Engine == "" {
			unknown = true
			return
		}
		switch operator {
		case "TableFullScan", "TableRangeScan", "TableRowIDScan", "IndexFullScan", "IndexRangeScan":
		default:
			unknown = true
			return
		}
		scan = true
		marker := strings.HasPrefix(info, "annIndex:") || strings.Contains(info, ", annIndex:")
		used = used || (t.Engine == TiFlash && operator == "TableFullScan" && marker)
	}
	if p.Executed != nil {
		for _, row := range p.Executed {
			inspect(row.ID, row.Task, row.AccessObject, row.OperatorInfo)
		}
	} else {
		for _, row := range p.Planned {
			inspect(row.ID, row.Task, row.AccessObject, row.OperatorInfo)
		}
	}
	if used {
		return VectorIndexUsed
	}
	if scan && !unknown {
		return VectorIndexNotUsed
	}
	return VectorIndexUnknown
}

// Summary describes operator locations and cardinalities without I/O.
func (p VectorPlan) Summary() PlanSummary { return summarizeReadPlan(p.Planned, p.Executed) }

// Diagnostics reports execution facts, storage/MPP mismatches, and VEC003 when
// an approximate search has recognized scans without a vector index. The latter
// is informational: approximate mode permits an index but never requires one.
func (p VectorPlan) Diagnostics() []check.Diagnostic {
	base := AggregatePlan{Requested: p.Requested, Planned: p.Planned, Executed: p.Executed}
	result := base.Diagnostics()
	if p.Mode == VectorApproximate && p.IndexUsage() == VectorIndexNotUsed {
		result = append(result, vectorDiagnostic("VEC003", "Vector index not used", "The inspected plan has recognized table scans without an ANN index", "Check matching index readiness and prefilters; keep tenant and soft-delete restrictions before Top-K"))
	}
	return result
}

// SchemaDiagnostics checks a search against an offline physical schema snapshot.
// VEC001 reports absent/non-vector columns or dimension mismatch; VEC002 reports
// missing matching vector indexes or filters that prevent the documented ANN
// path. A nil catalog reports unknown metadata, never a missing index. These
// checks do not prove live replica/index readiness or optimizer selection.
func (q *VectorQuery[T]) SchemaDiagnostics(catalog *schema.Catalog) ([]check.Diagnostic, error) {
	c, err := q.compileVector(nil)
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return []check.Diagnostic{vectorDiagnostic("VEC001", "Vector schema not checked", "No schema snapshot was supplied", "Supply a current schema snapshot to check dimensions and index declarations")}, nil
	}
	field, _ := vectorSourceField(c.source, q.field)
	table, tableExists := catalog.Table(c.source.TableName())
	column, columnExists := table.Column(field.ColumnName())
	if !tableExists || !columnExists || column.TypeName() != "VECTOR" || (column.VectorDimensions() != 0 && column.VectorDimensions() != q.input.Dimensions()) {
		d := vectorDiagnostic("VEC001", "Vector schema mismatch", fmt.Sprintf("%s.%s must be a VECTOR compatible with %d dimensions", c.source.TableName(), field.ColumnName(), q.input.Dimensions()), "Align the query vector, mapped field, and physical column")
		d.Severity = check.SeverityWarning
		return []check.Diagnostic{d}, nil
	}
	if q.mode != VectorApproximate {
		return nil, nil
	}
	var result []check.Diagnostic
	function, _ := vectorDistanceFunction(q.metric)
	matched := false
	for _, index := range table.Indexes() {
		indexed, metric, recognized := index.Vector()
		matched = matched || (recognized && strings.EqualFold(indexed, column.Name()) && metric == function && column.VectorDimensions() > 0)
	}
	if !matched {
		result = append(result, vectorDiagnostic("VEC002", "Matching vector index absent", "The snapshot has no usable fixed-dimensional vector index for this column and metric", "Create a matching vector index explicitly and inspect the live plan"))
	}
	_, active := activeSoftDeleteField(c.source, q.withDeleted)
	if len(q.predicates) > 0 || active {
		result = append(result, vectorDiagnostic("VEC002", "Prefilter restricts vector index use", "Where or the active soft-delete scope filters before Top-K; TiDB's documented ANN path does not support prefilters", "Keep required filters; measure the exact filtered search and inspect the plan"))
	}
	return result, nil
}

func vectorDiagnostic(code, title, message, suggestion string) check.Diagnostic {
	return check.Diagnostic{Code: code, Severity: check.SeverityInfo, Title: title, Message: message, Suggestion: suggestion, Suppressible: true, Reference: "https://docs.pingcap.com/ai/vector-search-index/"}
}
