package sourcecheck

import (
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/check"
)

const codeNarrowProjection = "SRC001"

type sourceFunctionKey struct {
	packagePath string
	name        string
}

type sourceQueryFunction struct {
	file  *sourceFile
	decl  *ast.FuncDecl
	model sourceTypeKey
}

type queryProjection uint8

const (
	queryProjectionUnknown queryProjection = iota
	queryProjectionDefault
	queryProjectionExplicit
)

type sourceQuerySummary struct {
	recognized bool
	model      sourceTypeKey
	projection queryProjection
	preload    bool
	pattern    sourceQueryPattern
}

type sourceFunctionContext struct {
	file    *sourceFile
	body    *ast.BlockStmt
	parents sourceParents
}

type sourceAnalyzer struct {
	configuration          analysisConfiguration
	fileSet                *token.FileSet
	files                  []*sourceFile
	models                 map[sourceTypeKey]*sourceModel
	queryFunctions         map[sourceFunctionKey]*sourceQueryFunction
	functionCache          map[sourceFunctionKey]sourceQuerySummary
	relationCache          map[sourceRelationKey]sourceRelationResult
	seenModels             map[sourceTypeKey]struct{}
	seenPatternDiagnostics map[sourceDiagnosticKey]struct{}
	analysis               Analysis
}

func newSourceAnalyzer(fileSet *token.FileSet, files []*sourceFile, configuration analysisConfiguration) *sourceAnalyzer {
	analyzer := &sourceAnalyzer{
		configuration:  configuration,
		fileSet:        fileSet,
		files:          files,
		models:         indexSourceModels(files, configuration.schemaEnabled),
		queryFunctions: make(map[sourceFunctionKey]*sourceQueryFunction),
		functionCache:  make(map[sourceFunctionKey]sourceQuerySummary),
		seenModels:     make(map[sourceTypeKey]struct{}),
		analysis: Analysis{
			Statistics:  Statistics{Files: len(files)},
			Diagnostics: make([]check.Diagnostic, 0),
		},
	}
	analyzer.indexQueryFunctions()
	return analyzer
}

func (analyzer *sourceAnalyzer) analyze() Analysis {
	for _, file := range analyzer.files {
		for _, declaration := range file.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			analyzer.analyzeFunctionBody(file, function.Body)
		}
	}
	analyzer.analysis.Statistics.ModelTypes = len(analyzer.seenModels)
	sort.SliceStable(analyzer.analysis.Diagnostics, func(left, right int) bool {
		leftLocation := analyzer.analysis.Diagnostics[left].Location
		rightLocation := analyzer.analysis.Diagnostics[right].Location
		if leftLocation.Path != rightLocation.Path {
			return leftLocation.Path < rightLocation.Path
		}
		if leftLocation.Line != rightLocation.Line {
			return leftLocation.Line < rightLocation.Line
		}
		if leftLocation.Column != rightLocation.Column {
			return leftLocation.Column < rightLocation.Column
		}
		return analyzer.analysis.Diagnostics[left].Code < analyzer.analysis.Diagnostics[right].Code
	})
	return analyzer.analysis
}

func (analyzer *sourceAnalyzer) indexQueryFunctions() {
	for _, file := range analyzer.files {
		for _, declaration := range file.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Body == nil {
				continue
			}
			model, ok := selectQueryResultModel(file, function.Type.Results)
			if !ok {
				continue
			}
			key := sourceFunctionKey{packagePath: file.packageKey, name: function.Name.Name}
			analyzer.queryFunctions[key] = &sourceQueryFunction{file: file, decl: function, model: model}
		}
	}
}

