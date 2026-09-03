package sourcecheck

import (
	"go/ast"
	"reflect"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
)

type sourceTypeKey struct {
	packagePath string
	name        string
}

type sourceModel struct {
	name          string
	file          *sourceFile
	structure     *ast.StructType
	fields        []string
	fieldSet      map[string]struct{}
	primaryFields []string
	uniqueKeys    []sourceUniqueKey
	uniqueByName  map[string]int
	softDelete    bool
	physical      *sourcePhysicalModel
	ambiguous     bool
}

type sourceUniqueKey struct {
	name   string
	fields []string
}

type sourcePhysicalModel struct {
	table            string
	columns          map[string]string
	softDeleteColumn string
	ambiguous        bool
}

func indexSourceModels(files []*sourceFile, physical bool) map[sourceTypeKey]*sourceModel {
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
				models[key] = describeSourceModel(file, key, structure, physical)
			}
		}
	}
	return models
}

func describeSourceModel(file *sourceFile, key sourceTypeKey, structure *ast.StructType, physical bool) *sourceModel {
	result := &sourceModel{
		name:      key.name,
		file:      file,
		structure: structure,
		fields:    make([]string, 0, len(structure.Fields.List)),
		fieldSet:  make(map[string]struct{}, len(structure.Fields.List)),
	}
	var columnSet map[string]struct{}
	if physical {
		result.physical = &sourcePhysicalModel{
			table:   modelmeta.SnakeCase(key.name),
			columns: make(map[string]string, len(structure.Fields.List)),
		}
		columnSet = make(map[string]struct{}, len(structure.Fields.List))
	}
	metaSeen := false
	for _, field := range structure.Fields.List {
		if meta, exact := sourceModelMetaField(file, field); meta {
			if !physical {
				continue
			}
			if metaSeen || !exact {
				result.ambiguous = true
				result.physical.ambiguous = true
				continue
			}
			metaSeen = true
			tag, present, tagOK := sourceModelTag(field)
			table, err := modelmeta.ParseTable(tag, present)
			if !tagOK || err != nil {
				result.ambiguous = true
				result.physical.ambiguous = true
				continue
			}
			if table != "" {
				result.physical.table = table
			}
			continue
		}
		tag, present, tagOK := sourceModelTag(field)
		if !tagOK {
			result.ambiguous = true
			if physical {
				result.physical.ambiguous = true
			}
			continue
		}
		ignored, err := modelmeta.ParseIgnore(tag, present)
		if err != nil {
			result.ambiguous = true
			if physical {
				result.physical.ambiguous = true
			}
			continue
		}
		if ignored {
			continue
		}
		first, _, _ := strings.Cut(tag, ",")
		if sourceRelationKind(first) {
			continue
		}
		if len(field.Names) == 0 {
			result.ambiguous = true
			if physical {
				result.physical.ambiguous = true
			}
			continue
		}
		if !sourceScalarShape(field.Type) {
			result.ambiguous = true
			if physical {
				result.physical.ambiguous = true
			}
			continue
		}
		for _, name := range field.Names {
			if !ast.IsExported(name.Name) {
				continue
			}
			var options modelmeta.FieldTag
			if physical || present && tag != "" {
				var err error
				options, err = modelmeta.ParseField(name.Name, tag, present)
				if err != nil {
					result.ambiguous = true
					if physical {
						result.physical.ambiguous = true
					}
					continue
				}
			}
			if options.Computed {
				if len(options.UniqueGroups) != 0 {
					result.ambiguous = true
					if physical {
						result.physical.ambiguous = true
					}
				}
				continue
			}
			if _, exists := result.fieldSet[name.Name]; exists {
				result.ambiguous = true
				if physical {
					result.physical.ambiguous = true
				}
				continue
			}
			if !physical {
				result.fieldSet[name.Name] = struct{}{}
				result.fields = append(result.fields, name.Name)
				if options.PrimaryKey {
					result.primaryFields = append(result.primaryFields, name.Name)
				}
				result.appendUniqueKeyFields(options.UniqueGroups, name.Name)
				if options.SoftDelete {
					if result.softDelete {
						result.ambiguous = true
						continue
					}
					result.softDelete = true
				}
				continue
			}
			column := options.Column
			if !modelmeta.ValidSQLIdentifier(column) {
				result.ambiguous = true
				result.physical.ambiguous = true
				continue
			}
			if _, exists := columnSet[column]; exists {
				result.ambiguous = true
				result.physical.ambiguous = true
				continue
			}
			result.fieldSet[name.Name] = struct{}{}
			result.fields = append(result.fields, name.Name)
			if options.PrimaryKey {
				result.primaryFields = append(result.primaryFields, name.Name)
			}
			result.appendUniqueKeyFields(options.UniqueGroups, name.Name)
			if options.SoftDelete {
				if result.softDelete {
					result.ambiguous = true
					result.physical.ambiguous = true
					continue
				}
				result.softDelete = true
			}
			result.physical.columns[name.Name] = column
			columnSet[column] = struct{}{}
			if options.SoftDelete {
				if result.physical.softDeleteColumn != "" {
					result.ambiguous = true
					result.physical.ambiguous = true
					continue
				}
				result.physical.softDeleteColumn = column
			}
		}
	}
	if physical && !modelmeta.ValidSQLIdentifier(result.physical.table) {
		result.ambiguous = true
		result.physical.ambiguous = true
	}
	return result
}

func (model *sourceModel) appendUniqueKeyFields(groups []string, field string) {
	for _, group := range groups {
		if model.uniqueByName == nil {
			model.uniqueByName = make(map[string]int)
		}
		index, exists := model.uniqueByName[group]
		if !exists {
			index = len(model.uniqueKeys)
			model.uniqueByName[group] = index
			model.uniqueKeys = append(model.uniqueKeys, sourceUniqueKey{name: group})
		}
		model.uniqueKeys[index].fields = append(model.uniqueKeys[index].fields, field)
	}
}

func sourceModelMetaField(file *sourceFile, field *ast.Field) (bool, bool) {
	expression := field.Type
	pointer := false
	for {
		current, ok := expression.(*ast.StarExpr)
		if !ok {
			break
		}
		pointer = true
		expression = current.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Meta" {
		return false, false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || identifier.Name != file.modelAlias || file.modelAlias == "" {
		return false, false
	}
	return true, !pointer && len(field.Names) == 0
}

func sourceModelTag(field *ast.Field) (string, bool, bool) {
	if field.Tag == nil {
		return "", false, true
	}
	value, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", false, false
	}
	tag, present := reflect.StructTag(value).Lookup("tidbgo")
	return tag, present, true
}

func sourceRelationKind(value string) bool {
	_, ok := modelmeta.ParseRelationKind(value)
	return ok
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
