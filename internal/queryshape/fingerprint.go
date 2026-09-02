package queryshape

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

const fingerprintPrefix = "q1:"

// Fingerprint returns a versioned SHA-256 identity for the logical query and
// compiler shape without encoding bind values or pagination values.
func (q Query) Fingerprint() string {
	encoder := fingerprintEncoder{hash: sha256.New()}
	encoder.string("go-tidb-query-shape")
	encoder.integer(Version)
	encoder.string(q.Model)
	encoder.string(q.Table)
	encoder.strings(q.Projection)
	encoder.predicates(q.Predicates)
	encoder.order(q.Order)
	encoder.boolean(q.SeekAfter)
	encoder.boolean(q.Limit.Set)
	encoder.boolean(q.Offset.Set)
	encoder.boolean(q.WithDeleted)
	encoder.string(q.SoftDeleteColumn)
	encoder.preloads(q.Preloads)
	encoder.string(string(q.Compiler.Rewrite))
	encoder.string(q.Compiler.Relation)
	var digest [sha256.Size]byte
	sum := encoder.hash.Sum(digest[:0])
	var result [len(fingerprintPrefix) + sha256.Size*2]byte
	copy(result[:], fingerprintPrefix)
	hex.Encode(result[len(fingerprintPrefix):], sum)
	return string(result[:])
}

type fingerprintEncoder struct {
	hash          hash.Hash
	integerBuffer [binary.MaxVarintLen64]byte
	textBuffer    [256]byte
}

func (e *fingerprintEncoder) boolean(value bool) {
	if value {
		e.integer(1)
		return
	}
	e.integer(0)
}

func (e *fingerprintEncoder) integer(value int) {
	length := binary.PutUvarint(e.integerBuffer[:], uint64(value))
	_, _ = e.hash.Write(e.integerBuffer[:length])
}

func (e *fingerprintEncoder) string(value string) {
	e.integer(len(value))
	for len(value) != 0 {
		length := copy(e.textBuffer[:], value)
		_, _ = e.hash.Write(e.textBuffer[:length])
		value = value[length:]
	}
}

func (e *fingerprintEncoder) strings(values []string) {
	e.integer(len(values))
	for _, value := range values {
		e.string(value)
	}
}

func (e *fingerprintEncoder) predicates(values []Predicate) {
	e.integer(len(values))
	for index := range values {
		value := values[index]
		e.string(string(value.Operator))
		e.string(value.Table)
		e.string(value.Field)
		e.string(value.Column)
		e.string(value.Relation)
		e.string(value.RelationKind)
		e.strings(value.RelationSourceColumns)
		e.strings(value.RelationTargetColumns)
		e.string(value.JunctionTable)
		e.strings(value.JunctionSourceColumns)
		e.strings(value.JunctionTargetColumns)
		e.string(value.SoftDeleteColumn)
		e.integer(value.ValueCount)
		e.predicates(value.Children)
	}
}

func (e *fingerprintEncoder) order(values []OrderTerm) {
	e.integer(len(values))
	for index := range values {
		value := values[index]
		e.string(value.Field)
		e.string(value.Column)
		e.string(string(value.Direction))
	}
}

func (e *fingerprintEncoder) preloads(values []Preload) {
	e.integer(len(values))
	for index := range values {
		value := values[index]
		e.string(value.Path)
		e.string(value.Relation)
		e.string(value.Kind)
		e.string(value.Table)
		e.strings(value.SourceColumns)
		e.strings(value.TargetColumns)
		e.string(value.JunctionTable)
		e.strings(value.JunctionSourceColumns)
		e.strings(value.JunctionTargetColumns)
		e.strings(value.Projection)
		e.order(value.Order)
		e.boolean(value.Inline)
		e.boolean(value.LoadAllSources)
		e.integer(value.BatchSize)
		e.boolean(value.WithDeleted)
		e.string(value.SoftDeleteColumn)
		e.preloads(value.Children)
	}
}
