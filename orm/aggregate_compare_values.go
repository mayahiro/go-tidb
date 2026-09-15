package orm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"time"
)

func snapshotAggregateCompareArguments(arguments []any) ([]any, error) {
	result := make([]any, len(arguments))
	valuer := reflect.TypeFor[driver.Valuer]()
	for i, original := range arguments {
		value := reflect.ValueOf(original)
		seen := make(map[reflect.Type]bool)
		for value.IsValid() && value.Kind() == reflect.Pointer && !value.Type().Implements(valuer) {
			if value.IsNil() {
				value = reflect.Value{}
				break
			}
			if seen[value.Type()] {
				return nil, fmt.Errorf("orm: aggregate Compare argument %d has a recursive pointer", i+1)
			}
			seen[value.Type()] = true
			value = value.Elem()
		}
		var converted any
		var err error
		// Preserve uint64 for drivers such as go-sql-driver/mysql that accept
		// the full unsigned range through NamedValueChecker.
		if value.IsValid() && value.Kind() == reflect.Uint64 && !value.Type().Implements(valuer) {
			converted = value.Uint()
		} else {
			converted, err = driver.DefaultParameterConverter.ConvertValue(original)
		}
		if err != nil {
			return nil, fmt.Errorf("orm: freeze aggregate Compare argument %d: %w", i+1, err)
		}
		if data, ok := converted.([]byte); ok {
			converted = bytes.Clone(data)
		}
		result[i] = converted
	}
	return result, nil
}

func readAggregateComparisonRows(ctx context.Context, session QueryExecutor, c compiledAggregate, arguments, buffer []any, maxRows int) ([]any, error) {
	clear(buffer)
	buffer = buffer[:0]
	rows, err := session.QueryContext(ctx, c.sql, arguments...)
	if err != nil {
		return buffer, fmt.Errorf("orm: query aggregate Compare: %w", err)
	}
	if rows == nil {
		return buffer, fmt.Errorf("orm: aggregate Compare executor returned nil rows")
	}
	columns, err := rows.Columns()
	if err != nil {
		return buffer, closeRowsAfterError("aggregate Compare", rows, err)
	}
	if len(columns) != len(c.outputs) {
		return buffer, closeRowsAfterError("aggregate Compare", rows, fmt.Errorf("orm: aggregate Compare result column count differs from selection"))
	}
	for i, column := range columns {
		if column != c.outputs[i].name {
			return buffer, closeRowsAfterError("aggregate Compare", rows, fmt.Errorf("orm: aggregate Compare result column %d differs from output %s", i+1, c.outputs[i].name))
		}
	}
	destinations := make([]any, len(columns))
	for rows.Next() {
		if len(buffer)/len(columns) >= maxRows {
			return buffer, closeRowsAfterError("aggregate Compare", rows, fmt.Errorf("orm: aggregate Compare result exceeds MaxRows (%d)", maxRows))
		}
		offset := len(buffer)
		for range columns {
			buffer = append(buffer, nil)
		}
		for i := range columns {
			destinations[i] = &buffer[offset+i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return buffer, closeRowsAfterError("aggregate Compare", rows, fmt.Errorf("orm: scan aggregate Compare: %w", err))
		}
	}
	return buffer, finishRows("aggregate Compare", rows)
}

func equalAggregateComparisonRows(reference, current []any, outputs []aggregateOutput, options AggregateCompareOptions) error {
	if len(reference) != len(current) {
		return fmt.Errorf("result row count differs from the reference")
	}
	for i, value := range reference {
		if !equalAggregateComparisonValue(value, current[i], options) {
			return fmt.Errorf("result differs from the reference at row %d, output %s", i/len(outputs)+1, outputs[i%len(outputs)].name)
		}
	}
	return nil
}

func equalAggregateComparisonValue(left, right any, o AggregateCompareOptions) bool {
	switch value := left.(type) {
	case nil:
		return right == nil
	case int64:
		other, ok := right.(int64)
		return ok && value == other
	case uint64:
		other, ok := right.(uint64)
		return ok && value == other
	case bool:
		other, ok := right.(bool)
		return ok && value == other
	case string:
		other, ok := right.(string)
		return ok && value == other
	case []byte:
		other, ok := right.([]byte)
		return ok && bytes.Equal(value, other)
	case time.Time:
		other, ok := right.(time.Time)
		return ok && value.Equal(other)
	case float32:
		other, ok := right.(float32)
		return ok && equalAggregateComparisonFloat(float64(value), float64(other), o)
	case float64:
		other, ok := right.(float64)
		return ok && equalAggregateComparisonFloat(value, other, o)
	default:
		return false
	}
}

func equalAggregateComparisonFloat(left, right float64, o AggregateCompareOptions) bool {
	if left == right {
		return true
	}
	if math.IsNaN(left) || math.IsNaN(right) || math.IsInf(left, 0) || math.IsInf(right, 0) {
		return false
	}
	difference := math.Abs(left - right)
	return difference <= o.FloatAbsoluteTolerance || difference/math.Max(math.Abs(left), math.Abs(right)) <= o.FloatRelativeTolerance
}
