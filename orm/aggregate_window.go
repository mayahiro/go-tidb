package orm

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// UnboundedPreceding starts a ROWS frame at the partition's first row.
	UnboundedPreceding int64 = math.MinInt64
	// CurrentRow denotes the current row in a ROWS frame.
	CurrentRow int64 = 0
	// UnboundedFollowing ends a ROWS frame at the partition's last row.
	UnboundedFollowing int64 = math.MaxInt64
)

// WindowFrame describes a SQL ROWS frame, created with RowsBetween.
type WindowFrame struct{ start, end int64 }

// RowsBetween creates a ROWS frame. Negative bounds mean preceding rows,
// positive bounds mean following rows, and zero means the current row.
// Use UnboundedPreceding/UnboundedFollowing for partition boundaries.
// For example, RowsBetween(-2, CurrentRow) is a three-row moving frame.
// Invalid or reversed boundaries are rejected by Build.
func RowsBetween(start, end int64) *WindowFrame { return &WindowFrame{start: start, end: end} }

// WindowSpec references selected aggregate output Go names. PartitionBy divides
// groups into partitions; OrderBy orders groups within each partition, not the
// final result. Add a unique tie-breaker for deterministic row-based operations.
// Nil Rows uses SQL's default frame: the whole partition without ordering, or
// RANGE UNBOUNDED PRECEDING through CURRENT ROW (including peers) with ordering.
// Over copies the slices and frame so later caller changes do not affect it.
type WindowSpec struct {
	PartitionBy []string
	OrderBy     []OrderTerm
	Rows        *WindowFrame
}

// WindowExpression is an immutable function over the grouped aggregate result.
// It requires Over and As and cannot reference another window output.
type WindowExpression struct {
	function, field, alias string
	offset                 int64
	spec                   WindowSpec
	over, invalid          bool
}

// RowNumber numbers rows within each partition starting at one.
func RowNumber() WindowExpression { return WindowExpression{function: "ROW_NUMBER"} }

// Rank returns a one-based rank with gaps after ties.
func Rank() WindowExpression { return WindowExpression{function: "RANK"} }

// DenseRank returns a one-based rank without gaps after ties.
func DenseRank() WindowExpression { return WindowExpression{function: "DENSE_RANK"} }

// Lag reads a selected aggregate output from offset preceding groups. Offset
// must be nonnegative; zero reads the current group. Missing rows return NULL.
func Lag(output string, offset int64) WindowExpression {
	return WindowExpression{function: "LAG", field: output, offset: offset}
}

// Lead reads a selected aggregate output from offset following groups.
// It has the same offset and NULL contract as Lag.
func Lead(output string, offset int64) WindowExpression {
	return WindowExpression{function: "LEAD", field: output, offset: offset}
}

// FirstValue reads a selected aggregate output from the frame's first group.
func FirstValue(output string) WindowExpression {
	return WindowExpression{function: "FIRST_VALUE", field: output}
}

// LastValue reads a selected aggregate output from the frame's last group.
// With the default ordered frame this is the current peer group, not necessarily
// the partition's last group; use an explicit frame when that is intended.
func LastValue(output string) WindowExpression {
	return WindowExpression{function: "LAST_VALUE", field: output}
}

// Over converts CountAll, Count, Sum, Avg, Min, or Max to a window expression.
// Its field now references a selected aggregate output, not a source field.
// Conditional and distinct aggregates are not supported as window functions.
func (e AggregateExpression) Over(spec WindowSpec) WindowExpression {
	return WindowExpression{function: e.function, field: e.field, alias: e.alias, invalid: e.condition != nil}.Over(spec)
}

// Over returns the expression with a detached window specification.
func (e WindowExpression) Over(spec WindowSpec) WindowExpression {
	spec.PartitionBy = append([]string(nil), spec.PartitionBy...)
	spec.OrderBy = append([]OrderTerm(nil), spec.OrderBy...)
	if spec.Rows != nil {
		frame := *spec.Rows
		spec.Rows = &frame
	}
	e.spec, e.over = spec, true
	return e
}

// As returns the expression with an exported destination Go field name.
func (e WindowExpression) As(name string) WindowExpression { e.alias = name; return e }

