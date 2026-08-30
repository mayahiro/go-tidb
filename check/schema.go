package check

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/mayahiro/go-tidb/model"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

const (
	codeInvalidSchemaCatalog    = "CMP001"
	codeMissingPhysicalTable    = "CMP002"
	codeMissingPhysicalColumn   = "CMP003"
	codeIncompatibleColumnType  = "CMP004"
	codeNullableDatabaseColumn  = "CMP005"
	codeNullableGoField         = "CMP006"
	codePrimaryKeyMismatch      = "CMP007"
	codeAutoRandomMismatch      = "CMP008"
	codeGeneratedColumnWritable = "CMP009"
	codeRequiredDatabaseColumn  = "CMP010"
	codeRelationTargetNotUnique = "CMP011"
	codeJunctionPairNotUnique   = "CMP012"
	codeRequiredJunctionColumn  = "CMP013"
	codeMissingRelationIndex    = "CMP014"

	relationIndexReference = "https://docs.pingcap.com/developer/dev-guide-index-best-practice/"
)

// Schema checks the physical schema catalog against the application-model and
// relation responsibilities declared by T without accessing a database.
//
// Database-only nullable, defaulted, and generated columns are accepted. The
// result is ordered deterministically and is a non-nil empty slice when no
// diagnostics are found.
func Schema[T any](catalog *physicalschema.Catalog) []Diagnostic {
	return SchemaType(catalog, reflect.TypeFor[T]())
}

// SchemaType checks the physical schema catalog against the application-model
// and relation responsibilities declared by modelType without accessing a
// database. Pointer layers around a named struct are ignored.
func SchemaType(catalog *physicalschema.Catalog, modelType reflect.Type) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if catalog == nil {
		return append(diagnostics, Diagnostic{
			Code:         codeInvalidSchemaCatalog,
			Severity:     SeverityError,
			Title:        "Schema catalog is unavailable",
			Message:      "schema compatibility requires a non-nil catalog returned by schema.Parse",
			Suggestion:   "Parse a self-contained TiDB CREATE TABLE snapshot before running the compatibility check",
			Suppressible: false,
		})
	}
	descriptor, err := model.DescribeType(modelType)
	if err != nil {
		return appendModelValidationDiagnostics(diagnostics, err)
	}
	table, exists := catalog.Table(descriptor.TableName())
	if !exists {
		return append(diagnostics, Diagnostic{
			Code:         codeMissingPhysicalTable,
			Severity:     SeverityError,
			Title:        "Model table is missing",
			Message:      fmt.Sprintf("model %s maps to table %q, but that table is absent from the SQL snapshot", descriptor.Name(), descriptor.TableName()),
			Suggestion:   "Use a snapshot containing this table, or correct the model table mapping",
			Suppressible: false,
		})
	}

	diagnostics = appendColumnCompatibilityDiagnostics(diagnostics, descriptor, table)
	diagnostics = appendPrimaryKeyCompatibilityDiagnostic(diagnostics, descriptor, table)
	diagnostics = appendRequiredColumnDiagnostics(diagnostics, descriptor, table)
	return appendRelationSchemaDiagnostics(diagnostics, catalog, descriptor)
}

