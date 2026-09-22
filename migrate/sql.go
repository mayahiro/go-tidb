package migrate

import (
	"fmt"
	"strings"
)

type token struct {
	text       string
	kind       byte
	start, end int
}

// tokens preserves quoted values and TiDB executable comments. It deliberately
// does not implement SQL grammar; the server remains the SQL syntax authority.
func tokens(source string) ([]token, error) {
	return scanTokens(source, false)
}

func scanTokens(source string, directives bool) ([]token, error) {
	if strings.IndexByte(source, 0) >= 0 {
		return nil, fmt.Errorf("migrate: SQL contains NUL")
	}
	var result []token
	for i := 0; i < len(source); {
		start := i
		c := source[i]
		if c == 0 {
			return nil, fmt.Errorf("migrate: SQL contains NUL at byte %d", i)
		}
		if c <= ' ' {
			i++
			continue
		}
		if c == '#' || (c == '-' && i+2 < len(source) && source[i+1] == '-' && source[i+2] <= ' ') {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			if directives && strings.HasPrefix(source[start:i], "-- tidbgo:") {
				line := strings.LastIndexByte(source[:start], '\n') + 1
				if strings.TrimSpace(source[line:start]) != "" {
					return nil, fmt.Errorf("migrate: section directives must occupy their own line")
				}
				result = append(result, token{strings.TrimSpace(source[start:i]), 'd', start, i})
			}
			continue
		}
		if c == '/' && i+1 < len(source) && source[i+1] == '*' {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("migrate: unterminated SQL comment at byte %d", start)
			}
			i += end + 4
			executable := strings.HasPrefix(source[start:i], "/*!") || strings.HasPrefix(source[start:i], "/*T!")
			if executable {
				if strings.Contains(source[start:i], ";") {
					return nil, fmt.Errorf("migrate: executable comments cannot contain statement delimiters")
				}
				result = append(result, token{source[start:i], 'e', start, i})
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			i++
			closed := false
			for i < len(source) {
				if source[i] == '\\' && c != '`' {
					i += 2
					continue
				}
				if source[i] != c {
					i++
					continue
				}
				i++
				if i < len(source) && source[i] == c {
					i++
					continue
				}
				closed = true
				break
			}
			if !closed {
				return nil, fmt.Errorf("migrate: unterminated SQL quote at byte %d", start)
			}
			result = append(result, token{source[start:i], c, start, i})
			continue
		}
		if wordByte(c) {
			for i < len(source) && wordByte(source[i]) {
				i++
			}
			result = append(result, token{source[start:i], 'w', start, i})
			continue
		}
		i++
		result = append(result, token{source[start:i], c, start, i})
	}
	return result, nil
}

// migrationParts preserves source text in each direction.
// Directives inside quoted values or block comments remain ordinary SQL text.
func migrationParts(source string) (up, down string, err error) {
	ts, err := scanTokens(source, true)
	if err != nil {
		return "", "", err
	}
	if len(ts) == 0 || ts[0].kind != 'd' || ts[0].text != "-- tidbgo:up" {
		return "", "", fmt.Errorf("migrate: SQL must start with -- tidbgo:up after any header comments")
	}
	first, statements, boundary := 1, 0, len(source)
	for i := 1; i < len(ts); i++ {
		t := ts[i]
		if t.kind == 'd' {
			if t.text != "-- tidbgo:down" || boundary != len(source) {
				return "", "", fmt.Errorf("migrate: expected one up section followed by an optional down section")
			}
			if i != first || statements == 0 {
				return "", "", fmt.Errorf("migrate: up must contain SQL terminated by a semicolon before down")
			}
			boundary, first, statements = t.start, i+1, 0
			continue
		}
		if t.kind == ';' {
			if i > first {
				if err := validateStatement(ts[first:i]); err != nil {
					return "", "", err
				}
				statements++
			}
			first = i + 1
		}
	}
	if first < len(ts) {
		if err := validateStatement(ts[first:]); err != nil {
			return "", "", err
		}
		statements++
	}
	if statements == 0 {
		return "", "", fmt.Errorf("migrate: sections must contain SQL; omit the down section for irreversible changes")
	}
	return source[:boundary], source[boundary:], nil
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 128
}

func identifier(t token) string {
	if t.kind == '`' {
		return strings.ReplaceAll(t.text[1:len(t.text)-1], "``", "`")
	}
	return t.text
}

func splitSQL(source string) ([]string, error) {
	ts, err := tokens(source)
	if err != nil {
		return nil, err
	}
	var result []string
	start, first := 0, 0
	for i, t := range ts {
		if t.kind != ';' {
			continue
		}
		if i > first {
			if err := validateStatement(ts[first:i]); err != nil {
				return nil, err
			}
			result = append(result, strings.TrimSpace(source[start:t.start]))
		}
		start, first = t.end, i+1
	}
	if first < len(ts) {
		if err := validateStatement(ts[first:]); err != nil {
			return nil, err
		}
		result = append(result, strings.TrimSpace(source[start:]))
	}
	return result, nil
}