// Window appends functions evaluated after GroupBy and Having and before final
// OrderBy, Limit, and Offset. Window fields reference Select output names.
// Window outputs can be used by final OrderBy but not GroupBy or Having.
func (q *AggregateQuery[T]) Window(expressions ...WindowExpression) *AggregateQuery[T] {
	if q != nil {
		q.windows = append(q.windows, expressions...)
	}
	return q
}

func (q *AggregateQuery[T]) compileWindows(resolver *planAccessResolver) (compiledAggregate, error) {
	if err := validatePagination(q.pagination); err != nil {
		return compiledAggregate{}, err
	}
	if err := q.policy.validate(); err != nil {
		return compiledAggregate{}, err
	}
	base := *q
	base.windows, base.orderBy, base.pagination = nil, nil, pagination{}
	base.policy.MPP, base.policy.mppSet = "", false
	c, err := base.compileWithResolver(resolver)
	if err != nil {
		return compiledAggregate{}, err
	}
	outputs := make([]aggregateOutput, len(c.outputs), len(c.outputs)+len(q.windows))
	copy(outputs, c.outputs)
	var sql strings.Builder
	sql.Grow(len(c.sql) + len(outputs)*32 + len(q.windows)*128 + 80)
	sql.WriteString("SELECT ")
	ReadPolicy{MPP: q.policy.MPP}.writeTables(&sql, nil)
	for i, output := range c.outputs {
		if i != 0 {
			sql.WriteString(", ")
		}
		writeQualifiedIdentifier(&sql, "w", output.name)
	}
	var args []any
	for _, window := range q.windows {
		if !validAggregateOutputName(window.alias) {
			return compiledAggregate{}, fmt.Errorf("orm: window output %q requires an exported Go name of at most 64 bytes; use As", window.alias)
		}
		for _, earlier := range outputs {
			if strings.EqualFold(earlier.name, window.alias) {
				return compiledAggregate{}, fmt.Errorf("orm: window output %q collides with %q", window.alias, earlier.name)
			}
		}
		sql.WriteString(", ")
		if err := window.write(&sql, c.outputs, &args); err != nil {
			return compiledAggregate{}, err
		}
		sql.WriteString(" AS ")
		writeQuotedIdentifier(&sql, window.alias)
		outputs = append(outputs, aggregateOutput{name: window.alias})
	}
	sql.WriteString(" FROM (")
	sql.WriteString(c.sql)
	sql.WriteString(") AS `w`")
	args = append(args, c.arguments...)
	for i, order := range q.orderBy {
		if _, ok := aggregateOutputByName(outputs, order.field); !ok {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate OrderBy references unknown output %q", order.field)
		}
		if order.direction != orderAscending && order.direction != orderDescending {
			return compiledAggregate{}, fmt.Errorf("orm: aggregate OrderBy has invalid direction")
		}
		if i == 0 {
			sql.WriteString(" ORDER BY ")
		} else {
			sql.WriteString(", ")
		}
		if _, ok := aggregateOutputByName(c.outputs, order.field); ok {
			writeQualifiedIdentifier(&sql, "w", order.field)
		} else {
			writeQuotedIdentifier(&sql, order.field)
		}
		writeWindowDirection(&sql, order.direction)
	}
	if q.pagination.limitSet {
		sql.WriteString(" LIMIT ?")
		args = append(args, q.pagination.limit)
	}
	if q.pagination.offsetSet {
		sql.WriteString(" OFFSET ?")
		args = append(args, q.pagination.offset)
	}
	c.sql, c.arguments, c.outputs = sql.String(), args, outputs
	return c, nil
}

