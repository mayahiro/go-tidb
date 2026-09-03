package model

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
)

// ModelTag is the struct tag used for tidbgo model metadata and field exclusion.
const ModelTag = "tidbgo"

var (
	// ErrModelType reports an unnamed or non-struct model type.
	ErrModelType = errors.New("model metadata requires a named struct type")

	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	valuerType  = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
	timeType    = reflect.TypeOf(time.Time{})
	metaType    = reflect.TypeFor[Meta]()
	modelCache  sync.Map
)

// Issue identifies one deterministic model-mapping problem.
type Issue struct {
	// Field is the dotted Go field path associated with the issue.
	Field string
	// Message explains why the mapping is invalid.
	Message string
}

// ValidationError aggregates model-mapping issues in declaration order.
type ValidationError struct {
	// Issues contains detached validation details in declaration order.
	Issues []Issue
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "invalid model metadata"
	}
	if len(e.Issues) == 1 {
		return e.Issues[0].Field + ": " + e.Issues[0].Message
	}
	return fmt.Sprintf("model metadata has %d validation errors, first at %s: %s", len(e.Issues), e.Issues[0].Field, e.Issues[0].Message)
}

// Describe returns cached metadata for T without executing user methods or
// performing network I/O.
func Describe[T any]() (*Descriptor, error) {
	return DescribeType(reflect.TypeFor[T]())
}

// DescribeType returns cached metadata for modelType without executing user
// methods or performing network I/O. Pointer layers around a named struct are
// ignored for cache identity.
func DescribeType(modelType reflect.Type) (*Descriptor, error) {
	base, err := modelStructType(modelType)
	if err != nil {
		return nil, err
	}
	if cached, ok := modelCache.Load(base); ok {
		return cached.(*Descriptor), nil
	}
	if base.Name() == "" {
		return nil, fmt.Errorf("%w: got %s", ErrModelType, base)
	}

	descriptor, parseErr := parseDescriptor(base)
	if parseErr != nil {
		return nil, parseErr
	}
	result, _ := modelCache.LoadOrStore(base, descriptor)
	return result.(*Descriptor), nil
}

func modelStructType(modelType reflect.Type) (reflect.Type, error) {
	if modelType == nil {
		return nil, fmt.Errorf("%w: got <nil>", ErrModelType)
	}
	for modelType.Kind() == reflect.Pointer {
		modelType = modelType.Elem()
	}
	if modelType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: got %s", ErrModelType, modelType)
	}
	return modelType, nil
}

type descriptorParser struct {
	fields       []Field
	columns      map[string]string
	fieldNames   map[string]int
	members      map[string]string
	tableName    string
	metaSeen     bool
	autoRandom   string
	softDelete   string
	declarations []relationDeclaration
	relations    []Relation
	issues       []Issue
}

func parseDescriptor(modelType reflect.Type) (*Descriptor, error) {
	parser := parseModelShape(modelType)
	if len(parser.issues) == 0 {
		parser.resolveRelations(modelType)
	}
	if len(parser.issues) != 0 {
		return nil, &ValidationError{Issues: append([]Issue(nil), parser.issues...)}
	}

	byColumn := make(map[string]int, len(parser.fields))
	byGoName := make(map[string]int, len(parser.fields))
	primaryKey := make([]int, 0)
	softDelete := -1
	for index, field := range parser.fields {
		byColumn[field.columnName] = index
		byGoName[field.goName] = index
		if field.primaryKey {
			primaryKey = append(primaryKey, index)
		}
		if field.softDelete {
			softDelete = index
		}
	}
	byRelation := make(map[string]int, len(parser.relations))
	for index, relation := range parser.relations {
		byRelation[relation.goName] = index
	}
	return &Descriptor{
		modelType:  modelType,
		tableName:  parser.tableName,
		fields:     parser.fields,
		byColumn:   byColumn,
		byGoName:   byGoName,
		primaryKey: primaryKey,
		softDelete: softDelete,
		relations:  parser.relations,
		byRelation: byRelation,
	}, nil
}

func parseModelShape(modelType reflect.Type) *descriptorParser {
	parser := &descriptorParser{
		columns:    make(map[string]string),
		fieldNames: make(map[string]int),
		members:    make(map[string]string),
	}
	parser.parseFields(modelType, modelType.Name(), nil, map[reflect.Type]bool{modelType: true})
	if parser.tableName == "" {
		parser.tableName = modelmeta.SnakeCase(modelType.Name())
		if !modelmeta.ValidSQLIdentifier(parser.tableName) {
			parser.add(modelType.Name(), fmt.Sprintf("default table name %q must be a simple SQL identifier of at most 64 bytes", parser.tableName))
		}
	}
	if len(parser.fields) == 0 && len(parser.issues) == 0 {
		parser.add(modelType.Name(), "must contain at least one mapped field")
	}
	return parser
}

