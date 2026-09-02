package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

type orderDirection uint8

const (
	orderAscending orderDirection = iota + 1
	orderDescending
)

type orderTerm struct {
	field     string
	direction orderDirection
}

type pagination struct {
	limit     int64
	offset    int64
	limitSet  bool
	offsetSet bool
}

type compiledClauses struct {
	sql       string
	arguments []any
}

func compileSelectClauses(descriptor *model.Descriptor, base *selectStatement, selection *selectQuery) (compiledSelect, error) {
	if err := validatePagination(selection.pagination); err != nil {
		return compiledSelect{}, err
	}
	if err := validateSeekAfter(descriptor, selection.orderBy, selection.seekAfter, selection.pagination); err != nil {
		return compiledSelect{}, err
	}

	argumentCount, sqlCapacity := predicateCompileCapacity(selection.predicates)
	softDeleteField, filterSoftDeleted := activeSoftDeleteField(descriptor, selection.withDeleted)
	if filterSoftDeleted {
		sqlCapacity += len(softDeleteField.ColumnName()) + len("`` IS NULL")
	}
	hasRelationPredicate := predicatesHaveRelation(selection.predicates)
	if hasRelationPredicate {
		sqlCapacity += relationPredicateExtraSQLCapacity(descriptor, selection.predicates)
	}
	seekArgumentCount, seekSQLCapacity := seekAfterCompileCapacity(selection.orderBy, selection.seekAfter)
	argumentCount += seekArgumentCount
	sqlCapacity += seekSQLCapacity
	if filterSoftDeleted || len(selection.predicates) != 0 || selection.seekAfter != nil {
		sqlCapacity += len(" WHERE ")
	}
	if filterSoftDeleted && len(selection.predicates) != 0 {
		sqlCapacity += len(" AND ")
	}
	if (filterSoftDeleted || len(selection.predicates) != 0) && selection.seekAfter != nil {
		sqlCapacity += len(" AND ")
	}
	if hasRelationPredicate && base.qualifier == "" {
		sqlCapacity += relationRootAliasSQLCapacity
	}
	sqlCapacity += orderCompileCapacity(selection.orderBy)
	if selection.pagination.limitSet {
		argumentCount++
		sqlCapacity += len(" LIMIT ?")
	}
	if selection.pagination.offsetSet {
		argumentCount++
		sqlCapacity += len(" OFFSET ?")
	}

	var query strings.Builder
	query.Grow(len(base.sql) + sqlCapacity)
	query.WriteString(base.sql)
	qualifier := base.qualifier
	if hasRelationPredicate && qualifier == "" {
		qualifier = relationRootAlias
		writeRelationRootAlias(&query)
	}

	var arguments []any
	if argumentCount != 0 {
		arguments = make([]any, 0, argumentCount)
	}
	predicates := predicateCompiler{
		descriptor: descriptor,
		query:      &query,
		arguments:  arguments,
		qualifier:  qualifier,
	}
	wroteWhere := false
	if filterSoftDeleted {
		query.WriteString(" WHERE ")
		writeActiveSoftDeletePredicate(&query, qualifier, softDeleteField)
		wroteWhere = true
	}
	if len(selection.predicates) != 0 {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		for index := range selection.predicates {
			if index != 0 {
				query.WriteString(" AND ")
			}
			if err := predicates.write(selection.predicates[index]); err != nil {
				return compiledSelect{}, err
			}
		}
	}
	if selection.seekAfter != nil {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		keyset := keysetCompiler{
			descriptor: descriptor,
			query:      &query,
			orderBy:    selection.orderBy,
			cursor:     selection.seekAfter,
			arguments:  predicates.arguments,
			qualifier:  qualifier,
		}
		if err := keyset.writeLevel(0); err != nil {
			return compiledSelect{}, err
		}
		predicates.arguments = keyset.arguments
	}
	if err := writeOrderBy(&query, descriptor, selection.orderBy, qualifier); err != nil {
		return compiledSelect{}, err
	}
	if selection.pagination.limitSet {
		query.WriteString(" LIMIT ?")
		predicates.arguments = append(predicates.arguments, selection.pagination.limit)
	}
	if selection.pagination.offsetSet {
		query.WriteString(" OFFSET ?")
		predicates.arguments = append(predicates.arguments, selection.pagination.offset)
	}

	return compiledSelect{
		statement: &selectStatement{
			sql:            query.String(),
			scanPlan:       base.scanPlan,
			qualifier:      qualifier,
			inlinePreloads: base.inlinePreloads,
		},
		arguments: predicates.arguments,
	}, nil
}