func appendColumnCompatibilityDiagnostics(diagnostics []Diagnostic, descriptor *model.Descriptor, table physicalschema.Table) []Diagnostic {
	for _, field := range descriptor.Fields() {
		if field.IsComputed() {
			continue
		}
		column, exists := table.Column(field.ColumnName())
		if !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeMissingPhysicalColumn,
				Severity:     SeverityError,
				Title:        "Model column is missing",
				Message:      fmt.Sprintf("%s.%s maps to column %q, but table %q does not contain it", descriptor.Name(), field.GoName(), field.ColumnName(), table.Name()),
				Suggestion:   "Correct the field mapping or add the physical column before using this model",
				Suppressible: false,
			})
			continue
		}
		location := schemaLocation(column.Position())
		if !compatibleSQLType(field.Kind(), column.TypeName()) {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeIncompatibleColumnType,
				Severity:     SeverityError,
				Title:        "Column type is incompatible with the Go field",
				Message:      fmt.Sprintf("%s.%s uses %s, but %s.%s is %s", descriptor.Name(), field.GoName(), field.GoType(), table.Name(), column.Name(), column.TypeName()),
				Suggestion:   "Use a compatible Go representation or change the physical column type",
				Location:     location,
				Suppressible: false,
			})
		}
		if field.Kind() != model.KindCustom {
			goNullable := field.PointerDepth() > 0 || field.Kind() == model.KindBytes || field.IsSoftDelete()
			switch {
			case column.Nullable() && !goNullable:
				diagnostics = append(diagnostics, Diagnostic{
					Code:         codeNullableDatabaseColumn,
					Severity:     SeverityWarning,
					Title:        "Nullable column uses a non-nullable Go field",
					Message:      fmt.Sprintf("%s.%s cannot represent NULL allowed by %s.%s", descriptor.Name(), field.GoName(), table.Name(), column.Name()),
					Suggestion:   "Use a pointer or nullable Scanner type when rows can contain NULL, or make the physical column NOT NULL",
					Location:     location,
					Suppressible: true,
				})
			case !column.Nullable() && goNullable:
				diagnostics = append(diagnostics, Diagnostic{
					Code:         codeNullableGoField,
					Severity:     SeverityWarning,
					Title:        "Nullable Go field maps to a NOT NULL column",
					Message:      fmt.Sprintf("%s.%s can produce NULL, but %s.%s is NOT NULL", descriptor.Name(), field.GoName(), table.Name(), column.Name()),
					Suggestion:   "Ensure writes always provide a value, or align the Go and physical nullability",
					Location:     location,
					Suppressible: true,
				})
			}
		}
		if field.IsAutoRandom() != column.AutoRandom() {
			message := fmt.Sprintf("%s.%s and %s.%s disagree about AUTO_RANDOM", descriptor.Name(), field.GoName(), table.Name(), column.Name())
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeAutoRandomMismatch,
				Severity:     SeverityError,
				Title:        "AUTO_RANDOM mapping does not match",
				Message:      message,
				Suggestion:   "Declare auto_random exactly when the mapped TiDB column uses AUTO_RANDOM",
				Location:     location,
				Suppressible: false,
				Reference:    "https://docs.pingcap.com/tidbcloud/auto-random/",
			})
		}
		if column.Generated() {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeGeneratedColumnWritable,
				Severity:     SeverityError,
				Title:        "Generated column is writable in the model",
				Message:      fmt.Sprintf("%s.%s maps to generated column %s.%s, but ordinary model fields are included in mutations", descriptor.Name(), field.GoName(), table.Name(), column.Name()),
				Suggestion:   "Exclude the physical generated column from mutation models and scan it through an explicit raw-result field when needed",
				Location:     location,
				Suppressible: false,
			})
		}
	}
	return diagnostics
}

func appendPrimaryKeyCompatibilityDiagnostic(diagnostics []Diagnostic, descriptor *model.Descriptor, table physicalschema.Table) []Diagnostic {
	modelPrimaryKey := descriptor.PrimaryKeyFields()
	if len(modelPrimaryKey) == 0 {
		return diagnostics
	}
	modelColumns := make([]string, len(modelPrimaryKey))
	for index, field := range modelPrimaryKey {
		modelColumns[index] = field.ColumnName()
	}
	physicalColumns := table.PrimaryKeyColumns()
	if equalIdentifiers(modelColumns, physicalColumns) {
		return diagnostics
	}
	return append(diagnostics, Diagnostic{
		Code:         codePrimaryKeyMismatch,
		Severity:     SeverityError,
		Title:        "Primary key mapping does not match",
		Message:      fmt.Sprintf("model %s declares primary key (%s), but table %s declares (%s)", descriptor.Name(), strings.Join(modelColumns, ", "), table.Name(), strings.Join(physicalColumns, ", ")),
		Suggestion:   "Make the ordered pk fields match the physical primary key",
		Location:     schemaLocation(table.Position()),
		Suppressible: false,
	})
}

