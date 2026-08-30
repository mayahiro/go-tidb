package check

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/mayahiro/go-tidb/model"
)

const (
	codeInvalidModel          = "MOD001"
	codeIgnoredDBTag          = "MOD002"
	codeUnexportedModelTag    = "MOD003"
	codeLikelyMisplacedOption = "MOD004"
	codeMissingPrimaryKey     = "MOD005"
	codeReadOnlyCustomField   = "MOD006"
	codeWriteOnlyCustomField  = "MOD007"
)

var (
	modelCheckScannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	modelCheckValuerType  = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
	modelCheckTimeType    = reflect.TypeFor[time.Time]()
	modelCheckMetaType    = reflect.TypeFor[model.Meta]()
)

var likelyModelTagOptions = [...]string{
	"pk",
	"auto_random",
	"computed",
	"soft_delete",
	"belongs_to",
	"has_one",
	"has_many",
	"many_to_many",
}

// Model checks the model metadata for T without executing user methods,
// reading configuration, or accessing a database.
//
// The result is ordered deterministically and is a non-nil empty slice when
// no diagnostics are found. T may be a named struct or any pointer depth to
// one.
func Model[T any]() []Diagnostic {
	return ModelType(reflect.TypeFor[T]())
}

// ModelType checks modelType without executing user methods, reading
// configuration, or accessing a database.
//
// The result is ordered deterministically and is a non-nil empty slice when
// no diagnostics are found. Pointer layers around a named struct are ignored.
func ModelType(modelType reflect.Type) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		diagnostics = appendModelValidationDiagnostics(diagnostics, err)
	}

	base, validType := checkedModelStructType(modelType)
	if !validType {
		return diagnostics
	}
	diagnostics = appendModelTagDiagnostics(diagnostics, base)
	if descriptor == nil {
		return diagnostics
	}
	return appendModelCapabilityDiagnostics(diagnostics, descriptor)
}

func appendModelValidationDiagnostics(diagnostics []Diagnostic, err error) []Diagnostic {
	var validation *model.ValidationError
	if !errors.As(err, &validation) {
		return append(diagnostics, Diagnostic{
			Code:         codeInvalidModel,
			Severity:     SeverityError,
			Title:        "Invalid model metadata",
			Message:      err.Error(),
			Suggestion:   "Use a named struct accepted by model.Describe and fix its metadata before building queries",
			Suppressible: false,
		})
	}
	for _, issue := range validation.Issues {
		diagnostics = append(diagnostics, Diagnostic{
			Code:     codeInvalidModel,
			Severity: SeverityError,
			Title:    "Invalid model metadata",
			Message:  issue.Message,
			Evidence: []Evidence{{
				Message: "Go field: " + issue.Field,
			}},
			Suggestion:   "Fix the model metadata before building queries",
			Suppressible: false,
		})
	}
	return diagnostics
}

func checkedModelStructType(modelType reflect.Type) (reflect.Type, bool) {
	if modelType == nil {
		return nil, false
	}
	for modelType.Kind() == reflect.Pointer {
		modelType = modelType.Elem()
	}
	return modelType, modelType.Kind() == reflect.Struct && modelType.Name() != ""
}

func appendModelTagDiagnostics(diagnostics []Diagnostic, modelType reflect.Type) []Diagnostic {
	stack := map[reflect.Type]bool{modelType: true}
	return inspectModelStructTags(diagnostics, modelType, modelType.Name(), stack)
}

func inspectModelStructTags(diagnostics []Diagnostic, structType reflect.Type, path string, stack map[reflect.Type]bool) []Diagnostic {
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		if !field.IsExported() {
			if value, present := field.Tag.Lookup(model.ModelTag); present {
				fieldPath := path + "." + field.Name
				diagnostics = append(diagnostics, Diagnostic{
					Code:         codeUnexportedModelTag,
					Severity:     SeverityWarning,
					Title:        "Unexported field has tidbgo metadata",
					Message:      fmt.Sprintf("%s declares tidbgo:%q, but unexported fields are not mapped", fieldPath, value),
					Suggestion:   "Export the field when it is part of the model, or remove the unused tidbgo tag",
					Suppressible: true,
				})
			}
			continue
		}

		if value, present := field.Tag.Lookup("db"); present {
			fieldPath := path + "." + field.Name
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeIgnoredDBTag,
				Severity:     SeverityWarning,
				Title:        "db tag is ignored",
				Message:      fmt.Sprintf("%s declares db:%q, but go-tidb reads only tidbgo metadata", fieldPath, value),
				Suggestion:   "Use tidbgo for go-tidb mapping, and keep db only when another library intentionally reads it",
				Suppressible: true,
			})
		}

		if value, present := field.Tag.Lookup(model.ModelTag); present {
			if option, suspicious := likelyMisplacedModelTagOption(field, value); suspicious {
				fieldPath := path + "." + field.Name
				diagnostics = append(diagnostics, Diagnostic{
					Code:         codeLikelyMisplacedOption,
					Severity:     SeverityWarning,
					Title:        "tidbgo column position resembles an option",
					Message:      fmt.Sprintf("%s uses %q as its column name, but that value resembles the %q option", fieldPath, firstModelTagValue(value), option),
					Suggestion:   fmt.Sprintf("If this is the %s option, move it after the column position, for example tidbgo:\",%s\"", option, option),
					Suppressible: true,
				})
			}
		}

		if embedded, ok := flattenableModelCheckField(field); ok && !stack[embedded] {
			stack[embedded] = true
			diagnostics = inspectModelStructTags(diagnostics, embedded, path+"."+field.Name, stack)
			delete(stack, embedded)
		}
	}
	return diagnostics
}

