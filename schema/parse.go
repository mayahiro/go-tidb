package schema

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoCreateTables reports SQL that contains no CREATE TABLE definition.
	ErrNoCreateTables = errors.New("schema SQL contains no CREATE TABLE definitions")
)

// ParseError reports one unsupported or malformed part of a CREATE TABLE
// definition. Position identifies the original SQL source location.
type ParseError struct {
	// Position identifies the original SQL source location.
	Position Position
	// Message explains the unsupported or malformed syntax.
	Message string
}

// Error implements error.
func (e *ParseError) Error() string {
	if e == nil {
		return "invalid schema SQL"
	}
	return fmt.Sprintf("schema SQL at line %d, column %d: %s", e.Position.Line, e.Position.Column, e.Message)
}

// Parse builds an immutable catalog from TiDB CREATE TABLE statements without
// executing SQL or accessing a database. Non-CREATE-TABLE statements are
// ignored so schema-only dump wrappers such as SET and DROP TABLE are accepted.
//
// Parse accepts ordinary TiDB CREATE TABLE SQL and SHOW CREATE TABLE output,
// including TiDB executable comments used to preserve AUTO_RANDOM metadata.
// CREATE TABLE LIKE and CREATE TABLE AS SELECT are rejected because they do not
// contain a self-contained physical table definition.
func Parse(sql string) (*Catalog, error) {
	ranges, err := splitSQLStatements(sql)
	if err != nil {
		return nil, err
	}

	catalog := &Catalog{
		tables: make([]Table, 0),
		byName: make(map[string]int),
	}
	for _, statement := range ranges {
		tokens, tokenErr := lexSQL(sql, statement.start, statement.end)
		if tokenErr != nil {
			return nil, tokenErr
		}
		table, createTable, parseErr := parseCreateTable(sql, tokens)
		if parseErr != nil {
			return nil, parseErr
		}
		if !createTable {
			continue
		}
		key := foldIdentifier(table.name)
		if previous, exists := catalog.byName[key]; exists {
			previousTable := catalog.tables[previous]
			return nil, parseErrorAt(sql, table.position.Offset-1, fmt.Sprintf("table %q is already defined at line %d", table.name, previousTable.position.Line))
		}
		catalog.byName[key] = len(catalog.tables)
		catalog.tables = append(catalog.tables, table)
	}
	if len(catalog.tables) == 0 {
		return nil, ErrNoCreateTables
	}
	return catalog, nil
}

type sqlRange struct {
	start int
	end   int
}

func splitSQLStatements(sql string) ([]sqlRange, error) {
	ranges := make([]sqlRange, 0)
	start := 0
	for index := 0; index < len(sql); {
		switch sql[index] {
		case '\'', '"', '`':
			quote := sql[index]
			quoteStart := index
			index++
			closed := false
			for index < len(sql) {
				if sql[index] == '\\' && quote != '`' {
					index += 2
					continue
				}
				if sql[index] != quote {
					index++
					continue
				}
				if index+1 < len(sql) && sql[index+1] == quote {
					index += 2
					continue
				}
				index++
				closed = true
				break
			}
			if !closed {
				return nil, parseErrorAt(sql, quoteStart, "unterminated quoted value")
			}
		case '#':
			index = skipLine(sql, index+1)
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				index = skipLine(sql, index+2)
				continue
			}
			index++
		case '/':
			if index+1 >= len(sql) || sql[index+1] != '*' {
				index++
				continue
			}
			commentStart := index
			end := strings.Index(sql[index+2:], "*/")
			if end < 0 {
				return nil, parseErrorAt(sql, commentStart, "unterminated block comment")
			}
			index += end + 4
		case ';':
			if hasNonSpace(sql[start:index]) {
				ranges = append(ranges, sqlRange{start: start, end: index})
			}
			index++
			start = index
		default:
			index++
		}
	}
	if hasNonSpace(sql[start:]) {
		ranges = append(ranges, sqlRange{start: start, end: len(sql)})
	}
	return ranges, nil
}

func skipLine(sql string, index int) int {
	for index < len(sql) && sql[index] != '\n' {
		index++
	}
	return index
}