func (p *descriptorParser) parseFields(modelType reflect.Type, path string, indexPrefix []int, stack map[reflect.Type]bool) {
	for fieldIndex := 0; fieldIndex < modelType.NumField(); fieldIndex++ {
		structField := modelType.Field(fieldIndex)
		if !structField.IsExported() {
			continue
		}

		fieldPath := path + "." + structField.Name
		index := append(append([]int(nil), indexPrefix...), fieldIndex)
		if indirectType(structField.Type) == metaType {
			p.parseMeta(structField, fieldPath, len(indexPrefix) == 0)
			continue
		}
		ignored, ignoreErr := taggedModelIgnore(structField)
		if ignoreErr != nil {
			p.add(fieldPath, ignoreErr.Error())
			continue
		}
		if ignored {
			continue
		}
		classification := classify(structField.Type)
		if startsWithRelationKind(structField.Tag.Get(ModelTag)) && (relationFieldType(structField.Type) || !classification.supported) {
			p.parseRelationField(structField, fieldPath, index)
			continue
		}

		column, options, modelTagErr := taggedModelField(structField)
		if modelTagErr != nil {
			p.add(fieldPath, modelTagErr.Error())
			continue
		}

		embeddedType := indirectType(structField.Type)
		if structField.Anonymous && embeddedType.Kind() == reflect.Struct && !classification.supported {
			if options.primaryKey {
				p.add(fieldPath, "primary-key metadata must be declared on a mapped scalar field")
				continue
			}
			if options.explicitColumn {
				p.add(fieldPath, "column metadata must be declared on a mapped scalar field")
				continue
			}
			if options.softDelete {
				p.add(fieldPath, "soft-delete metadata must be declared on a mapped scalar field")
				continue
			}
			if stack[embeddedType] {
				p.add(fieldPath, "embedded struct cycle is not supported")
				continue
			}
			stack[embeddedType] = true
			p.parseFields(embeddedType, fieldPath, index, stack)
			delete(stack, embeddedType)
			continue
		}

		if !classification.supported {
			if _, _, relationErr := relationFieldShape(structField.Type); relationErr == nil {
				p.add(fieldPath, "relation-shaped field requires a tidbgo relation tag or tidbgo:\"-\"")
				continue
			}
			p.add(fieldPath, fmt.Sprintf("type %s is not a supported scalar, sql.Scanner, or driver.Valuer", structField.Type))
			continue
		}
		if options.computed && options.primaryKey {
			p.add(fieldPath, "computed fields cannot be primary-key fields")
			continue
		}
		if options.softDelete {
			if options.primaryKey || options.autoRandom {
				p.add(fieldPath, "soft-delete fields cannot be primary-key fields")
				continue
			}
			if options.computed {
				p.add(fieldPath, "soft-delete fields cannot be computed fields")
				continue
			}
			if classification.baseType != timeType || classification.pointerDepth > 1 {
				p.add(fieldPath, "soft-delete fields must use time.Time or *time.Time")
				continue
			}
			if p.softDelete != "" {
				p.add(fieldPath, fmt.Sprintf("soft-delete field is already declared by %s", p.softDelete))
				continue
			}
		}
		if options.autoRandom {
			if !options.primaryKey {
				p.add(fieldPath, "AUTO_RANDOM fields must also be primary-key fields")
				continue
			}
			if options.computed {
				p.add(fieldPath, "AUTO_RANDOM fields cannot be computed fields")
				continue
			}
			if classification.pointerDepth != 0 || classification.kind != KindInt && classification.kind != KindUint {
				p.add(fieldPath, "AUTO_RANDOM fields must use a non-pointer signed or unsigned integer type")
				continue
			}
			if p.autoRandom != "" {
				p.add(fieldPath, fmt.Sprintf("AUTO_RANDOM field is already declared by %s", p.autoRandom))
				continue
			}
			p.autoRandom = fieldPath
		}
		if !modelmeta.ValidSQLIdentifier(column) {
			p.add(fieldPath, fmt.Sprintf("column name %q must be a simple SQL identifier of at most 64 bytes", column))
			continue
		}
		if previous, exists := p.columns[column]; exists {
			p.add(fieldPath, fmt.Sprintf("column %q is already mapped by %s", column, previous))
			continue
		}
		if !p.reserveMember(structField.Name, fieldPath) {
			continue
		}

		p.columns[column] = fieldPath
		p.fieldNames[structField.Name] = len(p.fields)
		if options.softDelete {
			p.softDelete = fieldPath
		}
		p.fields = append(p.fields, Field{
			goName:       structField.Name,
			columnName:   column,
			goType:       structField.Type,
			baseType:     classification.baseType,
			index:        index,
			pointerDepth: classification.pointerDepth,
			kind:         classification.kind,
			usesScanner:  classification.scanner,
			usesValuer:   classification.valuer,
			native:       classification.native,
			primaryKey:   options.primaryKey,
			autoRandom:   options.autoRandom,
			computed:     options.computed,
			softDelete:   options.softDelete,
		})
	}
}

