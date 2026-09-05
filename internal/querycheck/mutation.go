package querycheck

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/queryshape"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const (
	// CodeMutationMissingIndex reports a conditional write without a supported
	// predicate lookup through the leading column of a default-usable index.
	CodeMutationMissingIndex = "QRY008"
	// CodeMutationIndexUncertain reports a conditional write whose index
	// coverage cannot be established by this offline check.
	CodeMutationIndexUncertain = "QRY009"
)

// MutationIndexStatus separates completed checks from uncertain predicates
// and unavailable schema input. Checked does not prove an efficient plan.
type MutationIndexStatus uint8

const (
	// MutationIndexUnavailable means the physical schema could not be checked.
	MutationIndexUnavailable MutationIndexStatus = iota
	// MutationIndexChecked means a supported case was resolved, with or without
	// a missing-index warning, or proved to match no rows from empty lists.
	MutationIndexChecked
	// MutationIndexUncertain means the shape needs unsupported index reasoning.
	MutationIndexUncertain
)

// MutationIndexResult reports diagnostic evidence and statement coverage.
type MutationIndexResult struct {
	Status      MutationIndexStatus
	Diagnostics []check.Diagnostic
}

// MutationIndexDiagnostics checks scalar conditional writes against an offline
// snapshot. One usable leading-column constraint suffices; it does not require
// an index containing every WHERE column or predict selectivity, index choice,
// lock scope, or RU. Unsupported cases remain explicitly uncertain.
func MutationIndexDiagnostics(shape queryshape.Mutation, catalog *physicalschema.Catalog) MutationIndexResult {
	if catalog == nil {
		return mutationSchemaUnavailable(shape, "a non-nil catalog returned by schema.Parse is required", check.Location{})
	}
	table, exists := catalog.Table(shape.Table)
	if !exists {
		return mutationSchemaUnavailable(shape, fmt.Sprintf("table %q is absent from the SQL snapshot", shape.Table), check.Location{})
	}
	var columns []string
	uncertain := inspectMutationPredicates(shape.Predicates, &columns)
	if shape.SoftDeleteColumn != "" {
		columns = appendIdentifier(columns, shape.SoftDeleteColumn)
	}
	for _, column := range columns {
		if _, exists := table.Column(column); !exists {
			return mutationSchemaUnavailable(shape, fmt.Sprintf("column %q is absent from table %q", column, table.Name()), schemaLocation(table.Position()))
		}
	}
	// Empty IN lists and their logical combinations can prove an empty result
	// without interpreting any bind value. Other contradictions are not inferred.
	if mutationConstantGroup(shape.Predicates, false) == mutationFalse {
		return MutationIndexResult{Status: MutationIndexChecked}
	}
	unsupportedIndex := false
	for _, index := range table.Indexes() {
		if !index.SupportsDefaultColumnLookup() {
			unsupportedIndex = true
			continue
		}
		indexedColumns := index.Columns()
		if len(indexedColumns) == 0 {
			continue
		}
		leading := indexedColumns[0]
		if strings.EqualFold(shape.SoftDeleteColumn, leading) || mutationGroupBoundsColumn(shape.Predicates, leading, false) {
			return MutationIndexResult{Status: MutationIndexChecked}
		}
	}
	if uncertain == "" && unsupportedIndex {
		uncertain = "the snapshot contains an expression, prefix-length, specialized, partial, or invisible index outside default direct-column coverage"
	}
	if uncertain != "" {
		return MutationIndexResult{Status: MutationIndexUncertain, Diagnostics: []check.Diagnostic{{
			Code: CodeMutationIndexUncertain, Severity: check.SeverityInfo,
			Title:      "Conditional write index coverage is uncertain",
			Message:    "Conditional write for " + shape.Model + " on " + shape.Table + ": " + uncertain,
			Evidence:   []check.Evidence{{Message: "Referenced filter columns: " + table.Name() + "(" + strings.Join(columns, ", ") + ")"}},
			Suggestion: "Inspect the generated statement with EXPLAIN; this result is not a successful index check, and EXPLAIN ANALYZE executes the write",
			Location:   schemaLocation(table.Position()), Suppressible: true, Reference: indexReference,
		}}}
	}
	return MutationIndexResult{Status: MutationIndexChecked, Diagnostics: []check.Diagnostic{{
		Code: CodeMutationMissingIndex, Severity: check.SeverityWarning,
		Title:      "Conditional write has no matching index prefix",
		Message:    "Conditional write for " + shape.Model + " on " + shape.Table + " has no supported predicate lookup through the leading column of a default-usable index in the SQL snapshot",
		Evidence:   []check.Evidence{{Message: "Referenced filter columns: " + table.Name() + "(" + strings.Join(columns, ", ") + ")"}},
		Suggestion: "Use EXPLAIN to review row scope, selectivity, and index write cost before changing indexes; an index alone cannot narrow an all-row write, and EXPLAIN ANALYZE executes the write",
		Location:   schemaLocation(table.Position()), Suppressible: true, Reference: indexReference,
	}}}
}

