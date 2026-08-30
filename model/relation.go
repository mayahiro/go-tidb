package model

import "reflect"

// RelationKind classifies a relation field.
type RelationKind string

const (
	// RelationBelongsTo maps source foreign-key fields to one target.
	RelationBelongsTo RelationKind = "belongs-to"
	// RelationHasOne maps one source key to at most one target.
	RelationHasOne RelationKind = "has-one"
	// RelationHasMany maps one source key to multiple targets.
	RelationHasMany RelationKind = "has-many"
	// RelationManyToMany maps source and target keys through a junction table.
	RelationManyToMany RelationKind = "many-to-many"
)

// Relation is immutable metadata for one mapped relation field.
//
// A direct mapping is also a data-integrity contract: mapped non-NULL key
// values identify an existing row on the other side. go-tidb does not enforce
// a physical foreign key, but query optimizations may rely on this invariant.
type Relation struct {
	goName     string
	kind       RelationKind
	targetType reflect.Type
	index      []int
	sourceKey  []Field
	targetKey  []Field
	junction   *Junction
}

// GoName returns the exported Go relation field name.
func (r Relation) GoName() string { return r.goName }

// Kind returns the declared relation cardinality.
func (r Relation) Kind() RelationKind { return r.kind }

// TargetType returns the non-pointer named target struct type.
func (r Relation) TargetType() reflect.Type { return r.targetType }

// Index returns a detached reflect field-index path for the relation field.
func (r Relation) Index() []int { return append([]int(nil), r.index...) }

// IsCollection reports whether the relation field is a slice.
func (r Relation) IsCollection() bool {
	return r.kind == RelationHasMany || r.kind == RelationManyToMany
}

// SourceKey returns source-model fields in join order.
func (r Relation) SourceKey() []Field { return cloneFields(r.sourceKey) }

// TargetKey returns target-model fields in join order.
func (r Relation) TargetKey() []Field { return cloneFields(r.targetKey) }

// Junction returns many-to-many junction metadata.
func (r Relation) Junction() (Junction, bool) {
	if r.junction == nil {
		return Junction{}, false
	}
	return r.junction.clone(), true
}

func (r Relation) clone() Relation {
	r.index = append([]int(nil), r.index...)
	r.sourceKey = cloneFields(r.sourceKey)
	r.targetKey = cloneFields(r.targetKey)
	if r.junction != nil {
		junction := r.junction.clone()
		r.junction = &junction
	}
	return r
}

// Junction is immutable physical mapping for a many-to-many relation.
type Junction struct {
	tableName     string
	sourceColumns []string
	targetColumns []string
}

// TableName returns the validated physical junction table name.
func (j Junction) TableName() string { return j.tableName }

// SourceColumns returns columns joined to Relation.SourceKey in order.
func (j Junction) SourceColumns() []string {
	return append([]string(nil), j.sourceColumns...)
}

// TargetColumns returns columns joined to Relation.TargetKey in order.
func (j Junction) TargetColumns() []string {
	return append([]string(nil), j.targetColumns...)
}

func (j Junction) clone() Junction {
	j.sourceColumns = append([]string(nil), j.sourceColumns...)
	j.targetColumns = append([]string(nil), j.targetColumns...)
	return j
}

func cloneFields(fields []Field) []Field {
	result := make([]Field, len(fields))
	for index, field := range fields {
		result[index] = field.clone()
	}
	return result
}
