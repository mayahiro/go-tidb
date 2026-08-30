package orm

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

const (
	inlinePreloadRootAlias = "tidbgo_t0"
	manyToManyTargetAlias  = "t"
	inlinePreloadAlias1    = "tidbgo_t1"
	inlinePreloadAlias2    = "tidbgo_t2"
	inlinePreloadAlias3    = "tidbgo_t3"
	inlinePreloadAlias4    = "tidbgo_t4"
	inlinePreloadAlias5    = "tidbgo_t5"
	inlinePreloadAlias6    = "tidbgo_t6"
	inlinePreloadAlias7    = "tidbgo_t7"
	inlinePreloadAlias8    = "tidbgo_t8"
)

func compileInlinePreloadStatement(descriptor *model.Descriptor, base *selectStatement, plans []*preloadPlan, rootAlias, rootSoftDeleteColumn string) *selectStatement {
	inline := inlinePreloadPlans(plans)
	if len(inline) == 0 {
		if rootSoftDeleteColumn != "" {
			return appendDefaultSoftDeleteScope(base, rootSoftDeleteColumn)
		}
		return base
	}

	nextAlias := 1
	assignInlinePreloadAliases(inline, rootAlias, &nextAlias)

	var query strings.Builder
	capacity := len(base.sql) + inlinePreloadSQLCapacity(inline)
	if rootSoftDeleteColumn != "" {
		capacity += len(rootSoftDeleteColumn) + len(" WHERE ``.`` IS NULL")
	}
	query.Grow(capacity)
	query.WriteString("SELECT ")
	for index, column := range base.scanPlan.columns {
		if index != 0 {
			query.WriteString(", ")
		}
		writeQualifiedIdentifier(&query, rootAlias, column)
	}
	writeInlinePreloadColumns(&query, inline)
	query.WriteString(" FROM ")
	writeQuotedIdentifier(&query, descriptor.TableName())
	query.WriteString(" AS ")
	writeQuotedIdentifier(&query, rootAlias)
	writeInlinePreloadJoins(&query, inline)
	if rootSoftDeleteColumn != "" {
		query.WriteString(" WHERE ")
		writePreloadSoftDeletePredicate(&query, rootAlias, rootSoftDeleteColumn)
	}

	return &selectStatement{
		sql:            query.String(),
		scanPlan:       base.scanPlan,
		qualifier:      rootAlias,
		inlinePreloads: inline,
	}
}

func appendDefaultSoftDeleteScope(base *selectStatement, column string) *selectStatement {
	var query strings.Builder
	query.Grow(len(base.sql) + len(column) + len(" WHERE `` IS NULL"))
	query.WriteString(base.sql)
	query.WriteString(" WHERE ")
	writePreloadSoftDeletePredicate(&query, base.qualifier, column)
	return &selectStatement{
		sql:            query.String(),
		scanPlan:       base.scanPlan,
		qualifier:      base.qualifier,
		inlinePreloads: base.inlinePreloads,
	}
}

