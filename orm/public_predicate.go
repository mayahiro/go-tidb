package orm

// Predicate is an immutable SELECT or mutation condition created by this
// package.
type Predicate struct {
	value predicate
}

// Equal creates a field = value predicate.
func Equal(field string, value any) Predicate {
	return comparisonPredicate(predicateEqual, field, value)
}

// NotEqual creates a field <> value predicate.
func NotEqual(field string, value any) Predicate {
	return comparisonPredicate(predicateNotEqual, field, value)
}

// GreaterThan creates a field > value predicate.
func GreaterThan(field string, value any) Predicate {
	return comparisonPredicate(predicateGreaterThan, field, value)
}

// GreaterThanOrEqual creates a field >= value predicate.
func GreaterThanOrEqual(field string, value any) Predicate {
	return comparisonPredicate(predicateGreaterThanOrEqual, field, value)
}

// LessThan creates a field < value predicate.
func LessThan(field string, value any) Predicate {
	return comparisonPredicate(predicateLessThan, field, value)
}

// LessThanOrEqual creates a field <= value predicate.
func LessThanOrEqual(field string, value any) Predicate {
	return comparisonPredicate(predicateLessThanOrEqual, field, value)
}

// In creates a field IN values predicate from a typed slice.
//
// An empty values list compiles to FALSE.
func In[T any](field string, values []T) Predicate {
	return listPredicate(predicateIn, field, values)
}

// NotIn creates a field NOT IN values predicate from a typed slice.
//
// An empty values list compiles to TRUE.
func NotIn[T any](field string, values []T) Predicate {
	return listPredicate(predicateNotIn, field, values)
}

// IsNull creates a field IS NULL predicate.
func IsNull(field string) Predicate {
	return Predicate{value: predicate{operator: predicateIsNull, field: field}}
}

// IsNotNull creates a field IS NOT NULL predicate.
func IsNotNull(field string) Predicate {
	return Predicate{value: predicate{operator: predicateIsNotNull, field: field}}
}

// Between creates an inclusive field BETWEEN lower AND upper predicate.
func Between(field string, lower, upper any) Predicate {
	return Predicate{value: predicate{operator: predicateBetween, field: field, values: []any{lower, upper}}}
}

// Contains creates a string predicate matching value anywhere in field.
func Contains(field string, value any) Predicate {
	return comparisonPredicate(predicateContains, field, value)
}

// HasPrefix creates a string predicate matching value at the start of field.
func HasPrefix(field string, value any) Predicate {
	return comparisonPredicate(predicateHasPrefix, field, value)
}

// HasSuffix creates a string predicate matching value at the end of field.
func HasSuffix(field string, value any) Predicate {
	return comparisonPredicate(predicateHasSuffix, field, value)
}

// Has creates a predicate requiring at least one related row matching every
// supplied target-model predicate.
//
// Without target predicates, Has only requires relation existence. Relation
// and target fields use exported Go field names, and nested relation
// predicates are allowed in the target scope. The compiler can emit EXISTS,
// TiDB semi-join hints, or a metadata-proven relation-first TopN query while
// preserving this logical predicate for relation-consistent data.
func Has(relation string, predicates ...Predicate) Predicate {
	children := make([]predicate, len(predicates))
	for index := range predicates {
		children[index] = predicates[index].value
	}
	return Predicate{value: predicate{operator: predicateHasRelation, hasRelation: true, field: relation, children: children}}
}

// And groups two or more predicates with AND.
func And(predicates ...Predicate) Predicate {
	return logicalPredicate(predicateAnd, predicates)
}

// Or groups two or more predicates with OR.
func Or(predicates ...Predicate) Predicate {
	return logicalPredicate(predicateOr, predicates)
}

// Not negates one predicate.
func Not(current Predicate) Predicate {
	return Predicate{value: predicate{operator: predicateNot, hasRelation: current.value.hasRelation, children: []predicate{current.value}}}
}

func comparisonPredicate(operator predicateOperator, field string, value any) Predicate {
	return Predicate{value: predicate{operator: operator, field: field, values: []any{value}}}
}

func listPredicate[T any](operator predicateOperator, field string, values []T) Predicate {
	arguments := make([]any, len(values))
	for index := range values {
		arguments[index] = values[index]
	}
	return Predicate{value: predicate{operator: operator, field: field, values: arguments}}
}

func logicalPredicate(operator predicateOperator, values []Predicate) Predicate {
	children := make([]predicate, len(values))
	hasRelation := false
	for index := range values {
		children[index] = values[index].value
		hasRelation = hasRelation || children[index].hasRelation
	}
	return Predicate{value: predicate{operator: operator, hasRelation: hasRelation, children: children}}
}