func appendRequiredColumnDiagnostics(diagnostics []Diagnostic, descriptor *model.Descriptor, table physicalschema.Table) []Diagnostic {
	mapped := make(map[string]struct{})
	for _, field := range descriptor.Fields() {
		if !field.IsComputed() {
			mapped[foldSchemaIdentifier(field.ColumnName())] = struct{}{}
		}
	}
	for _, column := range table.Columns() {
		if _, exists := mapped[foldSchemaIdentifier(column.Name())]; exists {
			continue
		}
		if column.Nullable() || column.HasDefault() || column.DatabaseGenerated() {
			continue
		}
		diagnostics = append(diagnostics, Diagnostic{
			Code:         codeRequiredDatabaseColumn,
			Severity:     SeverityWarning,
			Title:        "Required database column is absent from the model",
			Message:      fmt.Sprintf("%s.%s is NOT NULL without a default or database generation, but model %s does not map it", table.Name(), column.Name(), descriptor.Name()),
			Suggestion:   "Map the column when this model inserts rows, or suppress this diagnostic for an intentionally read-only or partial model",
			Location:     schemaLocation(column.Position()),
			Suppressible: true,
		})
	}
	return diagnostics
}

func appendRelationSchemaDiagnostics(diagnostics []Diagnostic, catalog *physicalschema.Catalog, descriptor *model.Descriptor) []Diagnostic {
	for _, relation := range descriptor.Relations() {
		targetDescriptor, err := model.DescribeType(relation.TargetType())
		if err != nil {
			continue
		}
		targetFields := relation.TargetKey()
		targetColumns := fieldColumnNames(targetFields)
		targetTable, exists := catalog.Table(targetDescriptor.TableName())
		if !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeMissingPhysicalTable,
				Severity:     SeverityError,
				Title:        "Relation target table is missing",
				Message:      fmt.Sprintf("%s.%s targets table %q, but that table is absent from the SQL snapshot", descriptor.Name(), relation.GoName(), targetDescriptor.TableName()),
				Suggestion:   "Use a snapshot containing the relation target table, or correct the relation mapping",
				Suppressible: false,
			})
		} else {
			var targetColumnsPresent bool
			diagnostics, targetColumnsPresent = appendRelationColumnDiagnostics(
				diagnostics,
				descriptor,
				relation,
				targetDescriptor,
				targetTable,
				targetFields,
			)
			if targetColumnsPresent {
				switch relation.Kind() {
				case model.RelationBelongsTo, model.RelationHasOne, model.RelationManyToMany:
					if !tableHasUniqueKey(targetTable, targetColumns) {
						diagnostics = append(diagnostics, Diagnostic{
							Code:         codeRelationTargetNotUnique,
							Severity:     SeverityError,
							Title:        "Relation target is not uniquely constrained",
							Message:      fmt.Sprintf("%s.%s targets %s(%s), but the SQL snapshot has no primary or unique key proving target identity", descriptor.Name(), relation.GoName(), targetDescriptor.TableName(), strings.Join(targetColumns, ", ")),
							Suggestion:   "Add a primary or unique key over the relation target columns, or correct the relation mapping",
							Location:     schemaLocation(targetTable.Position()),
							Suppressible: false,
						})
					}
				case model.RelationHasMany:
					if !tableHasIndexPrefix(targetTable, targetColumns) {
						diagnostics = appendMissingRelationIndexDiagnostic(diagnostics, descriptor, relation, targetTable, targetColumns)
					}
				}
			}
		}

		if relation.Kind() == model.RelationManyToMany {
			diagnostics = appendJunctionDiagnostics(diagnostics, catalog, descriptor, relation, targetDescriptor, targetFields)
		}
	}
	return diagnostics
}

func appendRelationColumnDiagnostics(
	diagnostics []Diagnostic,
	descriptor *model.Descriptor,
	relation model.Relation,
	targetDescriptor *model.Descriptor,
	targetTable physicalschema.Table,
	targetFields []model.Field,
) ([]Diagnostic, bool) {
	columnsPresent := true
	for _, field := range targetFields {
		column, exists := targetTable.Column(field.ColumnName())
		if !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Code:         codeMissingPhysicalColumn,
				Severity:     SeverityError,
				Title:        "Relation target column is missing",
				Message:      fmt.Sprintf("%s.%s targets %s.%s, but that column is absent from the SQL snapshot", descriptor.Name(), relation.GoName(), targetTable.Name(), field.ColumnName()),
				Suggestion:   "Correct the relation key mapping or add the physical target column",
				Location:     schemaLocation(targetTable.Position()),
				Suppressible: false,
			})
			columnsPresent = false
			continue
		}
		if compatibleSQLType(field.Kind(), column.TypeName()) {
			continue
		}
		diagnostics = append(diagnostics, Diagnostic{
			Code:         codeIncompatibleColumnType,
			Severity:     SeverityError,
			Title:        "Relation target column type is incompatible",
			Message:      fmt.Sprintf("%s.%s targets %s.%s for %s.%s, but the SQL column is %s", descriptor.Name(), relation.GoName(), targetTable.Name(), column.Name(), targetDescriptor.Name(), field.GoName(), column.TypeName()),
			Suggestion:   "Use a compatible relation key representation or change the physical column type",
			Location:     schemaLocation(column.Position()),
			Suppressible: false,
		})
	}
	return diagnostics, columnsPresent
}

