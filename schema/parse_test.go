package schema

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const catalogFixtureSQL = `
-- Schema-only dump wrappers are intentionally ignored.
SET @saved_sql_mode = 'STRICT_TRANS_TABLES;NO_ZERO_DATE';
DROP TABLE IF EXISTS accounts;

CREATE TABLE ` + "`application`.`accounts`" + ` (
  ` + "`id`" + ` bigint(20) NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  ` + "`email`" + ` varchar(255) NOT NULL,
  ` + "`nickname`" + ` varchar(255) DEFAULT NULL,
  ` + "`created_at`" + ` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  ` + "`search_text`" + ` text GENERATED ALWAYS AS (concat(` + "`email`" + `, ';', coalesce(` + "`nickname`" + `, ''))) STORED,
  ` + "`payload`" + ` varbinary(32) NULL,
  PRIMARY KEY (` + "`id`" + `) /*T![clustered_index] CLUSTERED */,
  UNIQUE KEY ` + "`accounts_email_key`" + ` (` + "`email`" + `),
  KEY ` + "`accounts_created_at_key`" + ` (` + "`created_at`" + ` DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 /*T! PRE_SPLIT_REGIONS=2 */;

CREATE TABLE sessions (
  id SERIAL,
  account_id BIGINT NOT NULL,
  token VARCHAR(64) UNIQUE KEY,
  state ENUM('ready,pending', 'done') NOT NULL,
  UNIQUE KEY sessions_account_token_key (account_id, token),
  KEY sessions_account_key (account_id)
);
`

func TestParseBuildsCatalogFromTiDBCreateTableSnapshot(t *testing.T) {
	t.Parallel()

	catalog, err := Parse(catalogFixtureSQL)
	if err != nil {
		t.Fatal(err)
	}
	tables := catalog.Tables()
	if got, want := len(tables), 2; got != want {
		t.Fatalf("table count = %d, want %d", got, want)
	}
	if got, want := tables[0].SchemaName(), "application"; got != want {
		t.Fatalf("schema name = %q, want %q", got, want)
	}
	if got, want := tables[0].Name(), "accounts"; got != want {
		t.Fatalf("table name = %q, want %q", got, want)
	}
	if tables[0].Position().Line == 0 || tables[0].Position().Column == 0 {
		t.Fatalf("table position = %#v", tables[0].Position())
	}

	accounts, ok := catalog.Table("ACCOUNTS")
	if !ok {
		t.Fatal("case-insensitive accounts lookup failed")
	}
	if got, want := len(accounts.Columns()), 6; got != want {
		t.Fatalf("column count = %d, want %d", got, want)
	}
	id, ok := accounts.Column("ID")
	if !ok || id.TypeName() != "BIGINT" || id.Nullable() || !id.AutoRandom() || id.AutoIncrement() || !id.DatabaseGenerated() {
		t.Fatalf("id = %#v", id)
	}
	nickname, ok := accounts.Column("nickname")
	if !ok || !nickname.Nullable() || !nickname.HasDefault() || nickname.DatabaseGenerated() {
		t.Fatalf("nickname = %#v", nickname)
	}
	createdAt, ok := accounts.Column("created_at")
	if !ok || createdAt.Nullable() || !createdAt.HasDefault() || createdAt.Generated() {
		t.Fatalf("created_at = %#v", createdAt)
	}
	searchText, ok := accounts.Column("search_text")
	if !ok || !searchText.Generated() || !searchText.DatabaseGenerated() {
		t.Fatalf("search_text = %#v", searchText)
	}
	if got, want := accounts.PrimaryKeyColumns(), []string{"id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary key = %#v, want %#v", got, want)
	}
	if !testTableHasUniqueKey(accounts, []string{"email"}) || !testTableHasUniqueKey(accounts, []string{"email", "id"}) {
		t.Fatal("email unique key was not recognized")
	}
	if testTableHasUniqueKey(accounts, []string{"nickname"}) {
		t.Fatal("nickname unexpectedly recognized as unique")
	}
}

func TestParseRecognizesInlineConstraintsAndSerial(t *testing.T) {
	t.Parallel()

	catalog, err := Parse(catalogFixtureSQL)
	if err != nil {
		t.Fatal(err)
	}
	sessions, ok := catalog.Table("sessions")
	if !ok {
		t.Fatal("sessions table not found")
	}
	id, _ := sessions.Column("id")
	if id.TypeName() != "BIGINT" || !id.Unsigned() || id.Nullable() || !id.AutoIncrement() {
		t.Fatalf("serial id = %#v", id)
	}
	token, _ := sessions.Column("token")
	if !token.Nullable() {
		t.Fatalf("token nullable = false, want true")
	}
	if !testTableHasUniqueKey(sessions, []string{"token"}) || !testTableHasUniqueKey(sessions, []string{"account_id", "token"}) {
		t.Fatalf("indexes = %#v", sessions.Indexes())
	}
	if got := sessions.PrimaryKeyColumns(); got != nil {
		t.Fatalf("primary key = %#v, want nil", got)
	}
}