func hasNonSpace(value string) bool {
	return strings.TrimSpace(value) != ""
}

type sqlTokenKind uint8

const (
	tokenWord sqlTokenKind = iota + 1
	tokenQuotedIdentifier
	tokenString
	tokenNumber
	tokenSymbol
)

type sqlToken struct {
	kind   sqlTokenKind
	text   string
	offset int
}

func (t sqlToken) keyword(keyword string) bool {
	return t.kind == tokenWord && strings.EqualFold(t.text, keyword)
}

func (t sqlToken) identifier() (string, bool) {
	if t.kind != tokenWord && t.kind != tokenQuotedIdentifier {
		return "", false
	}
	return t.text, true
}

type sqlLexer struct {
	source string
	tokens []sqlToken
}

func lexSQL(source string, start, end int) ([]sqlToken, error) {
	tokenCapacity := (end - start) / 6
	if tokenCapacity < 8 {
		tokenCapacity = 8
	}
	lexer := &sqlLexer{source: source, tokens: make([]sqlToken, 0, tokenCapacity)}
	if err := lexer.lexRange(start, end); err != nil {
		return nil, err
	}
	return lexer.tokens, nil
}

func (l *sqlLexer) lexRange(start, end int) error {
	for index := start; index < end; {
		character := l.source[index]
		if isSQLSpace(character) {
			index++
			continue
		}
		if character == '#' || character == '-' && index+1 < end && l.source[index+1] == '-' {
			index = skipLineBounded(l.source, index+1, end)
			continue
		}
		if character == '/' && index+1 < end && l.source[index+1] == '*' {
			closeOffset := strings.Index(l.source[index+2:end], "*/")
			if closeOffset < 0 {
				return parseErrorAt(l.source, index, "unterminated block comment")
			}
			payloadStart := index + 2
			payloadEnd := payloadStart + closeOffset
			if executableStart, executable := executableCommentPayload(l.source, payloadStart, payloadEnd); executable {
				if err := l.lexRange(executableStart, payloadEnd); err != nil {
					return err
				}
			}
			index = payloadEnd + 2
			continue
		}
		if character == '`' {
			value, next, err := lexQuoted(l.source, index, end, '`')
			if err != nil {
				return err
			}
			l.tokens = append(l.tokens, sqlToken{kind: tokenQuotedIdentifier, text: value, offset: index})
			index = next
			continue
		}
		if character == '\'' || character == '"' {
			value, next, err := lexQuoted(l.source, index, end, character)
			if err != nil {
				return err
			}
			l.tokens = append(l.tokens, sqlToken{kind: tokenString, text: value, offset: index})
			index = next
			continue
		}
		if isSQLWordStart(character) {
			wordStart := index
			index++
			for index < end && isSQLWordPart(l.source[index]) {
				index++
			}
			l.tokens = append(l.tokens, sqlToken{kind: tokenWord, text: l.source[wordStart:index], offset: wordStart})
			continue
		}
		if character >= '0' && character <= '9' {
			numberStart := index
			index++
			for index < end && l.source[index] >= '0' && l.source[index] <= '9' {
				index++
			}
			l.tokens = append(l.tokens, sqlToken{kind: tokenNumber, text: l.source[numberStart:index], offset: numberStart})
			continue
		}
		l.tokens = append(l.tokens, sqlToken{kind: tokenSymbol, text: l.source[index : index+1], offset: index})
		index++
	}
	return nil
}

func skipLineBounded(source string, index, end int) int {
	for index < end && source[index] != '\n' {
		index++
	}
	return index
}

func executableCommentPayload(source string, start, end int) (int, bool) {
	if start >= end {
		return 0, false
	}
	if end-start >= 2 && source[start] == 'T' && source[start+1] == '!' {
		start += 2
		for start < end && isSQLSpace(source[start]) {
			start++
		}
		if start < end && source[start] == '[' {
			closeOffset := strings.IndexByte(source[start:end], ']')
			if closeOffset < 0 {
				return 0, false
			}
			start += closeOffset + 1
		}
		return start, true
	}
	if source[start] == '!' {
		start++
		for start < end && source[start] >= '0' && source[start] <= '9' {
			start++
		}
		return start, true
	}
	return 0, false
}

