package sourcecheck

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/queryshape"
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

type sourceOrderTerm struct {
	field     string
	direction queryshape.OrderDirection
}

type sourceToggleState uint8

const (
	sourceToggleUnknown sourceToggleState = iota
	sourceToggleAbsent
	sourceTogglePresent
)

type sourcePredicatePattern struct {
	wildcards      []sourceWildcardPredicate
	equalityFields []string
	known          bool
	indexExact     bool
}

type sourceIndexPattern struct {
	orderTerms           []sourceOrderTerm
	orderTermsKnown      bool
	equalityFields       []string
	indexPredicatesKnown bool
	withDeleted          sourceToggleState
}

type sourceQueryPattern struct {
	limit           sourceBound
	offset          sourceBound
	order           sourceOrderState
	wildcards       []sourceWildcardPredicate
	predicatesKnown bool
	index           *sourceIndexPattern
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
	if analyzer.configuration.schemaEnabled {
		analyzer.recordSchemaIndexPattern(call, summary)
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

func (analyzer *sourceAnalyzer) applySourceOrderCall(
	context sourceFunctionContext,
	pattern sourceQueryPattern,
	call *ast.CallExpr,
	before token.Pos,
) sourceQueryPattern {
	if len(call.Args) == 0 {
		return pattern
	}
	index := pattern.index
	if index == nil {
		pattern.order = applySourceOrderPresence(pattern.order, call)
		return pattern
	}
	if call.Ellipsis.IsValid() {
		pattern.order = applySourceOrderPresence(pattern.order, call)
		index.orderTermsKnown = false
		return pattern
	}
	pattern.order = sourceOrderPresent
	for _, argument := range call.Args {
		term, ok := analyzer.sourceOrderExpression(context, argument, before, nil)
		if !ok {
			index.orderTermsKnown = false
			continue
		}
		index.orderTerms = append(index.orderTerms, term)
	}
	return pattern
}

func applySourceOrderPresence(current sourceOrderState, call *ast.CallExpr) sourceOrderState {
	if len(call.Args) == 0 {
		return current
	}
	if call.Ellipsis.IsValid() && current != sourceOrderPresent {
		return sourceOrderUnknown
	}
	return sourceOrderPresent
}

func (analyzer *sourceAnalyzer) sourceOrderExpression(
	context sourceFunctionContext,
	expression ast.Expr,
	before token.Pos,
	visiting map[*ast.Object]bool,
) (sourceOrderTerm, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.sourceOrderExpression(context, current.X, before, visiting)
	case *ast.Ident:
		if current.Obj == nil || visiting[current.Obj] {
			return sourceOrderTerm{}, false
		}
		definitions := sourceBuilderDefinitions(context.body, current.Obj, before)
		if len(definitions) != 1 {
			return sourceOrderTerm{}, false
		}
		if visiting == nil {
			visiting = make(map[*ast.Object]bool)
		}
		visiting[current.Obj] = true
		result, ok := analyzer.sourceOrderExpression(context, definitions[0].expr, definitions[0].position, visiting)
		delete(visiting, current.Obj)
		return result, ok
	case *ast.CallExpr:
		selector, ok := current.Fun.(*ast.SelectorExpr)
		if !ok || len(current.Args) != 1 || current.Ellipsis.IsValid() {
			return sourceOrderTerm{}, false
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || identifier.Obj != nil || context.file.ormAlias == "" || identifier.Name != context.file.ormAlias {
			return sourceOrderTerm{}, false
		}
		field, ok := sourceStringConstant(current.Args[0], nil)
		if !ok {
			return sourceOrderTerm{}, false
		}
		direction := queryshape.OrderAscending
		switch selector.Sel.Name {
		case "Asc":
		case "Desc":
			direction = queryshape.OrderDescending
		default:
			return sourceOrderTerm{}, false
		}
		return sourceOrderTerm{field: field, direction: direction}, true
	default:
		return sourceOrderTerm{}, false
	}
}

func (analyzer *sourceAnalyzer) sourceWherePredicates(
	context sourceFunctionContext,
	call *ast.CallExpr,
	before token.Pos,
) sourcePredicatePattern {
	result := sourcePredicatePattern{known: true, indexExact: true}
	for index, argument := range call.Args {
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			result.known = false
			result.indexExact = false
			continue
		}
		current, resolved := analyzer.sourcePredicateExpression(context, argument, before, nil, nil)
		result.wildcards = appendSourceWildcards(result.wildcards, current.wildcards)
		result.equalityFields = append(result.equalityFields, current.equalityFields...)
		result.known = result.known && resolved && current.known
		result.indexExact = result.indexExact && current.indexExact
	}
	return result
}

func (analyzer *sourceAnalyzer) sourcePredicateExpression(
	context sourceFunctionContext,
	expression ast.Expr,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
) (sourcePredicatePattern, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.sourcePredicateExpression(context, current.X, before, relations, visiting)
	case *ast.Ident:
		if current.Obj == nil || visiting[current.Obj] {
			return sourcePredicatePattern{}, false
		}
		definitions := sourceBuilderDefinitions(context.body, current.Obj, before)
		if len(definitions) != 1 {
			return sourcePredicatePattern{}, false
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
			return sourcePredicatePattern{}, false
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || identifier.Obj != nil || context.file.ormAlias == "" || identifier.Name != context.file.ormAlias {
			return sourcePredicatePattern{}, false
		}
		return analyzer.sourcePredicateCall(context, current, selector.Sel.Name, before, relations, visiting)
	default:
		return sourcePredicatePattern{}, false
	}
}

func (analyzer *sourceAnalyzer) sourcePredicateCall(
	context sourceFunctionContext,
	call *ast.CallExpr,
	constructor string,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
) (sourcePredicatePattern, bool) {
	switch constructor {
	case "Contains", "HasSuffix":
		if len(call.Args) != 2 || call.Ellipsis.IsValid() {
			return sourcePredicatePattern{}, false
		}
		field := "<dynamic>"
		if resolved, ok := sourceStringConstant(call.Args[0], nil); ok {
			field = resolved
		}
		return sourcePredicatePattern{
			wildcards: []sourceWildcardPredicate{{
				relations: append([]string(nil), relations...),
				field:     field,
				suffix:    constructor == "HasSuffix",
				position:  call.Pos(),
			}},
			known: true,
		}, true
	case "Equal":
		if len(call.Args) != 2 || call.Ellipsis.IsValid() {
			return sourcePredicatePattern{}, false
		}
		if !analyzer.configuration.schemaEnabled {
			return sourcePredicatePattern{known: true}, true
		}
		field, ok := sourceStringConstant(call.Args[0], nil)
		if !ok || len(relations) != 0 {
			return sourcePredicatePattern{known: true}, true
		}
		return sourcePredicatePattern{equalityFields: []string{field}, known: true, indexExact: true}, true
	case "NotEqual", "GreaterThan", "GreaterThanOrEqual", "LessThan", "LessThanOrEqual", "In", "NotIn", "IsNull", "IsNotNull", "Between", "HasPrefix":
		return sourcePredicatePattern{known: true}, true
	case "And":
		if len(call.Args) < 2 || call.Ellipsis.IsValid() {
			return sourcePredicatePattern{}, false
		}
		return analyzer.sourcePredicateArguments(context, call, before, relations, visiting, 0)
	case "Or", "Not":
		result, ok := analyzer.sourcePredicateArguments(context, call, before, relations, visiting, 0)
		result.indexExact = false
		return result, ok
	case "Has":
		if len(call.Args) == 0 {
			return sourcePredicatePattern{}, false
		}
		relation := "<dynamic>"
		if resolved, ok := sourceStringConstant(call.Args[0], nil); ok {
			relation = resolved
		}
		result, ok := analyzer.sourcePredicateArguments(context, call, before, append(relations, relation), visiting, 1)
		result.indexExact = false
		return result, ok
	default:
		return sourcePredicatePattern{}, false
	}
}

func (analyzer *sourceAnalyzer) sourcePredicateArguments(
	context sourceFunctionContext,
	call *ast.CallExpr,
	before token.Pos,
	relations []string,
	visiting map[*ast.Object]bool,
	first int,
) (sourcePredicatePattern, bool) {
	result := sourcePredicatePattern{known: true, indexExact: true}
	for index := first; index < len(call.Args); index++ {
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			result.known = false
			result.indexExact = false
			continue
		}
		current, resolved := analyzer.sourcePredicateExpression(context, call.Args[index], before, relations, visiting)
		result.wildcards = appendSourceWildcards(result.wildcards, current.wildcards)
		result.equalityFields = append(result.equalityFields, current.equalityFields...)
		result.known = result.known && resolved && current.known
		result.indexExact = result.indexExact && current.indexExact
	}
	return result, result.known
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
		index:           mergeSourceIndexPatterns(left.index, right.index),
	}
}

