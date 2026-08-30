package orm

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

const (
	likeEscapeCharacter  = byte('!')
	likePredicateSQL     = " LIKE ? ESCAPE '!'"
	likeExtraSQLCapacity = len(likePredicateSQL) - len(" LIKE ?")
)

type predicateOperator uint8

const (
	predicateEqual predicateOperator = iota + 1
	predicateNotEqual
	predicateGreaterThan
	predicateGreaterThanOrEqual
	predicateLessThan
	predicateLessThanOrEqual
	predicateIn
	predicateNotIn
	predicateIsNull
	predicateIsNotNull
	predicateBetween
	predicateContains
	predicateHasPrefix
	predicateHasSuffix
	predicateHasRelation
	predicateAnd
	predicateOr
	predicateNot
)

type predicate struct {
	operator    predicateOperator
	hasRelation bool
	field       string
	values      []any
	children    []predicate
}

type predicateCompiler struct {
	descriptor *model.Descriptor
	query      *strings.Builder
	arguments  []any
	qualifier  string
	depth      int
	operation  string
}

func (c *predicateCompiler) operationName() string {
	if c.operation == "" {
		return "SELECT"
	}
	return c.operation
}

func predicateCompileCapacity(predicates []predicate) (int, int) {
	argumentCount := 0
	sqlCapacity := 0
	for index := range predicates {
		current := predicates[index]
		argumentCount += len(current.values)
		sqlCapacity += len(current.field) + 16 + len(current.values)*3
		switch current.operator {
		case predicateContains, predicateHasPrefix, predicateHasSuffix:
			sqlCapacity += likeExtraSQLCapacity
		case predicateHasRelation:
			sqlCapacity += relationPredicateBaseSQLCapacity
		}
		childArguments, childSQL := predicateCompileCapacity(current.children)
		argumentCount += childArguments
		sqlCapacity += childSQL
	}
	return argumentCount, sqlCapacity
}

func predicatesHaveRelation(predicates []predicate) bool {
	for index := range predicates {
		current := predicates[index]
		if current.hasRelation || current.operator == predicateHasRelation {
			return true
		}
	}
	return false
}

func (c *predicateCompiler) write(current predicate) error {
	switch current.operator {
	case predicateAnd:
		return c.writeLogical(current, "AND", 2)
	case predicateOr:
		return c.writeLogical(current, "OR", 2)
	case predicateNot:
		return c.writeLogical(current, "", 1)
	case predicateHasRelation:
		return c.writeRelation(current)
	case predicateEqual,
		predicateNotEqual,
		predicateGreaterThan,
		predicateGreaterThanOrEqual,
		predicateLessThan,
		predicateLessThanOrEqual,
		predicateIn,
		predicateNotIn,
		predicateIsNull,
		predicateIsNotNull,
		predicateBetween,
		predicateContains,
		predicateHasPrefix,
		predicateHasSuffix:
		return c.writeScalar(current)
	default:
		return fmt.Errorf("orm: %s predicate has unknown operator %d", c.operationName(), current.operator)
	}
}

func (c *predicateCompiler) writeLogical(current predicate, separator string, childCount int) error {
	if current.field != "" || len(current.values) != 0 {
		return fmt.Errorf("orm: logical %s predicate must not contain a field or values", c.operationName())
	}
	if childCount == 1 && len(current.children) != 1 {
		return fmt.Errorf("orm: NOT %s predicate must contain exactly one child", c.operationName())
	}
	if childCount > 1 && len(current.children) < childCount {
		return fmt.Errorf("orm: %s %s predicate must contain at least two children", separator, c.operationName())
	}

	if childCount == 1 {
		c.query.WriteString("NOT (")
		if err := c.write(current.children[0]); err != nil {
			return err
		}
		c.query.WriteByte(')')
		return nil
	}

	c.query.WriteByte('(')
	for index := range current.children {
		if index != 0 {
			c.query.WriteByte(' ')
			c.query.WriteString(separator)
			c.query.WriteByte(' ')
		}
		if err := c.write(current.children[index]); err != nil {
			return err
		}
	}
	c.query.WriteByte(')')
	return nil
}

func (c *predicateCompiler) writeScalar(current predicate) error {
	if len(current.children) != 0 {
		return fmt.Errorf("orm: scalar %s predicate must not contain child predicates", c.operationName())
	}
	field, ok := c.descriptor.FieldByGoName(current.field)
	if !ok {
		return fmt.Errorf("orm: %s predicate field %s.%s is not a mapped scalar field", c.operationName(), c.descriptor.Name(), current.field)
	}
	if field.IsComputed() {
		return fmt.Errorf("orm: %s predicate field %s.%s is computed and unavailable in a base-table predicate", c.operationName(), c.descriptor.Name(), current.field)
	}

	switch current.operator {
	case predicateEqual:
		return c.writeComparison(current, field, "=")
	case predicateNotEqual:
		return c.writeComparison(current, field, "<>")
	case predicateGreaterThan:
		return c.writeComparison(current, field, ">")
	case predicateGreaterThanOrEqual:
		return c.writeComparison(current, field, ">=")
	case predicateLessThan:
		return c.writeComparison(current, field, "<")
	case predicateLessThanOrEqual:
		return c.writeComparison(current, field, "<=")
	case predicateIn:
		return c.writeIn(current, field, false)
	case predicateNotIn:
		return c.writeIn(current, field, true)
	case predicateIsNull:
		return c.writeNull(current, field, false)
	case predicateIsNotNull:
		return c.writeNull(current, field, true)
	case predicateBetween:
		return c.writeBetween(current, field)
	case predicateContains, predicateHasPrefix, predicateHasSuffix:
		return c.writeStringPattern(current, field)
	default:
		return fmt.Errorf("orm: %s predicate has unknown scalar operator %d", c.operationName(), current.operator)
	}
}

