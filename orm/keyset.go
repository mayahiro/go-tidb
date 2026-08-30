package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

type cursorValue struct {
	field string
	value any
	null  bool
}

type keysetCompiler struct {
	descriptor *model.Descriptor
	query      *strings.Builder
	orderBy    []orderTerm
	cursor     []cursorValue
	arguments  []any
	qualifier  string
}

func seekAfterCompileCapacity(orderBy []orderTerm, cursor []cursorValue) (int, int) {
	if cursor == nil {
		return 0, 0
	}
	argumentCount := 0
	for index := range cursor {
		if cursor[index].null {
			continue
		}
		argumentCount++
		if index+1 != len(cursor) {
			argumentCount++
		}
	}

	sqlCapacity := len(cursor) * 48
	for index := range orderBy {
		sqlCapacity += len(orderBy[index].field) * 3
	}
	return argumentCount, sqlCapacity
}

func validateSeekAfter(descriptor *model.Descriptor, orderBy []orderTerm, cursor []cursorValue, pagination pagination) error {
	if cursor == nil {
		return nil
	}
	if pagination.offsetSet {
		return fmt.Errorf("orm: SELECT SeekAfter cannot be combined with OFFSET")
	}
	if len(cursor) == 0 {
		return fmt.Errorf("orm: SELECT SeekAfter requires at least one cursor value")
	}
	if len(orderBy) == 0 {
		return fmt.Errorf("orm: SELECT SeekAfter requires ORDER BY")
	}
	if len(cursor) != len(orderBy) {
		return fmt.Errorf("orm: SELECT SeekAfter has %d cursor values for %d ORDER BY fields", len(cursor), len(orderBy))
	}

	orderedPrimaryKeys := 0
	for index := range orderBy {
		field, err := resolveOrderField(descriptor, orderBy, index)
		if err != nil {
			return err
		}
		current := cursor[index]
		if current.field != "" && current.field != orderBy[index].field {
			return fmt.Errorf("orm: SELECT SeekAfter cursor field %d is %q, want ORDER BY field %q", index+1, current.field, orderBy[index].field)
		}
		if !field.CanValue() {
			return fmt.Errorf("orm: SELECT SeekAfter field %s.%s cannot be used as a database argument", descriptor.Name(), field.GoName())
		}
		if current.null {
			if current.value != nil {
				return fmt.Errorf("orm: SELECT SeekAfter NULL cursor for %s.%s must not contain a value", descriptor.Name(), field.GoName())
			}
			if !cursorFieldCanBeNull(field) {
				return fmt.Errorf("orm: SELECT SeekAfter field %s.%s cannot represent NULL", descriptor.Name(), field.GoName())
			}
		} else if nilPredicateArgument(current.value) {
			return fmt.Errorf("orm: SELECT SeekAfter cursor for %s.%s must not be nil; mark the cursor value as NULL", descriptor.Name(), field.GoName())
		}
		if field.IsPrimaryKey() {
			orderedPrimaryKeys++
		}
	}

	primaryKeys := descriptor.PrimaryKeyFields()
	if len(primaryKeys) == 0 {
		return fmt.Errorf("orm: SELECT SeekAfter for %s requires a declared primary key in ORDER BY", descriptor.Name())
	}
	if orderedPrimaryKeys == len(primaryKeys) {
		return nil
	}
	for _, primaryKey := range primaryKeys {
		found := false
		for _, term := range orderBy {
			if term.field == primaryKey.GoName() {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("orm: SELECT SeekAfter ORDER BY for %s must include primary-key field %q", descriptor.Name(), primaryKey.GoName())
		}
	}
	return nil
}

func cursorFieldCanBeNull(field model.Field) bool {
	if field.IsPrimaryKey() {
		return false
	}
	if field.PointerDepth() != 0 {
		return true
	}
	switch field.Kind() {
	case model.KindBytes, model.KindCustom:
		return true
	default:
		return false
	}
}

func (c *keysetCompiler) writeLevel(index int) error {
	field, ok := c.descriptor.FieldByGoName(c.orderBy[index].field)
	if !ok {
		return fmt.Errorf("orm: SELECT SeekAfter field %s.%s is not a mapped scalar field", c.descriptor.Name(), c.orderBy[index].field)
	}
	current := c.cursor[index]
	last := index+1 == len(c.cursor)

	c.query.WriteByte('(')
	var err error
	if current.null {
		err = c.writeNullLevel(field, c.orderBy[index].direction, index, last)
	} else {
		err = c.writeValueLevel(field, c.orderBy[index].direction, current.value, index, last)
	}
	if err != nil {
		return err
	}
	c.query.WriteByte(')')
	return nil
}

func (c *keysetCompiler) writeNullLevel(field model.Field, direction orderDirection, index int, last bool) error {
	switch direction {
	case orderAscending:
		writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
		c.query.WriteString(" IS NOT NULL")
		if !last {
			c.query.WriteString(" OR (")
			writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
			c.query.WriteString(" IS NULL AND ")
			if err := c.writeLevel(index + 1); err != nil {
				return err
			}
			c.query.WriteByte(')')
		}
	case orderDescending:
		if last {
			c.query.WriteString("FALSE")
			return nil
		}
		writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
		c.query.WriteString(" IS NULL AND ")
		if err := c.writeLevel(index + 1); err != nil {
			return err
		}
	default:
		return fmt.Errorf("orm: SELECT SeekAfter field %s.%s has unknown direction %d", c.descriptor.Name(), field.GoName(), direction)
	}
	return nil
}

func (c *keysetCompiler) writeValueLevel(field model.Field, direction orderDirection, value any, index int, last bool) error {
	if direction == orderDescending && cursorFieldCanBeNull(field) {
		c.query.WriteByte('(')
		writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
		c.query.WriteString(" < ? OR ")
		writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
		c.query.WriteString(" IS NULL)")
	} else {
		writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
		if direction == orderAscending {
			c.query.WriteString(" > ?")
		} else if direction == orderDescending {
			c.query.WriteString(" < ?")
		} else {
			return fmt.Errorf("orm: SELECT SeekAfter field %s.%s has unknown direction %d", c.descriptor.Name(), field.GoName(), direction)
		}
	}
	c.arguments = append(c.arguments, value)
	if last {
		return nil
	}

	c.query.WriteString(" OR (")
	writeMaybeQualifiedIdentifier(c.query, c.qualifier, field.ColumnName())
	c.query.WriteString(" = ? AND ")
	c.arguments = append(c.arguments, value)
	if err := c.writeLevel(index + 1); err != nil {
		return err
	}
	c.query.WriteByte(')')
	return nil
}
