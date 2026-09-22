package migrate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mayahiro/go-tidb/schema"
)

// LintOptions selects offline checks. Empty Versions selects all files for
// format checks. SchemaFile requires explicit Versions and Direction: it must
// represent the state before those files, in the specified execution order.
// Without SchemaFile, an empty Direction checks both Up and available Down.
type LintOptions struct {
	SchemaFile string
	Versions   []string
	Direction  Direction
}

// LintIssue identifies an error, warning, or unverified check for one statement.
type LintIssue struct {
	Version   string    `json:"version"`
	Direction Direction `json:"direction"`
	Statement int       `json:"statement"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
}

// LintResult reports the scope of offline checks. ExecutionChecked is always
// false. Unverified counts statements whose checks were incomplete. Existence
// guards alone do not prove idempotence of a statement or a whole file.
type LintResult struct {
	Versions         int         `json:"versions"`
	Statements       int         `json:"statements"`
	Unverified       int         `json:"unverified"`
	SchemaChecked    bool        `json:"schema_checked"`
	ExecutionChecked bool        `json:"execution_checked"`
	Issues           []LintIssue `json:"issues"`
}

// HasErrors reports definite file-format or supported schema conflicts.
func (r LintResult) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

// Lint checks existence guards on supported CREATE/ADD/DROP forms and optionally
// compares table, column, and simple index structure against a prior schema.sql.
// It never connects to a database or enumerates TiDB-specific restrictions.
// Unsupported SQL is explicitly unverified, including subsequent schema checks
// that depend on it. Diagnostics do not prevent manual recovery through Apply.
func Lint(directory string, options LintOptions) (LintResult, error) {
	var result LintResult
	if options.Direction != "" && options.Direction != Up && options.Direction != Down {
		return result, fmt.Errorf("migrate: lint direction must be up or down")
	}
	if options.SchemaFile != "" && (len(options.Versions) == 0 || options.Direction == "") {
		return result, fmt.Errorf("migrate: schema lint requires explicit versions and direction for the prior snapshot")
	}
	migrations, err := Load(directory)
	if err != nil {
		return result, err
	}
	if len(options.Versions) > 0 {
		byVersion := make(map[string]Migration, len(migrations))
		for _, m := range migrations {
			byVersion[m.Version] = m
		}
		migrations = nil
		seen := make(map[string]bool)
		for _, version := range options.Versions {
			m, ok := byVersion[version]
			if !ok || seen[version] {
				return result, fmt.Errorf("migrate: missing or repeated lint version %q", version)
			}
			seen[version] = true
			migrations = append(migrations, m)
		}
	}
	var world *lintSchema
	if options.SchemaFile != "" {
		source, err := readSQL(options.SchemaFile)
		if err != nil {
			return result, err
		}
		world, err = loadLintSchema(source)
		if err != nil {
			return result, err
		}
		result.SchemaChecked = true
	}
	result.Versions = len(migrations)
	for _, m := range migrations {
		directions := []Direction{Up, Down}
		if options.Direction != "" {
			directions = []Direction{options.Direction}
		}
		for _, direction := range directions {
			if direction == Down && m.Down == "" {
				if options.Direction == Down {
					return result, fmt.Errorf("migrate: version %s has no down section", m.Version)
				}
				continue
			}
			statements, err := migrationStatements(m, direction)
			if err != nil {
				return result, err
			}
			for i, statement := range statements {
				result.Statements++
				check := lintCheck{result: &result, location: LintIssue{Version: m.Version, Direction: direction, Statement: i + 1}, world: world}
				check.statement(statement)
				if check.unverified {
					result.Unverified++
				}
			}
		}
	}
	return result, nil
}

type lintCheck struct {
	result     *LintResult
	location   LintIssue
	world      *lintSchema
	unverified bool
}

func (c *lintCheck) issue(severity, message string) {
	item := c.location
	item.Severity, item.Message = severity, message
	c.result.Issues = append(c.result.Issues, item)
	if severity == "unverified" {
		c.unverified = true
	}
	if c.world != nil && severity == "error" {
		c.world.unknown = true
	}
}

func (c *lintCheck) unsupported(message string) {
	c.issue("unverified", message)
	if c.world != nil {
		c.world.unknown = true
	}
}

func (c *lintCheck) guard(present bool, clause string) {
	if !present {
		c.issue("warning", "missing "+clause+"; retrying after partial execution may fail")
	}
}

func (c *lintCheck) statement(source string) {
	ts, err := tokens(source)
	if err != nil {
		c.issue("error", err.Error())
		return
	}
	if c.world != nil && c.world.unknown {
		c.issue("unverified", "schema state is unknown after an earlier unsupported or invalid statement")
	}
	if hasWords(ts, "CREATE", "TABLE") {
		rest, guard := existenceGuard(ts[2:], false)
		c.guard(guard, "IF NOT EXISTS")
		name, body, ok := takeName(rest)
		if !ok || len(body) == 0 || body[0].kind != '(' {
			c.unsupported("only unqualified CREATE TABLE with explicit columns is inspected")
			return
		}
		catalog, err := schema.Parse(source)
		if err != nil {
			c.unsupported("CREATE TABLE definition was not inspected: " + err.Error())
			return
		}
		table, ok := catalog.Table(name)
		if !ok {
			c.unsupported("CREATE TABLE definition could not be inspected")
			return
		}
		if c.world != nil && !c.world.unknown {
			c.createTable(table, guard)
		}
		return
	}
	if hasWords(ts, "DROP", "TABLE") {
		rest, guard := existenceGuard(ts[2:], true)
		c.guard(guard, "IF EXISTS")
		name, rest, ok := takeName(rest)
		if !ok || len(rest) > 0 {
			c.unsupported("only a single unqualified DROP TABLE is inspected")
			return
		}
		if c.world != nil && !c.world.unknown {
			if _, exists := c.world.tables[fold(name)]; !exists && !guard {
				c.issue("error", fmt.Sprintf("table %q does not exist in the prior schema", name))
				return
			}
			delete(c.world.tables, fold(name))
		}
		return
	}
	if hasWords(ts, "ALTER", "TABLE") {
		name, rest, ok := takeName(ts[2:])
		if !ok || len(rest) == 0 {
			c.unsupported("only unqualified ALTER TABLE is inspected")
			return
		}
		for _, part := range clauseParts(rest) {
			c.alter(source, name, part)
		}
		return
	}
	if hasWords(ts, "CREATE", "INDEX") || hasWords(ts, "CREATE", "UNIQUE", "INDEX") {
		unique := keyword(ts[1], "UNIQUE")
		start := 2
		if unique {
			start++
		}
		rest, guard := existenceGuard(ts[start:], false)
		c.guard(guard, "IF NOT EXISTS")
		name, rest, ok := takeName(rest)
		if !ok || !hasWords(rest, "ON") {
			c.unsupported("unsupported CREATE INDEX form")
			return
		}
		table, rest, ok := takeName(rest[1:])
		columns, simple := indexColumns(rest)
		if !ok || !simple {
			c.unsupported("only indexes of simple column names are inspected")
			return
		}
		c.addIndex(table, name, lintIndex{columns: columns, unique: unique}, guard)
		return
	}
	if hasWords(ts, "DROP", "INDEX") {
		rest, guard := existenceGuard(ts[2:], true)
		c.guard(guard, "IF EXISTS")
		name, rest, ok := takeName(rest)
		if !ok || !hasWords(rest, "ON") {
			c.unsupported("unsupported DROP INDEX form")
			return
		}
		table, rest, ok := takeName(rest[1:])
		if !ok || len(rest) > 0 {
			c.unsupported("unsupported DROP INDEX form")
			return
		}
		c.dropIndex(table, name, guard)
		return
	}
	c.unsupported("idempotence and schema effects of this SQL form are not inspected")
}

func (c *lintCheck) alter(source, table string, ts []token) {
	if hasWords(ts, "ADD") {
		rest := ts[1:]
		if hasWords(rest, "COLUMN") {
			rest = rest[1:]
		}
		// Index and constraint clauses are separate forms, not column names.
		if hasWords(rest, "INDEX") || hasWords(rest, "KEY") || hasWords(rest, "UNIQUE") {
			unique := hasWords(rest, "UNIQUE")
			if unique {
				rest = rest[1:]
			}
			if hasWords(rest, "INDEX") || hasWords(rest, "KEY") {
				rest = rest[1:]
			}
			rest, guard := existenceGuard(rest, false)
			c.guard(guard, "IF NOT EXISTS")
			name, body, ok := takeName(rest)
			columns, simple := indexColumns(body)
			if !ok || !simple {
				c.unsupported("only named indexes of simple columns are inspected")
				return
			}
			c.addIndex(table, name, lintIndex{columns: columns, unique: unique}, guard)
			return
		}
		if len(rest) == 0 || hasWords(rest, "CONSTRAINT") || hasWords(rest, "PRIMARY") || hasWords(rest, "FOREIGN") || hasWords(rest, "CHECK") || hasWords(rest, "PARTITION") {
			c.unsupported("this ADD clause is outside the column/index checks")
			return
		}
		rest, guard := existenceGuard(rest, false)
		c.guard(guard, "IF NOT EXISTS")
		name, _, ok := takeName(rest)
		if !ok || len(rest) < 2 {
			c.unsupported("unsupported ADD COLUMN form")
			return
		}
		// Position clauses do not affect the inspected column attributes. Server
		// restrictions on FIRST/AFTER are deliberately outside this lint's scope.
		if len(rest) > 2 && keyword(rest[len(rest)-2], "AFTER") {
			rest = rest[:len(rest)-2]
		} else if keyword(rest[len(rest)-1], "FIRST") {
			rest = rest[:len(rest)-1]
		}
		definition := source[rest[0].start:rest[len(rest)-1].end]
		catalog, err := schema.Parse("CREATE TABLE lint_column (" + definition + ")")
		if err != nil {
			c.unsupported("column definition was not inspected: " + err.Error())
			return
		}
		parsed, _ := catalog.Table("lint_column")
		column, ok := parsed.Column(name)
		if !ok || len(parsed.Indexes()) > 0 {
			c.unsupported("inline keys in ADD COLUMN are outside the schema simulation")
			return
		}
		if target := c.table(table); target != nil {
			if existing, exists := target.columns[fold(name)]; exists {
				if !guard {
					c.issue("error", fmt.Sprintf("column %q already exists in table %q", name, table))
				} else {
					c.compareColumn(existing, column)
				}
				return
			}
			target.columns[fold(name)] = column
		}
		return
	}
	if hasWords(ts, "DROP") {
		rest := ts[1:]
		index := hasWords(rest, "INDEX") || hasWords(rest, "KEY")
		if index || hasWords(rest, "COLUMN") {
			rest = rest[1:]
		}
		if hasWords(rest, "PRIMARY") || hasWords(rest, "FOREIGN") || hasWords(rest, "CONSTRAINT") || hasWords(rest, "PARTITION") {
			c.unsupported("unsupported DROP clause")
			return
		}
		rest, guard := existenceGuard(rest, true)
		c.guard(guard, "IF EXISTS")
		name, rest, ok := takeName(rest)
		if !ok || len(rest) > 0 {
			c.unsupported("unsupported DROP clause")
			return
		}
		if index {
			c.dropIndex(table, name, guard)
			return
		}
		if target := c.table(table); target != nil {
			if _, exists := target.columns[fold(name)]; !exists && !guard {
				c.issue("error", fmt.Sprintf("column %q does not exist in table %q", name, table))
				return
			}
			for _, idx := range target.indexes {
				if slices.Contains(idx.columns, fold(name)) || idx.complex {
					c.unsupported("effects of dropping an indexed column are not simulated")
					return
				}
			}
			delete(target.columns, fold(name))
		}
		return
	}
	c.unsupported("this ALTER clause is outside the ADD/DROP checks")
}

func keyword(t token, word string) bool { return t.kind == 'w' && strings.EqualFold(t.text, word) }
func isIdentifier(t token) bool         { return t.kind == 'w' || t.kind == '`' }
func fold(s string) string              { return strings.ToLower(s) }
func hasWords(ts []token, words ...string) bool {
	if len(ts) < len(words) {
		return false
	}
	for i, w := range words {
		if !keyword(ts[i], w) {
			return false
		}
	}
	return true
}
func takeName(ts []token) (string, []token, bool) {
	if len(ts) == 0 || !isIdentifier(ts[0]) || len(ts) > 1 && ts[1].kind == '.' {
		return "", ts, false
	}
	return identifier(ts[0]), ts[1:], true
}
func existenceGuard(ts []token, dropping bool) ([]token, bool) {
	words := []string{"IF", "NOT", "EXISTS"}
	if dropping {
		words = []string{"IF", "EXISTS"}
	}
	if hasWords(ts, words...) {
		return ts[len(words):], true
	}
	return ts, false
}
func clauseParts(ts []token) [][]token {
	var parts [][]token
	start, depth := 0, 0
	for i, t := range ts {
		switch t.kind {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, ts[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, ts[start:])
}
func indexColumns(ts []token) ([]string, bool) {
	if len(ts) < 3 || ts[0].kind != '(' || ts[len(ts)-1].kind != ')' {
		return nil, false
	}
	var columns []string
	for _, part := range clauseParts(ts[1 : len(ts)-1]) {
		if len(part) != 1 || !isIdentifier(part[0]) {
			return nil, false
		}
		columns = append(columns, fold(identifier(part[0])))
	}
	return columns, true
}
