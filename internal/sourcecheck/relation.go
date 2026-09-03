package sourcecheck

import (
	"go/ast"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
)

type sourceRelationKey struct {
	model    sourceTypeKey
	relation string
}

type sourceRelationResult struct {
	relation sourceResolvedRelation
	resolved bool
}

type sourceResolvedRelation struct {
	name                  string
	kind                  modelmeta.RelationKind
	target                sourceTypeKey
	sourceFields          []string
	targetFields          []string
	junctionTable         string
	junctionSourceColumns []string
	junctionTargetColumns []string
}

func (relation sourceResolvedRelation) collection() bool {
	return relation.kind == modelmeta.RelationHasMany || relation.kind == modelmeta.RelationManyToMany
}

func (analyzer *sourceAnalyzer) resolveSourceRelation(modelKey sourceTypeKey, relationName string) (sourceResolvedRelation, bool) {
	key := sourceRelationKey{model: modelKey, relation: relationName}
	if cached, exists := analyzer.relationCache[key]; exists {
		return cached.relation, cached.resolved
	}
	relation, resolved := analyzer.parseSourceRelation(modelKey, relationName)
	if analyzer.relationCache == nil {
		analyzer.relationCache = make(map[sourceRelationKey]sourceRelationResult)
	}
	analyzer.relationCache[key] = sourceRelationResult{relation: relation, resolved: resolved}
	return relation, resolved
}

func (analyzer *sourceAnalyzer) parseSourceRelation(modelKey sourceTypeKey, relationName string) (sourceResolvedRelation, bool) {
	source, exists := analyzer.models[modelKey]
	if !exists || source.ambiguous || source.structure == nil || source.file == nil {
		return sourceResolvedRelation{}, false
	}
	for _, field := range source.structure.Fields.List {
		if len(field.Names) != 1 || field.Names[0].Name != relationName || !ast.IsExported(relationName) {
			continue
		}
		tag, present, tagOK := sourceModelTag(field)
		if !tagOK || !present || tag == "" {
			return sourceResolvedRelation{}, false
		}
		targetKey, collection, shapeOK := sourceRelationFieldShape(source.file, field.Type)
		if !shapeOK {
			return sourceResolvedRelation{}, false
		}
		declaration, err := modelmeta.ParseRelation(tag, collection)
		if err != nil {
			return sourceResolvedRelation{}, false
		}
		target, exists := analyzer.models[targetKey]
		if !exists || target.ambiguous {
			return sourceResolvedRelation{}, false
		}
		relation := sourceResolvedRelation{name: relationName, kind: declaration.Kind, target: targetKey}
		if declaration.Kind == modelmeta.RelationManyToMany {
			if !validateSourceManyToManyRelation(source, target, declaration) {
				return sourceResolvedRelation{}, false
			}
			for _, pair := range declaration.SourcePairs {
				relation.sourceFields = append(relation.sourceFields, pair.Left)
				relation.junctionSourceColumns = append(relation.junctionSourceColumns, pair.Right)
			}
			for _, pair := range declaration.TargetPairs {
				relation.targetFields = append(relation.targetFields, pair.Right)
				relation.junctionTargetColumns = append(relation.junctionTargetColumns, pair.Left)
			}
			relation.junctionTable = declaration.Through
			return relation, true
		}
		joins := declaration.Joins
		if len(joins) == 0 {
			inferred, ok := inferSourceRelationJoin(source, target, relationName, declaration.Kind)
			if !ok {
				return sourceResolvedRelation{}, false
			}
			joins = []modelmeta.RelationPair{inferred}
		}
		for _, pair := range joins {
			relation.sourceFields = append(relation.sourceFields, pair.Left)
			relation.targetFields = append(relation.targetFields, pair.Right)
		}
		if !validateDirectSourceRelationFields(source, target, relation) {
			return sourceResolvedRelation{}, false
		}
		return relation, true
	}
	return sourceResolvedRelation{}, false
}

func sourceRelationFieldShape(file *sourceFile, expression ast.Expr) (sourceTypeKey, bool, bool) {
	collection := false
	if slice, ok := expression.(*ast.ArrayType); ok {
		if slice.Len != nil {
			return sourceTypeKey{}, false, false
		}
		collection = true
		expression = slice.Elt
	}
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	target, ok := file.sourceType(expression)
	return target, collection, ok
}