func lexQuoted(source string, start, end int, quote byte) (string, int, error) {
	contentStart := start + 1
	segmentStart := contentStart
	var value *strings.Builder
	for index := start + 1; index < end; {
		if source[index] == '\\' && quote != '`' && index+1 < end {
			if value == nil {
				value = &strings.Builder{}
				value.Grow(index - contentStart + 1)
			}
			value.WriteString(source[segmentStart:index])
			value.WriteByte(source[index+1])
			index += 2
			segmentStart = index
			continue
		}
		if source[index] != quote {
			index++
			continue
		}
		if index+1 < end && source[index+1] == quote {
			if value == nil {
				value = &strings.Builder{}
				value.Grow(index - contentStart + 1)
			}
			value.WriteString(source[segmentStart:index])
			value.WriteByte(quote)
			index += 2
			segmentStart = index
			continue
		}
		if value == nil {
			return source[contentStart:index], index + 1, nil
		}
		value.WriteString(source[segmentStart:index])
		return value.String(), index + 1, nil
	}
	return "", 0, parseErrorAt(source, start, "unterminated quoted value")
}

func isSQLSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func isSQLWordStart(character byte) bool {
	return character == '_' || character == '$' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= 0x80
}

func isSQLWordPart(character byte) bool {
	return isSQLWordStart(character) || character >= '0' && character <= '9'
}

func parseCreateTable(source string, tokens []sqlToken) (Table, bool, error) {
	if len(tokens) == 0 || !tokens[0].keyword("CREATE") {
		return Table{}, false, nil
	}
	index := 1
	if tokenAtKeyword(tokens, index, "GLOBAL") && tokenAtKeyword(tokens, index+1, "TEMPORARY") {
		index += 2
	} else if tokenAtKeyword(tokens, index, "TEMPORARY") {
		index++
	}
	if !tokenAtKeyword(tokens, index, "TABLE") {
		return Table{}, false, nil
	}
	index++
	if tokenAtKeyword(tokens, index, "IF") && tokenAtKeyword(tokens, index+1, "NOT") && tokenAtKeyword(tokens, index+2, "EXISTS") {
		index += 3
	}
	if index >= len(tokens) {
		return Table{}, true, parseErrorAt(source, tokens[len(tokens)-1].offset, "CREATE TABLE is missing a table name")
	}
	firstName, ok := tokens[index].identifier()
	if !ok {
		return Table{}, true, parseErrorAt(source, tokens[index].offset, "CREATE TABLE requires an identifier as its table name")
	}
	nameToken := tokens[index]
	index++
	schemaName := ""
	tableName := firstName
	if tokenAtSymbol(tokens, index, ".") {
		if index+1 >= len(tokens) {
			return Table{}, true, parseErrorAt(source, tokens[index].offset, "schema qualifier is missing a table name")
		}
		qualifiedName, qualified := tokens[index+1].identifier()
		if !qualified {
			return Table{}, true, parseErrorAt(source, tokens[index+1].offset, "schema qualifier must be followed by a table identifier")
		}
		schemaName = firstName
		tableName = qualifiedName
		nameToken = tokens[index+1]
		index += 2
	}
	if !tokenAtSymbol(tokens, index, "(") {
		return Table{}, true, parseErrorAt(source, nameToken.offset, "CREATE TABLE must contain an explicit column list; LIKE and AS SELECT are not supported")
	}
	closeIndex, matched := matchingParen(tokens, index)
	if !matched {
		return Table{}, true, parseErrorAt(source, tokens[index].offset, "CREATE TABLE column list has no closing parenthesis")
	}
	for tailIndex := closeIndex + 1; tailIndex < len(tokens); tailIndex++ {
		if tokens[tailIndex].keyword("SELECT") {
			return Table{}, true, parseErrorAt(source, tokens[tailIndex].offset, "CREATE TABLE AS SELECT is not supported")
		}
	}

	table := Table{
		schemaName: schemaName,
		name:       tableName,
		position:   positionAt(source, nameToken.offset),
		columns:    make([]Column, 0),
		byColumn:   make(map[string]int),
		indexes:    make([]Index, 0),
	}
	elements, splitErr := splitTableElements(source, tokens[index+1:closeIndex])
	if splitErr != nil {
		return Table{}, true, splitErr
	}
	for _, element := range elements {
		if tableConstraint(element) {
			parsedIndex, hasIndex, indexErr := parseTableIndex(source, element)
			if indexErr != nil {
				return Table{}, true, indexErr
			}
			if hasIndex {
				table.indexes = append(table.indexes, parsedIndex)
			}
			continue
		}
		column, inlineIndexes, columnErr := parseColumn(source, element)
		if columnErr != nil {
			return Table{}, true, columnErr
		}
		columnKey := foldIdentifier(column.name)
		if previous, exists := table.byColumn[columnKey]; exists {
			return Table{}, true, parseErrorAt(source, column.position.Offset-1, fmt.Sprintf("column %q is already defined at line %d", column.name, table.columns[previous].position.Line))
		}
		table.byColumn[columnKey] = len(table.columns)
		table.columns = append(table.columns, column)
		table.indexes = append(table.indexes, inlineIndexes...)
	}
	if len(table.columns) == 0 {
		return Table{}, true, parseErrorAt(source, tokens[index].offset, "CREATE TABLE must define at least one column")
	}
	if err := validateTable(source, &table); err != nil {
		return Table{}, true, err
	}
	return table, true, nil
}