func appendJunctionDiagnostics(
	diagnostics []Diagnostic,
	catalog *physicalschema.Catalog,
	descriptor *model.Descriptor,
	relation model.Relation,
	targetDescriptor *model.Descriptor,
	targetFields []model.Field,
) []Diagnostic {
	junction, exists := relation.Junction()
	if !exists {
		return diagnostics
	}
	table, exists := catalog.Table(junction.TableName())
	if !exists {
		return append(diagnostics, Diagnostic{
			Code:         codeMissingPhysicalTable,
			Severity:     SeverityError,
			Title:        "Relation junction table is missing",
			Message:      fmt.Sprintf("%s.%s maps through table %q, but that table is absent from the SQL snapshot", descriptor.Name(), relation.GoName(), junction.TableName()),
			Suggestion:   "Use a snapshot containing the junction table, or correct the through mapping",
			Suppressible: false,
		})
	}

	sourceFields := relation.SourceKey()
	sourceColumns := junction.SourceColumns()
	targetColumns := junction.TargetColumns()
	columnsPresent := true
	for index, columnName := range sourceColumns {
		var columnExists bool
		diagnostics, columnExists = appendJunctionColumnDiagnostic(
			diagnostics,
			descriptor,
			relation,
			table,
			columnName,
			descriptor,
			sourceFields[index],
		)
		columnsPresent = columnsPresent && columnExists
	}
	for index, columnName := range targetColumns {
		var columnExists bool
		diagnostics, columnExists = appendJunctionColumnDiagnostic(
			diagnostics,
			descriptor,
			relation,
			table,
			columnName,
			targetDescriptor,
			targetFields[index],
		)
		columnsPresent = columnsPresent && columnExists
	}
	if !columnsPresent {
		return diagnostics
	}

	pairColumns := make([]string, 0, len(sourceColumns)+len(targetColumns))
	pairColumns = append(pairColumns, sourceColumns...)
	pairColumns = append(pairColumns, targetColumns...)
	pairUnique, sourceIndexed := junctionIndexCoverage(table, pairColumns, sourceColumns)
	if !pairUnique {
		diagnostics = append(diagnostics, Diagnostic{
			Code:         codeJunctionPairNotUnique,
			Severity:     SeverityError,
			Title:        "Junction relation pair is not uniquely constrained",
			Message:      fmt.Sprintf("%s.%s maps through %s(%s), but the SQL snapshot has no primary or unique key over exactly that source-target pair", descriptor.Name(), relation.GoName(), table.Name(), strings.Join(pairColumns, ", ")),
			Suggestion:   "Add a primary or unique key over the complete source-target pair",
			Location:     schemaLocation(table.Position()),
			Suppressible: false,
		})
	}
	if !sourceIndexed {
		diagnostics = appendMissingRelationIndexDiagnostic(diagnostics, descriptor, relation, table, sourceColumns)
	}
	return appendRequiredJunctionColumnDiagnostics(diagnostics, descriptor, relation, table, pairColumns)
}

