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

type sourceOrderTerms struct {
	first      sourceOrderTerm
	additional []sourceOrderTerm
	count      int
}

type sourceDeferredOrderCall struct {
	file   *sourceFile
	body   *ast.BlockStmt
	call   *ast.CallExpr
	before token.Pos
}

type sourceDeferredOrderCalls struct {
	first      sourceDeferredOrderCall
	additional []sourceDeferredOrderCall
	count      int
}

func (calls *sourceDeferredOrderCalls) append(call sourceDeferredOrderCall) {
	if calls.count == 0 {
		calls.first = call
	} else {
		calls.additional = append(calls.additional, call)
	}
	calls.count++
}

func (calls sourceDeferredOrderCalls) at(index int) sourceDeferredOrderCall {
	if index == 0 {
		return calls.first
	}
	return calls.additional[index-1]
}

func (calls sourceDeferredOrderCalls) clone() sourceDeferredOrderCalls {
	calls.additional = append([]sourceDeferredOrderCall(nil), calls.additional...)
	return calls
}

func (terms *sourceOrderTerms) append(term sourceOrderTerm) {
	if terms.count == 0 {
		terms.first = term
	} else {
		terms.additional = append(terms.additional, term)
	}
	terms.count++
}

func (terms sourceOrderTerms) at(index int) sourceOrderTerm {
	if index == 0 {
		return terms.first
	}
	return terms.additional[index-1]
}

func (terms sourceOrderTerms) clone() sourceOrderTerms {
	terms.additional = append([]sourceOrderTerm(nil), terms.additional...)
	return terms
}

type sourceToggleState uint8

const (
	sourceToggleUnknown sourceToggleState = iota
	sourceToggleAbsent
	sourceTogglePresent
)

type sourcePredicatePattern struct {
	wildcards          []sourceWildcardPredicate
	equalityFields     []string
	hasPredicates      []sourceHasPredicate
	rootPredicateCount int
	rootCountKnown     bool
	known              bool
	indexExact         bool
}

type sourceHasPredicate struct {
	relation       string
	relationKnown  bool
	direct         bool
	equalityFields []string
	indexExact     bool
	position       token.Pos
}

type sourceIndexPattern struct {
	equalityFields       []string
	indexPredicatesKnown bool
}