func TestParseKeepsFunctionalIndexesOutOfUniqueKeyProof(t *testing.T) {
	t.Parallel()

	catalog, err := Parse("CREATE TABLE names (name VARCHAR(64), UNIQUE KEY names_lower_key ((lower(name))));")
	if err != nil {
		t.Fatal(err)
	}
	table, _ := catalog.Table("names")
	indexes := table.Indexes()
	if len(indexes) != 1 || !indexes[0].HasExpression() {
		t.Fatalf("indexes = %#v", indexes)
	}
	if testTableHasUniqueKey(table, []string{"name"}) {
		t.Fatal("functional index unexpectedly proved direct-column uniqueness")
	}
}

func TestParseKeepsPrefixIndexesOutOfSimpleColumnProof(t *testing.T) {
	t.Parallel()

	catalog, err := Parse("CREATE TABLE names (name VARCHAR(64), created_at DATETIME, UNIQUE KEY names_prefix_key (name(10)), KEY names_created_key (name, created_at DESC));")
	if err != nil {
		t.Fatal(err)
	}
	table, _ := catalog.Table("names")
	indexes := table.Indexes()
	if len(indexes) != 2 {
		t.Fatalf("indexes = %#v, want two", indexes)
	}
	if !indexes[0].HasExpression() || len(indexes[0].Columns()) != 0 {
		t.Fatalf("prefix index = %#v, want non-simple index", indexes[0])
	}
	if indexes[1].HasExpression() || !reflect.DeepEqual(indexes[1].Columns(), []string{"name", "created_at"}) {
		t.Fatalf("directed simple index = %#v", indexes[1])
	}
	if testTableHasUniqueKey(table, []string{"name"}) {
		t.Fatal("prefix index unexpectedly proved full-column uniqueness")
	}
}

func TestParseClassifiesIndexesForUnconditionalCoverage(t *testing.T) {
	t.Parallel()

	catalog, err := Parse(`CREATE TABLE index_capabilities (
  id BIGINT,
  tenant_id BIGINT,
  status VARCHAR(32),
  title TEXT,
  location GEOMETRY,
  UNIQUE KEY visible_unique (id),
  UNIQUE KEY invisible_unique (tenant_id) /*!80000 INVISIBLE */,
  UNIQUE KEY partial_unique (status) WHERE status = 'ready',
  KEY partial_lookup (tenant_id, id) WHERE status = 'ready',
  FULLTEXT KEY title_fulltext (title),
  SPATIAL KEY location_spatial (location)
);`)
	if err != nil {
		t.Fatal(err)
	}
	table, _ := catalog.Table("index_capabilities")
	indexes := make(map[string]Index)
	for _, index := range table.Indexes() {
		indexes[index.Name()] = index
	}

	visible := indexes["visible_unique"]
	if !visible.ProvidesUnconditionalUniqueness() || !visible.SupportsDefaultColumnLookup() {
		t.Fatalf("visible index capabilities = %#v", visible)
	}
	invisible := indexes["invisible_unique"]
	if !invisible.ProvidesUnconditionalUniqueness() || invisible.SupportsDefaultColumnLookup() {
		t.Fatalf("invisible index capabilities = %#v", invisible)
	}
	for _, name := range []string{"partial_unique", "partial_lookup", "title_fulltext", "location_spatial"} {
		index := indexes[name]
		if index.ProvidesUnconditionalUniqueness() || index.SupportsDefaultColumnLookup() {
			t.Fatalf("%s capabilities = %#v, want no unconditional coverage", name, index)
		}
	}
}

func TestCatalogReturnsDetachedIndexColumns(t *testing.T) {
	t.Parallel()

	catalog, err := Parse("CREATE TABLE values_table (id BIGINT PRIMARY KEY, code VARCHAR(20), UNIQUE KEY values_code_key (code));")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := catalog.Table("values_table")
	indexes := first.Indexes()
	indexes[0].Columns()[0] = "changed"
	primary := first.PrimaryKeyColumns()
	primary[0] = "changed"

	second, _ := catalog.Table("values_table")
	if got, want := second.PrimaryKeyColumns(), []string{"id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary key = %#v, want %#v", got, want)
	}
}

func TestCatalogPublicAccessorsAndNilBehavior(t *testing.T) {
	t.Parallel()

	var nilCatalog *Catalog
	if nilCatalog.Tables() != nil {
		t.Fatal("nil Catalog.Tables() must return nil")
	}
	if _, exists := nilCatalog.Table("missing"); exists {
		t.Fatal("nil Catalog.Table() unexpectedly found a table")
	}

	catalog, err := Parse("CREATE TABLE accessor_values (id BIGINT PRIMARY KEY, value VARCHAR(20), KEY value_key (value));")
	if err != nil {
		t.Fatal(err)
	}
	table, _ := catalog.Table("accessor_values")
	if _, exists := table.Column("missing"); exists {
		t.Fatal("missing column unexpectedly found")
	}
	column, _ := table.Column("value")
	if column.Name() != "value" || column.Position().Line != 1 {
		t.Fatalf("column = %#v", column)
	}
	indexes := table.Indexes()
	if len(indexes) != 2 || indexes[0].Name() != "PRIMARY" || !indexes[0].Primary() || indexes[0].Position().Line != 1 {
		t.Fatalf("indexes = %#v", indexes)
	}
	if indexes[1].Name() != "value_key" || indexes[1].Primary() || indexes[1].Unique() {
		t.Fatalf("ordinary index = %#v", indexes[1])
	}
	var parseErr *ParseError
	if got := parseErr.Error(); got != "invalid schema SQL" {
		t.Fatalf("nil ParseError.Error() = %q", got)
	}
}

