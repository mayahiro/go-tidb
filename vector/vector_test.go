package vector

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestVectorOwnershipRoundTripAndNull(t *testing.T) {
	input := []float32{0.1, -2, math.SmallestNonzeroFloat32, math.MaxFloat32}
	v, err := New(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 99
	values := v.Values()
	values[1] = 88
	encoded, _ := v.Value()
	var decoded Vector
	if err := decoded.Scan([]byte(encoded.(string))); err != nil || !reflect.DeepEqual(decoded.Values(), v.Values()) {
		t.Fatal(decoded, err)
	}
	if err := v.ValidateDimensions(4); err != nil {
		t.Fatal(err)
	}
	if v.ValidateDimensions(3) == nil || v.ValidateDimensions(0) == nil {
		t.Fatal("dimension mismatch accepted")
	}
	var null Vector
	if value, err := null.Value(); err != nil || value != nil || !null.IsNull() {
		t.Fatal(value, err)
	}
	empty, _ := New(nil)
	if value, _ := empty.Value(); value != "[]" || empty.IsNull() {
		t.Fatal(value)
	}
	if err := decoded.Scan(nil); err != nil || !decoded.IsNull() {
		t.Fatal(decoded, err)
	}
}

func TestVectorRejectsMalformedValuesAtomically(t *testing.T) {
	v, _ := New([]float32{1, 2})
	for _, input := range []any{"null", "[null]", "[true]", "[\"1\"]", "[[1]]", "[NaN]", "[1e1000]", "[1,]", "[1] []", "[1", "1", 1, "[" + strings.Repeat("1,", MaxDimensions) + "1]"} {
		if err := v.Scan(input); err == nil || !reflect.DeepEqual(v.Values(), []float32{1, 2}) {
			t.Fatalf("accepted malformed source of type %T or changed value", input)
		}
	}
	for _, input := range [][]float32{{float32(math.NaN())}, {float32(math.Inf(1))}, make([]float32, MaxDimensions+1)} {
		if _, err := New(input); err == nil {
			t.Fatal("invalid vector accepted")
		}
	}
}

func BenchmarkVectorRoundTrip(b *testing.B) {
	for _, n := range []int{3, 768, MaxDimensions} {
		values := make([]float32, n)
		for i := range values {
			values[i] = float32(i%13) / 13
		}
		v, _ := New(values)
		encoded, _ := v.Value()
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var decoded Vector
				if err := decoded.Scan(encoded); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