func compileUnorderedClauses(descriptor *model.Descriptor, baseSQL string, selection *selectQuery) (compiledClauses, error) {
	if err := validateWithDeleted(descriptor, selection.withDeleted, "SELECT"); err != nil {
		return compiledClauses{}, err
	}
	if err := validatePagination(selection.pagination); err != nil {
		return compiledClauses{}, err
	}
	if err := validateSeekAfter(descriptor, selection.orderBy, selection.seekAfter, selection.pagination); err != nil {
		return compiledClauses{}, err
	}

	argumentCount, sqlCapacity := predicateCompileCapacity(selection.predicates)
	softDeleteField, filterSoftDeleted := activeSoftDeleteField(descriptor, selection.withDeleted)
	if filterSoftDeleted {
		sqlCapacity += len(softDeleteField.ColumnName()) + len("`` IS NULL")
	}
	hasRelationPredicate := predicatesHaveRelation(selection.predicates)
	if hasRelationPredicate {
		sqlCapacity += relationPredicateExtraSQLCapacity(descriptor, selection.predicates)
	}
	seekArgumentCount, seekSQLCapacity := seekAfterCompileCapacity(selection.orderBy, selection.seekAfter)
	argumentCount += seekArgumentCount
	sqlCapacity += seekSQLCapacity
	if filterSoftDeleted || len(selection.predicates) != 0 || selection.seekAfter != nil {
		sqlCapacity += len(" WHERE ")
	}
	if filterSoftDeleted && len(selection.predicates) != 0 {
		sqlCapacity += len(" AND ")
	}
	if (filterSoftDeleted || len(selection.predicates) != 0) && selection.seekAfter != nil {
		sqlCapacity += len(" AND ")
	}
	if hasRelationPredicate {
		sqlCapacity += relationRootAliasSQLCapacity
	}
	if selection.pagination.limitSet {
		argumentCount++
		sqlCapacity += len(" LIMIT ?")
	}
	if selection.pagination.offsetSet {
		argumentCount++
		sqlCapacity += len(" OFFSET ?")
	}

	var query strings.Builder
	query.Grow(len(baseSQL) + sqlCapacity)
	query.WriteString(baseSQL)
	qualifier := ""
	if hasRelationPredicate {
		qualifier = relationRootAlias
		writeRelationRootAlias(&query)
	}

	var arguments []any
	if argumentCount != 0 {
		arguments = make([]any, 0, argumentCount)
	}
	predicates := predicateCompiler{
		descriptor: descriptor,
		query:      &query,
		arguments:  arguments,
		qualifier:  qualifier,
	}
	wroteWhere := false
	if filterSoftDeleted {
		query.WriteString(" WHERE ")
		writeActiveSoftDeletePredicate(&query, qualifier, softDeleteField)
		wroteWhere = true
	}
	if len(selection.predicates) != 0 {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		for index := range selection.predicates {
			if index != 0 {
				query.WriteString(" AND ")
			}
			if err := predicates.write(selection.predicates[index]); err != nil {
				return compiledClauses{}, err
			}
		}
	}
	if selection.seekAfter != nil {
		if wroteWhere {
			query.WriteString(" AND ")
		} else {
			query.WriteString(" WHERE ")
			wroteWhere = true
		}
		keyset := keysetCompiler{
			descriptor: descriptor,
			query:      &query,
			orderBy:    selection.orderBy,
			cursor:     selection.seekAfter,
			arguments:  predicates.arguments,
		}
		if err := keyset.writeLevel(0); err != nil {
			return compiledClauses{}, err
		}
		predicates.arguments = keyset.arguments
	}
	if selection.pagination.limitSet {
		query.WriteString(" LIMIT ?")
		predicates.arguments = append(predicates.arguments, selection.pagination.limit)
	}
	if selection.pagination.offsetSet {
		query.WriteString(" OFFSET ?")
		predicates.arguments = append(predicates.arguments, selection.pagination.offset)
	}

	return compiledClauses{
		sql:       query.String(),
		arguments: predicates.arguments,
	}, nil
}

