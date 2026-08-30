package orm

import (
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/model"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

type queryIndexModel struct {
	model.Meta `tidbgo:"table=query_index_items"`
	ID         int64 `tidbgo:",pk"`
	TenantID   int64
	Status     string
	Title      string
}

func TestSelectQueryDiagnosticsWithSchemaMatchesRootIndexPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		query  *SelectQuery[queryIndexModel]
		schema string
	}{
		{
			name: "composite equality and order",
			query: Query[queryIndexModel]().
				Where(Equal("TenantID", int64(7))).
				OrderBy(Desc("ID")).
				Limit(20),
			schema: queryIndexSchema("KEY tenant_id_id_key (tenant_id, id)"),
		},
		{
			name: "equality prefix order is interchangeable",
			query: Query[queryIndexModel]().
				Where(Equal("TenantID", int64(7)), Equal("Status", "ready")).
				OrderBy(Desc("ID")).
				Limit(20),
			schema: queryIndexSchema("KEY status_tenant_id_key (status, tenant_id, id)"),
		},
		{
			name: "primary key order",
			query: Query[queryIndexModel]().
				OrderBy(Desc("ID")).
				Limit(20),
			schema: queryIndexSchema("KEY tenant_id_key (tenant_id)"),
		},
		{
			name: "longer index",
			query: Query[queryIndexModel]().
				Where(Equal("TenantID", int64(7))).
				OrderBy(Asc("ID")).
				Limit(20),
			schema: queryIndexSchema("KEY tenant_id_id_title_key (tenant_id, id, title)"),
		},
		{
			name: "fully constrained unique key",
			query: Query[queryIndexModel]().
				Where(Equal("ID", int64(7))).
				OrderBy(Desc("Title")).
				Limit(20),
			schema: queryIndexSchema("KEY tenant_id_key (tenant_id)"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := parseQueryIndexCatalog(t, test.schema)
			if diagnostics := test.query.DiagnosticsWithSchema(catalog); len(diagnostics) != 0 {
				t.Fatalf("DiagnosticsWithSchema() = %#v, want none", diagnostics)
			}
		})
	}
}

func TestSelectQueryDiagnosticsWithSchemaReportsMissingRootIndexPrefix(t *testing.T) {
	t.Parallel()

	catalog := parseQueryIndexCatalog(t, queryIndexSchema("KEY tenant_id_key (tenant_id)"))
	diagnostics := Query[queryIndexModel]().
		Select("ID", "Title").
		Where(Equal("TenantID", "private-tenant")).
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(catalog)
	if len(diagnostics) != 1 {
		t.Fatalf("DiagnosticsWithSchema() = %#v, want one", diagnostics)
	}
	diagnostic := diagnostics[0]
	if diagnostic.Code != codeMissingIndexPrefix || diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible {
		t.Fatalf("diagnostic = %#v, want suppressible QRY007 warning", diagnostic)
	}
	if !strings.Contains(diagnostic.Message, "query_index_items") || !strings.Contains(diagnostic.Message, "tenant_id, id") {
		t.Fatalf("message = %q", diagnostic.Message)
	}
	if strings.Contains(diagnostic.Message, "private-tenant") || strings.Contains(queryDiagnosticEvidence(diagnostic), "private-tenant") {
		t.Fatalf("diagnostic exposed bind value: %#v", diagnostic)
	}
	if fingerprint := queryDiagnosticFingerprint(diagnostic); !strings.HasPrefix(fingerprint, "q1:") || len(fingerprint) != len("q1:")+64 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
}

func TestSelectQueryDiagnosticsWithSchemaRejectsPrefixLengthCoverage(t *testing.T) {
	t.Parallel()

	catalog := parseQueryIndexCatalog(t, queryIndexSchema("KEY tenant_title_prefix_key (tenant_id, title(10))"))
	diagnostics := Query[queryIndexModel]().
		Where(Equal("TenantID", int64(7))).
		OrderBy(Desc("Title")).
		Limit(20).
		DiagnosticsWithSchema(catalog)
	if len(diagnostics) != 1 || diagnostics[0].Code != codeMissingIndexPrefix {
		t.Fatalf("DiagnosticsWithSchema() = %#v, want QRY007", diagnostics)
	}
}

