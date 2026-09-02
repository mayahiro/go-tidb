package sourcecheck

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/querycheck"
)

type sourceBoundState uint8

const (
	sourceBoundUnknown sourceBoundState = iota
	sourceBoundUnset
	sourceBoundNonPositive
	sourceBoundPositive
)

type sourceBound struct {
	state    sourceBoundState
	value    int64
	exact    bool
	position token.Pos
}

type sourceOrderState uint8

const (
	sourceOrderUnknown sourceOrderState = iota
	sourceOrderAbsent
	sourceOrderPresent
)

type sourceWildcardPredicate struct {
	relations []string
	field     string
	suffix    bool
	position  token.Pos
}

type sourceQueryPattern struct {
	limit           sourceBound
	offset          sourceBound
	order           sourceOrderState
	wildcards       []sourceWildcardPredicate
	predicatesKnown bool
}

type sourceDiagnosticKey struct {
	code   string
	path   string
	line   int
	column int
}

func (analyzer *sourceAnalyzer) recordQueryPattern(call *ast.CallExpr, summary sourceQuerySummary) {
	analyzer.analysis.Statistics.QueryPatterns++
	if _, exists := analyzer.models[summary.model]; exists {
		analyzer.seenModels[summary.model] = struct{}{}
	}

	pattern := summary.pattern
	if sourcePatternIsCertain(pattern) {
		analyzer.analysis.Statistics.AnalyzedPatterns++
	} else {
		analyzer.analysis.Statistics.UncertainPatterns++
	}
	modelName := summary.model.name
	returnsRows := pattern.limit.state == sourceBoundUnset || pattern.limit.state == sourceBoundPositive
	if returnsRows && pattern.offset.state == sourceBoundPositive {
		offset := int64(0)
		if pattern.offset.exact {
			offset = pattern.offset.value
		}
		diagnostic := querycheck.OffsetPaginationDiagnostic(modelName, offset)
		diagnostic.Location = analyzer.sourcePatternLocation(pattern.offset.position, call.Pos())
		analyzer.appendPatternDiagnostic(diagnostic)
	}
	if returnsRows && pattern.limit.state == sourceBoundPositive && pattern.order == sourceOrderAbsent {
		diagnostic := querycheck.UnorderedPaginationDiagnostic(modelName)
		diagnostic.Location = analyzer.sourcePatternLocation(pattern.limit.position, call.Pos())
		analyzer.appendPatternDiagnostic(diagnostic)
	}
	for _, wildcard := range pattern.wildcards {
		scope := modelName
		if len(wildcard.relations) != 0 {
			scope += "." + strings.Join(wildcard.relations, ".")
		}
		diagnostic := querycheck.LeadingWildcardDiagnostic(scope, wildcard.field, wildcard.suffix)
		diagnostic.Location = analyzer.sourcePatternLocation(wildcard.position, call.Pos())
		analyzer.appendPatternDiagnostic(diagnostic)
	}
}

func sourcePatternIsCertain(pattern sourceQueryPattern) bool {
	if !pattern.predicatesKnown || pattern.limit.state == sourceBoundUnknown {
		return false
	}
	returnsRows := pattern.limit.state == sourceBoundUnset || pattern.limit.state == sourceBoundPositive
	if !returnsRows {
		return true
	}
	if pattern.offset.state == sourceBoundUnknown {
		return false
	}
	return pattern.limit.state != sourceBoundPositive || pattern.order != sourceOrderUnknown
}

func (analyzer *sourceAnalyzer) sourcePatternLocation(position, fallback token.Pos) check.Location {
	if !position.IsValid() {
		position = fallback
	}
	return analyzer.sourceLocation(position)
}

func (analyzer *sourceAnalyzer) appendPatternDiagnostic(diagnostic check.Diagnostic) {
	if analyzer.seenPatternDiagnostics == nil {
		analyzer.seenPatternDiagnostics = make(map[sourceDiagnosticKey]struct{})
	}
	key := sourceDiagnosticKey{
		code:   diagnostic.Code,
		path:   diagnostic.Location.Path,
		line:   diagnostic.Location.Line,
		column: diagnostic.Location.Column,
	}
	if _, exists := analyzer.seenPatternDiagnostics[key]; exists {
		return
	}
	analyzer.seenPatternDiagnostics[key] = struct{}{}
	analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, diagnostic)
}

func sourceBoundFromCall(call *ast.CallExpr, position token.Pos) sourceBound {
	if len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return sourceBound{state: sourceBoundUnknown}
	}
	value, ok := sourceInt64Constant(call.Args[0], nil)
	if !ok {
		return sourceBound{state: sourceBoundUnknown}
	}
	state := sourceBoundNonPositive
	if value > 0 {
		state = sourceBoundPositive
	}
	return sourceBound{state: state, value: value, exact: true, position: position}
}