func startsWithRelationKind(value string) bool {
	first, _, _ := strings.Cut(value, ",")
	_, ok := relationKind(first)
	return ok
}

func relationFieldType(fieldType reflect.Type) bool {
	_, _, err := relationFieldShape(fieldType)
	return err == nil
}

func (p *descriptorParser) reserveMember(name, path string) bool {
	if previous, exists := p.members[name]; exists {
		p.add(path, fmt.Sprintf("Go field name %q is already mapped by %s", name, previous))
		return false
	}
	p.members[name] = path
	return true
}

func (p *descriptorParser) parseMeta(field reflect.StructField, path string, topLevel bool) {
	if p.metaSeen {
		p.add(path, "model.Meta may be embedded only once")
		return
	}
	p.metaSeen = true
	if field.Type != metaType || !field.Anonymous {
		p.add(path, "model.Meta must be embedded directly as a value")
		return
	}
	if !topLevel {
		p.add(path, "model.Meta must be embedded directly in the model type")
		return
	}
	tableName, err := taggedModelMeta(field)
	if err != nil {
		p.add(path, err.Error())
		return
	}
	if tableName != "" {
		p.tableName = tableName
	}
}

func (p *descriptorParser) add(field, message string) {
	p.issues = append(p.issues, Issue{Field: field, Message: message})
}

func taggedModelIgnore(field reflect.StructField) (bool, error) {
	value, present := field.Tag.Lookup(ModelTag)
	return modelmeta.ParseIgnore(value, present)
}

type modelFieldOptions struct {
	primaryKey     bool
	autoRandom     bool
	computed       bool
	softDelete     bool
	explicitColumn bool
}

func taggedModelField(field reflect.StructField) (string, modelFieldOptions, error) {
	value, present := field.Tag.Lookup(ModelTag)
	parsed, err := modelmeta.ParseField(field.Name, value, present)
	if err != nil {
		return "", modelFieldOptions{}, err
	}
	return parsed.Column, modelFieldOptions{
		primaryKey:     parsed.PrimaryKey,
		autoRandom:     parsed.AutoRandom,
		computed:       parsed.Computed,
		softDelete:     parsed.SoftDelete,
		explicitColumn: parsed.ExplicitColumn,
	}, nil
}

func taggedModelMeta(field reflect.StructField) (string, error) {
	value, present := field.Tag.Lookup(ModelTag)
	return modelmeta.ParseTable(value, present)
}

type fieldClassification struct {
	baseType     reflect.Type
	pointerDepth int
	kind         Kind
	scanner      bool
	valuer       bool
	native       bool
	supported    bool
}

func classify(fieldType reflect.Type) fieldClassification {
	baseType := fieldType
	pointerDepth := 0
	for baseType.Kind() == reflect.Pointer {
		baseType = baseType.Elem()
		pointerDepth++
	}

	classification := fieldClassification{
		baseType:     baseType,
		pointerDepth: pointerDepth,
		scanner:      implements(fieldType, baseType, scannerType),
		valuer:       implements(fieldType, baseType, valuerType),
	}
	switch baseType.Kind() {
	case reflect.Bool:
		classification.kind = KindBool
		classification.native = true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		classification.kind = KindInt
		classification.native = true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		classification.kind = KindUint
		classification.native = true
	case reflect.Float32, reflect.Float64:
		classification.kind = KindFloat
		classification.native = true
	case reflect.String:
		classification.kind = KindString
		classification.native = true
	case reflect.Slice:
		if baseType.Elem().Kind() == reflect.Uint8 {
			classification.kind = KindBytes
			classification.native = true
		}
	case reflect.Struct:
		if baseType == timeType {
			classification.kind = KindTime
			classification.native = true
		}
	}
	if !classification.native && (classification.scanner || classification.valuer) {
		classification.kind = KindCustom
	}
	classification.supported = classification.native || classification.scanner || classification.valuer
	return classification
}

func implements(fieldType, baseType, interfaceType reflect.Type) bool {
	if fieldType.Implements(interfaceType) || baseType.Implements(interfaceType) {
		return true
	}
	if fieldType.Kind() != reflect.Pointer && reflect.PointerTo(fieldType).Implements(interfaceType) {
		return true
	}
	return baseType.Kind() != reflect.Pointer && reflect.PointerTo(baseType).Implements(interfaceType)
}

func indirectType(value reflect.Type) reflect.Type {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}
