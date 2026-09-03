package modelmeta

import (
	"errors"
	"fmt"
	"strings"
)

// FieldTag is the model-independent result of parsing one scalar tidbgo tag.
type FieldTag struct {
	Column         string
	PrimaryKey     bool
	AutoRandom     bool
	Computed       bool
	SoftDelete     bool
	ExplicitColumn bool
}

// ParseIgnore reports whether a tidbgo tag excludes a field and rejects an
// ignore marker combined with any other tag value.
func ParseIgnore(value string, present bool) (bool, error) {
	if !present || value == "" {
		return false, nil
	}
	parts := strings.Split(value, ",")
	for _, option := range parts {
		if option != "-" {
			continue
		}
		if len(parts) != 1 {
			return false, errors.New("tidbgo ignore option must not be combined with other options")
		}
		return true, nil
	}
	return false, nil
}

// ParseField parses one scalar tidbgo tag using goName for the default column.
func ParseField(goName, value string, present bool) (FieldTag, error) {
	if !present || value == "" {
		return FieldTag{Column: SnakeCase(goName)}, nil
	}

	parts := strings.Split(value, ",")
	result := FieldTag{Column: parts[0]}
	if result.Column == "" {
		result.Column = SnakeCase(goName)
	} else {
		result.ExplicitColumn = true
	}
	for _, option := range parts[1:] {
		switch option {
		case "pk":
			if result.PrimaryKey {
				return FieldTag{}, errors.New("tidbgo primary-key option must not be repeated")
			}
			result.PrimaryKey = true
		case "auto_random":
			if result.AutoRandom {
				return FieldTag{}, errors.New("tidbgo AUTO_RANDOM option must not be repeated")
			}
			result.AutoRandom = true
		case "computed":
			if result.Computed {
				return FieldTag{}, errors.New("tidbgo computed option must not be repeated")
			}
			result.Computed = true
		case "soft_delete":
			if result.SoftDelete {
				return FieldTag{}, errors.New("tidbgo soft-delete option must not be repeated")
			}
			result.SoftDelete = true
		case "":
			return FieldTag{}, errors.New("empty tidbgo tag option is not supported after the column position")
		default:
			return FieldTag{}, fmt.Errorf("tidbgo tag option %q is not supported after the column position", option)
		}
	}
	return result, nil
}

// ParseTable parses model.Meta options and returns an optional table override.
func ParseTable(value string, present bool) (string, error) {
	if !present || value == "" {
		return "", nil
	}

	var tableName string
	for _, option := range strings.Split(value, ",") {
		key, current, found := strings.Cut(option, "=")
		if !found || key != "table" {
			return "", fmt.Errorf("tidbgo tag option %q is not supported on model.Meta", option)
		}
		if tableName != "" {
			return "", errors.New("tidbgo table option must not be repeated")
		}
		if !ValidSQLIdentifier(current) {
			return "", fmt.Errorf("table name %q must be a simple SQL identifier of at most 64 bytes", current)
		}
		tableName = current
	}
	return tableName, nil
}