func appendJunctionColumnDiagnostic(
	diagnostics []Diagnostic,
	descriptor *model.Descriptor,
	relation model.Relation,
	table physicalschema.Table,
	columnName string,
	keyDescriptor *model.Descriptor,
	field model.Field,
) ([]Diagnostic, bool) {
	column, exists := table.Column(columnName)
	if !exists {
		return append(diagnostics, Diagnostic{
			Code:         codeMissingPhysicalColumn,
			Severity:     SeverityError,
			Title:        "Junction relation column is missing",
			Message:      fmt.Sprintf("%s.%s maps %s.%s through %s.%s, but that junction column is absent from the SQL snapshot", descriptor.Name(), relation.GoName(), keyDescriptor.Name(), field.GoName(), table.Name(), columnName),
			Suggestion:   "Correct the relation key mapping or add the physical junction column",
			Location:     schemaLocation(table.Position()),
			Suppressible: false,
		}), false
	}
	if compatibleSQLType(field.Kind(), column.TypeName()) {
		return diagnostics, true
	}
	return append(diagnostics, Diagnostic{
		Code:         codeIncompatibleColumnType,
		Severity:     SeverityError,
		Title:        "Junction relation column type is incompatible",
		Message:      fmt.Sprintf("%s.%s maps %s.%s through %s.%s, but the SQL column is %s", descriptor.Name(), relation.GoName(), keyDescriptor.Name(), field.GoName(), table.Name(), column.Name(), column.TypeName()),
		Suggestion:   "Use a compatible relation key representation or change the physical junction column type",
		Location:     schemaLocation(column.Position()),
		Suppressible: false,
	}), true
}

func appendRequiredJunctionColumnDiagnostics(
	diagnostics []Diagnostic,
	descriptor *model.Descriptor,
	relation model.Relation,
	table physicalschema.Table,
	mappedColumns []string,
) []Diagnostic {
	for _, column := range table.Columns() {
		if containsIdentifier(mappedColumns, column.Name()) {
			continue
		}
		if column.Nullable() || column.HasDefault() || column.DatabaseGenerated() {
			continue
		}
		diagnostics = append(diagnostics, Diagnostic{
			Code:         codeRequiredJunctionColumn,
			Severity:     SeverityError,
			Title:        "Junction table requires unmapped insert data",
			Message:      fmt.Sprintf("%s.%s inserts only relation keys, but %s.%s is NOT NULL without a default or database generation", descriptor.Name(), relation.GoName(), table.Name(), column.Name()),
			Suggestion:   "Use a payload-free junction with database-managed extra columns, or model the junction as an ordinary edge model",
			Location:     schemaLocation(column.Position()),
			Suppressible: false,
		})
	}
	return diagnostics
}

func appendMissingRelationIndexDiagnostic(
	diagnostics []Diagnostic,
	descriptor *model.Descriptor,
	relation model.Relation,
	table physicalschema.Table,
	columns []string,
) []Diagnostic {
	return append(diagnostics, Diagnostic{
		Code:         codeMissingRelationIndex,
		Severity:     SeverityWarning,
		Title:        "Relation lookup has no matching index prefix",
		Message:      fmt.Sprintf("%s.%s reads %s by relation columns (%s), but no index starts with all of those columns", descriptor.Name(), relation.GoName(), table.Name(), strings.Join(columns, ", ")),
		Suggestion:   "Verify the generated relation query with EXPLAIN and add a source-prefix index when the access path scans unnecessary rows",
		Location:     schemaLocation(table.Position()),
		Suppressible: true,
		Reference:    relationIndexReference,
	})
}

func fieldColumnNames(fields []model.Field) []string {
	columns := make([]string, len(fields))
	for index, field := range fields {
		columns[index] = field.ColumnName()
	}
	return columns
}

func tableHasUniqueKey(table physicalschema.Table, columns []string) bool {
	if len(columns) == 0 {
		return false
	}
	for _, index := range table.Indexes() {
		indexColumns := index.Columns()
		if !index.ProvidesUnconditionalUniqueness() || len(indexColumns) == 0 {
			continue
		}
		provesUnique := true
		for _, indexedColumn := range indexColumns {
			found := false
			for _, targetColumn := range columns {
				if strings.EqualFold(indexedColumn, targetColumn) {
					found = true
					break
				}
			}
			if !found {
				provesUnique = false
				break
			}
		}
		if provesUnique {
			return true
		}
	}
	return false
}

func tableHasIndexPrefix(table physicalschema.Table, columns []string) bool {
	if len(columns) == 0 {
		return false
	}
	for _, index := range table.Indexes() {
		indexColumns := index.Columns()
		if !index.SupportsDefaultColumnLookup() || len(indexColumns) < len(columns) {
			continue
		}
		if equalIdentifierSets(indexColumns[:len(columns)], columns) {
			return true
		}
	}
	return false
}