func orderCompileCapacity(terms []orderTerm) int {
	if len(terms) == 0 {
		return 0
	}
	capacity := len(" ORDER BY ")
	for index := range terms {
		capacity += len(terms[index].field) + len("`` DESC")
		if index != 0 {
			capacity += len(", ")
		}
	}
	return capacity
}

func writeOrderBy(query *strings.Builder, descriptor *model.Descriptor, terms []orderTerm, qualifier string) error {
	if len(terms) == 0 {
		return nil
	}
	query.WriteString(" ORDER BY ")
	for index := range terms {
		current := terms[index]
		for previous := 0; previous < index; previous++ {
			if terms[previous].field == current.field {
				return fmt.Errorf("orm: SELECT ORDER BY for %s repeats field %q", descriptor.Name(), current.field)
			}
		}
		field, ok := descriptor.FieldByGoName(current.field)
		if !ok {
			return fmt.Errorf("orm: SELECT ORDER BY field %s.%s is not a mapped scalar field", descriptor.Name(), current.field)
		}
		if field.IsComputed() {
			return fmt.Errorf("orm: SELECT ORDER BY field %s.%s is computed and unavailable in a base-table order", descriptor.Name(), current.field)
		}
		if index != 0 {
			query.WriteString(", ")
		}
		writeMaybeQualifiedIdentifier(query, qualifier, field.ColumnName())
		switch current.direction {
		case orderAscending:
			query.WriteString(" ASC")
		case orderDescending:
			query.WriteString(" DESC")
		default:
			return fmt.Errorf("orm: SELECT ORDER BY field %s.%s has unknown direction %d", descriptor.Name(), current.field, current.direction)
		}
	}
	return nil
}

func resolveOrderField(descriptor *model.Descriptor, terms []orderTerm, index int) (model.Field, error) {
	current := terms[index]
	for previous := 0; previous < index; previous++ {
		if terms[previous].field == current.field {
			return model.Field{}, fmt.Errorf("orm: SELECT ORDER BY for %s repeats field %q", descriptor.Name(), current.field)
		}
	}
	field, ok := descriptor.FieldByGoName(current.field)
	if !ok {
		return model.Field{}, fmt.Errorf("orm: SELECT ORDER BY field %s.%s is not a mapped scalar field", descriptor.Name(), current.field)
	}
	if field.IsComputed() {
		return model.Field{}, fmt.Errorf("orm: SELECT ORDER BY field %s.%s is computed and unavailable in a base-table order", descriptor.Name(), current.field)
	}
	switch current.direction {
	case orderAscending, orderDescending:
		return field, nil
	default:
		return model.Field{}, fmt.Errorf("orm: SELECT ORDER BY field %s.%s has unknown direction %d", descriptor.Name(), current.field, current.direction)
	}
}

func validatePagination(value pagination) error {
	if value.offsetSet && !value.limitSet {
		return fmt.Errorf("orm: SELECT OFFSET requires LIMIT")
	}
	if value.limitSet && value.limit < 0 {
		return fmt.Errorf("orm: SELECT LIMIT must not be negative")
	}
	if value.offsetSet && value.offset < 0 {
		return fmt.Errorf("orm: SELECT OFFSET must not be negative")
	}
	return nil
}