func (e WindowExpression) write(sql *strings.Builder, outputs []aggregateOutput, args *[]any) error {
	if !e.over || e.invalid {
		return fmt.Errorf("orm: window %s requires Over and an unconditional supported function", e.alias)
	}
	ordered, frame := false, true
	switch e.function {
	case "ROW_NUMBER", "RANK", "DENSE_RANK", "LAG", "LEAD":
		ordered, frame = true, false
	case "FIRST_VALUE", "LAST_VALUE":
		ordered = true
	case "COUNT(*)", "COUNT", "SUM", "AVG", "MIN", "MAX":
	default:
		return fmt.Errorf("orm: unsupported window function %q", e.function)
	}
	if ordered && len(e.spec.OrderBy) == 0 {
		return fmt.Errorf("orm: window %s requires OrderBy", e.alias)
	}
	if e.spec.Rows != nil && (!frame || len(e.spec.OrderBy) == 0) {
		return fmt.Errorf("orm: window %s does not accept a ROWS frame without an ordered frame-aware function", e.alias)
	}
	function := e.function
	if function == "COUNT(*)" {
		function = "COUNT"
	}
	sql.WriteString(function)
	sql.WriteByte('(')
	switch e.function {
	case "ROW_NUMBER", "RANK", "DENSE_RANK":
	case "COUNT(*)":
		sql.WriteByte('*')
	default:
		if _, ok := aggregateOutputByName(outputs, e.field); !ok {
			return fmt.Errorf("orm: window %s references unknown aggregate output %q", e.alias, e.field)
		}
		writeQualifiedIdentifier(sql, "w", e.field)
	}
	if e.function == "LAG" || e.function == "LEAD" {
		if e.offset < 0 {
			return fmt.Errorf("orm: window %s offset must be nonnegative", e.alias)
		}
		sql.WriteString(", ?")
		*args = append(*args, e.offset)
	}
	sql.WriteString(") OVER (")
	for i, name := range e.spec.PartitionBy {
		if _, ok := aggregateOutputByName(outputs, name); !ok {
			return fmt.Errorf("orm: window PartitionBy references unknown output %q", name)
		}
		for _, previous := range e.spec.PartitionBy[:i] {
			if name == previous {
				return fmt.Errorf("orm: window PartitionBy repeats output %q", name)
			}
		}
		if i == 0 {
			sql.WriteString("PARTITION BY ")
		} else {
			sql.WriteString(", ")
		}
		writeQualifiedIdentifier(sql, "w", name)
	}
	for i, term := range e.spec.OrderBy {
		order := term.value
		if _, ok := aggregateOutputByName(outputs, order.field); !ok || order.direction != orderAscending && order.direction != orderDescending {
			return fmt.Errorf("orm: invalid window OrderBy output %q", order.field)
		}
		for _, previous := range e.spec.OrderBy[:i] {
			if previous.value.field == order.field {
				return fmt.Errorf("orm: window OrderBy repeats output %q", order.field)
			}
		}
		if i == 0 {
			if len(e.spec.PartitionBy) != 0 {
				sql.WriteByte(' ')
			}
			sql.WriteString("ORDER BY ")
		} else {
			sql.WriteString(", ")
		}
		writeQualifiedIdentifier(sql, "w", order.field)
		writeWindowDirection(sql, order.direction)
	}
	if frame := e.spec.Rows; frame != nil {
		if frame.start > frame.end || frame.start == UnboundedFollowing || frame.end == UnboundedPreceding {
			return fmt.Errorf("orm: window %s has invalid ROWS boundaries", e.alias)
		}
		sql.WriteString(" ROWS BETWEEN ")
		writeWindowBoundary(sql, frame.start)
		sql.WriteString(" AND ")
		writeWindowBoundary(sql, frame.end)
	}
	sql.WriteByte(')')
	return nil
}

func writeWindowDirection(sql *strings.Builder, direction orderDirection) {
	if direction == orderAscending {
		sql.WriteString(" ASC")
	} else {
		sql.WriteString(" DESC")
	}
}

func writeWindowBoundary(sql *strings.Builder, boundary int64) {
	switch boundary {
	case UnboundedPreceding:
		sql.WriteString("UNBOUNDED PRECEDING")
	case UnboundedFollowing:
		sql.WriteString("UNBOUNDED FOLLOWING")
	case CurrentRow:
		sql.WriteString("CURRENT ROW")
	default:
		if boundary < 0 {
			sql.WriteString(strconv.FormatInt(-boundary, 10))
			sql.WriteString(" PRECEDING")
		} else {
			sql.WriteString(strconv.FormatInt(boundary, 10))
			sql.WriteString(" FOLLOWING")
		}
	}
}
