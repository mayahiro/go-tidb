package vector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"testing"
)

// The token-by-token alternative retains the original bounded decoder for comparison.
func decodeTokens(data []byte) ([]float32, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return nil, fmt.Errorf("vector: expected a numeric JSON array")
	}
	values := make([]float32, 0)
	for decoder.More() {
		if len(values) == MaxDimensions {
			return nil, fmt.Errorf("vector: dimension exceeds %d", MaxDimensions)
		}
		token, err := decoder.Token()
		number, ok := token.(json.Number)
		if err != nil || !ok {
			return nil, fmt.Errorf("vector: expected finite numeric elements")
		}
		value, err := strconv.ParseFloat(string(number), 32)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("vector: element is outside the finite float32 range")
		}
		values = append(values, float32(value))
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil, fmt.Errorf("vector: expected array end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("vector: unexpected trailing data")
	}
	return values, nil
}

func BenchmarkVectorDecoderAlternatives(b *testing.B) {
	for _, size := range []int{3, 768, MaxDimensions} {
		values := make([]float32, size)
		for i := range values {
			values[i] = float32(i%31) * 0.125
		}
		v, _ := New(values)
		text, _ := v.Value()
		data := []byte(text.(string))
		for _, method := range []string{"tokens", "typed_array"} {
			b.Run(strconv.Itoa(size)+"/"+method, func(b *testing.B) {
				read := func() ([]float32, error) {
					if method == "typed_array" {
						return decodeTypedArray(data)
					}
					return decodeTokens(data)
				}
				if got, err := read(); err != nil || !reflect.DeepEqual(got, values) {
					b.Fatal(got, err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if _, err := read(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
