package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
	"go.yaml.in/yaml/v4/plugin/limit"
)

const (
	maxYAMLDepth     = 32
	maxYAMLLineBytes = 1024 * 1024
)

// parseYAML parses YAML syntax with go-yaml and then applies the deliberately
// small tidbgo.yaml surface: nested mappings, scalar values, and the
// schema.command block sequence. Features outside that contract are rejected
// before values reach configuration resolution.
func parseYAML(data []byte) (map[string]rawValue, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, errors.New("configuration must be valid UTF-8")
	}
	if err := validateYAMLSource(data); err != nil {
		return nil, err
	}

	loader, err := yaml.NewLoader(
		bytes.NewReader(data),
		yaml.WithV4Defaults(),
		yaml.WithUniqueKeys(),
		yaml.WithPlugin(limit.New(
			limit.DepthValue(maxYAMLDepth),
			limit.AliasValue(0),
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("configure YAML parser: %w", err)
	}

	var document yaml.Node
	if err := loader.Load(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]rawValue{}, nil
		}
		return nil, yamlSyntaxError(err)
	}

	var extra yaml.Node
	if err := loader.Load(&extra); err == nil {
		return nil, errors.New("multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return nil, yamlSyntaxError(err)
	}

	reader := yamlReader{
		values: make(map[string]rawValue),
		seen:   make(map[string]struct{}),
	}
	if err := reader.readDocument(&document); err != nil {
		return nil, err
	}
	return reader.values, nil
}

type yamlReader struct {
	values map[string]rawValue
	seen   map[string]struct{}
}

func (r *yamlReader) readDocument(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nodeError(document, "configuration must contain one YAML mapping")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nodeError(root, "configuration root must be a mapping")
	}
	return r.readMapping(root, nil, 1)
}

func (r *yamlReader) readMapping(node *yaml.Node, parents []string, column int) error {
	if err := validateYAMLNode(node); err != nil {
		return err
	}
	if node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nodeError(node, "expected a mapping")
	}
	if node.Column != column {
		return nodeError(node, "mapping must use two-space indentation")
	}

	localKeys := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if err := validateYAMLNode(key); err != nil {
			return err
		}
		if key.Kind != yaml.ScalarNode || key.Style != 0 || !validKey(key.Value) {
			return nodeError(key, "invalid key")
		}
		if key.Column != column {
			return nodeError(key, "mapping must use two-space indentation")
		}
		if _, exists := localKeys[key.Value]; exists {
			return nodeError(key, "duplicate key "+key.Value)
		}
		localKeys[key.Value] = struct{}{}

		parts := append(append([]string(nil), parents...), key.Value)
		path := strings.Join(parts, ".")
		if _, exists := r.seen[path]; exists {
			return nodeError(key, "duplicate key "+path)
		}
		r.seen[path] = struct{}{}

		if err := r.readValue(path, parts, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *yamlReader) readValue(path string, parts []string, key, value *yaml.Node) error {
	if err := validateYAMLNode(value); err != nil {
		return err
	}

	switch value.Kind {
	case yaml.MappingNode:
		if !validMappingPath(path) || path == "schema.command" {
			return nodeError(key, "unsupported mapping "+path)
		}
		return r.readMapping(value, parts, key.Column+2)
	case yaml.SequenceNode:
		if path != "schema.command" {
			return nodeError(key, "unsupported sequence "+path)
		}
		items, err := readStringSequence(value, key.Column+2)
		if err != nil {
			return err
		}
		r.values[path] = rawValue{items: items, isList: true}
		return nil
	case yaml.ScalarNode:
		if value.Value == "" && value.Style == 0 && value.ShortTag() == "!!null" {
			if !validMappingPath(path) {
				return nodeError(key, "unsupported mapping "+path)
			}
			if path == "schema.command" {
				r.values[path] = rawValue{isList: true}
			}
			return nil
		}
		if err := validateScalar(value); err != nil {
			return err
		}
		r.values[path] = rawValue{scalar: value.Value}
		return nil
	default:
		return nodeError(value, "unsupported YAML value")
	}
}

func readStringSequence(node *yaml.Node, column int) ([]string, error) {
	if node.Column != column {
		return nil, nodeError(node, "sequence must use two-space indentation")
	}

	items := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if err := validateYAMLNode(item); err != nil {
			return nil, err
		}
		if item.Kind != yaml.ScalarNode {
			return nil, nodeError(item, "sequence items must be scalars")
		}
		if item.Column != column+2 {
			return nil, nodeError(item, "sequence must use two-space indentation")
		}
		if err := validateScalar(item); err != nil {
			return nil, err
		}
		if item.Value == "" {
			return nil, nodeError(item, "sequence items must not be empty")
		}
		items = append(items, item.Value)
	}
	return items, nil
}

func validateYAMLNode(node *yaml.Node) error {
	switch {
	case node.Anchor != "":
		return nodeError(node, "YAML anchors are not supported")
	case node.Kind == yaml.AliasNode:
		return nodeError(node, "YAML aliases are not supported")
	case node.Tag == "!" || node.Style&yaml.TaggedStyle != 0:
		return nodeError(node, "explicit YAML tags are not supported")
	case node.Style&yaml.FlowStyle != 0:
		return nodeError(node, "flow collections are not supported")
	case node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return nodeError(node, "block scalars are not supported")
	default:
		return nil
	}
}

func validateScalar(node *yaml.Node) error {
	if node.Style == 0 && strings.ContainsAny(node.Value, "\"'") {
		return nodeError(node, "quote characters must enclose the entire scalar")
	}
	return nil
}

func validateYAMLSource(data []byte) error {
	for index, rawLine := range bytes.Split(data, []byte{'\n'}) {
		lineNumber := index + 1
		line := strings.TrimSuffix(string(rawLine), "\r")
		if len(line) > maxYAMLLineBytes {
			return fmt.Errorf("line %d: YAML line exceeds %d bytes", lineNumber, maxYAMLLineBytes)
		}
		if strings.ContainsRune(line, '\t') {
			return fmt.Errorf("line %d: tabs are not supported", lineNumber)
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent%2 != 0 {
			return fmt.Errorf("line %d: indentation must use multiples of two spaces", lineNumber)
		}
		if marker := strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0]); marker == "---" || marker == "..." {
			return fmt.Errorf("line %d: document markers are not supported", lineNumber)
		}
	}
	return nil
}

func nodeError(node *yaml.Node, message string) error {
	if node != nil && node.Line > 0 {
		return fmt.Errorf("line %d: %s", node.Line, message)
	}
	return errors.New(message)
}

func yamlSyntaxError(err error) error {
	var loadError *yaml.LoadError
	if errors.As(err, &loadError) && loadError.Mark.Line > 0 {
		return fmt.Errorf("line %d: invalid YAML syntax", loadError.Mark.Line)
	}
	return errors.New("invalid YAML syntax")
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
