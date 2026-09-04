package check

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

type schemaCheckParent struct {
	model.Meta `tidbgo:"table=schema_check_parents"`
	ID         int64  `tidbgo:"id,pk,auto_random"`
	Code       string `tidbgo:"code"`
	Note       *string
	Data       []byte
	Child      *schemaCheckChild `tidbgo:"has_one,join=ID:ParentID"`
}

type schemaCheckChild struct {
	model.Meta `tidbgo:"table=schema_check_children"`
	ID         int64 `tidbgo:"id,pk"`
	ParentID   int64
}

type schemaCheckPlain struct {
	model.Meta `tidbgo:"table=schema_check_plain"`
	ID         int64  `tidbgo:"id,pk"`
	Code       string `tidbgo:"code"`
}

type schemaCheckSoftDelete struct {
	model.Meta `tidbgo:"table=schema_check_soft_delete"`
	ID         int64     `tidbgo:"id,pk"`
	DeletedAt  time.Time `tidbgo:"deleted_at,soft_delete"`
}

type schemaCheckDate string

func (*schemaCheckDate) Scan(any) error {
	panic("check.Schema must not call Scan")
}

type schemaCheckEncodedInt int64

func (schemaCheckEncodedInt) Value() (driver.Value, error) {
	panic("check.Schema must not call Value")
}

type schemaCheckPlainNamedString string

type schemaCheckNativeCustomValues struct {
	model.Meta `tidbgo:"table=schema_check_native_custom_values"`
	Date       schemaCheckDate
	Optional   *schemaCheckDate
	Encoded    schemaCheckEncodedInt
}

type schemaCheckPlainNamedValue struct {
	model.Meta `tidbgo:"table=schema_check_plain_named_values"`
	Value      schemaCheckPlainNamedString
}

type schemaCheckCustomRelationParent struct {
	model.Meta `tidbgo:"table=schema_check_custom_relation_parents"`
	ID         schemaCheckDate                   `tidbgo:",pk"`
	Targets    []schemaCheckCustomRelationTarget `tidbgo:"many_to_many,through=schema_check_custom_relation_links,source=ID:parent_id,target=target_id:ID"`
}

type schemaCheckCustomRelationTarget struct {
	model.Meta `tidbgo:"table=schema_check_custom_relation_targets"`
	ID         schemaCheckDate `tidbgo:",pk"`
}

type schemaCheckCandidateParent struct {
	model.Meta `tidbgo:"table=schema_check_candidate_parents"`
	ID         int64                      `tidbgo:",pk"`
	Edges      []schemaCheckCandidateEdge `tidbgo:"has_many,join=ID:ParentID"`
}