func selectQueryResultModel(file *sourceFile, results *ast.FieldList) (sourceTypeKey, bool) {
	if results == nil || len(results.List) != 1 {
		return sourceTypeKey{}, false
	}
	expression := results.List[0].Type
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	var genericBase ast.Expr
	var modelExpression ast.Expr
	switch current := expression.(type) {
	case *ast.IndexExpr:
		genericBase = current.X
		modelExpression = current.Index
	case *ast.IndexListExpr:
		if len(current.Indices) != 1 {
			return sourceTypeKey{}, false
		}
		genericBase = current.X
		modelExpression = current.Indices[0]
	default:
		return sourceTypeKey{}, false
	}
	selector, ok := genericBase.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "SelectQuery" {
		return sourceTypeKey{}, false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || identifier.Obj != nil || file.ormAlias == "" || identifier.Name != file.ormAlias {
		return sourceTypeKey{}, false
	}
	return file.sourceType(modelExpression)
}

func (analyzer *sourceAnalyzer) analyzeFunctionBody(file *sourceFile, body *ast.BlockStmt) {
	finder := sourceTerminalFinder{analyzer: analyzer, file: file}
	ast.Walk(&finder, body)
	if !finder.found {
		return
	}
	context := sourceFunctionContext{file: file, body: body, parents: sourceParentMap(body)}
	visitor := &sourceTerminalAnalyzer{analyzer: analyzer, context: context}
	ast.Walk(visitor, body)
}

type sourceTerminalFinder struct {
	analyzer *sourceAnalyzer
	file     *sourceFile
	found    bool
}

func (finder *sourceTerminalFinder) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if literal, nested := node.(*ast.FuncLit); nested {
		finder.analyzer.analyzeFunctionBody(finder.file, literal.Body)
		return nil
	}
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return finder
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if ok && sourceQueryPatternTerminal(selector.Sel.Name) {
		finder.found = true
	}
	return finder
}

type sourceTerminalAnalyzer struct {
	analyzer *sourceAnalyzer
	context  sourceFunctionContext
}

func (visitor *sourceTerminalAnalyzer) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if _, nested := node.(*ast.FuncLit); nested {
		return nil
	}
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return visitor
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !sourceQueryPatternTerminal(selector.Sel.Name) {
		return visitor
	}
	summary := visitor.analyzer.summarizeQueryExpression(visitor.context, selector.X, call.Pos(), nil, nil)
	if !summary.recognized {
		return visitor
	}
	visitor.analyzer.recordQueryPattern(call, summary)
	if sourceResultTerminal(selector.Sel.Name) {
		visitor.analyzer.recordResultQuery(visitor.context, call, selector.Sel.Name, summary)
	}
	return visitor
}

func sourceQueryPatternTerminal(name string) bool {
	switch name {
	case "All", "First", "Only", "Build", "Explain", "ExplainAnalyze":
		return true
	default:
		return false
	}
}

func sourceResultTerminal(name string) bool {
	switch name {
	case "All", "First", "Only":
		return true
	default:
		return false
	}
}

func (analyzer *sourceAnalyzer) recordResultQuery(context sourceFunctionContext, call *ast.CallExpr, terminal string, summary sourceQuerySummary) {
	analyzer.analysis.Statistics.ResultQueries++
	model, modelFound := analyzer.models[summary.model]
	if modelFound {
		analyzer.seenModels[summary.model] = struct{}{}
	}
	if summary.projection == queryProjectionExplicit {
		analyzer.analysis.Statistics.ExplicitProjections++
		return
	}
	if summary.projection != queryProjectionDefault || summary.preload || !modelFound || model.ambiguous || len(model.fields) == 0 {
		analyzer.analysis.Statistics.Uncertain++
		return
	}

	result, ok := sourceTerminalResult(context.parents, call)
	if !ok || result.Obj == nil {
		analyzer.analysis.Statistics.Uncertain++
		return
	}
	usage := collectSourceResultUsage(context, result.Obj, model, terminal == "All", make(map[*ast.Object]bool))
	if !usage.safe {
		analyzer.analysis.Statistics.Uncertain++
		return
	}
	analyzer.analysis.Statistics.Analyzed++
	if len(usage.fields) == 0 || len(usage.fields) >= len(model.fields) {
		return
	}
	analyzer.analysis.Diagnostics = append(analyzer.analysis.Diagnostics, analyzer.narrowProjectionDiagnostic(call, model, usage))
}

type sourceFieldUsage struct {
	fields map[string]token.Pos
	safe   bool
}