func TestSelectQueryDiagnosticsWithSchemaRejectsIndexesUnavailableForDefaultLookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query *SelectQuery[queryIndexModel]
		index string
	}{
		{
			name: "invisible",
			query: Query[queryIndexModel]().
				Where(Equal("TenantID", int64(7))).
				OrderBy(Desc("ID")).
				Limit(20),
			index: "KEY tenant_id_id_key (tenant_id, id) INVISIBLE",
		},
		{
			name: "partial",
			query: Query[queryIndexModel]().
				Where(Equal("TenantID", int64(7))).
				OrderBy(Desc("ID")).
				Limit(20),
			index: "KEY tenant_id_id_key (tenant_id, id) WHERE status = 'ready'",
		},
		{
			name: "fulltext",
			query: Query[queryIndexModel]().
				OrderBy(Desc("Title")).
				Limit(20),
			index: "FULLTEXT KEY title_key (title)",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			diagnostics := test.query.DiagnosticsWithSchema(parseQueryIndexCatalog(t, queryIndexSchema(test.index)))
			if len(diagnostics) != 1 || diagnostics[0].Code != codeMissingIndexPrefix {
				t.Fatalf("DiagnosticsWithSchema() = %#v, want QRY007", diagnostics)
			}
		})
	}
}

func TestSelectQueryDiagnosticsWithSchemaChecksRelationTopNAssociationIndex(t *testing.T) {
	t.Parallel()

	matching := parseQueryIndexCatalog(t, `CREATE TABLE relation_topn_video_genres (
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  PRIMARY KEY (video_id, genre_id),
  KEY genre_video_key (genre_id, video_id)
);`)
	if diagnostics := relationTopNBenchmarkQuery().DiagnosticsWithSchema(matching); len(diagnostics) != 0 {
		t.Fatalf("matching DiagnosticsWithSchema() = %#v, want none", diagnostics)
	}

	missing := parseQueryIndexCatalog(t, `CREATE TABLE relation_topn_video_genres (
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  PRIMARY KEY (video_id, genre_id)
);`)
	diagnostics := relationTopNBenchmarkQuery().DiagnosticsWithSchema(missing)
	if len(diagnostics) != 1 || diagnostics[0].Code != codeMissingIndexPrefix {
		t.Fatalf("missing DiagnosticsWithSchema() = %#v, want QRY007", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "relation-first TopN for relationTopNVideo.VideoGenres") ||
		!strings.Contains(queryDiagnosticEvidence(diagnostics[0]), "relation_topn_video_genres(genre_id, video_id)") {
		t.Fatalf("diagnostic = %#v", diagnostics[0])
	}
}

func TestSelectQueryDiagnosticsWithSchemaIncludesDefaultSoftDeleteFilter(t *testing.T) {
	t.Parallel()

	root := parseQueryIndexCatalog(t, `CREATE TABLE relation_topn_soft_videos (
  id BIGINT NOT NULL PRIMARY KEY,
  deleted_at DATETIME NULL,
  KEY deleted_id_key (deleted_at, id)
);`)
	if diagnostics := Query[relationTopNSoftVideo]().
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(root); len(diagnostics) != 0 {
		t.Fatalf("root soft-delete diagnostics = %#v, want none", diagnostics)
	}

	relation := parseQueryIndexCatalog(t, `CREATE TABLE relation_topn_soft_links (
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  deleted_at DATETIME NULL,
  PRIMARY KEY (video_id, genre_id),
  KEY genre_deleted_video_key (genre_id, deleted_at, video_id)
);`)
	diagnostics := Query[relationTopNSoftVideo]().
		WithDeleted().
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(relation)
	if len(diagnostics) != 0 {
		t.Fatalf("relation soft-delete diagnostics = %#v, want none", diagnostics)
	}
}

func TestSelectQueryDiagnosticsWithSchemaReportsUnavailableSchemaInput(t *testing.T) {
	t.Parallel()

	query := Query[queryIndexModel]().OrderBy(Desc("ID")).Limit(20)
	tests := []struct {
		name    string
		catalog *physicalschema.Catalog
		message string
	}{
		{name: "nil catalog", message: "non-nil catalog"},
		{name: "missing table", catalog: parseQueryIndexCatalog(t, "CREATE TABLE other_items (id BIGINT PRIMARY KEY);"), message: "absent"},
		{name: "missing column", catalog: parseQueryIndexCatalog(t, "CREATE TABLE query_index_items (tenant_id BIGINT, KEY tenant_id_key (tenant_id));"), message: "columns (id)"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			diagnostics := query.DiagnosticsWithSchema(test.catalog)
			if len(diagnostics) != 1 || diagnostics[0].Code != codeIndexCheckUnavailable || diagnostics[0].Severity != check.SeverityError || diagnostics[0].Suppressible {
				t.Fatalf("DiagnosticsWithSchema() = %#v, want non-suppressible QRY006", diagnostics)
			}
			if !strings.Contains(diagnostics[0].Message, test.message) {
				t.Fatalf("message = %q, want substring %q", diagnostics[0].Message, test.message)
			}
		})
	}
}