type schemaCheckCandidateEdge struct {
	model.Meta `tidbgo:"table=schema_check_candidate_edges"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64 `tidbgo:",unique=parent_genre"`
	GenreID    int64 `tidbgo:",unique=parent_genre"`
	Priority   int64
}

type schemaCheckRelationParent struct {
	model.Meta `tidbgo:"table=schema_check_relation_parents"`
	TenantID   int64                       `tidbgo:"tenant_id,pk"`
	ID         int64                       `tidbgo:"id,pk"`
	Children   []schemaCheckRelationChild  `tidbgo:"has_many,join=TenantID:ParentTenantID,join=ID:ParentID"`
	Roles      []schemaCheckRelationTarget `tidbgo:"many_to_many,through=schema_check_relation_links,source=TenantID:parent_tenant_id,source=ID:parent_id,target=role_tenant_id:TenantID,target=role_id:ID"`
}

type schemaCheckRelationChild struct {
	model.Meta     `tidbgo:"table=schema_check_relation_children"`
	ID             int64 `tidbgo:"id,pk"`
	ParentTenantID int64
	ParentID       int64
}

type schemaCheckRelationTarget struct {
	model.Meta `tidbgo:"table=schema_check_relation_targets"`
	TenantID   int64 `tidbgo:"tenant_id,pk"`
	ID         int64 `tidbgo:"id,pk"`
	Name       string
}

const compatibleSchemaSQL = `
CREATE TABLE schema_check_parents (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  code VARCHAR(64) NOT NULL,
  note VARCHAR(64) NULL,
  data BLOB NULL,
  managed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  optional_value VARCHAR(64) NULL,
  derived_value VARCHAR(128) GENERATED ALWAYS AS (concat(code, note)) STORED,
  PRIMARY KEY (id)
);
CREATE TABLE schema_check_children (
  id BIGINT NOT NULL,
  parent_id BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY schema_check_children_parent_key (parent_id)
);`

const compatibleRelationSchemaSQL = `
CREATE TABLE schema_check_relation_parents (
  tenant_id BIGINT NOT NULL,
  id BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE schema_check_relation_children (
  id BIGINT NOT NULL,
  parent_tenant_id BIGINT NOT NULL,
  parent_id BIGINT NOT NULL,
  PRIMARY KEY (id),
  KEY schema_check_relation_children_parent (parent_id, parent_tenant_id)
);
CREATE TABLE schema_check_relation_targets (
  tenant_id BIGINT NOT NULL,
  id BIGINT NOT NULL,
  name VARCHAR(64) NOT NULL,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE schema_check_relation_links (
  parent_tenant_id BIGINT NOT NULL,
  parent_id BIGINT NOT NULL,
  role_tenant_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY schema_check_relation_links_pair (parent_tenant_id, parent_id, role_tenant_id, role_id)
);`

const compatibleCandidateSchemaSQL = `
CREATE TABLE schema_check_candidate_parents (
  id BIGINT NOT NULL,
  PRIMARY KEY (id)
);
CREATE TABLE schema_check_candidate_edges (
  id BIGINT NOT NULL,
  parent_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  priority BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY schema_check_candidate_edges_pair (genre_id, parent_id),
  KEY schema_check_candidate_edges_parent (parent_id)
);`

func TestSchemaReturnsNoDiagnosticsForCompatibleDirectionalMapping(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, compatibleSchemaSQL)
	for name, diagnostics := range map[string][]Diagnostic{
		"value":   Schema[schemaCheckParent](catalog),
		"pointer": Schema[**schemaCheckParent](catalog),
		"type":    SchemaType(catalog, reflect.TypeFor[schemaCheckParent]()),
	} {
		if diagnostics == nil || len(diagnostics) != 0 {
			t.Fatalf("%s diagnostics = %#v, want non-nil empty", name, diagnostics)
		}
	}
}

func TestSchemaRejectsUnavailableInputs(t *testing.T) {
	t.Parallel()

	if diagnostics := Schema[schemaCheckParent](nil); len(diagnostics) != 1 || diagnostics[0].Code != codeInvalidSchemaCatalog || diagnostics[0].Suppressible {
		t.Fatalf("nil catalog diagnostics = %#v", diagnostics)
	}
	catalog := parseSchemaCheckCatalog(t, compatibleSchemaSQL)
	diagnostics := SchemaType(catalog, reflect.TypeFor[map[string]string]())
	if len(diagnostics) != 1 || diagnostics[0].Code != codeInvalidModel {
		t.Fatalf("invalid model diagnostics = %#v", diagnostics)
	}
}

func TestSchemaReportsMissingTable(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, "CREATE TABLE another_table (id BIGINT PRIMARY KEY);")
	diagnostics := Schema[schemaCheckParent](catalog)
	if got, want := diagnosticCodes(diagnostics), []string{codeMissingPhysicalTable}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaReportsColumnReadAndWriteIncompatibilitiesInFieldOrder(t *testing.T) {
	t.Parallel()

	sql := strings.Replace(compatibleSchemaSQL, "code VARCHAR(64) NOT NULL", "code BIGINT NULL", 1)
	sql = strings.Replace(sql, "note VARCHAR(64) NULL", "note VARCHAR(64) NOT NULL", 1)
	sql = strings.Replace(sql, "data BLOB NULL", "data BLOB NOT NULL", 1)
	catalog := parseSchemaCheckCatalog(t, sql)
	diagnostics := Schema[schemaCheckParent](catalog)
	wantCodes := []string{
		codeIncompatibleColumnType,
		codeNullableDatabaseColumn,
		codeNullableGoField,
		codeNullableGoField,
	}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
			t.Fatalf("diagnostic location = %#v", diagnostic)
		}
	}
}

func TestSchemaReportsMissingMappedAndRequiredUnmappedColumns(t *testing.T) {
	t.Parallel()

	sql := strings.Replace(compatibleSchemaSQL, "  code VARCHAR(64) NOT NULL,\n", "", 1)
	sql = strings.Replace(sql, "  managed_at", "  required_value VARCHAR(64) NOT NULL,\n  managed_at", 1)
	catalog := parseSchemaCheckCatalog(t, sql)
	diagnostics := Schema[schemaCheckParent](catalog)
	wantCodes := []string{codeMissingPhysicalColumn, codeRequiredDatabaseColumn}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if diagnostics[1].Severity != SeverityWarning || !diagnostics[1].Suppressible {
		t.Fatalf("required-column diagnostic = %#v", diagnostics[1])
	}
}

func TestSchemaReportsPrimaryKeyAndAutoRandomMismatch(t *testing.T) {
	t.Parallel()

	withoutAutoRandom := strings.Replace(compatibleSchemaSQL, " /*T![auto_rand] AUTO_RANDOM(5) */", "", 1)
	diagnostics := Schema[schemaCheckParent](parseSchemaCheckCatalog(t, withoutAutoRandom))
	if got, want := diagnosticCodes(diagnostics), []string{codeAutoRandomMismatch}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AUTO_RANDOM codes = %#v, want %#v", got, want)
	}

	plainSQL := `CREATE TABLE schema_check_plain (
  id BIGINT NOT NULL,
  code VARCHAR(64) NOT NULL,
  PRIMARY KEY (code)
);`
	diagnostics = Schema[schemaCheckPlain](parseSchemaCheckCatalog(t, plainSQL))
	if got, want := diagnosticCodes(diagnostics), []string{codePrimaryKeyMismatch}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary-key codes = %#v, want %#v", got, want)
	}
}

func TestSchemaValidatesCandidateUniqueKeysWithoutRequiringAnIndexNameOrOrder(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, compatibleCandidateSchemaSQL)
	for name, diagnostics := range map[string][]Diagnostic{
		"edge":   Schema[schemaCheckCandidateEdge](catalog),
		"parent": Schema[schemaCheckCandidateParent](catalog),
	} {
		if diagnostics == nil || len(diagnostics) != 0 {
			t.Fatalf("%s diagnostics = %#v, want non-nil empty", name, diagnostics)
		}
	}
}

func TestSchemaRejectsCandidateKeyWithoutUnconditionalPhysicalUniqueness(t *testing.T) {
	t.Parallel()

	for name, uniqueDefinition := range map[string]string{
		"ordinary": "KEY schema_check_candidate_edges_pair (genre_id, parent_id)",
		"superset": "UNIQUE KEY schema_check_candidate_edges_pair (genre_id, parent_id, priority)",
		"partial":  "UNIQUE KEY schema_check_candidate_edges_pair (genre_id, parent_id) WHERE priority > 0",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlText := strings.Replace(
				compatibleCandidateSchemaSQL,
				"UNIQUE KEY schema_check_candidate_edges_pair (genre_id, parent_id)",
				uniqueDefinition,
				1,
			)
			diagnostics := Schema[schemaCheckCandidateParent](parseSchemaCheckCatalog(t, sqlText))
			if got, want := diagnosticCodes(diagnostics), []string{codeCandidateKeyMismatch}; !reflect.DeepEqual(got, want) {
				t.Fatalf("codes = %#v, want %#v", got, want)
			}
			diagnostic := diagnostics[0]
			if diagnostic.Severity != SeverityError || diagnostic.Suppressible || diagnostic.Reference != uniqueConstraintReference ||
				!strings.Contains(diagnostic.Message, `candidate unique key "parent_genre"`) {
				t.Fatalf("diagnostic = %#v", diagnostic)
			}
		})
	}
}

func TestSchemaAcceptsInvisibleOrNarrowerCandidateKeyProof(t *testing.T) {
	t.Parallel()

	for name, uniqueDefinition := range map[string]string{
		"invisible": "UNIQUE KEY schema_check_candidate_edges_pair (parent_id, genre_id) /*!80000 INVISIBLE */",
		"subset":    "UNIQUE KEY schema_check_candidate_edges_pair (parent_id)",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlText := strings.Replace(
				compatibleCandidateSchemaSQL,
				"UNIQUE KEY schema_check_candidate_edges_pair (genre_id, parent_id)",
				uniqueDefinition,
				1,
			)
			if diagnostics := Schema[schemaCheckCandidateParent](parseSchemaCheckCatalog(t, sqlText)); len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %#v, want none", diagnostics)
			}
		})
	}
}

func TestSchemaReportsPhysicalAutoRandomMissingFromModel(t *testing.T) {
	t.Parallel()

	plainSQL := `CREATE TABLE schema_check_plain (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  code VARCHAR(64) NOT NULL,
  PRIMARY KEY (id)
);`
	diagnostics := Schema[schemaCheckPlain](parseSchemaCheckCatalog(t, plainSQL))
	if got, want := diagnosticCodes(diagnostics), []string{codeAutoRandomMismatch}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaReportsWritableGeneratedColumn(t *testing.T) {
	t.Parallel()

	sql := strings.Replace(compatibleSchemaSQL, "code VARCHAR(64) NOT NULL", "code VARCHAR(64) NOT NULL GENERATED ALWAYS AS ('fixed') STORED", 1)
	diagnostics := Schema[schemaCheckParent](parseSchemaCheckCatalog(t, sql))
	if got, want := diagnosticCodes(diagnostics), []string{codeGeneratedColumnWritable}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaRequiresPhysicalUniquenessForToOneTarget(t *testing.T) {
	t.Parallel()

	sql := strings.Replace(compatibleSchemaSQL, "UNIQUE KEY schema_check_children_parent_key", "KEY schema_check_children_parent_key", 1)
	diagnostics := Schema[schemaCheckParent](parseSchemaCheckCatalog(t, sql))
	if got, want := diagnosticCodes(diagnostics), []string{codeRelationTargetNotUnique}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	if diagnostics[0].Severity != SeverityError || diagnostics[0].Suppressible {
		t.Fatalf("to-one diagnostic = %#v", diagnostics[0])
	}
}

func TestSchemaReturnsNoDiagnosticsForCompatibleCollectionRelations(t *testing.T) {
	t.Parallel()

	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, compatibleRelationSchemaSQL))
	if diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want non-nil empty", diagnostics)
	}
}

func TestSchemaReportsMissingCollectionRelationTables(t *testing.T) {
	t.Parallel()

	sqlText := compatibleRelationSchemaSQL[:strings.Index(compatibleRelationSchemaSQL, "CREATE TABLE schema_check_relation_children")]
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	want := []string{codeMissingPhysicalTable, codeMissingPhysicalTable, codeMissingPhysicalTable}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaReportsMissingCollectionTargetAndJunctionColumns(t *testing.T) {
	t.Parallel()

	sqlText := strings.Replace(
		compatibleRelationSchemaSQL,
		"  parent_id BIGINT NOT NULL,\n  PRIMARY KEY (id),\n  KEY schema_check_relation_children_parent (parent_id, parent_tenant_id)",
		"  PRIMARY KEY (id),\n  KEY schema_check_relation_children_parent (parent_tenant_id)",
		1,
	)
	sqlText = strings.Replace(
		sqlText,
		"  role_id BIGINT NOT NULL,\n  created_at",
		"  created_at",
		1,
	)
	sqlText = strings.Replace(
		sqlText,
		"(parent_tenant_id, parent_id, role_tenant_id, role_id)",
		"(parent_tenant_id, parent_id, role_tenant_id)",
		1,
	)
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	want := []string{codeMissingPhysicalColumn, codeMissingPhysicalColumn}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaReportsCollectionRelationTypeIncompatibilities(t *testing.T) {
	t.Parallel()

	sqlText := strings.Replace(
		compatibleRelationSchemaSQL,
		"  parent_id BIGINT NOT NULL,\n  PRIMARY KEY (id)",
		"  parent_id VARCHAR(64) NOT NULL,\n  PRIMARY KEY (id)",
		1,
	)
	sqlText = strings.Replace(sqlText, "  role_id BIGINT NOT NULL,\n  created_at", "  role_id VARCHAR(64) NOT NULL,\n  created_at", 1)
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	want := []string{codeIncompatibleColumnType, codeIncompatibleColumnType}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
			t.Fatalf("diagnostic location = %#v", diagnostic)
		}
	}
}

func TestSchemaRequiresUniqueManyToManyTargetIdentity(t *testing.T) {
	t.Parallel()

	sqlText := strings.Replace(
		compatibleRelationSchemaSQL,
		"  name VARCHAR(64) NOT NULL,\n  PRIMARY KEY (tenant_id, id)",
		"  name VARCHAR(64) NOT NULL,\n  KEY schema_check_relation_targets_identity (tenant_id, id)",
		1,
	)
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	if got, want := diagnosticCodes(diagnostics), []string{codeRelationTargetNotUnique}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	if diagnostics[0].Severity != SeverityError || diagnostics[0].Suppressible {
		t.Fatalf("diagnostic = %#v", diagnostics[0])
	}
}

func TestSchemaRequiresExactUniqueJunctionPair(t *testing.T) {
	t.Parallel()

	for name, sqlText := range map[string]string{
		"ordinary": strings.Replace(compatibleRelationSchemaSQL, "UNIQUE KEY schema_check_relation_links_pair", "KEY schema_check_relation_links_pair", 1),
		"superset": strings.Replace(
			strings.Replace(compatibleRelationSchemaSQL, "  created_at", "  note VARCHAR(64) NULL,\n  created_at", 1),
			"(parent_tenant_id, parent_id, role_tenant_id, role_id)",
			"(parent_tenant_id, parent_id, role_tenant_id, role_id, note)",
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
			if got, want := diagnosticCodes(diagnostics), []string{codeJunctionPairNotUnique}; !reflect.DeepEqual(got, want) {
				t.Fatalf("codes = %#v, want %#v", got, want)
			}
			if diagnostics[0].Severity != SeverityError || diagnostics[0].Suppressible {
				t.Fatalf("diagnostic = %#v", diagnostics[0])
			}
		})
	}
}

func TestSchemaReportsRequiredUnmappedJunctionColumn(t *testing.T) {
	t.Parallel()

	sqlText := strings.Replace(compatibleRelationSchemaSQL, "  created_at", "  payload VARCHAR(64) NOT NULL,\n  created_at", 1)
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	if got, want := diagnosticCodes(diagnostics), []string{codeRequiredJunctionColumn}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	if diagnostics[0].Severity != SeverityError || diagnostics[0].Suppressible || diagnostics[0].Location.Line == 0 {
		t.Fatalf("diagnostic = %#v", diagnostics[0])
	}
}

func TestSchemaWarnsWhenCollectionRelationHasNoSourcePrefixIndex(t *testing.T) {
	t.Parallel()

	sqlText := strings.Replace(
		compatibleRelationSchemaSQL,
		"  PRIMARY KEY (id),\n  KEY schema_check_relation_children_parent (parent_id, parent_tenant_id)",
		"  PRIMARY KEY (id)",
		1,
	)
	sqlText = strings.Replace(
		sqlText,
		"(parent_tenant_id, parent_id, role_tenant_id, role_id)",
		"(role_tenant_id, role_id, parent_tenant_id, parent_id)",
		1,
	)
	diagnostics := Schema[schemaCheckRelationParent](parseSchemaCheckCatalog(t, sqlText))
	want := []string{codeMissingRelationIndex, codeMissingRelationIndex}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != SeverityWarning || !diagnostic.Suppressible || diagnostic.Reference != relationIndexReference {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
		if diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
			t.Fatalf("diagnostic location = %#v", diagnostic)
		}
	}
}

func TestSchemaIndexProofDistinguishesInvisibleAndPartialIndexes(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, `CREATE TABLE schema_index_capabilities (
  id BIGINT NOT NULL,
  code VARCHAR(32) NOT NULL,
  status VARCHAR(32) NOT NULL,
  UNIQUE KEY code_unique (code) /*!80000 INVISIBLE */,
  UNIQUE KEY status_partial_unique (status) WHERE status = 'ready',
  KEY id_invisible (id) /*!80000 INVISIBLE */,
  KEY id_partial (id) WHERE status = 'ready'
);`)
	table, _ := catalog.Table("schema_index_capabilities")
	if !tableHasUniqueKey(table, []string{"code"}) {
		t.Fatal("invisible unique index did not prove unconditional uniqueness")
	}
	if tableHasUniqueKey(table, []string{"status"}) {
		t.Fatal("partial unique index unexpectedly proved unconditional uniqueness")
	}
	if tableHasIndexPrefix(table, []string{"id"}) {
		t.Fatal("invisible or partial index unexpectedly proved default lookup coverage")
	}
}

func TestSchemaReportsMissingToOneTargetTableOrColumn(t *testing.T) {
	t.Parallel()

	withoutTarget := compatibleSchemaSQL[:strings.Index(compatibleSchemaSQL, "CREATE TABLE schema_check_children")]
	diagnostics := Schema[schemaCheckParent](parseSchemaCheckCatalog(t, withoutTarget))
	if got, want := diagnosticCodes(diagnostics), []string{codeMissingPhysicalTable}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-target codes = %#v, want %#v", got, want)
	}

	withoutTargetColumn := strings.Replace(compatibleSchemaSQL, "  parent_id BIGINT NOT NULL,\n", "", 1)
	withoutTargetColumn = strings.Replace(withoutTargetColumn, "  PRIMARY KEY (id),\n  UNIQUE KEY schema_check_children_parent_key (parent_id)\n", "  PRIMARY KEY (id)\n", 1)
	diagnostics = Schema[schemaCheckParent](parseSchemaCheckCatalog(t, withoutTargetColumn))
	if got, want := diagnosticCodes(diagnostics), []string{codeMissingPhysicalColumn}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-target-column codes = %#v, want %#v", got, want)
	}
}

func TestSchemaDoesNotInferCompatibilityForCustomOrUnknownTypes(t *testing.T) {
	t.Parallel()

	type customTypeSchemaCheck struct {
		model.Meta `tidbgo:"table=custom_type_schema_checks"`
		Value      scannerOnlyCheckedValue
	}
	catalog := parseSchemaCheckCatalog(t, "CREATE TABLE custom_type_schema_checks (value VECTOR(3) NOT NULL);")
	diagnostics := Schema[customTypeSchemaCheck](catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestSchemaDoesNotInferCompatibilityForNamedNativeCustomRepresentations(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, `CREATE TABLE schema_check_native_custom_values (
  date DATE NULL,
  optional DATE NOT NULL,
  encoded JSON NULL
);`)
	diagnostics := Schema[schemaCheckNativeCustomValues](catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestSchemaStillChecksNamedNativeTypesWithoutCustomRepresentations(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, `CREATE TABLE schema_check_plain_named_values (
  value DATE NULL
);`)
	diagnostics := Schema[schemaCheckPlainNamedValue](catalog)
	if got, want := diagnosticCodes(diagnostics), []string{codeIncompatibleColumnType, codeNullableDatabaseColumn}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
}

func TestSchemaDoesNotInferRelationTypesForNamedNativeCustomRepresentations(t *testing.T) {
	t.Parallel()

	catalog := parseSchemaCheckCatalog(t, `CREATE TABLE schema_check_custom_relation_parents (
  id BIGINT NOT NULL,
  PRIMARY KEY (id)
);
CREATE TABLE schema_check_custom_relation_targets (
  id BIGINT NOT NULL,
  PRIMARY KEY (id)
);
CREATE TABLE schema_check_custom_relation_links (
  parent_id BIGINT NOT NULL,
  target_id BIGINT NOT NULL,
  UNIQUE KEY schema_check_custom_relation_pair (parent_id, target_id)
);`)
	diagnostics := Schema[schemaCheckCustomRelationParent](catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestSchemaRecognizesValueSoftDeleteAsNullable(t *testing.T) {
	t.Parallel()

	sqlText := `CREATE TABLE schema_check_soft_delete (
  id BIGINT NOT NULL,
  deleted_at DATETIME NULL,
  PRIMARY KEY (id)
);`
	diagnostics := Schema[schemaCheckSoftDelete](parseSchemaCheckCatalog(t, sqlText))
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func BenchmarkSchema(b *testing.B) {
	catalog, err := physicalschema.Parse(compatibleSchemaSQL)
	if err != nil {
		b.Fatal(err)
	}
	if diagnostics := Schema[schemaCheckParent](catalog); len(diagnostics) != 0 {
		b.Fatalf("setup diagnostics = %#v", diagnostics)
	}
	b.ResetTimer()
	b.ReportAllocs()
	var diagnostics []Diagnostic
	for b.Loop() {
		diagnostics = Schema[schemaCheckParent](catalog)
	}
	schemaDiagnosticSink = diagnostics
}

func BenchmarkSchemaCollectionRelations(b *testing.B) {
	catalog, err := physicalschema.Parse(compatibleRelationSchemaSQL)
	if err != nil {
		b.Fatal(err)
	}
	if diagnostics := Schema[schemaCheckRelationParent](catalog); len(diagnostics) != 0 {
		b.Fatalf("setup diagnostics = %#v", diagnostics)
	}
	b.ResetTimer()
	b.ReportAllocs()
	var diagnostics []Diagnostic
	for b.Loop() {
		diagnostics = Schema[schemaCheckRelationParent](catalog)
	}
	schemaDiagnosticSink = diagnostics
}

func parseSchemaCheckCatalog(t *testing.T, sql string) *physicalschema.Catalog {
	t.Helper()
	catalog, err := physicalschema.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

var schemaDiagnosticSink []Diagnostic