func (c *predicateCompiler) writeComparison(current predicate, field model.Field, operator string) error {
	if err := c.requireValues(current, field, 1); err != nil {
		return err
	}
	c.writeField(field)
	c.query.WriteByte(' ')
	c.query.WriteString(operator)
	c.query.WriteString(" ?")
	c.arguments = append(c.arguments, current.values[0])
	return nil
}

func (c *predicateCompiler) writeIn(current predicate, field model.Field, negate bool) error {
	if !field.CanValue() {
		return c.fieldCannotValue(field)
	}
	for index, value := range current.values {
		if nilPredicateArgument(value) {
			return c.nilArgument(field, index)
		}
	}
	if len(current.values) == 0 {
		if negate {
			c.query.WriteString("TRUE")
		} else {
			c.query.WriteString("FALSE")
		}
		return nil
	}

	c.writeField(field)
	if negate {
		c.query.WriteString(" NOT IN (")
	} else {
		c.query.WriteString(" IN (")
	}
	for index, value := range current.values {
		if index != 0 {
			c.query.WriteString(", ")
		}
		c.query.WriteByte('?')
		c.arguments = append(c.arguments, value)
	}
	c.query.WriteByte(')')
	return nil
}

func (c *predicateCompiler) writeNull(current predicate, field model.Field, negate bool) error {
	if len(current.values) != 0 {
		return fmt.Errorf("orm: NULL %s predicate for %s.%s must not contain values", c.operationName(), c.descriptor.Name(), field.GoName())
	}
	c.writeField(field)
	if negate {
		c.query.WriteString(" IS NOT NULL")
	} else {
		c.query.WriteString(" IS NULL")
	}
	return nil
}

func (c *predicateCompiler) writeBetween(current predicate, field model.Field) error {
	if err := c.requireValues(current, field, 2); err != nil {
		return err
	}
	c.writeField(field)
	c.query.WriteString(" BETWEEN ? AND ?")
	c.arguments = append(c.arguments, current.values...)
	return nil
}

func (c *predicateCompiler) writeStringPattern(current predicate, field model.Field) error {
	if err := c.requireValues(current, field, 1); err != nil {
		return err
	}
	if field.Kind() != model.KindString {
		return fmt.Errorf("orm: string pattern predicate requires a string field, got %s.%s", c.descriptor.Name(), field.GoName())
	}
	value := reflect.ValueOf(current.values[0])
	if value.Kind() != reflect.String {
		return fmt.Errorf("orm: string pattern predicate for %s.%s requires a string value", c.descriptor.Name(), field.GoName())
	}

	c.writeField(field)
	c.query.WriteString(likePredicateSQL)
	leadingWildcard := current.operator != predicateHasPrefix
	trailingWildcard := current.operator != predicateHasSuffix
	c.arguments = append(c.arguments, escapeLikePattern(value.String(), leadingWildcard, trailingWildcard))
	return nil
}

func (c *predicateCompiler) writeField(field model.Field) {
	writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
}

func (c *predicateCompiler) requireValues(current predicate, field model.Field, count int) error {
	if len(current.values) != count {
		return fmt.Errorf("orm: %s predicate for %s.%s requires exactly %d value arguments", c.operationName(), c.descriptor.Name(), field.GoName(), count)
	}
	if !field.CanValue() {
		return c.fieldCannotValue(field)
	}
	for index, value := range current.values {
		if nilPredicateArgument(value) {
			return c.nilArgument(field, index)
		}
	}
	return nil
}

func (c *predicateCompiler) fieldCannotValue(field model.Field) error {
	return fmt.Errorf("orm: predicate field %s.%s cannot be used as a database argument", c.descriptor.Name(), field.GoName())
}

func (c *predicateCompiler) nilArgument(field model.Field, index int) error {
	return fmt.Errorf("orm: predicate argument %d for %s.%s must not be nil; use an IS NULL predicate", index+1, c.descriptor.Name(), field.GoName())
}

func nilPredicateArgument(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func escapeLikePattern(value string, leadingWildcard, trailingWildcard bool) string {
	escapedBytes := 0
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case likeEscapeCharacter, '%', '_':
			escapedBytes++
		}
	}

	capacity := len(value) + escapedBytes
	if leadingWildcard {
		capacity++
	}
	if trailingWildcard {
		capacity++
	}
	var pattern strings.Builder
	pattern.Grow(capacity)
	if leadingWildcard {
		pattern.WriteByte('%')
	}
	for index := 0; index < len(value); index++ {
		current := value[index]
		switch current {
		case likeEscapeCharacter, '%', '_':
			pattern.WriteByte(likeEscapeCharacter)
		}
		pattern.WriteByte(current)
	}
	if trailingWildcard {
		pattern.WriteByte('%')
	}
	return pattern.String()
}