func mutationSchemaUnavailable(shape queryshape.Mutation, reason string, location check.Location) MutationIndexResult {
	return MutationIndexResult{Status: MutationIndexUnavailable, Diagnostics: []check.Diagnostic{{
		Code: CodeIndexCheckUnavailable, Severity: check.SeverityError,
		Title:      "Query index check is unavailable",
		Message:    "Conditional write for " + shape.Model + " cannot compare its predicates with the physical schema because " + reason,
		Suggestion: "Use a self-contained schema snapshot containing the table and columns referenced by the conditional write",
		Location:   location,
	}}}
}

func inspectMutationPredicates(predicates []queryshape.MutationPredicate, columns *[]string) string {
	var uncertain string
	for _, predicate := range predicates {
		var reason string
		switch predicate.Operator {
		case queryshape.PredicateAnd, queryshape.PredicateOr, queryshape.PredicateNot:
			reason = inspectMutationPredicates(predicate.Children, columns)
			if predicate.Operator != queryshape.PredicateAnd && mutationPredicateConstant(predicate) == mutationVariable {
				reason = "OR or NOT without a shared supported index bound may require range rewrites or IndexMerge"
			}
			if len(predicate.Children) == 0 {
				reason = "a logical predicate has no children"
			}
		default:
			if (predicate.Operator == queryshape.PredicateIn || predicate.Operator == queryshape.PredicateNotIn) && predicate.EmptyList {
				continue // These compile to constants and reference no SQL column.
			}
			if predicate.Column != "" {
				*columns = appendIdentifier(*columns, predicate.Column)
			}
			if !supportedMutationBound(predicate.Operator) {
				reason = "predicate operator " + string(predicate.Operator) + " is outside supported positive scalar bounds"
			} else if predicate.Column == "" {
				reason = "a scalar predicate has no physical column metadata"
			}
		}
		if uncertain == "" {
			uncertain = reason
		}
	}
	return uncertain
}

func supportedMutationBound(operator queryshape.PredicateOperator) bool {
	switch operator {
	case queryshape.PredicateEqual, queryshape.PredicateIn, queryshape.PredicateIsNull,
		queryshape.PredicateGreaterThan, queryshape.PredicateGreaterThanOrEqual,
		queryshape.PredicateLessThan, queryshape.PredicateLessThanOrEqual, queryshape.PredicateBetween:
		return true
	default:
		return false
	}
}

func mutationGroupBoundsColumn(predicates []queryshape.MutationPredicate, column string, disjunction bool) bool {
	if len(predicates) == 0 {
		return false
	}
	for _, predicate := range predicates {
		bounded := mutationPredicateBoundsColumn(predicate, column)
		if bounded != disjunction {
			return bounded
		}
	}
	return disjunction
}

func mutationPredicateBoundsColumn(predicate queryshape.MutationPredicate, column string) bool {
	switch predicate.Operator {
	case queryshape.PredicateAnd, queryshape.PredicateOr:
		return mutationGroupBoundsColumn(predicate.Children, column, predicate.Operator == queryshape.PredicateOr)
	case queryshape.PredicateNot:
		return mutationPredicateConstant(predicate) == mutationFalse
	case queryshape.PredicateIn:
		if predicate.EmptyList {
			return true // FALSE contributes no unbounded rows to an OR branch.
		}
	}
	return supportedMutationBound(predicate.Operator) && strings.EqualFold(predicate.Column, column)
}

type mutationConstant uint8

const (
	mutationVariable mutationConstant = iota
	mutationFalse
	mutationTrue
)

func mutationPredicateConstant(predicate queryshape.MutationPredicate) mutationConstant {
	switch predicate.Operator {
	case queryshape.PredicateIn:
		if predicate.EmptyList {
			return mutationFalse
		}
	case queryshape.PredicateNotIn:
		if predicate.EmptyList {
			return mutationTrue
		}
	case queryshape.PredicateAnd, queryshape.PredicateOr:
		return mutationConstantGroup(predicate.Children, predicate.Operator == queryshape.PredicateOr)
	case queryshape.PredicateNot:
		if len(predicate.Children) == 1 {
			switch mutationPredicateConstant(predicate.Children[0]) {
			case mutationFalse:
				return mutationTrue
			case mutationTrue:
				return mutationFalse
			}
		}
	}
	return mutationVariable
}

func mutationConstantGroup(predicates []queryshape.MutationPredicate, disjunction bool) mutationConstant {
	result := mutationTrue
	decisive := mutationFalse
	if disjunction {
		result, decisive = mutationFalse, mutationTrue
	}
	if len(predicates) == 0 {
		return mutationVariable
	}
	for _, predicate := range predicates {
		current := mutationPredicateConstant(predicate)
		if current == decisive {
			return decisive
		}
		if current == mutationVariable {
			result = mutationVariable
		}
	}
	return result
}
