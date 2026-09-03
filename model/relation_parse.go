package model

import (
	"fmt"
	"reflect"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
)

type relationPair struct {
	left  string
	right string
}

type relationDeclaration struct {
	path        string
	goName      string
	index       []int
	kind        RelationKind
	targetType  reflect.Type
	joins       []relationPair
	through     string
	sourcePairs []relationPair
	targetPairs []relationPair
}

func relationFieldShape(fieldType reflect.Type) (target reflect.Type, collection bool, err error) {
	switch fieldType.Kind() {
	case reflect.Pointer:
		target = fieldType.Elem()
	case reflect.Slice:
		collection = true
		target = fieldType.Elem()
		if target.Kind() == reflect.Pointer {
			target = target.Elem()
		}
	default:
		return nil, false, fmt.Errorf("relation field type %s must be a pointer to a named struct or a slice of named structs or pointers", fieldType)
	}
	if target.Kind() != reflect.Struct || target.Name() == "" {
		return nil, false, fmt.Errorf("relation field type %s must target a named struct", fieldType)
	}
	return target, collection, nil
}

func (p *descriptorParser) parseRelationField(field reflect.StructField, path string, index []int) {
	if field.Anonymous {
		p.add(path, "relation fields must use an explicit exported field name")
		return
	}
	targetType, collection, err := relationFieldShape(field.Type)
	if err != nil {
		p.add(path, err.Error())
		return
	}
	value, present := field.Tag.Lookup(ModelTag)
	if !present || value == "" {
		p.add(path, "relation field requires a tidbgo relation tag")
		return
	}
	declaration, err := parseRelationTag(value, collection)
	if err != nil {
		p.add(path, err.Error())
		return
	}
	if !p.reserveMember(field.Name, path) {
		return
	}
	declaration.path = path
	declaration.goName = field.Name
	declaration.index = append([]int(nil), index...)
	declaration.targetType = targetType
	p.declarations = append(p.declarations, declaration)
}

func parseRelationTag(value string, collection bool) (relationDeclaration, error) {
	parsed, err := modelmeta.ParseRelation(value, collection)
	if err != nil {
		return relationDeclaration{}, err
	}
	kind, _ := relationKind(string(parsed.Kind))
	declaration := relationDeclaration{
		kind:    kind,
		through: parsed.Through,
	}
	for _, pair := range parsed.Joins {
		declaration.joins = append(declaration.joins, relationPair{left: pair.Left, right: pair.Right})
	}
	for _, pair := range parsed.SourcePairs {
		declaration.sourcePairs = append(declaration.sourcePairs, relationPair{left: pair.Left, right: pair.Right})
	}
	for _, pair := range parsed.TargetPairs {
		declaration.targetPairs = append(declaration.targetPairs, relationPair{left: pair.Left, right: pair.Right})
	}
	return declaration, nil
}

func relationKind(value string) (RelationKind, bool) {
	kind, ok := modelmeta.ParseRelationKind(value)
	if !ok {
		return "", false
	}
	switch kind {
	case modelmeta.RelationBelongsTo:
		return RelationBelongsTo, true
	case modelmeta.RelationHasOne:
		return RelationHasOne, true
	case modelmeta.RelationHasMany:
		return RelationHasMany, true
	case modelmeta.RelationManyToMany:
		return RelationManyToMany, true
	default:
		return "", false
	}
}

func (p *descriptorParser) resolveRelations(sourceType reflect.Type) {
	for _, declaration := range p.declarations {
		target := parseModelShape(declaration.targetType)
		if len(target.issues) != 0 {
			issue := target.issues[0]
			p.add(declaration.path, fmt.Sprintf("target model %s is invalid at %s: %s", declaration.targetType, issue.Field, issue.Message))
			continue
		}

		if declaration.kind == RelationManyToMany {
			p.resolveManyToMany(declaration, target)
			continue
		}
		p.resolveDirectRelation(sourceType, declaration, target)
	}
}

func (p *descriptorParser) resolveDirectRelation(sourceType reflect.Type, declaration relationDeclaration, target *descriptorParser) {
	joins := declaration.joins
	if len(joins) == 0 {
		var err error
		joins, err = inferredDirectJoins(sourceType, declaration, p.fields, target.fields)
		if err != nil {
			p.add(declaration.path, err.Error())
			return
		}
	}

	relation := Relation{
		goName:     declaration.goName,
		kind:       declaration.kind,
		targetType: declaration.targetType,
		index:      append([]int(nil), declaration.index...),
	}
	valid := true
	seenSource := make(map[string]bool, len(joins))
	seenTarget := make(map[string]bool, len(joins))
	for _, join := range joins {
		source, sourceOK := p.fieldByName(join.left)
		targetField, targetOK := target.fieldByName(join.right)
		if !sourceOK {
			p.add(declaration.path, fmt.Sprintf("source key field %q is not mapped", join.left))
			valid = false
		}
		if !targetOK {
			p.add(declaration.path, fmt.Sprintf("target key field %q is not mapped on %s", join.right, declaration.targetType))
			valid = false
		}
		if seenSource[join.left] || seenTarget[join.right] {
			p.add(declaration.path, fmt.Sprintf("join %q:%q repeats a relation key field", join.left, join.right))
			valid = false
		}
		seenSource[join.left] = true
		seenTarget[join.right] = true
		if !sourceOK || !targetOK {
			continue
		}
		if !compatibleRelationFields(source, targetField) {
			p.add(declaration.path, fmt.Sprintf("join fields %s.%s and %s.%s use incompatible Go representations", sourceType, source.GoName(), declaration.targetType, targetField.GoName()))
			valid = false
			continue
		}
		relation.sourceKey = append(relation.sourceKey, source)
		relation.targetKey = append(relation.targetKey, targetField)
	}
	if valid {
		p.relations = append(p.relations, relation)
	}
}