func flattenableModelCheckField(field reflect.StructField) (reflect.Type, bool) {
	if !field.Anonymous {
		return nil, false
	}
	if value, present := field.Tag.Lookup(model.ModelTag); present && value != "" {
		return nil, false
	}
	base := indirectModelCheckType(field.Type)
	if base.Kind() != reflect.Struct || base == modelCheckTimeType || base == modelCheckMetaType {
		return nil, false
	}
	if modelCheckImplements(field.Type, base, modelCheckScannerType) || modelCheckImplements(field.Type, base, modelCheckValuerType) {
		return nil, false
	}
	return base, true
}

func likelyMisplacedModelTagOption(field reflect.StructField, value string) (string, bool) {
	first := firstModelTagValue(value)
	if first == "" || first == "-" || strings.Contains(first, "=") {
		return "", false
	}
	if relationShapedModelCheckField(field.Type) && isRelationModelTagValue(first) {
		return "", false
	}

	normalized := strings.ToLower(first)
	for _, option := range likelyModelTagOptions {
		if !withinOneEdit(normalized, option) {
			continue
		}
		if first == inferredModelColumn(field.Name) {
			return "", false
		}
		return option, true
	}
	return "", false
}

func firstModelTagValue(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return first
}

func relationShapedModelCheckField(fieldType reflect.Type) bool {
	switch fieldType.Kind() {
	case reflect.Pointer:
		fieldType = fieldType.Elem()
	case reflect.Slice:
		fieldType = fieldType.Elem()
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
	default:
		return false
	}
	return fieldType.Kind() == reflect.Struct && fieldType.Name() != ""
}

func isRelationModelTagValue(value string) bool {
	switch value {
	case "belongs_to", "has_one", "has_many", "many_to_many":
		return true
	default:
		return false
	}
}

func withinOneEdit(left, right string) bool {
	if left == right {
		return true
	}
	if len(left) == len(right) {
		differences := 0
		for index := range len(left) {
			if left[index] == right[index] {
				continue
			}
			differences++
			if differences > 1 {
				return false
			}
		}
		return true
	}
	if len(left)+1 == len(right) {
		return oneInsertedByteApart(left, right)
	}
	if len(right)+1 == len(left) {
		return oneInsertedByteApart(right, left)
	}
	return false
}

func oneInsertedByteApart(shorter, longer string) bool {
	shortIndex := 0
	longIndex := 0
	skipped := false
	for shortIndex < len(shorter) && longIndex < len(longer) {
		if shorter[shortIndex] == longer[longIndex] {
			shortIndex++
			longIndex++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		longIndex++
	}
	return true
}

func appendModelCapabilityDiagnostics(diagnostics []Diagnostic, descriptor *model.Descriptor) []Diagnostic {
	fields := descriptor.Fields()
	hasPrimaryKey := false
	for _, field := range fields {
		hasPrimaryKey = hasPrimaryKey || field.IsPrimaryKey()
	}
	if !hasPrimaryKey {
		diagnostics = append(diagnostics, Diagnostic{
			Code:         codeMissingPrimaryKey,
			Severity:     SeverityInfo,
			Title:        "Model has no primary key",
			Message:      fmt.Sprintf("%s has no pk field, so primary-key Update and Delete operations are unavailable", descriptor.Name()),
			Suggestion:   "Add pk metadata only when this table model needs primary-key mutations",
			Suppressible: true,
		})
	}
	for _, field := range fields {
		if field.Kind() != model.KindCustom {
			continue
		}
		if !field.CanScan() {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeWriteOnlyCustomField,
				Severity:     SeverityWarning,
				Title:        "Custom field cannot be scanned",
				Message:      fmt.Sprintf("%s.%s implements driver.Valuer but not sql.Scanner, so model-aware SELECTs cannot scan it", descriptor.Name(), field.GoName()),
				Suggestion:   "Implement sql.Scanner, exclude the field, or use it only in a write-specific model",
				Suppressible: true,
			})
		}
		if !field.IsComputed() && !field.CanValue() {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeReadOnlyCustomField,
				Severity:     SeverityWarning,
				Title:        "Custom field cannot be used as an argument",
				Message:      fmt.Sprintf("%s.%s implements sql.Scanner but not driver.Valuer, so predicates and mutations cannot bind it", descriptor.Name(), field.GoName()),
				Suggestion:   "Implement driver.Valuer, mark a raw-result-only field as computed, or use it only in a read-specific model",
				Suppressible: true,
			})
		}
	}
	return diagnostics
}

func indirectModelCheckType(value reflect.Type) reflect.Type {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func modelCheckImplements(fieldType, baseType, interfaceType reflect.Type) bool {
	if fieldType.Implements(interfaceType) || baseType.Implements(interfaceType) {
		return true
	}
	if fieldType.Kind() != reflect.Pointer && reflect.PointerTo(fieldType).Implements(interfaceType) {
		return true
	}
	return baseType.Kind() != reflect.Pointer && reflect.PointerTo(baseType).Implements(interfaceType)
}

func inferredModelColumn(value string) string {
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
