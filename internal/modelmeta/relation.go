package modelmeta

import (
	"errors"
	"fmt"
	"strings"
)

// RelationKind is the model-independent relation kind encoded in a tidbgo tag.
type RelationKind string

const (
	RelationBelongsTo  RelationKind = "belongs_to"
	RelationHasOne     RelationKind = "has_one"
	RelationHasMany    RelationKind = "has_many"
	RelationManyToMany RelationKind = "many_to_many"
)

// RelationPair is one ordered pair from a relation tag.
type RelationPair struct {
	Left  string
	Right string
}

// RelationTag is the model-independent result of parsing one relation tag.
type RelationTag struct {
	Kind        RelationKind
	Joins       []RelationPair
	Through     string
	SourcePairs []RelationPair
	TargetPairs []RelationPair
}

// ParseRelation parses and validates one tidbgo relation tag.
func ParseRelation(value string, collection bool) (RelationTag, error) {
	parts := strings.Split(value, ",")
	kind, ok := ParseRelationKind(parts[0])
	if !ok {
		return RelationTag{}, errors.New("tidbgo relation kind must be the first tag value")
	}
	result := RelationTag{Kind: kind}
	for _, option := range parts[1:] {
		if _, relationKindOption := ParseRelationKind(option); relationKindOption {
			return RelationTag{}, errors.New("tidbgo relation kind must appear once as the first tag value")
		}
		key, current, found := strings.Cut(option, "=")
		if !found || current == "" {
			return RelationTag{}, fmt.Errorf("tidbgo relation option %q is not supported", option)
		}
		switch key {
		case "join":
			pair, err := parseRelationPair(current)
			if err != nil {
				return RelationTag{}, fmt.Errorf("invalid join option: %w", err)
			}
			result.Joins = append(result.Joins, pair)
		case "through":
			if result.Through != "" {
				return RelationTag{}, errors.New("through option must not be repeated")
			}
			if !ValidSQLIdentifier(current) {
				return RelationTag{}, fmt.Errorf("junction table name %q must be a simple SQL identifier of at most 64 bytes", current)
			}
			result.Through = current
		case "source":
			pair, err := parseRelationPair(current)
			if err != nil {
				return RelationTag{}, fmt.Errorf("invalid source option: %w", err)
			}
			if !ValidSQLIdentifier(pair.Right) {
				return RelationTag{}, fmt.Errorf("junction source column %q must be a simple SQL identifier of at most 64 bytes", pair.Right)
			}
			result.SourcePairs = append(result.SourcePairs, pair)
		case "target":
			pair, err := parseRelationPair(current)
			if err != nil {
				return RelationTag{}, fmt.Errorf("invalid target option: %w", err)
			}
			if !ValidSQLIdentifier(pair.Left) {
				return RelationTag{}, fmt.Errorf("junction target column %q must be a simple SQL identifier of at most 64 bytes", pair.Left)
			}
			result.TargetPairs = append(result.TargetPairs, pair)
		default:
			return RelationTag{}, fmt.Errorf("tidbgo relation option %q is not supported", option)
		}
	}

	if collection != (result.Kind == RelationHasMany || result.Kind == RelationManyToMany) {
		return RelationTag{}, fmt.Errorf("relation kind %q does not match the relation field type", result.Kind)
	}
	if result.Kind == RelationManyToMany {
		if len(result.Joins) != 0 {
			return RelationTag{}, errors.New("many_to_many does not support join options")
		}
		if result.Through == "" || len(result.SourcePairs) == 0 || len(result.TargetPairs) == 0 {
			return RelationTag{}, errors.New("many_to_many requires through, source, and target options")
		}
		return result, nil
	}
	if result.Through != "" || len(result.SourcePairs) != 0 || len(result.TargetPairs) != 0 {
		return RelationTag{}, errors.New("direct relations support join options only")
	}
	return result, nil
}

// ParseRelationKind parses the first positional value of a relation tag.
func ParseRelationKind(value string) (RelationKind, bool) {
	switch value {
	case "belongs_to":
		return RelationBelongsTo, true
	case "has_one":
		return RelationHasOne, true
	case "has_many":
		return RelationHasMany, true
	case "many_to_many":
		return RelationManyToMany, true
	default:
		return "", false
	}
}

func parseRelationPair(value string) (RelationPair, error) {
	left, right, found := strings.Cut(value, ":")
	if !found || left == "" || right == "" || strings.Contains(right, ":") {
		return RelationPair{}, fmt.Errorf("%q must contain exactly one non-empty ':' pair", value)
	}
	return RelationPair{Left: left, Right: right}, nil
}