func collectSourceResultUsage(context sourceFunctionContext, object *ast.Object, model *sourceModel, slice bool, visiting map[*ast.Object]bool) sourceFieldUsage {
	if object == nil || visiting[object] {
		return sourceFieldUsage{safe: false}
	}
	visiting[object] = true
	defer delete(visiting, object)

	result := sourceFieldUsage{fields: make(map[string]token.Pos), safe: true}
	ast.Inspect(context.body, func(node ast.Node) bool {
		if !result.safe || node == nil {
			return result.safe
		}
		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Obj != object {
			return true
		}
		parent := context.parents.parent(identifier)
		if sourceDeclarationIdentifier(identifier, parent) {
			return true
		}
		parent, child := sourceTransparentParent(context.parents, parent, identifier)
		switch current := parent.(type) {
		case *ast.SelectorExpr:
			if current.X != child || !sourceRecordField(result.fields, model, current.Sel.Name, current.Sel.Pos()) {
				result.safe = false
			}
		case *ast.IndexExpr:
			if !slice || current.X != child {
				result.safe = false
				break
			}
			grandParent, indexed := sourceTransparentParent(context.parents, context.parents.parent(current), current)
			selector, ok := grandParent.(*ast.SelectorExpr)
			if !ok || selector.X != indexed || !sourceRecordField(result.fields, model, selector.Sel.Name, selector.Sel.Pos()) {
				result.safe = false
			}
		case *ast.RangeStmt:
			if !slice || current.X != child {
				result.safe = false
				break
			}
			if value, ok := current.Value.(*ast.Ident); ok && value.Name != "_" && value.Obj != nil {
				nested := collectSourceResultUsage(context, value.Obj, model, false, visiting)
				if !nested.safe {
					result.safe = false
					break
				}
				for field, position := range nested.fields {
					if _, exists := result.fields[field]; !exists {
						result.fields[field] = position
					}
				}
			}
		case *ast.CallExpr:
			if !sourceLengthCall(current, child) {
				result.safe = false
			}
		case *ast.BinaryExpr:
			if !sourceNilComparison(current, child) {
				result.safe = false
			}
		default:
			result.safe = false
		}
		return true
	})
	return result
}

func sourceRecordField(fields map[string]token.Pos, model *sourceModel, name string, position token.Pos) bool {
	if _, exists := model.fieldSet[name]; !exists {
		return false
	}
	if _, exists := fields[name]; !exists {
		fields[name] = position
	}
	return true
}

func sourceDeclarationIdentifier(identifier *ast.Ident, parent ast.Node) bool {
	switch current := parent.(type) {
	case *ast.AssignStmt:
		if current.Tok != token.DEFINE {
			return false
		}
		for _, expression := range current.Lhs {
			if expression == identifier {
				return true
			}
		}
	case *ast.RangeStmt:
		return current.Tok == token.DEFINE && (current.Key == identifier || current.Value == identifier)
	case *ast.ValueSpec:
		for _, name := range current.Names {
			if name == identifier {
				return true
			}
		}
	}
	return false
}

func sourceTransparentParent(parents sourceParents, parent ast.Node, child ast.Node) (ast.Node, ast.Node) {
	for {
		parentheses, ok := parent.(*ast.ParenExpr)
		if !ok || parentheses.X != child {
			return parent, child
		}
		child = parentheses
		parent = parents.parent(parentheses)
	}
}

func sourceLengthCall(call *ast.CallExpr, child ast.Node) bool {
	if len(call.Args) != 1 || call.Args[0] != child {
		return false
	}
	function, ok := call.Fun.(*ast.Ident)
	return ok && function.Obj == nil && (function.Name == "len" || function.Name == "cap")
}

func sourceNilComparison(expression *ast.BinaryExpr, child ast.Node) bool {
	if expression.Op != token.EQL && expression.Op != token.NEQ {
		return false
	}
	var other ast.Expr
	if expression.X == child {
		other = expression.Y
	} else if expression.Y == child {
		other = expression.X
	} else {
		return false
	}
	identifier, ok := other.(*ast.Ident)
	return ok && identifier.Name == "nil" && identifier.Obj == nil
}

