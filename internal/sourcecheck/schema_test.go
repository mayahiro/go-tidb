package sourcecheck

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/model"
)

type lintSchemaItem struct {
	model.Meta `tidbgo:"table=lint_items"`
	ID         int64  `tidbgo:",pk"`
	TenantID   int64  `tidbgo:",unique=tenant_code"`
	Code       string `tidbgo:",unique=tenant_code"`
	Label      string
}

const lintSchemaItemSource = `package repository
import (
 "github.com/mayahiro/go-tidb/model"
 "github.com/mayahiro/go-tidb/orm"
)
type lintSchemaItem struct {
 model.Meta ` + "`tidbgo:\"table=lint_items\"`" + `
 ID int64 ` + "`tidbgo:\",pk\"`" + `
 TenantID int64 ` + "`tidbgo:\",unique=tenant_code\"`" + `
 Code string ` + "`tidbgo:\",unique=tenant_code\"`" + `
 Label string
}
func build() {
 q := orm.Query[lintSchemaItem]().Select("ID")
 _, _, _ = q.Build()
 _, _, _ = q.Build()
}
func init() { panic("source lint must not execute application initialization") }
`

const lintSchemaItemSQL = `CREATE TABLE lint_items (
 id BIGINT NOT NULL PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 code VARCHAR(64) NOT NULL,
 label VARCHAR(255) NOT NULL,
 UNIQUE KEY tenant_code (tenant_id, code)
);`