func sourceInt64Constant(expression ast.Expr, visiting map[*ast.Object]bool) (int64, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return sourceInt64Constant(current.X, visiting)
	case *ast.BasicLit:
		if current.Kind != token.INT {
			return 0, false
		}
		value, err := strconv.ParseInt(current.Value, 0, 64)
		return value, err == nil
	case *ast.UnaryExpr:
		value, ok := sourceInt64Constant(current.X, visiting)
		if !ok {
			return 0, false
		}
		switch current.Op {
		case token.ADD:
			return value, true
		case token.SUB:
			return -value, value != -1<<63
		default:
			return 0, false
		}
	case *ast.Ident:
		if current.Obj == nil || current.Obj.Kind != ast.Con || visiting[current.Obj] {
			return 0, false
		}
		value, ok := sourceConstantExpression(current)
		if !ok {
			return 0, false
		}
		if visiting == nil {
			visiting = make(map[*ast.Object]bool)
		}
		visiting[current.Obj] = true
		result, resolved := sourceInt64Constant(value, visiting)
		delete(visiting, current.Obj)
		return result, resolved
	case *ast.CallExpr:
		identifier, ok := current.Fun.(*ast.Ident)
		if !ok || identifier.Obj != nil || !sourceIntegerConversion(identifier.Name) || len(current.Args) != 1 || current.Ellipsis.IsValid() {
			return 0, false
		}
		return sourceInt64Constant(current.Args[0], visiting)
	default:
		return 0, false
	}
}

func sourceIntegerConversion(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	default:
		return false
	}
}

func sourceConstantExpression(identifier *ast.Ident) (ast.Expr, bool) {
	specification, ok := identifier.Obj.Decl.(*ast.ValueSpec)
	if !ok || len(specification.Values) == 0 {
		return nil, false
	}
	for index, name := range specification.Names {
		if name.Obj != identifier.Obj {
			continue
		}
		if len(specification.Values) == len(specification.Names) {
			return specification.Values[index], true
		}
		if len(specification.Names) == 1 {
			return specification.Values[0], true
		}
		return nil, false
	}
	return nil, false
}

func applySourceOrderCall(current sourceOrderState, call *ast.CallExpr) sourceOrderState {
	if len(call.Args) == 0 {
		return current
	}
	if call.Ellipsis.IsValid() {
		if current == sourceOrderPresent {
			return current
		}
		return sourceOrderUnknown
	}
	return sourceOrderPresent
}

func (analyzer *sourceAnalyzer) sourceWherePredicates(
	context sourceFunctionContext,
	call *ast.CallExpr,
	before token.Pos,
) ([]sourceWildcardPredicate, bool) {
	var wildcards []sourceWildcardPredicate
	known := true
	for index, argument := range call.Args {
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			known = false
			continue
		}
		current, resolved := analyzer.sourcePredicateExpression(context, argument, before, nil, nil)
		wildcards = appendSourceWildcards(wildcards, current)
		known = known && resolved
	}
	return wildcards, known
}

func (analyzer *sourceAnalyzer) sourcePredicateExpression(
	context sourceFunctionContext,
	expression ast.Expr,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
) ([]sourceWildcardPredicate, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.sourcePredicateExpression(context, current.X, before, relations, visiting)
	case *ast.Ident:
		if current.Obj == nil || visiting[current.Obj] {
			return nil, false
		}
		definitions := sourceBuilderDefinitions(context.body, current.Obj, before)
		if len(definitions) != 1 {
			return nil, false
		}
		if visiting == nil {
			visiting = make(map[*ast.Object]bool)
		}
		visiting[current.Obj] = true
		result, known := analyzer.sourcePredicateExpression(context, definitions[0].expr, definitions[0].position, relations, visiting)
		delete(visiting, current.Obj)
		return result, known
	case *ast.CallExpr:
		selector, ok := current.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil, false
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || identifier.Obj != nil || context.file.ormAlias == "" || identifier.Name != context.file.ormAlias {
			return nil, false
		}
		return analyzer.sourcePredicateCall(context, current, selector.Sel.Name, before, relations, visiting)
	default:
		return nil, false
	}
}

