// Package modelmeta contains deterministic model naming rules shared by the
// runtime descriptor and source analysis.
package modelmeta

import (
	"strings"
	"unicode"
)

// SnakeCase converts one Go identifier to its default physical SQL name.
func SnakeCase(value string) string {
	runes := []rune(value)
	var result strings.Builder
	for index, current := range runes {
		if unicode.IsUpper(current) {
			if index > 0 {
				previous := runes[index-1]
				nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
				if unicode.IsLower(previous) || unicode.IsDigit(previous) || unicode.IsUpper(previous) && nextIsLower {
					result.WriteByte('_')
				}
			}
			result.WriteRune(unicode.ToLower(current))
			continue
		}
		result.WriteRune(current)
	}
	return result.String()
}

// ValidSQLIdentifier reports whether value is a simple TiDB identifier that
// can be quoted without interpreting user-provided SQL syntax.
func ValidSQLIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || current == '_' || index > 0 && current >= '0' && current <= '9' {
			continue
		}
		return false
	}
	return true
}