func TestSourceSchemaMigrationCompatibilityMatchesCheckSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sql string
		codes     []string
	}{
		{name: "matching", sql: lintSchemaItemSQL},
		{name: "table removed", sql: strings.ReplaceAll(lintSchemaItemSQL, "lint_items", "other_items"), codes: []string{"CMP002"}},
		{name: "unselected column removed", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,\n", ""), codes: []string{"CMP003"}},
		{name: "unique constraint removed", sql: strings.ReplaceAll(lintSchemaItemSQL, "UNIQUE KEY", "KEY"), codes: []string{"CMP015"}},
		{name: "primary key changed", sql: strings.ReplaceAll(lintSchemaItemSQL, "PRIMARY KEY", "UNIQUE"), codes: []string{"CMP007"}},
		{name: "required column added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n region VARCHAR(32) NOT NULL,"), codes: []string{"CMP010"}},
		{name: "two required columns added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n region VARCHAR(32) NOT NULL, category VARCHAR(32) NOT NULL,"), codes: []string{"CMP010", "CMP010"}},
		{name: "nullable column added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n extra VARCHAR(32),")},
		{name: "defaulted column added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n extra VARCHAR(32) NOT NULL DEFAULT '',")},
		{name: "generated column added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n extra BIGINT AS (id + 1) STORED NOT NULL,")},
		{name: "auto increment column added", sql: strings.ReplaceAll(lintSchemaItemSQL, " label VARCHAR(255) NOT NULL,", " label VARCHAR(255) NOT NULL,\n extra BIGINT NOT NULL AUTO_INCREMENT UNIQUE,")},
		{name: "unique subset", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(code)")},
		{name: "unique reordered", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(code, tenant_id)")},
		{name: "unique name differs", sql: strings.ReplaceAll(lintSchemaItemSQL, "KEY tenant_code", "KEY physical_name")},
		{name: "invisible unique", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(tenant_id, code) INVISIBLE")},
		{name: "unique extra column", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(tenant_id, code, label)"), codes: []string{"CMP015"}},
		{name: "prefix unique", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(tenant_id, code(4))"), codes: []string{"CMP015"}},
		{name: "expression unique", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(tenant_id, (LOWER(code)))"), codes: []string{"CMP015"}},
		{name: "partial unique", sql: strings.ReplaceAll(lintSchemaItemSQL, "(tenant_id, code)", "(tenant_id, code) WHERE tenant_id > 0"), codes: []string{"CMP015"}},
		{name: "identifier case", sql: strings.ToUpper(lintSchemaItemSQL)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := parseSourceSchema(t, tc.sql)
			analysis := analyzeSourceWithOptions(t, lintSchemaItemSource, WithSchema(catalog))
			codes := sourceDiagnosticCodes(analysis)
			slices.Sort(codes)
			if !slices.Equal(codes, tc.codes) {
				t.Fatalf("codes = %v, want %v: %+v", codes, tc.codes, analysis.Diagnostics)
			}
			reflected := check.Schema[lintSchemaItem](catalog)
			if !reflect.DeepEqual(schemaDiagnosticSignatures(analysis.Diagnostics), schemaDiagnosticSignatures(reflected)) {
				t.Fatalf("source/reflection diagnostic semantics differ: %+v / %+v", analysis.Diagnostics, reflected)
			}
			statistics := analysis.Statistics
			if statistics.SchemaModels != 1 || statistics.AnalyzedSchemaModels != 1 || statistics.UncertainSchemaModels != 0 {
				t.Fatalf("one model used twice must be checked once: %+v", statistics)
			}
			for _, diagnostic := range analysis.Diagnostics {
				if diagnostic.Location.Path != "query.go" || diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
					t.Fatalf("missing Go declaration location: %+v", diagnostic)
				}
				if diagnostic.Code != "CMP002" && (len(diagnostic.Evidence) != 1 || diagnostic.Evidence[0].Location.Line == 0) {
					t.Fatalf("missing SQL snapshot evidence: %+v", diagnostic)
				}
			}
			if again := analyzeSourceWithOptions(t, lintSchemaItemSource, WithSchema(catalog)); !reflect.DeepEqual(analysis, again) {
				t.Fatal("analysis must be deterministic")
			}
		})
	}
}

func schemaDiagnosticSignatures(diagnostics []check.Diagnostic) []string {
	var result []string
	for _, diagnostic := range diagnostics {
		result = append(result, diagnostic.Code+"/"+string(diagnostic.Severity)+"/"+diagnostic.Message)
	}
	slices.Sort(result)
	return result
}

func TestSourceSchemaModelSelectionAndBoundary(t *testing.T) {
	t.Parallel()
	const imports = `package repository
import (
 "github.com/mayahiro/go-tidb/model"
 "github.com/mayahiro/go-tidb/orm"
 external "example.test/external"
 "database/sql"
)
`
	catalog := parseSourceSchema(t, `CREATE TABLE item (id BIGINT PRIMARY KEY, name VARCHAR(64)); CREATE TABLE child (item_id BIGINT, KEY item_lookup (item_id));`)
	for _, tc := range []struct {
		name, body string
		analyzed   int
		uncertain  int
		codes      []string
	}{
		{name: "explicit model without queries", body: "type Item struct { model.Meta; ID int64; Name string }", analyzed: 1},
		{name: "unrelated DTO", body: "type DTO struct { ID int64; Missing string }"},
		{name: "raw result DTO", body: `type DTO struct { ID int64; Missing string }; func rows() { _, _ = orm.Raw[DTO]("SELECT 1").All(ctx, db) }`},
		{name: "ordinary model", body: `type Item struct { ID int64; Missing string }; func rows() { _, _, _ = orm.Query[Item]().Build() }`, analyzed: 1, codes: []string{"CMP003"}},
		{name: "count only", body: `type Item struct { ID int64 }; func rows() { _, _ = orm.Query[Item]().Limit(5).Count(ctx, db) }`, analyzed: 1},
		{name: "exists only", body: `type Item struct { ID int64 }; func rows() { _, _ = orm.Query[Item]().Exists(ctx, db) }`, analyzed: 1},
		{name: "aggregate source only", body: `type Item struct { ID int64 }; type Output struct { Total int64 }; func rows() { _, _, _ = orm.Aggregate[Item]().Select(orm.CountAll().As("Total")).Build() }`, analyzed: 1},
		{name: "scan destination excluded", body: `type Item struct { ID int64 }; type DTO struct { Missing string }; func rows() { var rows []DTO; _ = orm.Query[Item]().ScanAll(ctx, db, &rows) }`, analyzed: 1},
		{name: "custom field representation", body: `type Item struct { model.Meta; ID external.CustomID; Name sql.NullString }`, analyzed: 1},
		{name: "ignored and computed", body: "type Item struct { model.Meta; ID int64; Ignored map[string]bool `tidbgo:\"-\"`; Calculated string `tidbgo:\",computed\"`; hidden map[string]int }", analyzed: 1},
		{name: "relation field excluded", body: "type Item struct { model.Meta; ID int64; Children []Child `tidbgo:\"has_many,join=ID:ItemID\"` }; type Child struct { ItemID int64 }", analyzed: 2},
		{name: "embedded model", body: "type Base struct { ID int64 }; type Item struct { model.Meta; Base; Name string }", uncertain: 1, codes: []string{"SRC002"}},
		{name: "invalid tag", body: "type Item struct { model.Meta; ID int64 `tidbgo:\",unknown\"` }", uncertain: 1, codes: []string{"SRC002"}},
		{name: "invalid table", body: "type Item struct { model.Meta `tidbgo:\"table=invalid-name\"`; ID int64 }", uncertain: 1, codes: []string{"SRC002"}},
		{name: "case duplicate column", body: "type Item struct { model.Meta; ID int64; Other int64 `tidbgo:\"ID\"` }", uncertain: 1, codes: []string{"SRC002"}},
		{name: "generic declaration", body: "type Item[T any] struct { model.Meta; ID int64; Name T }", uncertain: 1, codes: []string{"SRC002"}},
		{name: "alias use", body: `type Item struct { ID int64 }; type Alias = Item; func rows() { _, _, _ = orm.Query[Alias]().Build() }`, uncertain: 1, codes: []string{"SRC002"}},
		{name: "external source missing", body: `func rows() { _, _, _ = orm.Query[external.Item]().Build() }`, uncertain: 1, codes: []string{"SRC002"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis := analyzeSourceWithOptions(t, imports+tc.body, WithSchema(catalog))
			if !slices.Equal(sourceDiagnosticCodes(analysis), tc.codes) {
				t.Fatalf("diagnostics = %+v, want %v", analysis.Diagnostics, tc.codes)
			}
			statistics := analysis.Statistics
			if statistics.AnalyzedSchemaModels != tc.analyzed || statistics.UncertainSchemaModels != tc.uncertain || statistics.SchemaModels != tc.analyzed+tc.uncertain {
				t.Fatalf("schema coverage = %+v", statistics)
			}
			for _, diagnostic := range analysis.Diagnostics {
				if diagnostic.Code == "SRC002" && (diagnostic.Severity != check.SeverityInfo || !diagnostic.Suppressible || diagnostic.Location.Path != "query.go") {
					t.Fatalf("uncertainty must be explicit informational coverage: %+v", diagnostic)
				}
			}
			offline := analyzeSource(t, imports+tc.body)
			if offline.Statistics.SchemaModels != 0 || len(offline.Diagnostics) != 0 {
				t.Fatalf("schema checks must be opt-in: %+v", offline)
			}
		})
	}
}

func TestSourceSchemaCompositePrimaryOrder(t *testing.T) {
	const source = "package repository\nimport \"github.com/mayahiro/go-tidb/model\"\ntype Item struct { model.Meta; OwnerID int64 `tidbgo:\",pk\"`; ID int64 `tidbgo:\",pk\"` }"
	for _, tc := range []struct {
		key  string
		want []string
	}{
		{key: "owner_id, id"},
		{key: "id, owner_id", want: []string{"CMP007"}},
	} {
		catalog := parseSourceSchema(t, "CREATE TABLE item (owner_id BIGINT NOT NULL, id BIGINT NOT NULL, PRIMARY KEY ("+tc.key+"));")
		analysis := analyzeSourceWithOptions(t, source, WithSchema(catalog))
		if !slices.Equal(sourceDiagnosticCodes(analysis), tc.want) {
			t.Fatalf("primary key %s: %+v", tc.key, analysis.Diagnostics)
		}
	}
}

func TestSourceSchemaModelsAcrossPackages(t *testing.T) {
	directory := t.TempDir()
	writeSourceTestFile(t, filepath.Join(directory, "go.mod"), "module example.test/application\n\ngo 1.26\n")
	writeSourceTestFile(t, filepath.Join(directory, "a", "model.go"), "package a\nimport \"github.com/mayahiro/go-tidb/model\"\ntype Item struct { model.Meta `tidbgo:\"table=a_items\"`; ID int64 }")
	writeSourceTestFile(t, filepath.Join(directory, "b", "model.go"), "package b\nimport \"github.com/mayahiro/go-tidb/model\"\ntype Item struct { model.Meta `tidbgo:\"table=b_items\"`; ID int64; Missing string }")
	writeSourceTestFile(t, filepath.Join(directory, "query.go"), `package application
import (
 "example.test/application/a"
 "example.test/application/b"
 "github.com/mayahiro/go-tidb/orm"
)
func build() { _, _, _ = orm.Query[a.Item]().Build(); _, _, _ = orm.Query[b.Item]().Build() }
`)
	catalog := parseSourceSchema(t, "CREATE TABLE a_items (id BIGINT PRIMARY KEY); CREATE TABLE b_items (id BIGINT PRIMARY KEY);")
	analysis, err := AnalyzePath(directory, WithSchema(catalog))
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Statistics.SchemaModels != 2 || analysis.Statistics.AnalyzedSchemaModels != 2 || len(analysis.Diagnostics) != 1 {
		t.Fatalf("analysis = %+v", analysis)
	}
	if diagnostic := analysis.Diagnostics[0]; diagnostic.Code != "CMP003" || diagnostic.Location.Path != "b/model.go" || !strings.Contains(diagnostic.Message, "b_items") {
		t.Fatalf("diagnostic must identify the declaring package: %+v", diagnostic)
	}
}

func TestSourceSchemaMissingCatalogIsExplicit(t *testing.T) {
	analysis := analyzeSourceWithOptions(t, lintSchemaItemSource, WithSchema(nil))
	if !slices.Equal(sourceDiagnosticCodes(analysis), []string{"CMP001"}) || analysis.Statistics.UncertainSchemaModels != 1 {
		t.Fatalf("missing catalog = %+v", analysis)
	}
}
