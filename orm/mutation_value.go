package orm

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

var driverValuerType = reflect.TypeFor[driver.Valuer]()

func mutationArgument(root reflect.Value, descriptor *model.Descriptor, field model.Field, index []int) (any, error) {
	if !field.CanValue() {
		return nil, fmt.Errorf("orm: mutation field %s.%s cannot be used as a database argument", descriptor.Name(), field.GoName())
	}
	value, null, err := modelFieldValue(root, index)
	if err != nil {
		return nil, fmt.Errorf("orm: read mutation field %s.%s: %w", descriptor.Name(), field.GoName(), err)
	}
	if null || nilReflectValue(value) {
		return nil, nil
	}
	if field.IsSoftDelete() && field.PointerDepth() == 0 && value.Interface().(time.Time).IsZero() {
		return nil, nil
	}
	if value.CanInterface() && value.Type().Implements(driverValuerType) {
		return value.Interface(), nil
	}
	if value.CanAddr() && value.Addr().CanInterface() && value.Addr().Type().Implements(driverValuerType) {
		return value.Addr().Interface(), nil
	}
	if !value.CanInterface() {
		return nil, fmt.Errorf("orm: mutation field %s.%s is not accessible", descriptor.Name(), field.GoName())
	}
	return value.Interface(), nil
}

func modelFieldValue(root reflect.Value, index []int) (reflect.Value, bool, error) {
	current := root
	for depth, fieldIndex := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return reflect.Value{}, true, nil
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || fieldIndex < 0 || fieldIndex >= current.NumField() {
			return reflect.Value{}, false, fmt.Errorf("invalid field index path")
		}
		current = current.Field(fieldIndex)
		if depth == len(index)-1 {
			return current, false, nil
		}
	}
	return reflect.Value{}, false, fmt.Errorf("empty field index path")
}

func nilReflectValue(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func assignGeneratedInteger(root reflect.Value, descriptor *model.Descriptor, field mutationFieldPlan, value int64) error {
	address, err := scanFieldAddress(root, field.index)
	if err != nil {
		return fmt.Errorf("orm: bind generated field %s.%s: %w", descriptor.Name(), field.field.GoName(), err)
	}
	target := address.Elem()
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if target.OverflowInt(value) {
			return fmt.Errorf("orm: generated value %d overflows %s.%s", value, descriptor.Name(), field.field.GoName())
		}
		target.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value < 0 || target.OverflowUint(uint64(value)) {
			return fmt.Errorf("orm: generated value %d overflows %s.%s", value, descriptor.Name(), field.field.GoName())
		}
		target.SetUint(uint64(value))
	default:
		return fmt.Errorf("orm: generated field %s.%s is not an integer", descriptor.Name(), field.field.GoName())
	}
	return nil
}
