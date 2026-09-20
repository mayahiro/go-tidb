package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

// ScanAll executes the SELECT and scans all rows directly into destination,
// which must be a non-nil pointer to a slice. Scalar elements require exactly
// one selected column. Struct elements receive selected source Go fields by
// their exact exported Go names, including unambiguous promoted fields.
// Destination tags do not affect mapping or SQL; unselected fields stay zero.
// Pointers, time.Time, byte slices, and sql.Scanner values use database/sql
// conversion. A selected source soft-delete field scanned into time.Time keeps
// the model's NULL-to-zero convention.
//
// The source model controls SQL, predicates, soft deletion, indexes, and
// pagination. Select controls the projection independently of destination.
// Preload is rejected before execution; Has relation predicates remain valid.
// ScanAll replaces the destination only after scanning and closing rows
// successfully, returns a non-nil empty slice for no rows, and leaves the
// destination unchanged on error. It does not reuse the destination's storage
// or mutate the query. Structural mapping errors are reported before I/O;
// database value conversion errors are reported while scanning.
// sql.RawBytes is not supported because its storage expires during iteration.
func (q *SelectQuery[T]) ScanAll(ctx context.Context, executor QueryExecutor, destination any) error {
	if err := validateQueryExecution(ctx, executor); err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("orm: ScanAll with a nil SELECT query")
	}
	if len(q.selection.preloads) != 0 {
		return fmt.Errorf("orm: ScanAll does not support Preload; use All for relation hydration")
	}
	target := reflect.ValueOf(destination)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("orm: ScanAll destination must be a non-nil pointer to a slice")
	}
	selection := q.selection
	if selection.modelType == nil {
		selection.modelType = reflect.TypeFor[T]()
	}
	plan, err := scanAllPlanFor(selection.modelType, target.Elem().Type(), selection.projection)
	if err != nil {
		return err
	}
	if err := validateWithDeleted(plan.source, selection.withDeleted, "SELECT"); err != nil {
		return err
	}
	if err := validateForceIndex(&selection); err != nil {
		return err
	}
	if err := selection.validatePolicy(); err != nil {
		return err
	}
	compiled, err := compileSelectFromProjection(plan.source, plan.statement, &selection)
	if err != nil {
		return err
	}
	ctx = executorStatementContext(ctx, executor)
	if selection.readPolicy != nil {
		applySelectReadPolicy(plan.source, &selection, &compiled)
	}
	metadata := runtimeSelectMetadata(ctx, &selection, compiled, "scan_all")
	rows, err := queryRows(ctx, executor, compiled, metadata)
	if err != nil {
		return err
	}
	values, err := plan.collect(rows)
	if err != nil {
		return err
	}
	target.Elem().Set(values)
	return nil
}

type scanAllPlanKey struct {
	source, slice     reflect.Type
	projection        string
	defaultProjection bool
}

type scanAllPlan struct {
	source    *model.Descriptor
	statement *selectStatement
	slice     reflect.Type
	scalar    bool
	target    *scanPlan
}

type scanAllPlanResult struct {
	plan *scanAllPlan
	err  error
}

var (
	scanAllPlans        sync.Map
	scanAllScannerType  = reflect.TypeFor[sql.Scanner]()
	scanAllTimeType     = reflect.TypeFor[time.Time]()
	scanAllRawBytesType = reflect.TypeFor[sql.RawBytes]()
)

func scanAllPlanFor(source, slice reflect.Type, projection []string) (*scanAllPlan, error) {
	key := scanAllPlanKey{source: source, slice: slice, projection: strings.Join(projection, "\x00"), defaultProjection: projection == nil}
	if cached, ok := scanAllPlans.Load(key); ok {
		result := cached.(scanAllPlanResult)
		return result.plan, result.err
	}
	plan, err := compileScanAllPlan(source, slice, projection)
	result, _ := scanAllPlans.LoadOrStore(key, scanAllPlanResult{plan: plan, err: err})
	cached := result.(scanAllPlanResult)
	return cached.plan, cached.err
}

func compileScanAllPlan(source, slice reflect.Type, projection []string) (*scanAllPlan, error) {
	if source == nil || source.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: SELECT query model must be a non-pointer struct, got %v", source)
	}
	descriptor, err := model.DescribeType(source)
	if err != nil {
		return nil, fmt.Errorf("orm: compile ScanAll source model: %w", err)
	}
	fields, err := selectProjectionFields(descriptor, projection)
	if err != nil {
		return nil, err
	}
	columns := make([]scanProjectionColumn, len(fields))
	sourcePlan := &scanPlan{modelType: source, columns: make([]string, len(fields)), fields: make([]scanField, len(fields))}
	for i, field := range fields {
		columns[i] = scanProjectionColumn{name: field.GoName(), softDelete: field.IsSoftDelete()}
		sourcePlan.columns[i] = field.ColumnName()
		sourcePlan.fields[i] = scanField{goName: field.GoName(), index: field.Index(), softDeleteIndex: -1}
	}
	targetPlan, scalar, err := compileProjectionScanPlan(slice, columns)
	if err != nil {
		return nil, err
	}
	targetPlan.columns = sourcePlan.columns
	// Keep source metadata attached to SQL; only the collector uses destination fields.
	return &scanAllPlan{
		source: descriptor, slice: slice, scalar: scalar, target: targetPlan,
		statement: &selectStatement{sql: renderSelect(descriptor.TableName(), sourcePlan.columns), scanPlan: sourcePlan},
	}, nil
}