func tokenAtKeyword(tokens []sqlToken, index int, keyword string) bool {
	return index >= 0 && index < len(tokens) && tokens[index].keyword(keyword)
}

func tokenAtSymbol(tokens []sqlToken, index int, symbol string) bool {
	return index >= 0 && index < len(tokens) && tokens[index].kind == tokenSymbol && tokens[index].text == symbol
}

func matchingParen(tokens []sqlToken, openIndex int) (int, bool) {
	depth := 0
	for index := openIndex; index < len(tokens); index++ {
		if tokenAtSymbol(tokens, index, "(") {
			depth++
			continue
		}
		if !tokenAtSymbol(tokens, index, ")") {
			continue
		}
		depth--
		if depth == 0 {
			return index, true
		}
	}
	return 0, false
}

func splitTableElements(source string, tokens []sqlToken) ([][]sqlToken, error) {
	elements := make([][]sqlToken, 0)
	start := 0
	depth := 0
	for index := range tokens {
		switch {
		case tokenAtSymbol(tokens, index, "("):
			depth++
		case tokenAtSymbol(tokens, index, ")"):
			depth--
			if depth < 0 {
				return nil, parseErrorAt(source, tokens[index].offset, "unexpected closing parenthesis in table definition")
			}
		case tokenAtSymbol(tokens, index, ",") && depth == 0:
			if index == start {
				return nil, parseErrorAt(source, tokens[index].offset, "empty table element")
			}
			elements = append(elements, tokens[start:index])
			start = index + 1
		}
	}
	if depth != 0 {
		return nil, parseErrorAt(source, tokens[0].offset, "unbalanced parentheses in table definition")
	}
	if start >= len(tokens) {
		if len(tokens) == 0 {
			return elements, nil
		}
		return nil, parseErrorAt(source, tokens[len(tokens)-1].offset, "trailing comma in table definition")
	}
	elements = append(elements, tokens[start:])
	return elements, nil
}

func tableConstraint(tokens []sqlToken) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, keyword := range [...]string{"CONSTRAINT", "PRIMARY", "UNIQUE", "KEY", "INDEX", "FOREIGN", "CHECK", "FULLTEXT", "SPATIAL"} {
		if tokens[0].keyword(keyword) {
			return true
		}
	}
	return false
}