func validateStatement(ts []token) error {
	if len(ts) < 2 || ts[0].kind != 'w' {
		return fmt.Errorf("migrate: expected a supported SQL statement")
	}
	first, second := strings.ToUpper(ts[0].text), strings.ToUpper(ts[1].text)
	valid := false
	switch first {
	case "CREATE":
		valid = second == "TABLE" || second == "INDEX" || second == "UNIQUE" || second == "VECTOR"
	case "ALTER", "RENAME":
		valid = second == "TABLE"
	case "DROP":
		valid = second == "TABLE" || second == "INDEX"
	case "TRUNCATE":
		valid = true
	case "INSERT", "UPDATE", "DELETE", "REPLACE":
		valid = true
	}
	if !valid {
		return fmt.Errorf("migrate: unsupported statement %s; use table/index DDL or INSERT/UPDATE/DELETE/REPLACE", first)
	}
	return rejectReserved(ts)
}

func rejectReserved(ts []token) error {
	for _, t := range ts {
		if t.kind == 'e' {
			start, err := executableBodyStart(t.text)
			if err != nil {
				return err
			}
			body, err := tokens(t.text[start : len(t.text)-2])
			if err != nil {
				return err
			}
			if err := rejectReserved(body); err != nil {
				return err
			}
		}
		if t.kind != 'w' && t.kind != '`' {
			continue
		}
		name := strings.ToLower(identifier(t))
		if name == historyTable || name == "get_lock" || name == "release_lock" || name == "release_all_locks" {
			return fmt.Errorf("migrate: SQL references reserved migration state or locking functions")
		}
	}
	return nil
}

func quoteIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }

// canonicalSQL keeps precision, literals, comments carrying executable TiDB
// attributes, and every other token. Only allocator counters are omitted.
func canonicalSQL(source string) (string, error) {
	ts, err := tokens(source)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(source))
	depth := 0
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if t.kind == ';' {
			continue
		}
		if depth == 0 && t.kind == 'w' && allocator(t.text) && i+2 < len(ts) && ts[i+1].kind == '=' && decimalToken(ts[i+2]) {
			i += 2
			continue
		}
		if t.kind == 'e' && depth == 0 {
			cleaned, err := normalizeAllocatorComment(t.text)
			if err != nil {
				return "", err
			}
			if cleaned == "" {
				continue
			}
			t.text = cleaned
		}
		if t.kind == '(' {
			depth++
		}
		if t.kind == ')' {
			depth--
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if t.kind == 'w' {
			b.WriteString(strings.ToUpper(t.text))
		} else {
			b.WriteString(t.text)
		}
	}
	return b.String(), nil
}

func allocator(s string) bool {
	return strings.EqualFold(s, "AUTO_INCREMENT") || strings.EqualFold(s, "AUTO_RANDOM_BASE")
}
func decimalToken(t token) bool {
	if t.kind != 'w' || t.text == "" {
		return false
	}
	for _, c := range t.text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func normalizeAllocatorComment(s string) (string, error) {
	if !strings.Contains(strings.ToUpper(s), "AUTO_RANDOM_BASE") && !strings.Contains(strings.ToUpper(s), "AUTO_INCREMENT") {
		return s, nil
	}
	start, err := executableBodyStart(s)
	if err != nil {
		return "", err
	}
	body, err := canonicalSQL(s[start : len(s)-2])
	if err != nil || body == "" {
		return body, err
	}
	return s[:start] + " " + body + " */", nil
}

func executableBodyStart(s string) (int, error) {
	start := 3
	if strings.HasPrefix(s, "/*T!") {
		start = 4
	}
	if start < len(s) && s[start] == '[' {
		end := strings.IndexByte(s[start:], ']')
		if end < 0 {
			return 0, fmt.Errorf("migrate: invalid executable comment")
		}
		start += end + 1
	}
	for start < len(s)-2 && s[start] >= '0' && s[start] <= '9' {
		start++
	}
	return start, nil
}

// normalizedDDL preserves SHOW CREATE text, including adjacent literal prefixes,
// decimal syntax and executable comments. Only allocator options are removed.
func normalizedDDL(source string) (string, error) {
	ts, err := tokens(source)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(len(source))
	last, depth := 0, 0
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		if depth == 0 && t.kind == 'w' && allocator(t.text) && i+2 < len(ts) && ts[i+1].kind == '=' && decimalToken(ts[i+2]) {
			b.WriteString(strings.TrimRight(source[last:t.start], " \t\r\n"))
			last = ts[i+2].end
			i += 2
			continue
		}
		if t.kind == 'e' && depth == 0 {
			cleaned, err := normalizeAllocatorComment(t.text)
			if err != nil {
				return "", err
			}
			if cleaned != t.text {
				prefix := source[last:t.start]
				if cleaned == "" {
					prefix = strings.TrimRight(prefix, " \t\r\n")
				}
				b.WriteString(prefix)
				b.WriteString(cleaned)
				last = t.end
			}
		}
		if t.kind == '(' {
			depth++
		}
		if t.kind == ')' {
			depth--
		}
	}
	b.WriteString(source[last:])
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b.String()), ";")), nil
}

func migrationStatements(m Migration, direction Direction) ([]string, error) {
	source := m.Up
	if direction == Down {
		source = m.Down
	}
	statements, err := splitSQL(source)
	if err != nil {
		return nil, fmt.Errorf("migrate: %s %s: %w", filename(m), direction, err)
	}
	if len(statements) == 0 {
		return nil, fmt.Errorf("migrate: %s %s: section contains no SQL", filename(m), direction)
	}
	return statements, nil
}