func cloneSourceIndexPattern(current *sourceIndexPattern) *sourceIndexPattern {
	if current == nil {
		return nil
	}
	result := *current
	result.orderTerms = append([]sourceOrderTerm(nil), current.orderTerms...)
	result.equalityFields = append([]string(nil), current.equalityFields...)
	return &result
}

func mergeSourceIndexPatterns(left, right *sourceIndexPattern) *sourceIndexPattern {
	if left == nil || right == nil {
		return nil
	}
	equalityFields, sameEqualities := commonSourceStrings(left.equalityFields, right.equalityFields)
	orderTerms, sameOrderTerms := commonSourceOrderTerms(left.orderTerms, right.orderTerms)
	return &sourceIndexPattern{
		orderTerms:           orderTerms,
		orderTermsKnown:      left.orderTermsKnown && right.orderTermsKnown && sameOrderTerms,
		equalityFields:       equalityFields,
		indexPredicatesKnown: left.indexPredicatesKnown && right.indexPredicatesKnown && sameEqualities,
		withDeleted:          mergeSourceToggle(left.withDeleted, right.withDeleted),
	}
}

func commonSourceStrings(left, right []string) ([]string, bool) {
	if len(left) != len(right) {
		return nil, false
	}
	for index := range left {
		if left[index] != right[index] {
			return nil, false
		}
	}
	return append([]string(nil), left...), true
}

func commonSourceOrderTerms(left, right []sourceOrderTerm) ([]sourceOrderTerm, bool) {
	if len(left) != len(right) {
		return nil, false
	}
	for index := range left {
		if left[index] != right[index] {
			return nil, false
		}
	}
	return append([]sourceOrderTerm(nil), left...), true
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

func mergeSourceToggle(left, right sourceToggleState) sourceToggleState {
	if left == right {
		return left
	}
	return sourceToggleUnknown
}
