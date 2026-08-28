package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type yamlParent struct {
	indent int
	key    string
}

// parseYAML parses the deliberately small YAML surface used by tidbgo.yaml:
// nested mappings, scalar values, and block string sequences. Rejecting YAML
// features that the configuration schema does not need keeps this bootstrap
// package dependency-free and deterministic.
func parseYAML(data []byte) (map[string]rawValue, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	values := make(map[string]rawValue)
	seen := make(map[string]struct{})
	var parents []yamlParent

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.ContainsRune(line, '\t') {
			return nil, fmt.Errorf("line %d: tabs are not supported", lineNumber)
		}

		line, err := stripComment(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		indent := leadingSpaces(line)
		if indent%2 != 0 {
			return nil, fmt.Errorf("line %d: indentation must use multiples of two spaces", lineNumber)
		}
		content := strings.TrimSpace(line)
		if content == "---" || content == "..." {
			return nil, fmt.Errorf("line %d: document markers are not supported", lineNumber)
		}

		for len(parents) > 0 && parents[len(parents)-1].indent >= indent {
			parents = parents[:len(parents)-1]
		}

		if strings.HasPrefix(content, "-") {
			if len(content) == 1 || content[1] != ' ' {
				return nil, fmt.Errorf("line %d: list items must start with '- '", lineNumber)
			}
			if len(parents) == 0 || parents[len(parents)-1].indent != indent-2 {
				return nil, fmt.Errorf("line %d: list item has no parent key", lineNumber)
			}
			path := parentPath(parents)
			item, err := parseScalar(strings.TrimSpace(content[2:]))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			if item == "" {
				return nil, fmt.Errorf("line %d: list items must not be empty", lineNumber)
			}
			value, exists := values[path]
			if exists && !value.isList {
				return nil, fmt.Errorf("line %d: %s is already a scalar", lineNumber, path)
			}
			value.isList = true
			value.items = append(value.items, item)
			values[path] = value
			continue
		}
		if len(parents) == 0 && indent != 0 || len(parents) > 0 && parents[len(parents)-1].indent != indent-2 {
			return nil, fmt.Errorf("line %d: mapping has invalid indentation", lineNumber)
		}

		key, text, hasValue, err := splitMapping(content)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid key", lineNumber)
		}

		pathParts := make([]string, 0, len(parents)+1)
		for _, item := range parents {
			pathParts = append(pathParts, item.key)
		}
		pathParts = append(pathParts, key)
		path := strings.Join(pathParts, ".")
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("line %d: duplicate key %s", lineNumber, path)
		}
		seen[path] = struct{}{}

		if !hasValue {
			if !validMappingPath(path) {
				return nil, fmt.Errorf("line %d: unsupported mapping %s", lineNumber, path)
			}
			if path == "schema.command" {
				values[path] = rawValue{isList: true}
			}
			parents = append(parents, yamlParent{indent: indent, key: key})
			continue
		}

		value, err := parseScalar(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		values[path] = rawValue{scalar: value}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	return values, nil
}

func leadingSpaces(value string) int {
	return len(value) - len(strings.TrimLeft(value, " "))
}

func parentPath(parents []yamlParent) string {
	parts := make([]string, 0, len(parents))
	for _, item := range parents {
		parts = append(parts, item.key)
	}
	return strings.Join(parts, ".")
}

func validMappingPath(path string) bool {
	if path == "schema.command" {
		return true
	}
	prefix := path + "."
	for key := range fieldByKey {
		if strings.HasPrefix(key, prefix) && fieldByKey[key].allowFile {
			return true
		}
	}
	return false
}

func stripComment(line string) (string, error) {
	var quote rune
	escaped := false
	for index, current := range line {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && current == '\\' {
			escaped = true
			continue
		}
		if current == '\'' || current == '"' {
			if quote == 0 {
				quote = current
			} else if quote == current {
				quote = 0
			}
			continue
		}
		if current == '#' && quote == 0 && (index == 0 || line[index-1] == ' ') {
			return strings.TrimRight(line[:index], " "), nil
		}
	}
	if quote != 0 {
		return "", errors.New("unterminated quoted scalar")
	}
	return line, nil
}

func splitMapping(content string) (key, value string, hasValue bool, err error) {
	var quote rune
	escaped := false
	for index, current := range content {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && current == '\\' {
			escaped = true
			continue
		}
		if current == '\'' || current == '"' {
			if quote == 0 {
				quote = current
			} else if quote == current {
				quote = 0
			}
			continue
		}
		if current != ':' || quote != 0 {
			continue
		}
		key = strings.TrimSpace(content[:index])
		value = strings.TrimSpace(content[index+1:])
		return key, value, value != "", nil
	}
	return "", "", false, errors.New("expected a key and ':'")
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for _, current := range key {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '_' {
			continue
		}
		return false
	}
	return true
}

func parseScalar(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
		return "", errors.New("unsupported YAML value")
	}
	if value[0] == '"' {
		if value[len(value)-1] != '"' || len(value) == 1 {
			return "", errors.New("invalid double-quoted scalar")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", errors.New("invalid double-quoted scalar")
		}
		return decoded, nil
	}
	if value[0] == '\'' {
		if value[len(value)-1] != '\'' || len(value) == 1 {
			return "", errors.New("invalid single-quoted scalar")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	if strings.ContainsAny(value, "\"'") {
		return "", errors.New("quote characters must enclose the entire scalar")
	}
	return value, nil
}