func parseColumn(source string, tokens []sqlToken) (Column, []Index, error) {
	if len(tokens) < 2 {
		offset := 0
		if len(tokens) != 0 {
			offset = tokens[0].offset
		}
		return Column{}, nil, parseErrorAt(source, offset, "column definition requires a name and type")
	}
	name, ok := tokens[0].identifier()
	if !ok {
		return Column{}, nil, parseErrorAt(source, tokens[0].offset, "column definition must start with an identifier")
	}
	if tokens[1].kind != tokenWord {
		return Column{}, nil, parseErrorAt(source, tokens[1].offset, fmt.Sprintf("column %q requires a SQL type", name))
	}
	typeName := strings.ToUpper(tokens[1].text)
	if typeName == "NATIONAL" && len(tokens) > 2 && tokens[2].kind == tokenWord {
		typeName = strings.ToUpper(tokens[2].text)
	}
	column := Column{
		name:     name,
		typeName: typeName,
		position: positionAt(source, tokens[0].offset),
		nullable: true,
	}
	primary := false
	unique := false
	depth := 0
	for index := 2; index < len(tokens); index++ {
		if tokenAtSymbol(tokens, index, "(") {
			depth++
			continue
		}
		if tokenAtSymbol(tokens, index, ")") {
			depth--
			continue
		}
		if depth != 0 || tokens[index].kind != tokenWord {
			continue
		}
		switch {
		case tokens[index].keyword("NOT") && tokenAtKeyword(tokens, index+1, "NULL"):
			column.nullable = false
		case tokens[index].keyword("NULL") && !tokenAtKeyword(tokens, index-1, "NOT"):
			column.nullable = true
		case tokens[index].keyword("UNSIGNED"):
			column.unsigned = true
		case tokens[index].keyword("DEFAULT"):
			column.hasDefault = true
		case tokens[index].keyword("AUTO_INCREMENT"):
			column.autoIncrement = true
			column.nullable = false
		case tokens[index].keyword("AUTO_RANDOM"):
			column.autoRandom = true
			column.nullable = false
		case tokens[index].keyword("GENERATED") || tokens[index].keyword("AS") && tokenAtSymbol(tokens, index+1, "("):
			column.generated = true
		case tokens[index].keyword("PRIMARY") && tokenAtKeyword(tokens, index+1, "KEY"):
			primary = true
			column.nullable = false
		case tokens[index].keyword("KEY") && !tokenAtKeyword(tokens, index-1, "PRIMARY") && !tokenAtKeyword(tokens, index-1, "UNIQUE"):
			primary = true
			column.nullable = false
		case tokens[index].keyword("UNIQUE"):
			unique = true
		}
	}
	if typeName == "SERIAL" {
		column.typeName = "BIGINT"
		column.unsigned = true
		column.nullable = false
		column.autoIncrement = true
		unique = true
	}
	indexes := make([]Index, 0, 2)
	if primary {
		indexes = append(indexes, Index{
			name:     "PRIMARY",
			position: column.position,
			columns:  []string{name},
			primary:  true,
			unique:   true,
		})
	}
	if unique && !primary {
		indexes = append(indexes, Index{
			name:     name,
			position: column.position,
			columns:  []string{name},
			unique:   true,
		})
	}
	return column, indexes, nil
}

