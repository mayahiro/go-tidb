package sourcecheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mayahiro/go-tidb/check"
)

// Aggregate coverage intentionally concerns projection/grouping contracts only.
// Runtime Build remains authoritative for types, relations, predicates/windows,
// physical schema, and optimizer capabilities.
type sourceAggregate struct {
	recognized, known bool
	model             sourceTypeKey
	outputs           []sourceAggregateOutput
	groups            []string
}

type sourceAggregateOutput struct {
	name       string
	group      bool
	expression string
}

func (analyzer *sourceAnalyzer) recordAggregatePattern(context sourceFunctionContext, call *ast.CallExpr, expression ast.Expr) bool {
	summary := sourceAggregateExpression(context, expression, call.Pos(), nil)
	if !summary.recognized {
		return false
	}
	analyzer.analysis.Statistics.AggregatePatterns++
	analyzer.noteSchemaModel(summary.model, call.Pos())
	analyzer.seenModels[summary.model] = struct{}{}
	if !summary.known {
		analyzer.analysis.Statistics.UncertainAggregatePatterns++
		return true
	}
	analyzer.analysis.Statistics.AnalyzedAggregatePatterns++
	problem := sourceAggregateProblem(summary)
	if problem != "" {
		analyzer.appendPatternDiagnostic(check.Diagnostic{Code: "AGG001", Severity: check.SeverityWarning, Title: "Invalid aggregate projection or grouping", Message: problem, Location: analyzer.sourceLocation(call.Pos()), Suggestion: "Use unique exported output names and GroupBy each selected field/calendar output; run Build to validate the complete query", Suppressible: true})
	}
	return true
}

func sourceAggregateExpression(context sourceFunctionContext, expression ast.Expr, before token.Pos, visiting map[*ast.Object]bool) sourceAggregate {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return sourceAggregateExpression(context, value.X, before, visiting)
	case *ast.Ident:
		if value.Obj == nil || visiting[value.Obj] {
			return sourceAggregate{}
		}
		if visiting == nil {
			visiting = make(map[*ast.Object]bool)
		}
		visiting[value.Obj] = true
		defer delete(visiting, value.Obj)
		definitions := sourceBuilderDefinitions(context.body, value.Obj, before)
		var result sourceAggregate
		for _, definition := range definitions {
			candidate := sourceAggregateExpression(context, definition.expr, definition.position, visiting)
			if candidate.recognized {
				result = candidate
				break
			}
		}
		if !result.recognized {
			return result
		}
		if len(definitions) != 1 {
			result.known = false
			return result
		}
		// An escaped, reassigned, conditionally mutated, or aliased mutable builder
		// cannot establish its terminal projection merely from its initializer.
		ast.Inspect(context.body, func(node ast.Node) bool {
			if node == nil || !result.known {
				return false
			}
			if literal, ok := node.(*ast.FuncLit); ok {
				if sourceObjectReferenced(literal.Body, value.Obj) {
					result.known = false
				}
				return false
			}
			if node.Pos() >= before {
				return false
			}
			id, ok := node.(*ast.Ident)
			if ok && id.Obj == value.Obj && !sourceDeclarationIdentifier(id, context.parents.parent(id)) {
				result.known = false
			}
			return true
		})
		return result
	case *ast.CallExpr:
		if model, ok := sourceFactoryModel(context.file, value, "Aggregate"); ok {
			return sourceAggregate{recognized: true, known: true, model: model}
		}
		method, ok := value.Fun.(*ast.SelectorExpr)
		if !ok {
			return sourceAggregate{}
		}
		result := sourceAggregateExpression(context, method.X, before, visiting)
		if !result.recognized || !result.known {
			return result
		}
		switch method.Sel.Name {
		case "Select":
			if value.Ellipsis.IsValid() {
				result.known = false
				break
			}
			for _, arg := range value.Args {
				output, ok := sourceAggregateProjection(context.file, arg)
				if !ok {
					result.known = false
					break
				}
				result.outputs = append(result.outputs, output)
			}
		case "GroupBy":
			if value.Ellipsis.IsValid() {
				result.known = false
				break
			}
			for _, arg := range value.Args {
				name, ok := sourceStringConstant(arg, nil)
				if !ok {
					result.known = false
					break
				}
				result.groups = append(result.groups, name)
			}
		case "Where", "Having", "OrderBy", "Limit", "Offset", "WithDeleted", "ReadFrom", "MPP", "Window":
			// These do not change the base projection/grouping contract.
		default:
			result.known = false
		}
		return result
	}
	return sourceAggregate{}
}

