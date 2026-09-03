// Package model derives immutable application-model metadata from Go
// struct types without database access or generated code.
package model

import "reflect"

// Kind classifies the Go representation of a mapped column.
type Kind uint8

const (
	// KindBool represents bool and named bool types.
	KindBool Kind = iota + 1
	// KindInt represents signed integer types.
	KindInt
	// KindUint represents unsigned integer types.
	KindUint
	// KindFloat represents floating-point types.
	KindFloat
	// KindString represents string and named string types.
	KindString
	// KindBytes represents byte slices and named byte-slice types.
	KindBytes
	// KindTime represents time.Time.
	KindTime
	// KindCustom represents a type using sql.Scanner or driver.Valuer.
	KindCustom
)

// Meta is a zero-size marker for model-level struct metadata.
//
// Embed Meta directly in a model and use the model tag to override defaults,
// for example: Meta `tidbgo:"table=users"`.
type Meta struct{}

// Descriptor is immutable metadata for one named Go struct type.
//
// Descriptor values are cached by their non-pointer struct type. Accessor
// methods return detached slice values so callers cannot mutate cached state.
type Descriptor struct {
	modelType  reflect.Type
	tableName  string
	fields     []Field
	byColumn   map[string]int
	byGoName   map[string]int
	primaryKey []int
	uniqueKeys []UniqueKey
	softDelete int
	relations  []Relation
	byRelation map[string]int
}

// Type returns the non-pointer struct type represented by the descriptor.
func (d *Descriptor) Type() reflect.Type {
	if d == nil {
		return nil
	}
	return d.modelType
}

// Name returns the declared Go type name.
func (d *Descriptor) Name() string {
	if d == nil || d.modelType == nil {
		return ""
	}
	return d.modelType.Name()
}

// TableName returns the validated physical table name. Without a Meta
// override, the name is the deterministic snake_case form of the Go type.
func (d *Descriptor) TableName() string {
	if d == nil {
		return ""
	}
	return d.tableName
}

// Fields returns mapped fields in deterministic struct declaration order.
// Embedded structs are traversed depth first.
func (d *Descriptor) Fields() []Field {
	if d == nil {
		return nil
	}
	fields := make([]Field, len(d.fields))
	for index, field := range d.fields {
		fields[index] = field.clone()
	}
	return fields
}

// FieldByColumn returns the field mapped to column.
func (d *Descriptor) FieldByColumn(column string) (Field, bool) {
	if d == nil {
		return Field{}, false
	}
	index, ok := d.byColumn[column]
	if !ok {
		return Field{}, false
	}
	return d.fields[index], true
}

// FieldByGoName returns the mapped scalar field with the exported Go name.
func (d *Descriptor) FieldByGoName(name string) (Field, bool) {
	if d == nil {
		return Field{}, false
	}
	index, ok := d.byGoName[name]
	if !ok {
		return Field{}, false
	}
	return d.fields[index], true
}

// PrimaryKeyFields returns primary-key fields in struct declaration order.
// An empty result means that the model does not declare a primary key.
func (d *Descriptor) PrimaryKeyFields() []Field {
	if d == nil {
		return nil
	}
	fields := make([]Field, len(d.primaryKey))
	for index, fieldIndex := range d.primaryKey {
		fields[index] = d.fields[fieldIndex].clone()
	}
	return fields
}

// UniqueKeys returns explicitly declared candidate unique keys in first-group
// appearance order. Fields within each key follow struct declaration order.
// PrimaryKeyFields remains the separate physical primary-key declaration.
func (d *Descriptor) UniqueKeys() []UniqueKey {
	if d == nil {
		return nil
	}
	keys := make([]UniqueKey, len(d.uniqueKeys))
	for index, key := range d.uniqueKeys {
		keys[index] = key.clone()
	}
	return keys
}

// SoftDeleteField returns the time field that marks logically deleted rows.
// The boolean is false when the model does not declare a soft-delete field.
func (d *Descriptor) SoftDeleteField() (Field, bool) {
	if d == nil || d.softDelete < 0 || d.softDelete >= len(d.fields) {
		return Field{}, false
	}
	return d.fields[d.softDelete], true
}