func TestParseAcceptsQuotedIdentifiersAndNamedConstraints(t *testing.T) {
	t.Parallel()

	sqlText := "CREATE GLOBAL TEMPORARY TABLE IF NOT EXISTS `odd``table` (" +
		"`odd``column` VARCHAR(20) DEFAULT 'it\\'s', " +
		"CONSTRAINT `odd_unique` UNIQUE (`odd``column`), " +
		"CONSTRAINT `odd_check` CHECK (length(`odd``column`) > 0)" +
		");"
	catalog, err := Parse(sqlText)
	if err != nil {
		t.Fatal(err)
	}
	table, exists := catalog.Table("odd`table")
	if !exists {
		t.Fatal("escaped table identifier was not decoded")
	}
	if _, exists := table.Column("odd`column"); !exists {
		t.Fatal("escaped column identifier was not decoded")
	}
	indexes := table.Indexes()
	if len(indexes) != 1 || indexes[0].Name() != "odd_unique" || !indexes[0].Unique() {
		t.Fatalf("indexes = %#v", indexes)
	}
}

func TestParseAcceptsMySQLVersionExecutableComment(t *testing.T) {
	t.Parallel()

	catalog, err := Parse("/*!80000 CREATE TABLE versioned_table (id BIGINT PRIMARY KEY) */;")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := catalog.Table("versioned_table"); !exists {
		t.Fatal("version-comment CREATE TABLE was not parsed")
	}
}

func TestParseRejectsInvalidOrIncompleteSnapshots(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		sql         string
		wantNoTable bool
		want        string
	}{
		{name: "no table", sql: "SET sql_mode = 'STRICT_TRANS_TABLES';", wantNoTable: true},
		{name: "like", sql: "CREATE TABLE copied LIKE source_table;", want: "explicit column list"},
		{name: "as select", sql: "CREATE TABLE copied (id BIGINT) AS SELECT id FROM source_table;", want: "AS SELECT"},
		{name: "duplicate table", sql: "CREATE TABLE one (id BIGINT); CREATE TABLE ONE (id BIGINT);", want: "already defined"},
		{name: "duplicate column", sql: "CREATE TABLE one (id BIGINT, ID VARCHAR(10));", want: "already defined"},
		{name: "unknown index column", sql: "CREATE TABLE one (id BIGINT, UNIQUE KEY missing_key (missing));", want: "unknown column"},
		{name: "multiple primary keys", sql: "CREATE TABLE one (id BIGINT PRIMARY KEY, code BIGINT, PRIMARY KEY (code));", want: "more than one primary key"},
		{name: "unterminated quote", sql: "CREATE TABLE one (name VARCHAR(20) DEFAULT 'open);", want: "unterminated quoted value"},
		{name: "unterminated comment", sql: "/* open", want: "unterminated block comment"},
		{name: "trailing comma", sql: "CREATE TABLE one (id BIGINT,);", want: "trailing comma"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(test.sql)
			if test.wantNoTable {
				if !errors.Is(err, ErrNoCreateTables) {
					t.Fatalf("error = %v, want ErrNoCreateTables", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			var parseErr *ParseError
			if !errors.As(err, &parseErr) || parseErr.Position.Line < 1 || parseErr.Position.Column < 1 {
				t.Fatalf("parse error = %#v", err)
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	var catalog *Catalog
	var err error
	b.ReportAllocs()
	for b.Loop() {
		catalog, err = Parse(catalogFixtureSQL)
	}
	if err != nil {
		b.Fatal(err)
	}
	parsedCatalogSink = catalog
}

var parsedCatalogSink *Catalog

func FuzzParse(f *testing.F) {
	for _, sqlText := range []string{
		catalogFixtureSQL,
		"CREATE TABLE value_table (id BIGINT PRIMARY KEY);",
		"CREATE TABLE `broken (id BIGINT);",
		"/*T![auto_rand] AUTO_RANDOM(5) */",
		"",
	} {
		f.Add(sqlText)
	}
	f.Fuzz(func(t *testing.T, sqlText string) {
		_, _ = Parse(sqlText)
	})
}

func testTableHasUniqueKey(table Table, columns []string) bool {
	wanted := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		wanted[foldIdentifier(column)] = struct{}{}
	}
	for _, index := range table.Indexes() {
		indexColumns := index.Columns()
		if !index.Unique() || index.HasExpression() || len(indexColumns) == 0 {
			continue
		}
		matches := true
		for _, column := range indexColumns {
			if _, exists := wanted[foldIdentifier(column)]; !exists {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
