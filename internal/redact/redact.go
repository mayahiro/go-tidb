// Package redact removes secrets from text before it reaches logs or reports.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Replacement is emitted in place of secret material.
const Replacement = "[REDACTED]"

const sensitiveName = `(?:(?:[a-z0-9]+[_-])*(?:password|passwd|pwd|token|secret|dsn)|credential|api[_-]?key|access[_-]?key|private[_-]?key)`

var (
	urlCredentials         = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^:/@\s]+:)([^@\s/]+)(@)`)
	doubleQuotedAssignment = regexp.MustCompile(`(?i)(["']?` + sensitiveName + `["']?\s*[:=]\s*")((?:\\.|[^"\\])*)(")`)
	singleQuotedAssignment = regexp.MustCompile(`(?i)(["']?` + sensitiveName + `["']?\s*[:=]\s*')((?:\\.|[^'\\])*)(')`)
	unquotedAssignment     = regexp.MustCompile(`(?i)(["']?` + sensitiveName + `["']?\s*[:=]\s*)([^"',;}\s&]+)`)
)

// DSN returns a safe representation of a complete data source name.
func DSN(string) string {
	return Replacement
}

// String redacts common credential forms and every explicitly supplied secret
// from value. Longer explicit secrets are replaced first.
func String(value string, secrets ...string) string {
	explicit := append([]string(nil), secrets...)
	sort.SliceStable(explicit, func(left, right int) bool {
		return len(explicit[left]) > len(explicit[right])
	})
	for _, secret := range explicit {
		if secret == "" || secret == Replacement {
			continue
		}
		value = strings.ReplaceAll(value, secret, Replacement)
	}

	value = urlCredentials.ReplaceAllString(value, `${1}`+Replacement+`${3}`)
	value = doubleQuotedAssignment.ReplaceAllString(value, `${1}`+Replacement+`${3}`)
	value = singleQuotedAssignment.ReplaceAllString(value, `${1}`+Replacement+`${3}`)
	value = unquotedAssignment.ReplaceAllString(value, `${1}`+Replacement)
	return value
}

// Error returns a redacted error string. A nil error produces an empty string.
func Error(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	return String(err.Error(), secrets...)
}