func junctionIndexCoverage(table physicalschema.Table, pairColumns, sourceColumns []string) (bool, bool) {
	pairUnique := false
	sourceIndexed := false
	for _, index := range table.Indexes() {
		indexColumns := index.Columns()
		if index.ProvidesUnconditionalUniqueness() && len(indexColumns) == len(pairColumns) && equalIdentifierSets(indexColumns, pairColumns) {
			pairUnique = true
		}
		if index.SupportsDefaultColumnLookup() && len(indexColumns) >= len(sourceColumns) && equalIdentifierSets(indexColumns[:len(sourceColumns)], sourceColumns) {
			sourceIndexed = true
		}
		if pairUnique && sourceIndexed {
			return true, true
		}
	}
	return pairUnique, sourceIndexed
}

func equalIdentifierSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for leftIndex, leftIdentifier := range left {
		for previousIndex := 0; previousIndex < leftIndex; previousIndex++ {
			if strings.EqualFold(leftIdentifier, left[previousIndex]) {
				return false
			}
		}
		matched := false
		for _, rightIdentifier := range right {
			if strings.EqualFold(leftIdentifier, rightIdentifier) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func containsIdentifier(identifiers []string, target string) bool {
	for _, identifier := range identifiers {
		if strings.EqualFold(identifier, target) {
			return true
		}
	}
	return false
}

func compatibleSQLType(kind model.Kind, sqlType string) bool {
	if kind == model.KindCustom {
		return true
	}
	typeName := strings.ToUpper(sqlType)
	if _, known := knownSQLTypes[typeName]; !known {
		return true
	}
	switch kind {
	case model.KindBool:
		return typeName == "TINYINT" || typeName == "BOOL" || typeName == "BOOLEAN"
	case model.KindInt, model.KindUint:
		return sqlIntegerTypes[typeName] || typeName == "YEAR"
	case model.KindFloat:
		return sqlIntegerTypes[typeName] || sqlApproximateTypes[typeName] || sqlExactTypes[typeName]
	case model.KindString:
		return sqlTextTypes[typeName]
	case model.KindBytes:
		return sqlTextTypes[typeName] || sqlBinaryTypes[typeName] || typeName == "BIT"
	case model.KindTime:
		return typeName == "DATE" || typeName == "DATETIME" || typeName == "TIMESTAMP"
	default:
		return true
	}
}

var sqlIntegerTypes = map[string]bool{
	"TINYINT": true, "SMALLINT": true, "MEDIUMINT": true, "INT": true, "INTEGER": true, "BIGINT": true,
}

var sqlApproximateTypes = map[string]bool{
	"FLOAT": true, "DOUBLE": true, "REAL": true,
}

var sqlExactTypes = map[string]bool{
	"DECIMAL": true, "NUMERIC": true, "FIXED": true,
}

var sqlTextTypes = map[string]bool{
	"CHAR": true, "VARCHAR": true, "NCHAR": true, "NVARCHAR": true,
	"TINYTEXT": true, "TEXT": true, "MEDIUMTEXT": true, "LONGTEXT": true,
	"ENUM": true, "SET": true, "JSON": true,
}

var sqlBinaryTypes = map[string]bool{
	"BINARY": true, "VARBINARY": true,
	"TINYBLOB": true, "BLOB": true, "MEDIUMBLOB": true, "LONGBLOB": true,
}

var knownSQLTypes = func() map[string]struct{} {
	known := make(map[string]struct{})
	for _, types := range []map[string]bool{sqlIntegerTypes, sqlApproximateTypes, sqlExactTypes, sqlTextTypes, sqlBinaryTypes} {
		for typeName := range types {
			known[typeName] = struct{}{}
		}
	}
	for _, typeName := range []string{"BIT", "BOOL", "BOOLEAN", "YEAR", "DATE", "DATETIME", "TIMESTAMP", "TIME"} {
		known[typeName] = struct{}{}
	}
	return known
}()

func equalIdentifiers(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

func foldSchemaIdentifier(identifier string) string {
	return strings.ToLower(identifier)
}

func schemaLocation(position physicalschema.Position) Location {
	return Location{Line: position.Line, Column: position.Column}
}