func inferSourceRelationJoin(source, target *sourceModel, relationName string, kind modelmeta.RelationKind) (modelmeta.RelationPair, bool) {
	switch kind {
	case modelmeta.RelationBelongsTo:
		if len(target.primaryFields) != 1 {
			return modelmeta.RelationPair{}, false
		}
		return modelmeta.RelationPair{Left: relationName + target.primaryFields[0], Right: target.primaryFields[0]}, true
	case modelmeta.RelationHasOne, modelmeta.RelationHasMany:
		if len(source.primaryFields) != 1 {
			return modelmeta.RelationPair{}, false
		}
		return modelmeta.RelationPair{Left: source.primaryFields[0], Right: source.name + source.primaryFields[0]}, true
	default:
		return modelmeta.RelationPair{}, false
	}
}

func validateDirectSourceRelationFields(source, target *sourceModel, relation sourceResolvedRelation) bool {
	if len(relation.sourceFields) == 0 || len(relation.sourceFields) != len(relation.targetFields) {
		return false
	}
	seenSource := make(map[string]struct{}, len(relation.sourceFields))
	seenTarget := make(map[string]struct{}, len(relation.targetFields))
	for index := range relation.sourceFields {
		sourceName := relation.sourceFields[index]
		targetName := relation.targetFields[index]
		if _, duplicate := seenSource[sourceName]; duplicate {
			return false
		}
		if _, duplicate := seenTarget[targetName]; duplicate {
			return false
		}
		seenSource[sourceName] = struct{}{}
		seenTarget[targetName] = struct{}{}
		if _, exists := source.fieldSet[sourceName]; !exists {
			return false
		}
		if _, exists := target.fieldSet[targetName]; !exists {
			return false
		}
		sourceType, sourceOK := sourceSourceFieldType(source, sourceName)
		targetType, targetOK := sourceSourceFieldType(target, targetName)
		if !sourceOK || !targetOK || sourceType != targetType {
			return false
		}
	}
	return true
}

func validateSourceManyToManyRelation(source, target *sourceModel, declaration modelmeta.RelationTag) bool {
	seenSourceFields := make(map[string]struct{}, len(declaration.SourcePairs))
	seenTargetFields := make(map[string]struct{}, len(declaration.TargetPairs))
	seenColumns := make(map[string]struct{}, len(declaration.SourcePairs)+len(declaration.TargetPairs))
	for _, pair := range declaration.SourcePairs {
		if _, duplicate := seenSourceFields[pair.Left]; duplicate {
			return false
		}
		if _, duplicate := seenColumns[pair.Right]; duplicate {
			return false
		}
		if _, exists := source.fieldSet[pair.Left]; !exists {
			return false
		}
		seenSourceFields[pair.Left] = struct{}{}
		seenColumns[pair.Right] = struct{}{}
	}
	for _, pair := range declaration.TargetPairs {
		if _, duplicate := seenTargetFields[pair.Right]; duplicate {
			return false
		}
		if _, duplicate := seenColumns[pair.Left]; duplicate {
			return false
		}
		if _, exists := target.fieldSet[pair.Right]; !exists {
			return false
		}
		seenTargetFields[pair.Right] = struct{}{}
		seenColumns[pair.Left] = struct{}{}
	}
	return true
}

func sourceSourceFieldType(model *sourceModel, name string) (string, bool) {
	for _, field := range model.structure.Fields.List {
		matched := false
		for _, fieldName := range field.Names {
			if fieldName.Name == name {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		return sourceComparableType(model.file, field.Type)
	}
	return "", false
}

func sourceComparableType(file *sourceFile, expression ast.Expr) (string, bool) {
	for {
		pointer, ok := expression.(*ast.StarExpr)
		if !ok {
			break
		}
		expression = pointer.X
	}
	switch current := expression.(type) {
	case *ast.Ident:
		if sourceBuiltinType(current.Name) {
			return "builtin:" + current.Name, true
		}
		return file.packageKey + ":" + current.Name, true
	case *ast.SelectorExpr:
		identifier, ok := current.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		packagePath, exists := file.imports[identifier.Name]
		if !exists {
			return "", false
		}
		return packagePath + ":" + current.Sel.Name, true
	case *ast.ArrayType:
		if current.Len != nil {
			return "", false
		}
		identifier, ok := current.Elt.(*ast.Ident)
		if !ok || identifier.Name != "byte" {
			return "", false
		}
		return "builtin:[]byte", true
	default:
		return "", false
	}
}

func sourceBuiltinType(name string) bool {
	switch name {
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	default:
		return false
	}
}