type scanProjectionColumn struct {
	name       string
	softDelete bool
}

func compileProjectionScanPlan(slice reflect.Type, columns []scanProjectionColumn) (*scanPlan, bool, error) {
	element := slice.Elem()
	base := element
	seen := make(map[reflect.Type]bool)
	for base.Kind() == reflect.Pointer {
		if seen[base] {
			return nil, false, fmt.Errorf("orm: ScanAll unsupported recursive pointer element %s", element)
		}
		seen[base] = true
		base = base.Elem()
	}
	scalar := scanAllScalarType(element)
	if scalar && len(columns) != 1 {
		return nil, false, fmt.Errorf("orm: ScanAll scalar destination %s requires exactly one selected column, got %d", slice, len(columns))
	}
	if !scalar && base.Kind() != reflect.Struct {
		return nil, false, fmt.Errorf("orm: ScanAll unsupported slice element %s", element)
	}
	targetPlan := &scanPlan{modelType: base, fields: make([]scanField, len(columns))}
	for i, field := range columns {
		mapped := scanField{goName: field.name, softDeleteIndex: -1}
		fieldType := element
		if !scalar {
			targetField, ok := base.FieldByName(field.name)
			if !ok || targetField.PkgPath != "" {
				return nil, false, fmt.Errorf("orm: ScanAll destination %s must have an unambiguous exported Go field %s", element, field.name)
			}
			if !scanAllExportedPath(base, targetField.Index) {
				return nil, false, fmt.Errorf("orm: ScanAll destination field %s.%s has an unexported embedded path", base, field.name)
			}
			mapped.index, fieldType = targetField.Index, targetField.Type
			if !scanAllScalarType(fieldType) {
				return nil, false, fmt.Errorf("orm: ScanAll destination field %s.%s has unsupported scan type %s", base, field.name, fieldType)
			}
		}
		if field.softDelete && fieldType == scanAllTimeType {
			mapped.softDeleteIndex = targetPlan.softDeleteCount
			targetPlan.softDeleteCount++
		}
		targetPlan.fields[i] = mapped
	}
	return targetPlan, scalar, nil
}

func scanAllScalarType(value reflect.Type) bool {
	seen := make(map[reflect.Type]bool)
	for {
		if value == scanAllRawBytesType || value.Kind() == reflect.Interface && value.NumMethod() != 0 || seen[value] {
			return false
		}
		if value.Implements(scanAllScannerType) || reflect.PointerTo(value).Implements(scanAllScannerType) || value == scanAllTimeType {
			return true
		}
		if value.Kind() != reflect.Pointer {
			break
		}
		seen[value] = true
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.String:
		return true
	case reflect.Slice:
		return value.Elem().Kind() == reflect.Uint8
	case reflect.Interface:
		return value.NumMethod() == 0
	default:
		return false
	}
}

func scanAllExportedPath(root reflect.Type, index []int) bool {
	for _, i := range index {
		for root.Kind() == reflect.Pointer {
			root = root.Elem()
		}
		field := root.Field(i)
		if field.PkgPath != "" {
			return false
		}
		root = field.Type
	}
	return true
}

func (p *scanAllPlan) collect(rows resultRows) (reflect.Value, error) {
	values := reflect.New(p.slice).Elem()
	values.Set(reflect.MakeSlice(p.slice, 0, 0))
	decoder := p.target.newDecoder()
	defer decoder.releaseReusable()
	for rows.Next() {
		index := values.Len()
		values.Grow(1)
		values.SetLen(index + 1)
		value := values.Index(index)
		for i, field := range p.target.fields {
			var address reflect.Value
			if p.scalar {
				address = value.Addr()
			} else {
				var err error
				address, err = scanFieldAddress(value, field.index)
				if err != nil {
					return reflect.Value{}, closeRowsAfterError(p.source.Name(), rows, fmt.Errorf("orm: bind ScanAll field %s: %w", field.goName, err))
				}
			}
			destination, err := decoder.scanDestination(field, address)
			if err != nil {
				return reflect.Value{}, closeRowsAfterError(p.source.Name(), rows, fmt.Errorf("orm: bind ScanAll field %s: %w", field.goName, err))
			}
			decoder.destinations[i] = destination
		}
		if err := rows.Scan(decoder.destinations...); err != nil {
			return reflect.Value{}, closeRowsAfterError(p.source.Name(), rows, fmt.Errorf("orm: scan ScanAll row into %s: %w", p.slice.Elem(), err))
		}
		decoder.releaseReusable()
	}
	if err := finishRows(p.source.Name(), rows); err != nil {
		return reflect.Value{}, err
	}
	return values, nil
}
