// Package schemacompat contains physical-schema proofs shared by reflection
// and source-based model compatibility checks.
package schemacompat

import (
	"strings"

	"github.com/mayahiro/go-tidb/schema"
)

// HasUniqueKey reports whether an unconditional, complete-column primary or
// unique key constrains columns or a subset of columns. Nullable unique keys
// prove uniqueness only for non-NULL values, as in TiDB's unique constraints.
func HasUniqueKey(table schema.Table, columns []string) bool {
	if len(columns) == 0 {
		return false
	}
	for _, index := range table.Indexes() {
		indexColumns := index.Columns()
		if !index.ProvidesUnconditionalUniqueness() || len(indexColumns) == 0 {
			continue
		}
		provesUnique := true
		for _, indexedColumn := range indexColumns {
			found := false
			for _, targetColumn := range columns {
				if strings.EqualFold(indexedColumn, targetColumn) {
					found = true
					break
				}
			}
			if !found {
				provesUnique = false
				break
			}
		}
		if provesUnique {
			return true
		}
	}
	return false
}

// EqualColumns compares ordered column names without regard to letter case.
func EqualColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}
