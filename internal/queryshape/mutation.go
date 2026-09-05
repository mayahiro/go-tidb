package queryshape

// Mutation describes the row-selection side of a validated conditional write.
// Assignments and bind values are deliberately absent. SQL statement identity
// remains the existing runtime statement fingerprint, not a query fingerprint.
type Mutation struct {
	Model            string              `json:"model"`
	Table            string              `json:"table"`
	Predicates       []MutationPredicate `json:"predicates"`
	SoftDeleteColumn string              `json:"soft_delete_column,omitempty"`
}

// MutationPredicate is a scalar-only predicate tree. EmptyList distinguishes
// IN () and NOT IN () compiled as FALSE and TRUE without retaining list values.
type MutationPredicate struct {
	Operator  PredicateOperator   `json:"operator"`
	Column    string              `json:"column,omitempty"`
	EmptyList bool                `json:"empty_list,omitempty"`
	Children  []MutationPredicate `json:"children,omitempty"`
}
