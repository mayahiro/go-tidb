// Package vector provides a driver-independent TiDB VECTOR value with
// database/sql scanning, argument conversion, and dimension validation.
package vector

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// MaxDimensions is TiDB's maximum VECTOR dimension count.
const MaxDimensions = 16383

// Metric is an explicitly selected TiDB vector distance metric.
type Metric string

const (
	// L2 selects Euclidean distance.
	L2 Metric = "l2"
	// Cosine selects one minus cosine similarity.
	Cosine Metric = "cosine"
)

// Vector owns finite float32 elements. The zero value represents SQL NULL;
// New with an empty slice represents an empty vector. Values and constructors
// detach caller slices. Scan replaces the value only on success.
type Vector struct{ elements []float32 }

// New validates and copies elements without accessing a database.
func New(elements []float32) (Vector, error) {
	if len(elements) > MaxDimensions {
		return Vector{}, fmt.Errorf("vector: dimension exceeds %d", MaxDimensions)
	}
	for _, value := range elements {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return Vector{}, fmt.Errorf("vector: elements must be finite float32 values")
		}
	}
	values := make([]float32, len(elements))
	copy(values, elements)
	return Vector{elements: values}, nil
}

// Dimensions returns the number of elements; NULL and empty vectors return zero.
func (v Vector) Dimensions() int { return len(v.elements) }

// IsNull distinguishes SQL NULL from an empty vector.
func (v Vector) IsNull() bool { return v.elements == nil }

// Values returns a detached copy, preserving nil for SQL NULL.
func (v Vector) Values() []float32 {
	if v.IsNull() {
		return nil
	}
	values := make([]float32, len(v.elements))
	copy(values, v.elements)
	return values
}

// ValidateDimensions checks a fixed, positive search/storage dimension count.
// SQL NULL is rejected; callers handle nullable columns separately.
func (v Vector) ValidateDimensions(expected int) error {
	if expected < 1 || expected > MaxDimensions || v.IsNull() || v.Dimensions() != expected {
		return fmt.Errorf("vector: expected %d dimensions, got %d (null=%t)", expected, v.Dimensions(), v.IsNull())
	}
	return nil
}

// Value implements driver.Valuer using TiDB's text vector representation.
// SQL NULL returns nil. Encoding preserves float32 round trips.
func (v Vector) Value() (driver.Value, error) {
	if v.IsNull() {
		return nil, nil
	}
	buffer := make([]byte, 0, 2+len(v.elements)*12)
	buffer = append(buffer, '[')
	for i, value := range v.elements {
		if i != 0 {
			buffer = append(buffer, ',')
		}
		buffer = strconv.AppendFloat(buffer, float64(value), 'g', -1, 32)
	}
	buffer = append(buffer, ']')
	return string(buffer), nil
}

// Scan implements sql.Scanner for string, []byte, or SQL NULL. Invalid JSON,
// nonnumeric elements, nonfinite values, excess dimensions, and unsupported
// driver values are rejected without changing the destination.
func (v *Vector) Scan(source any) error {
	if v == nil {
		return fmt.Errorf("vector: Scan destination must not be nil")
	}
	if source == nil {
		v.elements = nil
		return nil
	}
	var data []byte
	switch value := source.(type) {
	case string:
		data = []byte(value)
	case []byte:
		data = value
	default:
		return fmt.Errorf("vector: unsupported Scan source %T", source)
	}
	values, err := decodeTypedArray(data)
	if err != nil {
		return err
	}
	v.elements = values
	return nil
}

// Count delimiters before allocating so hostile input cannot request an unbounded
// element slice. JSON accepts null into float32 without an error, so reject it
// explicitly; numeric arrays never contain that token.
func decodeTypedArray(data []byte) ([]float32, error) {
	if bytes.Contains(data, []byte("null")) {
		return nil, fmt.Errorf("vector: expected finite numeric elements")
	}
	capacity := bytes.Count(data, []byte(",")) + 1
	if capacity > MaxDimensions {
		return nil, fmt.Errorf("vector: dimension exceeds %d", MaxDimensions)
	}
	values := make([]float32, 0, capacity)
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("vector: expected a finite float32 JSON array")
	}
	return values, nil
}