func sourceTerminalResult(parents sourceParents, call *ast.CallExpr) (*ast.Ident, bool) {
	var child ast.Node = call
	parent := parents.parent(call)
	for {
		parentheses, ok := parent.(*ast.ParenExpr)
		if !ok || parentheses.X != child {
			break
		}
		child = parentheses
		parent = parents.parent(parentheses)
	}
	assignment, ok := parent.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Rhs) != 1 || assignment.Rhs[0] != child || len(assignment.Lhs) == 0 {
		return nil, false
	}
	identifier, ok := assignment.Lhs[0].(*ast.Ident)
	return identifier, ok && identifier.Name != "_"
}

func (analyzer *sourceAnalyzer) narrowProjectionDiagnostic(call *ast.CallExpr, model *sourceModel, usage sourceFieldUsage) check.Diagnostic {
	used := make([]string, 0, len(usage.fields))
	unused := make([]string, 0, len(model.fields)-len(usage.fields))
	for _, field := range model.fields {
		if _, exists := usage.fields[field]; exists {
			used = append(used, field)
		} else {
			unused = append(unused, field)
		}
	}
	evidence := make([]check.Evidence, 0, len(used)+1)
	evidence = append(evidence, check.Evidence{
		Message: "Default projection fields not observed in the local result flow: " + sourceQualifiedFields(model.name, unused),
	})
	for _, field := range used {
		evidence = append(evidence, check.Evidence{
			Message:  "Observed result field: " + model.name + "." + field,
			Location: analyzer.sourceLocation(usage.fields[field]),
		})
	}
	return check.Diagnostic{
		Code:     codeNarrowProjection,
		Severity: check.SeverityWarning,
		Title:    "Query can use a narrower projection",
		Message: "The local " + model.name + " result flow uses " + strconv.Itoa(len(used)) +
			" of " + strconv.Itoa(len(model.fields)) + " mapped fields from the default projection",
		Evidence:     evidence,
		Suggestion:   "Add Select(" + sourceQuotedFields(used) + ") before the terminal when this local result is intentionally partial",
		Location:     analyzer.sourceLocation(call.Pos()),
		Suppressible: true,
	}
}

func sourceQualifiedFields(model string, fields []string) string {
	values := make([]string, len(fields))
	for index, field := range fields {
		values[index] = model + "." + field
	}
	return strings.Join(values, ", ")
}

func sourceQuotedFields(fields []string) string {
	values := make([]string, len(fields))
	for index, field := range fields {
		values[index] = strconv.Quote(field)
	}
	return strings.Join(values, ", ")
}

func (analyzer *sourceAnalyzer) sourceLocation(position token.Pos) check.Location {
	resolved := analyzer.fileSet.PositionFor(position, false)
	return check.Location{Path: resolved.Filename, Line: resolved.Line, Column: resolved.Column}
}

type sourceParent struct {
	node   ast.Node
	parent ast.Node
}

type sourceParents []sourceParent

func (parents sourceParents) parent(node ast.Node) ast.Node {
	for index := len(parents) - 1; index >= 0; index-- {
		if parents[index].node == node {
			return parents[index].parent
		}
	}
	return nil
}

