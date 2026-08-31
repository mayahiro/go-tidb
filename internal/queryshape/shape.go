// Package queryshape defines the database-independent query representation
// shared by offline diagnostics and opt-in runtime capture.
package queryshape

// Version identifies the canonical shape and fingerprint format.
const Version = 1

// Query is a bind-value-free description of one validated typed SELECT.
type Query struct {
	Model            string           `json:"model"`
	Table            string           `json:"table"`
	Projection       []string         `json:"projection"`
	Predicates       []Predicate      `json:"predicates"`
	Order            []OrderTerm      `json:"order"`
	SeekAfter        bool             `json:"seek_after"`
	Limit            Bound            `json:"limit"`
	Offset           Bound            `json:"offset"`
	WithDeleted      bool             `json:"with_deleted"`
	SoftDeleteColumn string           `json:"soft_delete_column,omitempty"`
	Preloads         []Preload        `json:"preloads"`
	Compiler         CompilerDecision `json:"compiler"`
	IndexAccesses    []IndexAccess    `json:"index_accesses"`
}

// Bound describes whether a LIMIT or OFFSET is present and retains its value
// for diagnostics. Fingerprints intentionally encode only presence because
// the value remains a bind argument in generated SQL.
type Bound struct {
	Set   bool  `json:"set"`
	Value int64 `json:"-"`
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
	Operator              PredicateOperator `json:"operator"`
	Table                 string            `json:"table,omitempty"`
	Field                 string            `json:"field,omitempty"`
	Column                string            `json:"column,omitempty"`
	Relation              string            `json:"relation,omitempty"`
	RelationKind          string            `json:"relation_kind,omitempty"`
	RelationSourceColumns []string          `json:"relation_source_columns,omitempty"`
	RelationTargetColumns []string          `json:"relation_target_columns,omitempty"`
	JunctionTable         string            `json:"junction_table,omitempty"`
	JunctionSourceColumns []string          `json:"junction_source_columns,omitempty"`
	JunctionTargetColumns []string          `json:"junction_target_columns,omitempty"`
	SoftDeleteColumn      string            `json:"soft_delete_column,omitempty"`
	ValueCount            int               `json:"value_count,omitempty"`
	Children              []Predicate       `json:"children,omitempty"`
}

// OrderDirection identifies one query ordering direction.
type OrderDirection string

const (
	OrderAscending  OrderDirection = "ascending"
	OrderDescending OrderDirection = "descending"
)

// OrderTerm identifies one mapped ordering field and physical column.
type OrderTerm struct {
	Field     string         `json:"field,omitempty"`
	Column    string         `json:"column"`
	Direction OrderDirection `json:"direction"`
}

// Preload describes one compiled Relation load without runtime parent keys.
type Preload struct {
	Path                  string      `json:"path"`
	Relation              string      `json:"relation"`
	Kind                  string      `json:"kind"`
	Table                 string      `json:"table"`
	SourceColumns         []string    `json:"source_columns"`
	TargetColumns         []string    `json:"target_columns"`
	JunctionTable         string      `json:"junction_table,omitempty"`
	JunctionSourceColumns []string    `json:"junction_source_columns,omitempty"`
	JunctionTargetColumns []string    `json:"junction_target_columns,omitempty"`
	Projection            []string    `json:"projection"`
	Order                 []OrderTerm `json:"order"`
	Inline                bool        `json:"inline"`
	LoadAllSources        bool        `json:"load_all_sources"`
	BatchSize             int         `json:"batch_size"`
	WithDeleted           bool        `json:"with_deleted"`
	SoftDeleteColumn      string      `json:"soft_delete_column,omitempty"`
	Children              []Preload   `json:"children"`
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
	Rewrite  CompilerRewrite `json:"rewrite"`
	Relation string          `json:"relation,omitempty"`
	Reason   string          `json:"reason,omitempty"`
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
	Kind            IndexAccessKind `json:"kind"`
	Table           string          `json:"table"`
	Relation        string          `json:"relation,omitempty"`
	EqualityColumns []string        `json:"equality_columns"`
	OrderColumns    []string        `json:"order_columns"`
}