func (p *descriptorParser) resolveManyToMany(declaration relationDeclaration, target *descriptorParser) {
	relation := Relation{
		goName:     declaration.goName,
		kind:       declaration.kind,
		targetType: declaration.targetType,
		index:      append([]int(nil), declaration.index...),
		junction: &Junction{
			tableName: declaration.through,
		},
	}
	valid := true
	seenSourceFields := make(map[string]bool, len(declaration.sourcePairs))
	seenTargetFields := make(map[string]bool, len(declaration.targetPairs))
	seenColumns := make(map[string]bool, len(declaration.sourcePairs)+len(declaration.targetPairs))
	for _, pair := range declaration.sourcePairs {
		field, ok := p.fieldByName(pair.left)
		if !ok {
			p.add(declaration.path, fmt.Sprintf("source key field %q is not mapped", pair.left))
			valid = false
		}
		if seenSourceFields[pair.left] || seenColumns[pair.right] {
			p.add(declaration.path, fmt.Sprintf("source mapping %q:%q repeats a field or junction column", pair.left, pair.right))
			valid = false
		}
		seenSourceFields[pair.left] = true
		seenColumns[pair.right] = true
		if ok {
			relation.sourceKey = append(relation.sourceKey, field)
			relation.junction.sourceColumns = append(relation.junction.sourceColumns, pair.right)
		}
	}
	for _, pair := range declaration.targetPairs {
		field, ok := target.fieldByName(pair.right)
		if !ok {
			p.add(declaration.path, fmt.Sprintf("target key field %q is not mapped on %s", pair.right, declaration.targetType))
			valid = false
		}
		if seenTargetFields[pair.right] || seenColumns[pair.left] {
			p.add(declaration.path, fmt.Sprintf("target mapping %q:%q repeats a field or junction column", pair.left, pair.right))
			valid = false
		}
		seenTargetFields[pair.right] = true
		seenColumns[pair.left] = true
		if ok {
			relation.targetKey = append(relation.targetKey, field)
			relation.junction.targetColumns = append(relation.junction.targetColumns, pair.left)
		}
	}
	if valid {
		p.relations = append(p.relations, relation)
	}
}

func (p *descriptorParser) fieldByName(name string) (Field, bool) {
	index, ok := p.fieldNames[name]
	if !ok {
		return Field{}, false
	}
	return p.fields[index], true
}

func inferredDirectJoins(sourceType reflect.Type, declaration relationDeclaration, sourceFields, targetFields []Field) ([]relationPair, error) {
	switch declaration.kind {
	case RelationBelongsTo:
		targetPrimaryKey := primaryKeyFields(targetFields)
		if len(targetPrimaryKey) != 1 {
			return nil, fmt.Errorf("belongs_to inference requires one target primary-key field; declare join=Source:Target explicitly")
		}
		return []relationPair{{
			left:  declaration.goName + targetPrimaryKey[0].GoName(),
			right: targetPrimaryKey[0].GoName(),
		}}, nil
	case RelationHasOne, RelationHasMany:
		sourcePrimaryKey := primaryKeyFields(sourceFields)
		if len(sourcePrimaryKey) != 1 {
			return nil, fmt.Errorf("%s inference requires one source primary-key field; declare join=Source:Target explicitly", declaration.kind)
		}
		return []relationPair{{
			left:  sourcePrimaryKey[0].GoName(),
			right: sourceType.Name() + sourcePrimaryKey[0].GoName(),
		}}, nil
	default:
		return nil, fmt.Errorf("relation kind %q does not support direct join inference", declaration.kind)
	}
}

func primaryKeyFields(fields []Field) []Field {
	result := make([]Field, 0)
	for _, field := range fields {
		if field.primaryKey {
			result = append(result, field)
		}
	}
	return result
}

func compatibleRelationFields(source, target Field) bool {
	if source.kind != target.kind {
		return false
	}
	if source.kind == KindCustom {
		return source.baseType == target.baseType
	}
	return source.baseType.Kind() == target.baseType.Kind()
}