func sourceParentMap(root ast.Node) sourceParents {
	parents := make(sourceParents, 0, sourceParentCapacity(root))
	stack := make([]ast.Node, 0, 8)
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) != 0 && sourceNeedsParent(node) {
			parents = append(parents, sourceParent{node: node, parent: stack[len(stack)-1]})
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func sourceParentCapacity(root ast.Node) int {
	// Query-heavy functions approach one tracked AST node per four source
	// bytes. Bound the hint so large literals do not cause proportional waste.
	capacity := int(root.End()-root.Pos()) / 4
	if capacity < 8 {
		return 8
	}
	if capacity > 128 {
		return 128
	}
	return capacity
}

func sourceNeedsParent(node ast.Node) bool {
	switch node.(type) {
	case *ast.Ident, *ast.CallExpr, *ast.ParenExpr, *ast.SelectorExpr, *ast.IndexExpr:
		return true
	default:
		return false
	}
}

func (analyzer *sourceAnalyzer) summarizeQueryExpression(
	context sourceFunctionContext,
	expression ast.Expr,
	before token.Pos,
	functionStack map[sourceFunctionKey]bool,
	objectStack map[*ast.Object]bool,
) sourceQuerySummary {
	if expression == nil {
		return sourceQuerySummary{}
	}
	switch current := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.summarizeQueryExpression(context, current.X, before, functionStack, objectStack)
	case *ast.Ident:
		return analyzer.summarizeBuilderObject(context, current, before, functionStack, objectStack)
	case *ast.CallExpr:
		if model, ok := sourceQueryFactoryModel(context.file, current); ok {
			return newSourceQuerySummary(model, analyzer.configuration.schemaEnabled)
		}
		if selector, ok := current.Fun.(*ast.SelectorExpr); ok {
			summary := analyzer.summarizeQueryExpression(context, selector.X, before, functionStack, objectStack)
			return analyzer.applySourceQueryMethod(context, summary, current, before)
		}
		if identifier, ok := current.Fun.(*ast.Ident); ok && (identifier.Obj == nil || identifier.Obj.Kind == ast.Fun) {
			key := sourceFunctionKey{packagePath: context.file.packageKey, name: identifier.Name}
			return analyzer.summarizeQueryFunction(key, functionStack)
		}
	}
	return sourceQuerySummary{}
}

func sourceQueryFactoryModel(file *sourceFile, call *ast.CallExpr) (sourceTypeKey, bool) {
	var genericBase ast.Expr
	var modelExpression ast.Expr
	switch current := call.Fun.(type) {
	case *ast.IndexExpr:
		genericBase = current.X
		modelExpression = current.Index
	case *ast.IndexListExpr:
		if len(current.Indices) != 1 {
			return sourceTypeKey{}, false
		}
		genericBase = current.X
		modelExpression = current.Indices[0]
	default:
		return sourceTypeKey{}, false
	}
	selector, ok := genericBase.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Query" {
		return sourceTypeKey{}, false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || identifier.Obj != nil || file.ormAlias == "" || identifier.Name != file.ormAlias {
		return sourceTypeKey{}, false
	}
	return file.sourceType(modelExpression)
}

func newSourceQuerySummary(model sourceTypeKey, schemaEnabled bool) sourceQuerySummary {
	result := sourceQuerySummary{
		recognized: true,
		model:      model,
		projection: queryProjectionDefault,
		pattern: sourceQueryPattern{
			limit:           sourceBound{state: sourceBoundUnset},
			offset:          sourceBound{state: sourceBoundUnset},
			order:           sourceOrderAbsent,
			orderTermsKnown: true,
			predicatesKnown: true,
			rootCountKnown:  true,
			seekAfter:       sourceToggleAbsent,
			withDeleted:     sourceToggleAbsent,
		},
	}
	if schemaEnabled {
		result.pattern.index = &sourceIndexPattern{
			indexPredicatesKnown: true,
		}
	}
	return result
}