func sourceAggregateProjection(file *sourceFile, expression ast.Expr) (sourceAggregateOutput, bool) {
	if paren, ok := expression.(*ast.ParenExpr); ok {
		return sourceAggregateProjection(file, paren.X)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return sourceAggregateOutput{}, false
	}
	var name string
	aliased := false
	if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "As" {
		if len(call.Args) != 1 || call.Ellipsis.IsValid() {
			return sourceAggregateOutput{}, false
		}
		name, ok = sourceStringConstant(call.Args[0], nil)
		if !ok {
			return sourceAggregateOutput{}, false
		}
		call, ok = method.X.(*ast.CallExpr)
		if !ok {
			return sourceAggregateOutput{}, false
		}
		aliased = true
	}
	constructor, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return sourceAggregateOutput{}, false
	}
	qualifier, ok := constructor.X.(*ast.Ident)
	if !ok || qualifier.Obj != nil || qualifier.Name != file.ormAlias || file.ormAlias == "" || call.Ellipsis.IsValid() {
		return sourceAggregateOutput{}, false
	}
	group := false
	key := ""
	switch constructor.Sel.Name {
	case "Field", "Date", "YearMonth":
		group = true
		if len(call.Args) != 1 {
			return sourceAggregateOutput{}, false
		}
		field, known := sourceStringConstant(call.Args[0], nil)
		if !known {
			return sourceAggregateOutput{}, false
		}
		key = constructor.Sel.Name + ":" + field
		if !aliased && constructor.Sel.Name == "Field" {
			name, ok = sourceStringConstant(call.Args[0], nil)
			if !ok {
				return sourceAggregateOutput{}, false
			}
		}
	case "CountAll", "Count", "CountDistinct", "CountIf", "Sum", "SumIf", "Avg", "Min", "Max":
	default:
		return sourceAggregateOutput{}, false
	}
	return sourceAggregateOutput{name: name, group: group, expression: key}, true
}

func sourceAggregateProblem(summary sourceAggregate) string {
	if len(summary.outputs) == 0 {
		return "Aggregate Select requires at least one output"
	}
	names := make(map[string]bool, len(summary.outputs))
	groups := make(map[string]bool, len(summary.groups))
	for _, group := range summary.groups {
		for _, output := range summary.outputs {
			if output.group && output.name == group {
				if groups[output.expression] {
					return fmt.Sprintf("GroupBy %q duplicates a group expression", group)
				}
				groups[output.expression] = true
			}
		}
	}
	for _, output := range summary.outputs {
		if !token.IsIdentifier(output.name) || !ast.IsExported(output.name) || len(output.name) > 64 {
			return fmt.Sprintf("Aggregate output %q requires an exported Go name; use As", output.name)
		}
		fold := strings.ToLower(output.name)
		if names[fold] {
			return fmt.Sprintf("Aggregate output %q is duplicated", output.name)
		}
		names[fold] = true
		if output.group && !groups[output.expression] {
			return fmt.Sprintf("Selected field/calendar output %q is absent from GroupBy", output.name)
		}
	}
	for _, group := range summary.groups {
		valid := false
		for _, output := range summary.outputs {
			valid = valid || (output.group && output.name == group)
		}
		if !valid {
			return fmt.Sprintf("GroupBy %q must reference a selected field/calendar output", group)
		}
	}
	return ""
}
