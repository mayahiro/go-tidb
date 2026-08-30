// Package queryshape defines the database-independent query representation
// shared by offline diagnostics and opt-in runtime capture.
package queryshape

// Version identifies the canonical shape and fingerprint format.
const Version = 1

// Query is a bind-value-free description of one validated typed SELECT.
type Query struct {
	Model            string
	Table            string
	Projection       []string
	Predicates       []Predicate
	Order            []OrderTerm
	SeekAfter        bool
	Limit            Bound
	Offset           Bound
	WithDeleted      bool
	SoftDeleteColumn string
	Preloads         []Preload
	Compiler         CompilerDecision
	IndexAccesses    []IndexAccess
}

// Bound describes whether a LIMIT or OFFSET is present and retains its value
// for diagnostics. Fingerprints intentionally encode only presence because
// the value remains a bind argument in generated SQL.
type Bound struct {
	Set   bool
	Value int64
}

// PredicateOperator identifies one logical or scalar predicate operation.
type PredicateOperator string

const (
	PredicateEqual              PredicateOperator = "equal"
	PredicateNotEqual           PredicateOperator = "not_equal"
	PredicateGreaterThan        PredicateOperator = "greater_than"
	PredicateGreaterThanOrEqual PredicateOperator = "greater_than_or_equal"
	PredicateLessThan           PredicateOperator = "less_than"
	PredicateLessThanOrEqual    PredicateOperator = "less_than_or_equal"
	PredicateIn                 PredicateOperator = "in"
	PredicateNotIn              PredicateOperator = "not_in"
	PredicateIsNull             PredicateOperator = "is_null"
	PredicateIsNotNull          PredicateOperator = "is_not_null"
	PredicateBetween            PredicateOperator = "between"
	PredicateContains           PredicateOperator = "contains"
	PredicateHasPrefix          PredicateOperator = "has_prefix"
	PredicateHasSuffix          PredicateOperator = "has_suffix"
	PredicateHasRelation        PredicateOperator = "has_relation"
	PredicateAnd                PredicateOperator = "and"
	PredicateOr                 PredicateOperator = "or"
	PredicateNot                PredicateOperator = "not"
)

// Predicate is one bind-value-free query predicate. Table and Column are set
// for scalar predicates, while Relation and Table identify relation targets.
type Predicate struct {
	Operator              PredicateOperator
	Table                 string
	Field                 string
	Column                string
	Relation              string
	RelationKind          string
	RelationSourceColumns []string
	RelationTargetColumns []string
	JunctionTable         string
	JunctionSourceColumns []string
	JunctionTargetColumns []string
	SoftDeleteColumn      string
	ValueCount            int
	Children              []Predicate
}

// OrderDirection identifies one query ordering direction.
type OrderDirection string

const (
	OrderAscending  OrderDirection = "ascending"
	OrderDescending OrderDirection = "descending"
)

// OrderTerm identifies one mapped ordering field and physical column.
type OrderTerm struct {
	Field     string
	Column    string
	Direction OrderDirection
}

// Preload describes one compiled Relation load without runtime parent keys.
type Preload struct {
	Path                  string
	Relation              string
	Kind                  string
	Table                 string
	SourceColumns         []string
	TargetColumns         []string
	JunctionTable         string
	JunctionSourceColumns []string
	JunctionTargetColumns []string
	Projection            []string
	Order                 []OrderTerm
	Inline                bool
	LoadAllSources        bool
	BatchSize             int
	WithDeleted           bool
	SoftDeleteColumn      string
	Children              []Preload
}

// CompilerRewrite identifies a query compiler decision that changes the SQL
// access shape while preserving logical results.
type CompilerRewrite string

const (
	CompilerRewriteNone                 CompilerRewrite = "none"
	CompilerRewriteRelationTopN         CompilerRewrite = "relation_topn"
	CompilerRewriteRelationTopNFallback CompilerRewrite = "relation_topn_fallback"
)

// CompilerDecision records a compiler rewrite or a deterministic fallback.
type CompilerDecision struct {
	Rewrite  CompilerRewrite
	Relation string
	Reason   string
}

// IndexAccessKind identifies a high-confidence ordered access shape that can
// be compared with a physical index prefix offline.
type IndexAccessKind string

const (
	IndexAccessRootOrderedLimit IndexAccessKind = "root_ordered_limit"
	IndexAccessRelationTopN     IndexAccessKind = "relation_topn"
)

// IndexAccess describes equality columns followed by ordered columns for one
// positive-LIMIT access. Directions are uniform and can use a forward or
// reverse scan of the same simple index prefix.
type IndexAccess struct {
	Kind            IndexAccessKind
	Table           string
	Relation        string
	EqualityColumns []string
	OrderColumns    []string
}