func (analyzer *sourceAnalyzer) applySourceQueryMethod(
	context sourceFunctionContext,
	summary sourceQuerySummary,
	call *ast.CallExpr,
	before token.Pos,
) sourceQuerySummary {
	if !summary.recognized {
		return summary
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return summary
	}
	method := selector.Sel.Name
	switch method {
	case "Select":
		summary.projection = queryProjectionExplicit
	case "Preload":
		summary.preload = true
	case "Limit":
		summary.pattern.limit = sourceBoundFromCall(call, selector.Sel.Pos())
	case "Offset":
		summary.pattern.offset = sourceBoundFromCall(call, selector.Sel.Pos())
	case "OrderBy":
		summary.pattern = analyzer.applySourceOrderCall(context, summary.pattern, call, before)
	case "SeekAfter":
		summary.pattern.seekAfter = sourceTogglePresent
	case "Where":
		predicates := analyzer.sourceWherePredicates(context, call, before)
		summary.pattern.wildcards = appendSourceWildcards(summary.pattern.wildcards, predicates.wildcards)
		summary.pattern.predicatesKnown = summary.pattern.predicatesKnown && predicates.known
		summary.pattern.rootPredicateCount += predicates.rootPredicateCount
		summary.pattern.rootCountKnown = summary.pattern.rootCountKnown && predicates.rootCountKnown
		summary.pattern.hasPredicates = append(summary.pattern.hasPredicates, predicates.hasPredicates...)
		if len(predicates.hasPredicates) != 0 && summary.pattern.deferredOrderCalls.count != 0 {
			summary.pattern = analyzer.resolveDeferredSourceOrderCalls(summary.pattern)
		}
		if summary.pattern.index != nil {
			index := summary.pattern.index
			index.equalityFields = append(index.equalityFields, predicates.equalityFields...)
			index.indexPredicatesKnown = index.indexPredicatesKnown && predicates.indexExact
			summary.pattern.index = index
		}
	case "WithDeleted":
		if len(call.Args) == 0 && !call.Ellipsis.IsValid() {
			summary.pattern.withDeleted = sourceTogglePresent
		} else {
			summary.pattern.withDeleted = sourceToggleUnknown
		}
	}
	return summary
}

func (analyzer *sourceAnalyzer) summarizeQueryFunction(key sourceFunctionKey, stack map[sourceFunctionKey]bool) sourceQuerySummary {
	function, exists := analyzer.queryFunctions[key]
	if !exists {
		return sourceQuerySummary{}
	}
	if cached, exists := analyzer.functionCache[key]; exists {
		return cloneSourceQuerySummaryIndex(cached)
	}
	if stack == nil {
		stack = make(map[sourceFunctionKey]bool)
	}
	if stack[key] {
		return sourceQuerySummary{recognized: true, model: function.model, projection: queryProjectionUnknown}
	}
	stack[key] = true
	defer delete(stack, key)

	context := sourceFunctionContext{
		file:    function.file,
		body:    function.decl.Body,
		parents: sourceParentMap(function.decl.Body),
	}
	returns := make([]sourceQuerySummary, 0, 2)
	ast.Inspect(function.decl.Body, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		statement, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(statement.Results) != 1 {
			returns = append(returns, sourceQuerySummary{recognized: true, model: function.model, projection: queryProjectionUnknown})
			return true
		}
		returns = append(returns, analyzer.summarizeQueryExpression(context, statement.Results[0], statement.Pos(), stack, nil))
		return true
	})

	result := sourceQuerySummary{recognized: true, model: function.model, projection: queryProjectionUnknown}
	if len(returns) != 0 {
		result = returns[0]
		if !result.recognized || result.model != function.model {
			result = sourceQuerySummary{recognized: true, model: function.model, projection: queryProjectionUnknown}
		}
		for _, current := range returns[1:] {
			result = mergeSourceQuerySummaries(result, current, function.model)
		}
	}
	analyzer.functionCache[key] = result
	return cloneSourceQuerySummaryIndex(result)
}

func cloneSourceQuerySummaryIndex(summary sourceQuerySummary) sourceQuerySummary {
	summary.pattern.orderTerms = summary.pattern.orderTerms.clone()
	summary.pattern.deferredOrderCalls = summary.pattern.deferredOrderCalls.clone()
	summary.pattern.hasPredicates = cloneSourceHasPredicates(summary.pattern.hasPredicates)
	summary.pattern.wildcards = append([]sourceWildcardPredicate(nil), summary.pattern.wildcards...)
	summary.pattern.index = cloneSourceIndexPattern(summary.pattern.index)
	return summary
}

func cloneSourceHasPredicates(values []sourceHasPredicate) []sourceHasPredicate {
	result := make([]sourceHasPredicate, len(values))
	for index := range values {
		result[index] = values[index]
		result[index].equalityFields = append([]string(nil), values[index].equalityFields...)
	}
	return result
}