func TestSelectQueryDiagnosticsWithSchemaSkipsAmbiguousIndexShapes(t *testing.T) {
	t.Parallel()

	catalog := parseQueryIndexCatalog(t, queryIndexSchema("KEY tenant_id_key (tenant_id)"))
	tests := map[string]*SelectQuery[queryIndexModel]{
		"range predicate": Query[queryIndexModel]().
			Where(GreaterThan("TenantID", int64(7))).
			OrderBy(Desc("ID")).
			Limit(20),
		"null predicate": Query[queryIndexModel]().
			Where(IsNull("Status")).
			OrderBy(Desc("ID")).
			Limit(20),
		"mixed order": Query[queryIndexModel]().
			Where(Equal("TenantID", int64(7))).
			OrderBy(Asc("Status"), Desc("ID")).
			Limit(20),
		"zero limit": Query[queryIndexModel]().
			Where(Equal("TenantID", int64(7))).
			OrderBy(Desc("ID")).
			Limit(0),
	}
	for name, query := range tests {
		name, query := name, query
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if diagnostics := query.DiagnosticsWithSchema(catalog); len(diagnostics) != 0 {
				t.Fatalf("DiagnosticsWithSchema() = %#v, want none", diagnostics)
			}
		})
	}
}

func TestSelectQueryFingerprintExcludesBindAndPaginationValues(t *testing.T) {
	t.Parallel()

	catalog := parseQueryIndexCatalog(t, queryIndexSchema("KEY tenant_id_key (tenant_id)"))
	first := Query[queryIndexModel]().
		Select("ID").
		Where(Equal("TenantID", "private-first")).
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(catalog)
	second := Query[queryIndexModel]().
		Select("ID").
		Where(Equal("TenantID", "private-second")).
		OrderBy(Desc("ID")).
		Limit(200).
		DiagnosticsWithSchema(catalog)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("diagnostics = %#v, %#v", first, second)
	}
	if got, want := queryDiagnosticFingerprint(first[0]), queryDiagnosticFingerprint(second[0]); got != want {
		t.Fatalf("fingerprints = %q, %q, want equal", got, want)
	}

	differentProjection := Query[queryIndexModel]().
		Select("ID", "Title").
		Where(Equal("TenantID", "private-third")).
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(catalog)
	if got, notWant := queryDiagnosticFingerprint(differentProjection[0]), queryDiagnosticFingerprint(first[0]); got == notWant {
		t.Fatalf("fingerprint = %q, want projection-sensitive value", got)
	}
}

