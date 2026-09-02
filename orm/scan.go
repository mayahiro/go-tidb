package orm

import (
	"database/sql"
	"fmt"
	"reflect"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type rowScanner interface {
	Scan(destinations ...any) error
}

type scanField struct {
	goName          string
	index           []int
	softDeleteIndex int
}

type scanPlan struct {
	modelType       reflect.Type
	columns         []string
	fields          []scanField
	softDeleteCount int
}

type scanPlanResult struct {
	plan *scanPlan
	err  error
}

type rowDecoder struct {
	plan         *scanPlan
	destinations []any
	prefix       int
	inline       []*preloadPlan
	joinedValues []any
	softDelete   []softDeleteTimeScanner
}

type softDeleteTimeScanner struct {
	target *time.Time
}

func (s *softDeleteTimeScanner) Scan(source any) error {
	if s == nil || s.target == nil {
		return fmt.Errorf("soft-delete time destination is not initialized")
	}
	if source == nil {
		*s.target = time.Time{}
		return nil
	}
	var value sql.NullTime
	if err := value.Scan(source); err != nil {
		return err
	}
	*s.target = value.Time
	return nil
}

func compileScanPlanFields(descriptor *model.Descriptor, fields []model.Field) (*scanPlan, error) {
	plan := &scanPlan{
		modelType: descriptor.Type(),
		columns:   make([]string, len(fields)),
		fields:    make([]scanField, len(fields)),
	}
	softDeleteCount := 0
	for index, field := range fields {
		if !field.CanScan() {
			return nil, fmt.Errorf("orm: field %s.%s cannot be read from a database row", descriptor.Name(), field.GoName())
		}
		plan.columns[index] = field.ColumnName()
		softDeleteIndex := -1
		if field.IsSoftDelete() && field.PointerDepth() == 0 {
			softDeleteIndex = softDeleteCount
			softDeleteCount++
		}
		plan.fields[index] = scanField{goName: field.GoName(), index: field.Index(), softDeleteIndex: softDeleteIndex}
	}
	plan.softDeleteCount = softDeleteCount
	return plan, nil
}

func (p *scanPlan) newDecoder() *rowDecoder {
	if p == nil {
		return nil
	}
	return &rowDecoder{
		plan:         p,
		destinations: make([]any, len(p.fields)),
		softDelete:   make([]softDeleteTimeScanner, p.softDeleteCount),
	}
}

func (s *selectStatement) newDecoder(prefix int) *rowDecoder {
	if s == nil || s.scanPlan == nil {
		return nil
	}
	joined := inlinePreloadColumnCount(s.inlinePreloads)
	return &rowDecoder{
		plan:         s.scanPlan,
		destinations: make([]any, prefix+len(s.scanPlan.fields)+joined),
		prefix:       prefix,
		inline:       s.inlinePreloads,
		joinedValues: make([]any, joined),
		softDelete:   make([]softDeleteTimeScanner, s.scanPlan.softDeleteCount),
	}
}

func (d *rowDecoder) scan(row rowScanner, target any) error {
	if d != nil && d.prefix == 0 && len(d.inline) == 0 {
		return d.scanScalar(row, target)
	}
	return d.scanWithPrefix(row, target, nil)
}

func (d *rowDecoder) scanScalar(row rowScanner, target any) error {
	if d == nil || d.plan == nil {
		return fmt.Errorf("orm: scan row with an uninitialized decoder")
	}
	if row == nil {
		return fmt.Errorf("orm: scan row from a nil source")
	}

	root, err := d.plan.targetValue(target)
	if err != nil {
		return err
	}
	for index, field := range d.plan.fields {
		address, addressErr := scanFieldAddress(root, field.index)
		if addressErr != nil {
			clear(d.destinations)
			d.clearSoftDeleteTargets()
			return fmt.Errorf("orm: bind field %s.%s: %w", d.plan.modelType.Name(), field.goName, addressErr)
		}
		destination, bindErr := d.scanDestination(field, address)
		if bindErr != nil {
			clear(d.destinations)
			d.clearSoftDeleteTargets()
			return fmt.Errorf("orm: bind field %s.%s: %w", d.plan.modelType.Name(), field.goName, bindErr)
		}
		d.destinations[index] = destination
	}

	scanErr := row.Scan(d.destinations...)
	clear(d.destinations)
	d.clearSoftDeleteTargets()
	if scanErr != nil {
		return fmt.Errorf("orm: scan %s row: %w", d.plan.modelType.Name(), scanErr)
	}
	return nil
}

func (d *rowDecoder) scanWithPrefix(row rowScanner, target any, prefix []any) error {
	if d == nil || d.plan == nil {
		return fmt.Errorf("orm: scan row with an uninitialized decoder")
	}
	if row == nil {
		return fmt.Errorf("orm: scan row from a nil source")
	}
	if len(prefix) != d.prefix {
		return fmt.Errorf("orm: scan row with %d prefix destinations, want %d", len(prefix), d.prefix)
	}

	root, err := d.plan.targetValue(target)
	if err != nil {
		return err
	}
	copy(d.destinations, prefix)
	for index, field := range d.plan.fields {
		address, addressErr := scanFieldAddress(root, field.index)
		if addressErr != nil {
			clear(d.destinations)
			d.clearSoftDeleteTargets()
			return fmt.Errorf("orm: bind field %s.%s: %w", d.plan.modelType.Name(), field.goName, addressErr)
		}
		destination, bindErr := d.scanDestination(field, address)
		if bindErr != nil {
			clear(d.destinations)
			d.clearSoftDeleteTargets()
			return fmt.Errorf("orm: bind field %s.%s: %w", d.plan.modelType.Name(), field.goName, bindErr)
		}
		d.destinations[d.prefix+index] = destination
	}
	joinedStart := d.prefix + len(d.plan.fields)
	for index := range d.joinedValues {
		d.joinedValues[index] = nil
		d.destinations[joinedStart+index] = &d.joinedValues[index]
	}

	scanErr := row.Scan(d.destinations...)
	clear(d.destinations)
	d.clearSoftDeleteTargets()
	if scanErr != nil {
		clear(d.joinedValues)
		return fmt.Errorf("orm: scan %s row: %w", d.plan.modelType.Name(), scanErr)
	}
	if err := hydrateInlinePreloads(root, d.inline, d.joinedValues); err != nil {
		clear(d.joinedValues)
		return fmt.Errorf("orm: scan %s inline preload: %w", d.plan.modelType.Name(), err)
	}
	clear(d.joinedValues)
	return nil
}

func (d *rowDecoder) scanDestination(field scanField, address reflect.Value) (any, error) {
	if field.softDeleteIndex < 0 {
		return address.Interface(), nil
	}
	if field.softDeleteIndex >= len(d.softDelete) {
		return nil, fmt.Errorf("soft-delete scanner index is invalid")
	}
	target, ok := address.Interface().(*time.Time)
	if !ok {
		return nil, fmt.Errorf("soft-delete destination has type %s, want *time.Time", address.Type())
	}
	scanner := &d.softDelete[field.softDeleteIndex]
	scanner.target = target
	return scanner, nil
}

func (d *rowDecoder) clearSoftDeleteTargets() {
	for index := range d.softDelete {
		d.softDelete[index].target = nil
	}
}

func (p *scanPlan) targetValue(target any) (reflect.Value, error) {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Type() != p.modelType {
		return reflect.Value{}, fmt.Errorf("orm: scan target must be a non-nil *%s", p.modelType)
	}
	return value.Elem(), nil
}

func scanFieldAddress(root reflect.Value, index []int) (reflect.Value, error) {
	current := root
	for depth, fieldIndex := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				current.Set(reflect.New(current.Type().Elem()))
			}
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || fieldIndex < 0 || fieldIndex >= current.NumField() {
			return reflect.Value{}, fmt.Errorf("invalid field index path")
		}
		current = current.Field(fieldIndex)
		if depth == len(index)-1 {
			if !current.CanAddr() || !current.CanInterface() {
				return reflect.Value{}, fmt.Errorf("field is not addressable")
			}
			return current.Addr(), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("empty field index path")
}