func mergeSourceQuerySummaries(left, right sourceQuerySummary, model sourceTypeKey) sourceQuerySummary {
	result := sourceQuerySummary{recognized: true, model: model, projection: queryProjectionUnknown}
	if !left.recognized || !right.recognized || left.model != model || right.model != model {
		return result
	}
	if left.projection == right.projection {
		result.projection = left.projection
	}
	result.preload = left.preload || right.preload
	result.pattern = mergeSourceQueryPatterns(left.pattern, right.pattern)
	return result
}

type sourceBuilderDefinition struct {
	position token.Pos
	expr     ast.Expr
}

func (analyzer *sourceAnalyzer) summarizeBuilderObject(
	context sourceFunctionContext,
	identifier *ast.Ident,
	before token.Pos,
	functionStack map[sourceFunctionKey]bool,
	objectStack map[*ast.Object]bool,
) sourceQuerySummary {
	object := identifier.Obj
	if object == nil {
		return sourceQuerySummary{}
	}
	if objectStack == nil {
		objectStack = make(map[*ast.Object]bool)
	}
	if objectStack[object] {
		return sourceQuerySummary{}
	}
	objectStack[object] = true
	defer delete(objectStack, object)

	definitions := sourceBuilderDefinitions(context.body, object, before)
	var result sourceQuerySummary
	for _, definition := range definitions {
		current := analyzer.summarizeBuilderDefinition(context, definition.expr, object, result, definition.position, functionStack, objectStack)
		if !current.recognized {
			if result.recognized {
				result.projection = queryProjectionUnknown
			}
			continue
		}
		if !result.recognized {
			result = current
			continue
		}
		result = mergeSourceQuerySummaries(result, current, result.model)
	}
	if !result.recognized {
		return result
	}

	calls := sourceBuilderCalls(context, object, before, identifier.Pos())
	if !calls.safe {
		result.projection = queryProjectionUnknown
		result.pattern = sourceQueryPattern{}
	}
	if calls.selectCall && result.projection == queryProjectionDefault {
		result.projection = queryProjectionUnknown
	}
	if calls.preloadCall {
		result.preload = true
	}
	if calls.limitCall {
		result.pattern.limit = sourceBound{state: sourceBoundUnknown}
	}
	if calls.offsetCall {
		result.pattern.offset = sourceBound{state: sourceBoundUnknown}
	}
	if calls.orderCall {
		if result.pattern.order != sourceOrderPresent {
			result.pattern.order = sourceOrderUnknown
		}
		result.pattern.orderTermsKnown = false
	}
	if calls.whereCall {
		result.pattern.predicatesKnown = false
		result.pattern.rootCountKnown = false
	}
	if calls.seekAfterCall {
		result.pattern.seekAfter = sourceToggleUnknown
	}
	if calls.withDeletedCall {
		result.pattern.withDeleted = sourceToggleUnknown
	}
	if result.pattern.index != nil && (calls.orderCall || calls.whereCall || calls.withDeletedCall) {
		index := result.pattern.index
		if calls.whereCall {
			index.indexPredicatesKnown = false
		}
		result.pattern.index = index
	}
	return result
}

func sourceBuilderDefinitions(body *ast.BlockStmt, object *ast.Object, before token.Pos) []sourceBuilderDefinition {
	definitions := make([]sourceBuilderDefinition, 0, 4)
	ast.Inspect(body, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		if node.Pos() >= before {
			return false
		}
		switch current := node.(type) {
		case *ast.AssignStmt:
			for index, left := range current.Lhs {
				identifier, ok := left.(*ast.Ident)
				if !ok || identifier.Obj != object {
					continue
				}
				if len(current.Rhs) == len(current.Lhs) {
					definitions = append(definitions, sourceBuilderDefinition{position: current.Pos(), expr: current.Rhs[index]})
				} else if len(current.Rhs) == 1 && len(current.Lhs) == 1 {
					definitions = append(definitions, sourceBuilderDefinition{position: current.Pos(), expr: current.Rhs[0]})
				}
			}
		case *ast.ValueSpec:
			for index, name := range current.Names {
				if name.Obj != object || index >= len(current.Values) {
					continue
				}
				definitions = append(definitions, sourceBuilderDefinition{position: current.Pos(), expr: current.Values[index]})
			}
		}
		return true
	})
	sort.SliceStable(definitions, func(left, right int) bool {
		return definitions[left].position < definitions[right].position
	})
	return definitions
}