type sourceQueryPattern struct {
	limit              sourceBound
	offset             sourceBound
	order              sourceOrderState
	orderTerms         sourceOrderTerms
	orderTermsKnown    bool
	deferredOrderCalls sourceDeferredOrderCalls
	wildcards          []sourceWildcardPredicate
	predicatesKnown    bool
	rootPredicateCount int
	rootCountKnown     bool
	hasPredicates      []sourceHasPredicate
	seekAfter          sourceToggleState
	withDeleted        sourceToggleState
	index              *sourceIndexPattern
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

	if len(summary.pattern.hasPredicates) != 0 && summary.pattern.deferredOrderCalls.count != 0 {
		summary.pattern = analyzer.resolveDeferredSourceOrderCalls(summary.pattern)
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
	relationTopN := analyzer.analyzeSourceRelationTopN(summary)
	if relationTopN.candidate {
		analyzer.analysis.Statistics.RelationTopNPatterns++
		if relationTopN.exact {
			analyzer.analysis.Statistics.AnalyzedRelationTopNPatterns++
		} else {
			analyzer.analysis.Statistics.UncertainRelationTopNPatterns++
		}
		if relationTopN.exact && relationTopN.decision.Rewrite == queryshape.CompilerRewriteRelationTopNFallback {
			diagnostic := querycheck.RelationTopNFallbackDiagnostic(
				modelName,
				relationTopN.decision.Relation,
				relationTopN.decision.Reason,
			)
			diagnostic.Location = analyzer.sourcePatternLocation(relationTopN.predicate.position, call.Pos())
			analyzer.appendPatternDiagnostic(diagnostic)
		}
	}
	if analyzer.configuration.schemaEnabled {
		analyzer.recordSchemaIndexPattern(call, summary, relationTopN)
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
	if call.Ellipsis.IsValid() {
		pattern.order = applySourceOrderPresence(pattern.order, call)
		pattern.orderTermsKnown = false
		return pattern
	}
	pattern.order = sourceOrderPresent
	if !analyzer.configuration.schemaEnabled && len(pattern.hasPredicates) == 0 {
		pattern.deferredOrderCalls.append(sourceDeferredOrderCall{
			file:   context.file,
			body:   context.body,
			call:   call,
			before: before,
		})
		return pattern
	}
	return analyzer.applyResolvedSourceOrderCall(pattern, context, call, before)
}

func (analyzer *sourceAnalyzer) applyResolvedSourceOrderCall(
	pattern sourceQueryPattern,
	context sourceFunctionContext,
	call *ast.CallExpr,
	before token.Pos,
) sourceQueryPattern {
	for _, argument := range call.Args {
		term, ok := analyzer.sourceOrderExpression(context, argument, before, nil)
		if !ok {
			pattern.orderTermsKnown = false
			continue
		}
		pattern.orderTerms.append(term)
	}
	return pattern
}

func (analyzer *sourceAnalyzer) resolveDeferredSourceOrderCalls(pattern sourceQueryPattern) sourceQueryPattern {
	for index := 0; index < pattern.deferredOrderCalls.count; index++ {
		deferred := pattern.deferredOrderCalls.at(index)
		context := sourceFunctionContext{file: deferred.file, body: deferred.body}
		pattern = analyzer.applyResolvedSourceOrderCall(pattern, context, deferred.call, deferred.before)
	}
	pattern.deferredOrderCalls = sourceDeferredOrderCalls{}
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
	result := sourcePredicatePattern{known: true, indexExact: true, rootCountKnown: true}
	for index, argument := range call.Args {
		if call.Ellipsis.IsValid() && index == len(call.Args)-1 {
			result.known = false
			result.indexExact = false
			result.rootCountKnown = false
			continue
		}
		result.rootPredicateCount++
		current, resolved := analyzer.sourcePredicateExpression(context, argument, before, nil, true, nil)
		result.wildcards = appendSourceWildcards(result.wildcards, current.wildcards)
		result.equalityFields = append(result.equalityFields, current.equalityFields...)
		result.hasPredicates = append(result.hasPredicates, current.hasPredicates...)
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
	direct bool,
	visiting map[*ast.Object]bool,
) (sourcePredicatePattern, bool) {
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.sourcePredicateExpression(context, current.X, before, relations, direct, visiting)
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
		result, known := analyzer.sourcePredicateExpression(context, definitions[0].expr, definitions[0].position, relations, direct, visiting)
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
		return analyzer.sourcePredicateCall(context, current, selector.Sel.Name, before, relations, direct, visiting)
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
	direct bool,
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
		field, ok := sourceStringConstant(call.Args[0], nil)
		if !ok {
			return sourcePredicatePattern{known: true}, true
		}
		return sourcePredicatePattern{equalityFields: []string{field}, known: true, indexExact: true}, true
	case "NotEqual", "GreaterThan", "GreaterThanOrEqual", "LessThan", "LessThanOrEqual", "In", "NotIn", "IsNull", "IsNotNull", "Between", "HasPrefix":
		return sourcePredicatePattern{known: true}, true
	case "And":
		if len(call.Args) < 2 || call.Ellipsis.IsValid() {
			return sourcePredicatePattern{}, false
		}
		return analyzer.sourcePredicateArguments(context, call, before, relations, false, visiting, 0)
	case "Or", "Not":
		result, ok := analyzer.sourcePredicateArguments(context, call, before, relations, false, visiting, 0)
		result.equalityFields = nil
		result.indexExact = false
		return result, ok
	case "Has":
		if len(call.Args) == 0 {
			return sourcePredicatePattern{}, false
		}
		relation := "<dynamic>"
		relationKnown := false
		if resolved, ok := sourceStringConstant(call.Args[0], nil); ok {
			relation = resolved
			relationKnown = true
		}
		rootRelation := len(relations) == 0
		result, ok := analyzer.sourcePredicateArguments(context, call, before, append(relations, relation), false, visiting, 1)
		if rootRelation {
			result.hasPredicates = []sourceHasPredicate{{
				relation:       relation,
				relationKnown:  relationKnown,
				direct:         direct,
				equalityFields: append([]string(nil), result.equalityFields...),
				indexExact:     result.indexExact && result.known,
				position:       call.Pos(),
			}}
		} else {
			result.hasPredicates = nil
		}
		result.equalityFields = nil
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
	direct bool,
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
		current, resolved := analyzer.sourcePredicateExpression(context, call.Args[index], before, relations, direct, visiting)
		result.wildcards = appendSourceWildcards(result.wildcards, current.wildcards)
		result.equalityFields = append(result.equalityFields, current.equalityFields...)
		result.hasPredicates = append(result.hasPredicates, current.hasPredicates...)
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
	orderTerms, sameOrderTerms := commonSourceOrderTerms(left.orderTerms, right.orderTerms)
	deferredOrderCalls, sameDeferredOrderCalls := commonSourceDeferredOrderCalls(left.deferredOrderCalls, right.deferredOrderCalls)
	hasPredicates, sameHasPredicates := commonSourceHasPredicates(left.hasPredicates, right.hasPredicates)
	rootPredicateCount, sameRootPredicateCount := commonSourceInt(left.rootPredicateCount, right.rootPredicateCount)
	return sourceQueryPattern{
		limit:              mergeSourceBounds(left.limit, right.limit),
		offset:             mergeSourceBounds(left.offset, right.offset),
		order:              mergeSourceOrder(left.order, right.order),
		orderTerms:         orderTerms,
		orderTermsKnown:    left.orderTermsKnown && right.orderTermsKnown && sameOrderTerms && sameDeferredOrderCalls,
		deferredOrderCalls: deferredOrderCalls,
		wildcards:          wildcards,
		predicatesKnown:    left.predicatesKnown && right.predicatesKnown && sameWildcards && sameHasPredicates,
		rootPredicateCount: rootPredicateCount,
		rootCountKnown:     left.rootCountKnown && right.rootCountKnown && sameRootPredicateCount,
		hasPredicates:      hasPredicates,
		seekAfter:          mergeSourceToggle(left.seekAfter, right.seekAfter),
		withDeleted:        mergeSourceToggle(left.withDeleted, right.withDeleted),
		index:              mergeSourceIndexPatterns(left.index, right.index),
	}
}

func cloneSourceIndexPattern(current *sourceIndexPattern) *sourceIndexPattern {
	if current == nil {
		return nil
	}
	result := *current
	result.equalityFields = append([]string(nil), current.equalityFields...)
	return &result
}

func mergeSourceIndexPatterns(left, right *sourceIndexPattern) *sourceIndexPattern {
	if left == nil || right == nil {
		return nil
	}
	equalityFields, sameEqualities := commonSourceStrings(left.equalityFields, right.equalityFields)
	return &sourceIndexPattern{
		equalityFields:       equalityFields,
		indexPredicatesKnown: left.indexPredicatesKnown && right.indexPredicatesKnown && sameEqualities,
	}
}

func commonSourceHasPredicates(left, right []sourceHasPredicate) ([]sourceHasPredicate, bool) {
	if len(left) != len(right) {
		return nil, false
	}
	result := make([]sourceHasPredicate, len(left))
	for index := range left {
		if left[index].relation != right[index].relation || left[index].relationKnown != right[index].relationKnown ||
			left[index].direct != right[index].direct || left[index].indexExact != right[index].indexExact {
			return nil, false
		}
		equalities, same := commonSourceStrings(left[index].equalityFields, right[index].equalityFields)
		if !same {
			return nil, false
		}
		result[index] = left[index]
		result[index].equalityFields = equalities
		if left[index].position != right[index].position {
			result[index].position = token.NoPos
		}
	}
	return result, true
}

func commonSourceInt(left, right int) (int, bool) {
	if left != right {
		return 0, false
	}
	return left, true
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

func commonSourceOrderTerms(left, right sourceOrderTerms) (sourceOrderTerms, bool) {
	if left.count != right.count {
		return sourceOrderTerms{}, false
	}
	for index := 0; index < left.count; index++ {
		if left.at(index) != right.at(index) {
			return sourceOrderTerms{}, false
		}
	}
	return left.clone(), true
}

func commonSourceDeferredOrderCalls(left, right sourceDeferredOrderCalls) (sourceDeferredOrderCalls, bool) {
	if left.count != right.count {
		return sourceDeferredOrderCalls{}, false
	}
	for index := 0; index < left.count; index++ {
		leftCall := left.at(index)
		rightCall := right.at(index)
		if leftCall.file != rightCall.file || leftCall.body != rightCall.body || leftCall.call != rightCall.call || leftCall.before != rightCall.before {
			return sourceDeferredOrderCalls{}, false
		}
	}
	return left.clone(), true
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
