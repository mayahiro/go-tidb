package sourcecheck

import (
	"go/ast"
	"reflect"
	"strconv"
	"strings"
)

type sourceTypeKey struct {
	packagePath string
	name        string
}

type sourceModel struct {
	name      string
	fields    []string
	fieldSet  map[string]struct{}
	ambiguous bool
}

func indexSourceModels(files []*sourceFile) map[sourceTypeKey]*sourceModel {
	models := make(map[sourceTypeKey]*sourceModel)
	for _, file := range files {
		for _, declaration := range file.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range general.Specs {
				typeSpecification, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpecification.Assign.IsValid() {
					continue
				}
				structure, ok := typeSpecification.Type.(*ast.StructType)
				if !ok {
					continue
				}
				key := sourceTypeKey{packagePath: file.packageKey, name: typeSpecification.Name.Name}
				models[key] = describeSourceModel(file, key, structure)
			}
		}
	}
	return models
}

func describeSourceModel(file *sourceFile, key sourceTypeKey, structure *ast.StructType) *sourceModel {
	result := &sourceModel{
		name:     key.name,
		fields:   make([]string, 0, len(structure.Fields.List)),
		fieldSet: make(map[string]struct{}, len(structure.Fields.List)),
	}
	for _, field := range structure.Fields.List {
		if sourceModelMetaField(file, field.Type) {
			continue
		}
		tag, tagOK := sourceModelTag(field)
		if !tagOK {
			result.ambiguous = true
			continue
		}
		if tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		if len(parts) != 0 && sourceRelationKind(parts[0]) {
			continue
		}
		computed := false
		for _, option := range parts[1:] {
			if option == "computed" {
				computed = true
				break
			}
		}
		if computed {
			continue
		}
		if len(field.Names) == 0 {
			result.ambiguous = true
			continue
		}
		if !sourceScalarShape(field.Type) {
			result.ambiguous = true
			continue
		}
		for _, name := range field.Names {
			if !ast.IsExported(name.Name) {
				continue
			}
			if _, exists := result.fieldSet[name.Name]; exists {
				result.ambiguous = true
				continue
			}
			result.fieldSet[name.Name] = struct{}{}
			result.fields = append(result.fields, name.Name)
		}
	}
	return result
}

func sourceModelMetaField(file *sourceFile, expression ast.Expr) bool {
	for {
		pointer, ok := expression.(*ast.StarExpr)
		if !ok {
			break
		}
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Meta" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == file.modelAlias && file.modelAlias != ""
}

func sourceModelTag(field *ast.Field) (string, bool) {
	if field.Tag == nil {
		return "", true
	}
	value, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", false
	}
	return reflect.StructTag(value).Get("tidbgo"), true
}

func sourceRelationKind(value string) bool {
	switch value {
	case "belongs_to", "has_one", "has_many", "many_to_many":
		return true
	default:
		return false
	}
}

func sourceScalarShape(expression ast.Expr) bool {
	for {
		pointer, ok := expression.(*ast.StarExpr)
		if !ok {
			break
		}
		expression = pointer.X
	}
	switch current := expression.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		return true
	case *ast.ArrayType:
		if current.Len != nil {
			return false
		}
		byteType, ok := current.Elt.(*ast.Ident)
		return ok && byteType.Name == "byte"
	default:
		return false
	}
}

func (file *sourceFile) sourceType(expression ast.Expr) (sourceTypeKey, bool) {
	for {
		pointer, ok := expression.(*ast.StarExpr)
		if !ok {
			break
		}
		expression = pointer.X
	}
	switch current := expression.(type) {
	case *ast.Ident:
		return sourceTypeKey{packagePath: file.packageKey, name: current.Name}, true
	case *ast.SelectorExpr:
		identifier, ok := current.X.(*ast.Ident)
		if !ok {
			return sourceTypeKey{}, false
		}
		packagePath, exists := file.imports[identifier.Name]
		if !exists {
			return sourceTypeKey{}, false
		}
		return sourceTypeKey{packagePath: packagePath, name: current.Sel.Name}, true
	default:
		return sourceTypeKey{}, false
	}
}
