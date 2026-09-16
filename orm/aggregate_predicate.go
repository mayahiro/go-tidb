package orm

import (
	"fmt"
)

type aggregatePredicateCompiler struct {
	*predicateCompiler
	outputs []aggregateOutput
}

func (c *aggregatePredicateCompiler) write(p predicate) error {
	if p.hasRelation || p.operator == predicateHasRelation {
		return fmt.Errorf("orm: aggregate Having does not support relations")
	}
	if p.operator == predicateAnd || p.operator == predicateOr || p.operator == predicateNot {
		if p.field != "" || len(p.values) != 0 || p.operator == predicateNot && len(p.children) != 1 || p.operator != predicateNot && len(p.children) < 2 {
			return fmt.Errorf("orm: invalid logical aggregate Having predicate")
		}
		if p.operator == predicateNot {
			c.query.WriteString("NOT ")
		}
		c.query.WriteByte('(')
		for i, child := range p.children {
			if i != 0 {
				if p.operator == predicateOr {
					c.query.WriteString(" OR ")
				} else {
					c.query.WriteString(" AND ")
				}
			}
			if err := c.write(child); err != nil {
				return err
			}
		}
		c.query.WriteByte(')')
		return nil
	}
	output, ok := aggregateOutputByName(c.outputs, p.field)
	if !ok {
		return fmt.Errorf("orm: aggregate Having references unknown output %q", p.field)
	}
	if len(p.children) != 0 {
		return fmt.Errorf("orm: scalar aggregate Having predicate has children")
	}
	operator := ""
	count := 1
	switch p.operator {
	case predicateEqual:
		operator = " = "
	case predicateNotEqual:
		operator = " <> "
	case predicateGreaterThan:
		operator = " > "
	case predicateGreaterThanOrEqual:
		operator = " >= "
	case predicateLessThan:
		operator = " < "
	case predicateLessThanOrEqual:
		operator = " <= "
	case predicateIsNull:
		operator, count = " IS NULL", 0
	case predicateIsNotNull:
		operator, count = " IS NOT NULL", 0
	case predicateBetween:
		operator, count = " BETWEEN ", 2
	case predicateIn:
		operator, count = " IN (", len(p.values)
	case predicateNotIn:
		operator, count = " NOT IN (", len(p.values)
	default:
		return fmt.Errorf("orm: unsupported aggregate Having operator %d", p.operator)
	}
	if len(p.values) != count {
		return fmt.Errorf("orm: aggregate Having output %s expects %d values", p.field, count)
	}
	for _, value := range p.values {
		if nilPredicateArgument(value) {
			return fmt.Errorf("orm: aggregate Having output %s has a NULL argument; use IsNull or IsNotNull", p.field)
		}
	}
	if count == 0 && (p.operator == predicateIn || p.operator == predicateNotIn) {
		if p.operator == predicateIn {
			c.query.WriteString("FALSE")
		} else {
			c.query.WriteString("TRUE")
		}
		return nil
	}
	// TiDB can reject a repeated non-column group expression in HAVING when
	// its source column is not selected. A calendar key is constant within
	// its group, including the all-NULL group, so MIN(key) preserves its value
	// while resolving the source column inside an aggregate. Referencing a
	// user alias instead could bind to a different grouped source column.
	calendar := output.function == "DATE" || output.function == "YEAR_MONTH"
	if calendar {
		c.query.WriteString("MIN(")
	}
	if err := output.write(c.predicateCompiler); err != nil {
		return err
	}
	if calendar {
		c.query.WriteByte(')')
	}
	c.query.WriteString(operator)
	for i, value := range p.values {
		if i != 0 {
			if p.operator == predicateBetween {
				c.query.WriteString(" AND ")
			} else {
				c.query.WriteString(", ")
			}
		}
		c.query.WriteByte('?')
		c.arguments = append(c.arguments, value)
	}
	if p.operator == predicateIn || p.operator == predicateNotIn {
		c.query.WriteByte(')')
	}
	return nil
}
