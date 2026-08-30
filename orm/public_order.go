package orm

// OrderTerm is an immutable scalar SELECT ordering created by Asc or Desc.
type OrderTerm struct {
	value orderTerm
}

// Asc orders a mapped Go field in ascending TiDB order.
func Asc(field string) OrderTerm {
	return OrderTerm{value: orderTerm{field: field, direction: orderAscending}}
}

// Desc orders a mapped Go field in descending TiDB order.
func Desc(field string) OrderTerm {
	return OrderTerm{value: orderTerm{field: field, direction: orderDescending}}
}