func parseTableIndex(source string, tokens []sqlToken) (Index, bool, error) {
	if len(tokens) == 0 {
		return Index{}, false, nil
	}
	positionToken := tokens[0]
	constraintName := ""
	if tokens[0].keyword("CONSTRAINT") {
		if len(tokens) < 2 {
			return Index{}, false, parseErrorAt(source, tokens[0].offset, "CONSTRAINT is missing its definition")
		}
		tokens = tokens[1:]
		if len(tokens) > 0 && !tokens[0].keyword("PRIMARY") && !tokens[0].keyword("UNIQUE") && !tokens[0].keyword("FOREIGN") && !tokens[0].keyword("CHECK") {
			constraintName, _ = tokens[0].identifier()
			tokens = tokens[1:]
		}
		if len(tokens) == 0 {
			return Index{}, false, parseErrorAt(source, positionToken.offset, "CONSTRAINT is missing its definition")
		}
	}
	if tokens[0].keyword("FOREIGN") || tokens[0].keyword("CHECK") {
		return Index{}, false, nil
	}
	specialized := tokens[0].keyword("FULLTEXT") || tokens[0].keyword("SPATIAL")
	if specialized {
		tokens = tokens[1:]
		if len(tokens) == 0 {
			return Index{}, false, parseErrorAt(source, positionToken.offset, "index definition is incomplete")
		}
	}
	primary := tokens[0].keyword("PRIMARY")
	unique := primary || tokens[0].keyword("UNIQUE")
	ordinary := tokens[0].keyword("KEY") || tokens[0].keyword("INDEX")
	if !primary && !unique && !ordinary {
		return Index{}, false, nil
	}
	openIndex := -1
	for index := 1; index < len(tokens); index++ {
		if tokenAtSymbol(tokens, index, "(") {
			openIndex = index
			break
		}
	}
	if openIndex < 0 {
		return Index{}, false, parseErrorAt(source, tokens[0].offset, "index definition requires a column list")
	}
	closeIndex, matched := matchingParen(tokens, openIndex)
	if !matched {
		return Index{}, false, parseErrorAt(source, tokens[openIndex].offset, "index column list has no closing parenthesis")
	}
	name := ""
	if primary {
		name = "PRIMARY"
	} else {
		name = constraintName
		for index := 1; index < openIndex; index++ {
			if tokens[index].keyword("KEY") || tokens[index].keyword("INDEX") || tokens[index].keyword("GLOBAL") || tokens[index].keyword("LOCAL") {
				continue
			}
			if candidate, ok := tokens[index].identifier(); ok {
				name = candidate
			}
		}
	}
	parts, err := splitIndexParts(source, tokens[openIndex+1:closeIndex])
	if err != nil {
		return Index{}, false, err
	}
	index := Index{
		name:        name,
		position:    positionAt(source, positionToken.offset),
		columns:     make([]string, 0, len(parts)),
		primary:     primary,
		unique:      unique,
		specialized: specialized,
	}
	for _, token := range tokens[closeIndex+1:] {
		if token.keyword("WHERE") {
			index.partial = true
		}
		if token.keyword("INVISIBLE") {
			index.invisible = true
		}
	}
	for _, part := range parts {
		column, simple := simpleIndexColumn(part)
		if !simple {
			index.hasExpression = true
			continue
		}
		index.columns = append(index.columns, column)
	}
	return index, true, nil
}

func simpleIndexColumn(tokens []sqlToken) (string, bool) {
	if len(tokens) == 0 {
		return "", false
	}
	column, simple := tokens[0].identifier()
	if !simple {
		return "", false
	}
	if len(tokens) == 1 {
		return column, true
	}
	if len(tokens) == 2 && (tokens[1].keyword("ASC") || tokens[1].keyword("DESC")) {
		return column, true
	}
	return "", false
}

func splitIndexParts(source string, tokens []sqlToken) ([][]sqlToken, error) {
	parts, err := splitTableElements(source, tokens)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		offset := 0
		if len(tokens) > 0 {
			offset = tokens[0].offset
		}
		return nil, parseErrorAt(source, offset, "index requires at least one part")
	}
	return parts, nil
}

func validateTable(source string, table *Table) error {
	primaryCount := 0
	for indexPosition := range table.indexes {
		index := &table.indexes[indexPosition]
		if index.primary {
			primaryCount++
			if primaryCount > 1 {
				return parseErrorAt(source, index.position.Offset-1, "table defines more than one primary key")
			}
		}
		for _, columnName := range index.columns {
			columnPosition, exists := table.byColumn[foldIdentifier(columnName)]
			if !exists {
				return parseErrorAt(source, index.position.Offset-1, fmt.Sprintf("index %q references unknown column %q", index.name, columnName))
			}
			if index.primary {
				table.columns[columnPosition].nullable = false
			}
		}
	}
	return nil
}

func parseErrorAt(source string, offset int, message string) error {
	return &ParseError{Position: positionAt(source, offset), Message: message}
}

func positionAt(source string, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	line := 1
	column := 1
	for index := 0; index < offset; index++ {
		if source[index] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return Position{Offset: offset + 1, Line: line, Column: column}
}