func (analyzer *sourceAnalyzer) sourcePredicateCall(
	context sourceFunctionContext,
	call *ast.CallExpr,
	constructor string,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
) ([]sourceWildcardPredicate, bool) {
	switch constructor {
	case "Contains", "HasSuffix":
		if len(call.Args) != 2 || call.Ellipsis.IsValid() {
			return nil, false
		}
		field := "<dynamic>"
		if resolved, ok := sourceStringConstant(call.Args[0], nil); ok {
			field = resolved
		}
		return []sourceWildcardPredicate{{
			relations: append([]string(nil), relations...),
			field:     field,
			suffix:    constructor == "HasSuffix",
			position:  call.Pos(),
		}}, true
	case "Equal", "NotEqual", "GreaterThan", "GreaterThanOrEqual", "LessThan", "LessThanOrEqual", "In", "NotIn", "IsNull", "IsNotNull", "Between", "HasPrefix":
		return nil, true
	case "And", "Or", "Not":
		return analyzer.sourcePredicateArguments(context, call, before, relations, visiting, 0)
	case "Has":
		if len(call.Args) == 0 {
			return nil, false
		}
		relation := "<dynamic>"
		if resolved, ok := sourceStringConstant(call.Args[0], nil); ok {
			relation = resolved
		}
		return analyzer.sourcePredicateArguments(context, call, before, append(relations, relation), visiting, 1)
	default:
		return nil, false
	}
}

func (analyzer *sourceAnalyzer) sourcePredicateArguments(
	context sourceFunctionContext,
	call *ast.CallExpr,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
	first int,
) ([]sourceWildcardPredicate, bool) {
	var wildcards []sourceWildcardPredicate
	known := true
	for index := first; index < len(call.Args); index++ {
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			known = false
			continue
		}
		current, resolved := analyzer.sourcePredicateExpression(context, call.Args[index], before, relations, visiting)
		wildcards = appendSourceWildcards(wildcards, current)
		known = known && resolved
	}
	return wildcards, known
}

func sourceStringConstant(expression ast.Expr, visiting map[*ast.Object]bool) (string, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return sourceStringConstant(current.X, visiting)
	case *ast.BasicLit:
		if current.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(current.Value)
		return value, err == nil
	case *ast.Ident:
		if current.Obj == nil || current.Obj.Kind != ast.Con || visiting[current.Obj] {
			return "", false
		}
		value, ok := sourceConstantExpression(current)
		if !ok {
			return "", false
		}
		if visiting == nil {
			visiting = make(map[*ast.Object]bool)
		}
		visiting[current.Obj] = true
		result, resolved := sourceStringConstant(value, visiting)
		delete(visiting, current.Obj)
		return result, resolved
	default:
		return "", false
	}
}

func appendSourceWildcards(target, values []sourceWildcardPredicate) []sourceWildcardPredicate {
	for _, value := range values {
		duplicate := false
		for _, existing := range target {
			if existing.position == value.position && existing.suffix == value.suffix {
				duplicate = true
				break
			}
		}
		if !duplicate {
			target = append(target, value)
		}
	}
	return target
}

func mergeSourceQueryPatterns(left, right sourceQueryPattern) sourceQueryPattern {
	wildcards, sameWildcards := commonSourceWildcards(left.wildcards, right.wildcards)
	return sourceQueryPattern{
		limit:           mergeSourceBounds(left.limit, right.limit),
		offset:          mergeSourceBounds(left.offset, right.offset),
		order:           mergeSourceOrder(left.order, right.order),
		wildcards:       wildcards,
		predicatesKnown: left.predicatesKnown && right.predicatesKnown && sameWildcards,
	}
}

func commonSourceWildcards(left, right []sourceWildcardPredicate) ([]sourceWildcardPredicate, bool) {
	if len(left) == 0 && len(right) == 0 {
		return nil, true
	}
	common := make([]sourceWildcardPredicate, 0, min(len(left), len(right)))
	for _, candidate := range left {
		if sourceWildcardContains(right, candidate) {
			common = append(common, candidate)
		}
	}
	return common, len(common) == len(left) && len(common) == len(right)
}

func sourceWildcardContains(values []sourceWildcardPredicate, target sourceWildcardPredicate) bool {
	for _, value := range values {
		if value.position != target.position || value.suffix != target.suffix || value.field != target.field || len(value.relations) != len(target.relations) {
			continue
		}
		matched := true
		for index := range value.relations {
			if value.relations[index] != target.relations[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func mergeSourceBounds(left, right sourceBound) sourceBound {
	if left.state != right.state {
		return sourceBound{state: sourceBoundUnknown}
	}
	result := sourceBound{state: left.state}
	if left.state != sourceBoundPositive {
		return result
	}
	if left.exact && right.exact && left.value == right.value {
		result.value = left.value
		result.exact = true
	}
	if left.position == right.position {
		result.position = left.position
	}
	return result
}

func mergeSourceOrder(left, right sourceOrderState) sourceOrderState {
	if left == right {
		return left
	}
	return sourceOrderUnknown
}