// Relations returns relation metadata in deterministic struct declaration
// order. Embedded structs are traversed depth first.
func (d *Descriptor) Relations() []Relation {
	if d == nil {
		return nil
	}
	relations := make([]Relation, len(d.relations))
	for index, relation := range d.relations {
		relations[index] = relation.clone()
	}
	return relations
}

// RelationByName returns the relation mapped to an exported Go field name.
func (d *Descriptor) RelationByName(name string) (Relation, bool) {
	if d == nil {
		return Relation{}, false
	}
	index, ok := d.byRelation[name]
	if !ok {
		return Relation{}, false
	}
	return d.relations[index], true
}

// Field is immutable metadata for one mapped struct field.
type Field struct {
	goName       string
	columnName   string
	goType       reflect.Type
	baseType     reflect.Type
	index        []int
	pointerDepth int
	kind         Kind
	usesScanner  bool
	usesValuer   bool
	native       bool
	primaryKey   bool
	autoRandom   bool
	computed     bool
	softDelete   bool
}

// GoName returns the exported struct field name.
func (f Field) GoName() string { return f.goName }

// ColumnName returns the validated SQL column identifier.
func (f Field) ColumnName() string { return f.columnName }

// GoType returns the field type exactly as declared on the struct.
func (f Field) GoType() reflect.Type { return f.goType }

// BaseType returns the field type after removing pointer layers.
func (f Field) BaseType() reflect.Type { return f.baseType }

// Index returns a detached reflect field-index path.
func (f Field) Index() []int { return append([]int(nil), f.index...) }

// PointerDepth returns the number of pointer layers on the declared field.
func (f Field) PointerDepth() int { return f.pointerDepth }

// Kind returns the field representation class.
func (f Field) Kind() Kind { return f.kind }

// IsPrimaryKey reports whether the field is an ordered primary-key component
// declared with the model tag.
func (f Field) IsPrimaryKey() bool { return f.primaryKey }

// IsAutoRandom reports whether TiDB generates this primary-key field with
// AUTO_RANDOM during INSERT.
func (f Field) IsAutoRandom() bool { return f.autoRandom }

// IsComputed reports whether the field is populated only from an explicitly
// supplied query result and is not a physical base-table column.
func (f Field) IsComputed() bool { return f.computed }

// IsSoftDelete reports whether the field stores the logical deletion time.
func (f Field) IsSoftDelete() bool { return f.softDelete }

// UsesScanner reports whether the field or its address implements sql.Scanner.
func (f Field) UsesScanner() bool { return f.usesScanner }

// UsesValuer reports whether the field or its address implements driver.Valuer.
func (f Field) UsesValuer() bool { return f.usesValuer }

// CanScan reports whether database/sql can use the native representation or
// an sql.Scanner implementation for reads.
func (f Field) CanScan() bool { return f.native || f.usesScanner }

// CanValue reports whether database/sql can use the native representation or
// a driver.Valuer implementation for query and mutation arguments.
func (f Field) CanValue() bool { return f.native || f.usesValuer }

func (f Field) clone() Field {
	f.index = append([]int(nil), f.index...)
	return f
}

// UniqueKey is immutable metadata for one candidate unique key declared by
// repeating `tidbgo:",unique=<group>"` on its scalar fields.
//
// The group name is logical model metadata and does not name a physical SQL
// index. Use check.Schema to verify that the SQL snapshot enforces the claim.
type UniqueKey struct {
	name   string
	fields []Field
}

// Name returns the logical candidate-key group name.
func (k UniqueKey) Name() string { return k.name }

// Fields returns candidate-key fields in struct declaration order.
func (k UniqueKey) Fields() []Field { return cloneFields(k.fields) }

func (k UniqueKey) clone() UniqueKey {
	k.fields = cloneFields(k.fields)
	return k
}