func inlinePreloadPlans(plans []*preloadPlan) []*preloadPlan {
	count := 0
	for _, plan := range plans {
		if plan.inline {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	if count == len(plans) {
		return plans
	}
	result := make([]*preloadPlan, 0, count)
	for _, plan := range plans {
		if plan.inline {
			result = append(result, plan)
		}
	}
	return result
}

func assignInlinePreloadAliases(plans []*preloadPlan, sourceAlias string, nextAlias *int) {
	for _, plan := range plans {
		plan.sourceAlias = sourceAlias
		plan.targetAlias = inlinePreloadAlias(*nextAlias)
		(*nextAlias)++
		assignInlinePreloadAliases(plan.inlineChildren, plan.targetAlias, nextAlias)
	}
}

func inlinePreloadAlias(index int) string {
	switch index {
	case 1:
		return inlinePreloadAlias1
	case 2:
		return inlinePreloadAlias2
	case 3:
		return inlinePreloadAlias3
	case 4:
		return inlinePreloadAlias4
	case 5:
		return inlinePreloadAlias5
	case 6:
		return inlinePreloadAlias6
	case 7:
		return inlinePreloadAlias7
	case 8:
		return inlinePreloadAlias8
	default:
		return "tidbgo_t" + strconv.Itoa(index)
	}
}

func inlinePreloadSQLCapacity(plans []*preloadPlan) int {
	capacity := 0
	for _, plan := range plans {
		capacity += len(plan.targetTable) + len(plan.sourceAlias) + len(plan.targetAlias) + 40
		for _, column := range plan.targetStatement.scanPlan.columns {
			capacity += len(plan.targetAlias) + len(column) + 8
		}
		for index := range plan.sourceKey {
			capacity += len(plan.sourceAlias) + len(plan.sourceKey[index].ColumnName()) + len(plan.targetAlias) + len(plan.targetKey[index].ColumnName()) + 12
		}
		capacity += inlinePreloadSQLCapacity(plan.inlineChildren)
	}
	return capacity
}

func writeInlinePreloadColumns(query *strings.Builder, plans []*preloadPlan) {
	for _, plan := range plans {
		for _, column := range plan.targetStatement.scanPlan.columns {
			query.WriteString(", ")
			writeQualifiedIdentifier(query, plan.targetAlias, column)
		}
		writeInlinePreloadColumns(query, plan.inlineChildren)
	}
}

func writeInlinePreloadJoins(query *strings.Builder, plans []*preloadPlan) {
	for _, plan := range plans {
		query.WriteString(" LEFT JOIN ")
		writeQuotedIdentifier(query, plan.targetTable)
		query.WriteString(" AS ")
		writeQuotedIdentifier(query, plan.targetAlias)
		query.WriteString(" ON (")
		for index := range plan.sourceKey {
			if index != 0 {
				query.WriteString(" AND ")
			}
			writeQualifiedIdentifier(query, plan.sourceAlias, plan.sourceKey[index].ColumnName())
			query.WriteString(" = ")
			writeQualifiedIdentifier(query, plan.targetAlias, plan.targetKey[index].ColumnName())
		}
		if plan.softDelete != nil && !plan.withDeleted {
			query.WriteString(" AND ")
			writePreloadSoftDeletePredicate(query, plan.targetAlias, plan.softDelete.column)
		}
		query.WriteByte(')')
		writeInlinePreloadJoins(query, plan.inlineChildren)
	}
}

func inlinePreloadColumnCount(plans []*preloadPlan) int {
	count := 0
	for _, plan := range plans {
		count += plan.inlineColumnCount
	}
	return count
}

func preloadTargetKeyScanIndexes(plan *preloadPlan) ([]int, error) {
	indexes := make([]int, len(plan.targetKey))
	for keyIndex, key := range plan.targetKey {
		found := false
		for scanIndex, field := range plan.targetStatement.scanPlan.fields {
			if field.goName == key.GoName() {
				indexes[keyIndex] = scanIndex
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("orm: SELECT preload target %s.%s projection omits relation key field %s.%s", plan.sourceName, plan.relationName, plan.targetType.Name(), key.GoName())
		}
	}
	return indexes, nil
}

func hydrateInlinePreloads(root reflect.Value, plans []*preloadPlan, values []any) error {
	offset := 0
	for _, plan := range plans {
		ownCount := len(plan.targetStatement.scanPlan.fields)
		totalCount := plan.inlineColumnCount
		if offset+totalCount > len(values) {
			return fmt.Errorf("relation %s.%s has incomplete joined values", plan.sourceName, plan.relationName)
		}
		ownValues := values[offset : offset+ownCount]
		joinedValues := values[offset+ownCount : offset+totalCount]
		offset += totalCount

		present, err := inlinePreloadKeyPresent(plan, ownValues)
		if err != nil {
			return err
		}
		if !present {
			continue
		}

		target := reflect.New(plan.targetType)
		for index, field := range plan.targetStatement.scanPlan.fields {
			address, addressErr := scanFieldAddress(target.Elem(), field.index)
			if addressErr != nil {
				return fmt.Errorf("bind joined field %s.%s: %w", plan.targetType.Name(), field.goName, addressErr)
			}
			if scanErr := scanInlineFieldValue(address.Elem(), ownValues[index], field.softDeleteIndex >= 0); scanErr != nil {
				return fmt.Errorf("scan joined field %s.%s: %w", plan.targetType.Name(), field.goName, scanErr)
			}
		}
		if err := hydrateInlinePreloads(target.Elem(), plan.inlineChildren, joinedValues); err != nil {
			return err
		}
		relation, err := preloadRelationField(root, plan.relationIndex)
		if err != nil {
			return err
		}
		relation.Set(target)
	}
	if offset != len(values) {
		return fmt.Errorf("inline preload consumed %d joined values, received %d", offset, len(values))
	}
	return nil
}

func scanInlineFieldValue(target reflect.Value, source any, nullZeroTime bool) error {
	if nullZeroTime && source == nil {
		target.SetZero()
		return nil
	}
	return scanInlineValue(target, source)
}

func inlinePreloadKeyPresent(plan *preloadPlan, values []any) (bool, error) {
	nullCount := 0
	for _, index := range plan.targetKeyScan {
		if values[index] == nil {
			nullCount++
		}
	}
	if nullCount == 0 {
		return true, nil
	}
	if nullCount == len(plan.targetKeyScan) {
		return false, nil
	}
	return false, fmt.Errorf("joined %s row for %s.%s has a partially NULL relation key", plan.targetType.Name(), plan.sourceName, plan.relationName)
}

func scanInlineValue(target reflect.Value, source any) error {
	if target.CanAddr() && target.Addr().CanInterface() {
		if scanner, ok := target.Addr().Interface().(sql.Scanner); ok {
			return scanner.Scan(source)
		}
	}
	if target.Kind() == reflect.Pointer {
		if source == nil {
			target.SetZero()
			return nil
		}
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		if target.CanInterface() {
			if scanner, ok := target.Interface().(sql.Scanner); ok {
				return scanner.Scan(source)
			}
		}
		return scanInlineValue(target.Elem(), source)
	}
	if source == nil {
		return fmt.Errorf("converting NULL to %s is unsupported", target.Type())
	}

	if target.Type() == reflect.TypeFor[time.Time]() {
		value, ok := source.(time.Time)
		if !ok {
			return fmt.Errorf("converting driver.Value type %T to time.Time is unsupported", source)
		}
		target.Set(reflect.ValueOf(value))
		return nil
	}

	switch target.Kind() {
	case reflect.Interface:
		value := reflect.ValueOf(source)
		if value.Type().AssignableTo(target.Type()) || target.NumMethod() == 0 {
			target.Set(value)
			return nil
		}
	case reflect.String:
		value, err := inlineStringValue(source)
		if err != nil {
			return err
		}
		target.SetString(value)
		return nil
	case reflect.Bool:
		value, err := driver.Bool.ConvertValue(source)
		if err != nil {
			return err
		}
		target.SetBool(value.(bool))
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := inlineStringValue(source)
		if err != nil {
			return err
		}
		number, err := strconv.ParseInt(value, 10, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetInt(number)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := inlineStringValue(source)
		if err != nil {
			return err
		}
		number, err := strconv.ParseUint(value, 10, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetUint(number)
		return nil
	case reflect.Float32, reflect.Float64:
		value, err := inlineStringValue(source)
		if err != nil {
			return err
		}
		number, err := strconv.ParseFloat(value, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetFloat(number)
		return nil
	case reflect.Slice:
		if target.Type().Elem().Kind() != reflect.Uint8 {
			break
		}
		var bytes []byte
		switch value := source.(type) {
		case []byte:
			bytes = append(bytes, value...)
		case string:
			bytes = []byte(value)
		default:
			text, err := inlineStringValue(source)
			if err != nil {
				return err
			}
			bytes = []byte(text)
		}
		target.Set(reflect.ValueOf(bytes).Convert(target.Type()))
		return nil
	}
	return fmt.Errorf("converting driver.Value type %T to %s is unsupported", source, target.Type())
}

func inlineStringValue(value any) (string, error) {
	switch current := value.(type) {
	case string:
		return current, nil
	case []byte:
		return string(current), nil
	case bool:
		return strconv.FormatBool(current), nil
	case int64:
		return strconv.FormatInt(current, 10), nil
	case uint64:
		return strconv.FormatUint(current, 10), nil
	case float64:
		return strconv.FormatFloat(current, 'g', -1, 64), nil
	case time.Time:
		return current.Format(time.RFC3339Nano), nil
	default:
		return "", fmt.Errorf("converting driver.Value type %T to string is unsupported", value)
	}
}
