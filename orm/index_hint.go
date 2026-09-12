package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/internal/modelmeta"
)

// ForceIndex selects one physical index for the root table of this SELECT.
//
// The last call replaces the previous name. Names must match
// [A-Za-z_][A-Za-z0-9_]* and contain at most 64 bytes; empty names are invalid.
// Build validates and quotes the name without checking the database schema.
// The hint applies at every offset and to all terminals, including Count and
// Exists. It does not propagate to relation targets or secondary preloads.
// Root-replacing relation TopN and Count rewrites are disabled when it is set.
// Use a separate query when Count should choose its index independently.
// Index availability and the resulting plan remain the database's responsibility;
// measure representative and unfavorable inputs before selecting an index.
func (q *SelectQuery[T]) ForceIndex(name string) *SelectQuery[T] {
	if q == nil {
		return nil
	}
	q.selection.forceIndex = name
	q.selection.forceIndexSet = true
	return q
}

func validateForceIndex(selection *selectQuery) error {
	if selection.forceIndexSet && !modelmeta.ValidSQLIdentifier(selection.forceIndex) {
		return fmt.Errorf("orm: SELECT ForceIndex requires a simple SQL identifier of at most 64 bytes, got %q", selection.forceIndex)
	}
	return nil
}

func forceIndexSQLCapacity(name string) int {
	if name == "" {
		return 0
	}
	return len(" FORCE INDEX (``)") + len(name)
}

func writeForceIndex(query *strings.Builder, name string) {
	if name == "" {
		return
	}
	query.WriteString(" FORCE INDEX (")
	writeQuotedIdentifier(query, name)
	query.WriteByte(')')
}