func TestSelectQueryDiagnosticsWithSchemaReturnsBuildFailureOnly(t *testing.T) {
	t.Parallel()

	diagnostics := Query[queryIndexModel]().Select("Missing").DiagnosticsWithSchema(nil)
	if len(diagnostics) != 1 || diagnostics[0].Code != codeInvalidQuery {
		t.Fatalf("DiagnosticsWithSchema() = %#v, want QRY001 only", diagnostics)
	}
}

func TestSelectQueryDiagnosticsWithSchemaDoesNotExecuteDriverValuer(t *testing.T) {
	t.Parallel()

	catalog := parseQueryIndexCatalog(t, `CREATE TABLE valuer_predicate_model (
  id BIGINT NOT NULL PRIMARY KEY,
  value DECIMAL(20, 2) NOT NULL,
  KEY value_id_key (value, id)
);`)
	calls := 0
	value := observedValuer{calls: &calls, text: "private-value"}
	diagnostics := Query[valuerPredicateModel]().
		Select("ID").
		Where(Equal("Value", value)).
		OrderBy(Desc("ID")).
		Limit(20).
		DiagnosticsWithSchema(catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("DiagnosticsWithSchema() = %#v, want none", diagnostics)
	}
	if calls != 0 {
		t.Fatalf("driver.Valuer calls = %d, want 0", calls)
	}
}

func BenchmarkSelectQueryDiagnosticsWithSchema(b *testing.B) {
	query, catalog := querySchemaDiagnosticBenchmarkFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		queryDiagnosticSink = query.DiagnosticsWithSchema(catalog)
	}
}

func BenchmarkSelectQueryDiagnosticsSchemaComparison(b *testing.B) {
	query, catalog := querySchemaDiagnosticBenchmarkFixture(b)
	b.Run("BuilderOnly", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			queryDiagnosticSink = query.Diagnostics()
		}
	})
	b.Run("WithSchema", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			queryDiagnosticSink = query.DiagnosticsWithSchema(catalog)
		}
	})
}

func querySchemaDiagnosticBenchmarkFixture(b *testing.B) (*SelectQuery[queryIndexModel], *physicalschema.Catalog) {
	b.Helper()
	catalog, err := physicalschema.Parse(queryIndexSchema("KEY tenant_id_id_key (tenant_id, id)"))
	if err != nil {
		b.Fatal(err)
	}
	query := Query[queryIndexModel]().
		Select("ID", "Title").
		Where(Equal("TenantID", int64(7))).
		OrderBy(Desc("ID")).
		Limit(20)
	return query, catalog
}

func queryIndexSchema(index string) string {
	return `CREATE TABLE query_index_items (
  id BIGINT NOT NULL PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  status VARCHAR(32) NOT NULL,
  title VARCHAR(255) NOT NULL,
  ` + index + `
);`
}

func parseQueryIndexCatalog(t testing.TB, sqlText string) *physicalschema.Catalog {
	t.Helper()
	catalog, err := physicalschema.Parse(sqlText)
	if err != nil {
		t.Fatalf("schema.Parse() error = %v", err)
	}
	return catalog
}

func queryDiagnosticEvidence(diagnostic check.Diagnostic) string {
	var result strings.Builder
	for _, evidence := range diagnostic.Evidence {
		result.WriteString(evidence.Message)
		result.WriteByte('\n')
	}
	return result.String()
}

func queryDiagnosticFingerprint(diagnostic check.Diagnostic) string {
	for _, evidence := range diagnostic.Evidence {
		const prefix = "Query fingerprint: "
		if strings.HasPrefix(evidence.Message, prefix) {
			return strings.TrimPrefix(evidence.Message, prefix)
		}
	}
	return ""
}