func (analyzer *sourceAnalyzer) summarizeBuilderDefinition(
	context sourceFunctionContext,
	expression ast.Expr,
	object *ast.Object,
	current sourceQuerySummary,
	before token.Pos,
	functionStack map[sourceFunctionKey]bool,
	objectStack map[*ast.Object]bool,
) sourceQuerySummary {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return analyzer.summarizeBuilderDefinition(context, value.X, object, current, before, functionStack, objectStack)
	case *ast.Ident:
		if value.Obj == object {
			return current
		}
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			base := analyzer.summarizeBuilderDefinition(context, selector.X, object, current, before, functionStack, objectStack)
			if base.recognized {
				return analyzer.applySourceQueryMethod(context, base, value, before)
			}
		}
	}
	return analyzer.summarizeQueryExpression(context, expression, before, functionStack, objectStack)
}

type sourceBuilderCallSet struct {
	selectCall      bool
	preloadCall     bool
	limitCall       bool
	offsetCall      bool
	orderCall       bool
	whereCall       bool
	seekAfterCall   bool
	withDeletedCall bool
	safe            bool
}

func sourceBuilderCalls(context sourceFunctionContext, object *ast.Object, before, allowed token.Pos) sourceBuilderCallSet {
	result := sourceBuilderCallSet{safe: true}
	ast.Inspect(context.body, func(node ast.Node) bool {
		if !result.safe || node == nil {
			return result.safe
		}
		if literal, nested := node.(*ast.FuncLit); nested {
			if sourceObjectReferenced(literal.Body, object) {
				result.safe = false
			}
			return false
		}
		if node.Pos() >= before {
			return false
		}
		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Obj != object || identifier.Pos() == allowed {
			return true
		}
		if sourceDeclarationIdentifier(identifier, context.parents.parent(identifier)) {
			return true
		}
		if sourceAssignmentTargetIdentifier(identifier, context.parents.parent(identifier)) {
			return true
		}
		methods, receiver := sourceReceiverMethods(context.parents, identifier)
		if !receiver {
			result.safe = false
			return false
		}
		for _, method := range methods {
			switch method {
			case "Select":
				result.selectCall = true
			case "Preload":
				result.preloadCall = true
			case "Limit":
				result.limitCall = true
			case "Offset":
				result.offsetCall = true
			case "OrderBy":
				result.orderCall = true
			case "Where":
				result.whereCall = true
			case "SeekAfter":
				result.seekAfterCall = true
			case "WithDeleted":
				result.withDeletedCall = true
			}
		}
		return true
	})
	return result
}

func sourceObjectReferenced(root ast.Node, object *ast.Object) bool {
	referenced := false
	ast.Inspect(root, func(node ast.Node) bool {
		if referenced || node == nil {
			return !referenced
		}
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Obj == object {
			referenced = true
			return false
		}
		return true
	})
	return referenced
}

func sourceAssignmentTargetIdentifier(identifier *ast.Ident, parent ast.Node) bool {
	assignment, ok := parent.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, expression := range assignment.Lhs {
		if expression == identifier {
			return true
		}
	}
	return false
}

func sourceReceiverMethods(parents sourceParents, identifier *ast.Ident) ([]string, bool) {
	methods := make([]string, 0, 2)
	var child ast.Node = identifier
	parent := parents.parent(identifier)
	for {
		switch current := parent.(type) {
		case *ast.ParenExpr:
			if current.X != child {
				return nil, false
			}
			child = current
			parent = parents.parent(current)
		case *ast.SelectorExpr:
			if current.X != child {
				return nil, false
			}
			methods = append(methods, current.Sel.Name)
			child = current
			parent = parents.parent(current)
		case *ast.CallExpr:
			if current.Fun != child {
				return nil, false
			}
			child = current
			parent = parents.parent(current)
		default:
			return methods, len(methods) != 0
		}
	}
}
