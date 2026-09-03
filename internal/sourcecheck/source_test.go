package sourcecheck

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/internal/querycheck"
	"github.com/mayahiro/go-tidb/internal/relationtopn"
	physicalschema "github.com/mayahiro/go-tidb/schema"
)

func TestAnalyzeInputsReportsNarrowLocalDefaultProjection(t *testing.T) {
	t.Parallel()

	analysis, err := analyzeInputs([]sourceInput{{
		absolutePath: filepath.Join(t.TempDir(), "query.go"),
		displayPath:  "internal/repository/query.go",
		source: []byte(`package repository

import (
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type Order struct{ ID int64 }

type User struct {
	model.Meta ` + "`tidbgo:\"table=users\"`" + `
	ID int64
	Email string
	Biography string
	Count int64 ` + "`tidbgo:\"count,computed\"`" + `
	Ignored string ` + "`tidbgo:\"-\"`" + `
	Orders []Order ` + "`tidbgo:\"has_many\"`" + `
}

func loadUsers() int {
	users, err := orm.Query[User]().Where(orm.Equal("ID", 1)).All(ctx, db)
	if err != nil { return 0 }
	for index := range users {
		_ = users[index].ID
		_ = users[index].Email
	}
	return len(users)
}
`),
	}})
	if err != nil {
		t.Fatalf("analyzeInputs() error = %v", err)
	}
	wantStatistics := Statistics{
		Files:            1,
		ModelTypes:       1,
		ResultQueries:    1,
		QueryPatterns:    1,
		Analyzed:         1,
		AnalyzedPatterns: 1,
	}
	if !reflect.DeepEqual(analysis.Statistics, wantStatistics) {
		t.Fatalf("Statistics = %#v, want %#v", analysis.Statistics, wantStatistics)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one", analysis.Diagnostics)
	}
	diagnostic := analysis.Diagnostics[0]
	if diagnostic.Code != codeNarrowProjection || diagnostic.Title != "Query can use a narrower projection" || !diagnostic.Suppressible {
		t.Fatalf("Diagnostic = %#v", diagnostic)
	}
	if got, want := diagnostic.Location.Path, "internal/repository/query.go"; got != want {
		t.Fatalf("Location.Path = %q, want %q", got, want)
	}
	if got, want := diagnostic.Suggestion, `Add Select("ID", "Email") before the terminal when this local result is intentionally partial`; got != want {
		t.Fatalf("Suggestion = %q, want %q", got, want)
	}
	joined := diagnostic.Message
	for _, evidence := range diagnostic.Evidence {
		joined += "\n" + evidence.Message
	}
	for _, want := range []string{"uses 2 of 3", "User.Biography", "User.ID", "User.Email"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Diagnostic text = %q, want substring %q", joined, want)
		}
	}
	for _, excluded := range []string{"Count", "Ignored", "Orders"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("Diagnostic text = %q, must exclude %q", joined, excluded)
		}
	}
}

func TestAnalyzeInputsFollowsLocalQueryHelperAndMutableBuilder(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct {
	ID int64
	Email string
	Biography string
}

func userQuery(active bool) *orm.SelectQuery[User] {
	query := orm.Query[User]()
	if active {
		query = query.Where(orm.Equal("Active", true))
	}
	return query
}

func loadUser() {
	user, err := userQuery(true).Only(ctx, db)
	if err != nil { return }
	_ = user.ID
}
`)
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, QueryPatterns: 1, Analyzed: 1, UncertainPatterns: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 1 || !strings.Contains(analysis.Diagnostics[0].Suggestion, `Select("ID")`) {
		t.Fatalf("Diagnostics = %#v", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsIgnoresBodylessQueryHelper(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Email string }

func userQuery() *orm.SelectQuery[User]

func loadUser() {
	user, _ := userQuery().First(ctx, db)
	_ = user.ID
}
`)
	if got, want := analysis.Statistics, (Statistics{Files: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsReportsNarrowFirstProjection(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Email string }

func loadUser() {
	user, _ := orm.Query[User]().First(ctx, db)
	_ = user.ID
}
`)
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, QueryPatterns: 1, Analyzed: 1, AnalyzedPatterns: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsReportsCoverageWithoutGuessing(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct {
	ID int64
	Email string
}

func explicit() {
	users, _ := orm.Query[User]().Select("ID").All(ctx, db)
	_ = users[0].ID
}

func escaped() []User {
	users, _ := orm.Query[User]().All(ctx, db)
	return users
}

func passed() {
	users, _ := orm.Query[User]().All(ctx, db)
	consume(users)
}

func aliased() {
	users, _ := orm.Query[User]().All(ctx, db)
	copy := users
	_ = copy[0].ID
}

func preloaded() {
	users, _ := orm.Query[User]().Preload("Orders").All(ctx, db)
	_ = users[0].ID
}

func allFields() {
	user, _ := orm.Query[User]().Only(ctx, db)
	_ = user.ID
	_ = user.Email
}

func countOnly() int {
	users, _ := orm.Query[User]().All(ctx, db)
	return len(users)
}
`)
	want := Statistics{
		Files:               1,
		ModelTypes:          1,
		ResultQueries:       7,
		QueryPatterns:       7,
		ExplicitProjections: 1,
		Analyzed:            2,
		Uncertain:           4,
		AnalyzedPatterns:    7,
	}
	if !reflect.DeepEqual(analysis.Statistics, want) {
		t.Fatalf("Statistics = %#v, want %#v", analysis.Statistics, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsTreatsRelationAndMethodUseAsUncertain(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type Order struct { ID int64 }
type User struct {
	ID int64
	Email string
	Orders []Order `+"`tidbgo:\"has_many\"`"+`
}
func (User) Validate() bool { return true }

func relation() {
	users, _ := orm.Query[User]().All(ctx, db)
	for _, user := range users { _ = user.Orders }
}

func method() {
	user, _ := orm.Query[User]().Only(ctx, db)
	_ = user.Validate()
}
`)
	if got, want := analysis.Statistics.Uncertain, 2; got != want {
		t.Fatalf("Uncertain = %d, want %d", got, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsTreatsCapturedBuilderAsUncertain(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Email string }

func load(selectID bool) {
	query := orm.Query[User]()
	configure := func() {
		if selectID { query = query.Select("ID") }
	}
	configure()
	users, _ := query.All(ctx, db)
	_ = users[0].ID
}
`)
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, QueryPatterns: 1, Uncertain: 1, UncertainPatterns: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsAnalyzesFunctionLiteralResultLocally(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Email string }

func handler() func() {
	return func() {
		users, _ := orm.Query[User]().All(ctx, db)
		_ = users[0].ID
	}
}
`)
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, QueryPatterns: 1, Analyzed: 1, AnalyzedPatterns: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsReportsResolvedQueryPatterns(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

const pageSize int64 = 20
const pageOffset = 40

type Genre struct { ID int64; Name string }
type Video struct {
	ID int64
	Title string
	Genres []Genre `+"`tidbgo:\"many_to_many,junction=videos_genres,source=VideoID,target=GenreID\"`"+`
}

func load(term, suffix string) {
	predicate := orm.And(
		orm.Contains("Title", term),
		orm.Has("Genres", orm.HasSuffix("Name", suffix)),
	)
	_, _ = orm.Query[Video]().
		Where(predicate).
		Limit(pageSize).
		Offset(pageOffset).
		All(ctx, db)
}
`)
	if got, want := analysis.Statistics.QueryPatterns, 1; got != want {
		t.Fatalf("QueryPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedPatterns, 1; got != want {
		t.Fatalf("AnalyzedPatterns = %d, want %d", got, want)
	}
	wantCodes := []string{
		querycheck.CodeLeadingWildcardFilter,
		querycheck.CodeLeadingWildcardFilter,
		querycheck.CodeOffsetPagination,
		querycheck.CodeUnorderedPagination,
	}
	gotCodes := sourceDiagnosticCodes(analysis)
	sort.Strings(wantCodes)
	if !reflect.DeepEqual(gotCodes, wantCodes) {
		t.Fatalf("diagnostic codes = %#v, want %#v", gotCodes, wantCodes)
	}
	joined := ""
	for _, diagnostic := range analysis.Diagnostics {
		joined += diagnostic.Message + "\n"
		if diagnostic.Location.Path != "query.go" || diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
			t.Fatalf("diagnostic location = %#v, want query.go source position", diagnostic.Location)
		}
	}
	for _, want := range []string{"skips 40 rows", "Video.Title", "Video.Genres.Name", "LIMIT without ORDER BY"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostic messages = %q, want substring %q", joined, want)
		}
	}
}

func TestAnalyzeInputsReportsPatternCoverageWithoutGuessing(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Name string }

func customPredicate() orm.Predicate

func queries(limit int64) {
	_, _ = orm.Query[User]().
		Where(orm.HasPrefix("Name", "A")).
		OrderBy(orm.Desc("ID")).
		Limit(20).
		All(ctx, db)
	_, _ = orm.Query[User]().Limit(limit).All(ctx, db)
	_, _ = orm.Query[User]().Where(customPredicate()).All(ctx, db)

	query := orm.Query[User]()
	query.Limit(20)
	_, _ = query.All(ctx, db)
}
`)
	if got, want := analysis.Statistics.QueryPatterns, 4; got != want {
		t.Fatalf("QueryPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedPatterns, 1; got != want {
		t.Fatalf("AnalyzedPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.UncertainPatterns, 3; got != want {
		t.Fatalf("UncertainPatterns = %d, want %d", got, want)
	}
	if codes := sourceDiagnosticCodes(analysis); len(codes) != 0 {
		t.Fatalf("diagnostic codes = %#v, want none", codes)
	}
}

func TestAnalyzeInputsChecksOfflineBuildAndDeduplicatesHelperPatterns(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Name string }

func userQuery() *orm.SelectQuery[User] {
	return orm.Query[User]().Where(orm.Contains("Name", "x")).Limit(10)
}

func buildQueries() {
	_, _, _ = userQuery().Build()
	_, _, _ = userQuery().Build()
}
`)
	if got, want := analysis.Statistics, (Statistics{
		Files:            1,
		ModelTypes:       1,
		QueryPatterns:    2,
		AnalyzedPatterns: 2,
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{
		querycheck.CodeUnorderedPagination,
		querycheck.CodeLeadingWildcardFilter,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
}

func TestAnalyzeInputsDoesNotCarryPatternsFromReplacedBuilders(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; Name string }

func build() {
	query := orm.Query[User]().Where(orm.Contains("Name", "old")).Limit(10)
	query = orm.Query[User]().OrderBy(orm.Desc("ID")).Limit(10)
	_, _, _ = query.Build()
}
`)
	if got, want := analysis.Statistics.QueryPatterns, 1; got != want {
		t.Fatalf("QueryPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.UncertainPatterns, 1; got != want {
		t.Fatalf("UncertainPatterns = %d, want %d", got, want)
	}
	if codes := sourceDiagnosticCodes(analysis); len(codes) != 0 {
		t.Fatalf("diagnostic codes = %#v, want none", codes)
	}
}

func TestAnalyzeInputsUsesResolvedORMAliasOnly(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import dborm "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64 }
type fakeORM struct{}

func build() {
	_, _, _ = dborm.Query[User]().Limit(10).Build()
	dborm := fakeORM{}
	_, _, _ = dborm.Query[User]().Limit(20).Build()
}
`)
	if got, want := analysis.Statistics.QueryPatterns, 1; got != want {
		t.Fatalf("QueryPatterns = %d, want %d", got, want)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeUnorderedPagination}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
}

func TestAnalyzeInputsChecksResolvedRootIndexPatternAgainstSchema(t *testing.T) {
	t.Parallel()

	analysis := analyzeSourceWithOptions(t, `package repository

import (
	"time"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type ActivityEvent struct {
	model.Meta `+"`tidbgo:\"table=activity_events\"`"+`
	ID int64 `+"`tidbgo:\"event_id,pk\"`"+`
	TenantID int64 `+"`tidbgo:\"tenant_id\"`"+`
	DeletedAt *time.Time `+"`tidbgo:\"deleted_at,soft_delete\"`"+`
}

func build() {
	order := orm.Desc("ID")
	_, _, _ = orm.Query[ActivityEvent]().
		Where(orm.And(orm.Equal("TenantID", 7), orm.Equal("TenantID", 7))).
		OrderBy(order).
		Limit(20).
		Build()
}
`, WithSchema(parseSourceSchema(t, `CREATE TABLE activity_events (
  event_id BIGINT NOT NULL PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  deleted_at DATETIME NULL,
  KEY wrong_index (tenant_id)
);`)))
	if got, want := analysis.Statistics.IndexPatterns, 1; got != want {
		t.Fatalf("IndexPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedIndexPatterns, 1; got != want {
		t.Fatalf("AnalyzedIndexPatterns = %d, want %d", got, want)
	}
	if got := analysis.Statistics.UncertainIndexPatterns; got != 0 {
		t.Fatalf("UncertainIndexPatterns = %d, want 0", got)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	diagnostic := analysis.Diagnostics[0]
	if diagnostic.Location.Path != "query.go" || diagnostic.Location.Line == 0 || diagnostic.Location.Column == 0 {
		t.Fatalf("diagnostic location = %#v, want Go source location", diagnostic.Location)
	}
	joined := diagnostic.Message
	for _, evidence := range diagnostic.Evidence {
		joined += "\n" + evidence.Message
	}
	for _, want := range []string{"activity_events", "tenant_id, deleted_at, event_id", "Schema table declaration"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostic text = %q, want substring %q", joined, want)
		}
	}
	if strings.Contains(joined, "Query fingerprint") {
		t.Fatalf("source diagnostic must not claim a runtime query fingerprint: %q", joined)
	}
}

func TestAnalyzeInputsAcceptsMatchingSourceIndexAndWithDeletedScope(t *testing.T) {
	t.Parallel()

	analysis := analyzeSourceWithOptions(t, `package repository

import (
	"time"
	"github.com/mayahiro/go-tidb/orm"
)

type ActivityEvent struct {
	ID int64 `+"`tidbgo:\",pk\"`"+`
	TenantID int64
	DeletedAt *time.Time `+"`tidbgo:\",soft_delete\"`"+`
}

func active() {
	_, _, _ = orm.Query[ActivityEvent]().Where(orm.Equal("TenantID", 7)).OrderBy(orm.Desc("ID")).Limit(20).Build()
}

func all() {
	_, _, _ = orm.Query[ActivityEvent]().WithDeleted().Where(orm.Equal("TenantID", 7)).OrderBy(orm.Desc("ID")).Limit(20).Build()
}
`, WithSchema(parseSourceSchema(t, `CREATE TABLE activity_event (
  id BIGINT NOT NULL PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  deleted_at DATETIME NULL,
  KEY active_page (deleted_at, tenant_id, id),
  KEY all_page (tenant_id, id)
);`)))
	if got, want := analysis.Statistics.IndexPatterns, 2; got != want {
		t.Fatalf("IndexPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedIndexPatterns, 2; got != want {
		t.Fatalf("AnalyzedIndexPatterns = %d, want %d", got, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsReportsUnavailableSourceIndexSchema(t *testing.T) {
	t.Parallel()

	analysis := analyzeSourceWithOptions(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64 }

func build() {
	_, _, _ = orm.Query[User]().OrderBy(orm.Desc("ID")).Limit(20).Build()
}
`, WithSchema(parseSourceSchema(t, `CREATE TABLE other_users (id BIGINT PRIMARY KEY);`)))
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeIndexCheckUnavailable}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if diagnostic := analysis.Diagnostics[0]; diagnostic.Suppressible || diagnostic.Location.Path != "query.go" || !strings.Contains(diagnostic.Message, `table "user" is absent`) {
		t.Fatalf("Diagnostic = %#v", diagnostic)
	}
}

func TestAnalyzeInputsCountsUncertainSourceIndexPatternsWithoutGuessing(t *testing.T) {
	t.Parallel()

	analysis := analyzeSourceWithOptions(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type Order struct { ID int64; UserID int64 }
type User struct {
	ID int64
	TenantID int64
	Name string
	Orders []Order `+"`tidbgo:\"has_many,join=ID:UserID\"`"+`
}

func queries(order orm.OrderTerm) {
	_, _, _ = orm.Query[User]().Where(orm.HasPrefix("Name", "A")).OrderBy(orm.Desc("ID")).Limit(20).Build()
	_, _, _ = orm.Query[User]().Where(orm.Equal("TenantID", 7)).OrderBy(orm.Asc("TenantID"), orm.Desc("ID")).Limit(20).Build()
	_, _, _ = orm.Query[User]().Where(orm.Equal("TenantID", 7)).OrderBy(order).Limit(20).Build()
	_, _, _ = orm.Query[User]().Where(orm.Has("Orders", orm.Equal("ID", 1))).OrderBy(orm.Desc("ID")).Limit(20).Build()
	_, _, _ = orm.Query[User]().Where(orm.And(orm.Equal("TenantID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build()
	query := orm.Query[User]().Where(orm.Equal("TenantID", 7)).OrderBy(orm.Desc("ID")).Limit(20)
	query.WithDeleted()
	_, _, _ = query.Build()
}
`, WithSchema(parseSourceSchema(t, `CREATE TABLE user (
  id BIGINT PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  KEY tenant_id_id (tenant_id, id)
);`)))
	if got, want := analysis.Statistics.IndexPatterns, 6; got != want {
		t.Fatalf("IndexPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.UncertainIndexPatterns, 6; got != want {
		t.Fatalf("UncertainIndexPatterns = %d, want %d", got, want)
	}
	if got := analysis.Statistics.AnalyzedIndexPatterns; got != 0 {
		t.Fatalf("AnalyzedIndexPatterns = %d, want 0", got)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsAppliesRelationTopNCompilerDecisionWithoutSchema(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import (
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type Video struct {
	model.Meta `+"`tidbgo:\"table=videos\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	Links []VideoLink `+"`tidbgo:\"has_many,join=ID:VideoID\"`"+`
}

type VideoLink struct {
	model.Meta `+"`tidbgo:\"table=video_links\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	VideoID int64 `+"`tidbgo:\",unique=video_genre\"`"+`
	GenreID int64 `+"`tidbgo:\",unique=video_genre\"`"+`
	Priority int64
}

type UnprovenVideo struct {
	model.Meta `+"`tidbgo:\"table=unproven_videos\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	Links []UnprovenLink `+"`tidbgo:\"has_many,join=ID:VideoID\"`"+`
}

type UnprovenLink struct {
	model.Meta `+"`tidbgo:\"table=unproven_links\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	VideoID int64
	GenreID int64
}

func queries() {
	_, _, _ = orm.Query[Video]().OrderBy(orm.Desc("ID")).Where(orm.Has("Links", orm.Equal("GenreID", 7))).Limit(20).Build()
	_, _, _ = orm.Query[UnprovenVideo]().Where(orm.Has("Links", orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build()
}
`)
	if got, want := analysis.Statistics.RelationTopNPatterns, 2; got != want {
		t.Fatalf("RelationTopNPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedRelationTopNPatterns, 2; got != want {
		t.Fatalf("AnalyzedRelationTopNPatterns = %d, want %d", got, want)
	}
	if got := analysis.Statistics.UncertainRelationTopNPatterns; got != 0 {
		t.Fatalf("UncertainRelationTopNPatterns = %d, want 0", got)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeRelationTopNFallback}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	diagnostic := analysis.Diagnostics[0]
	if diagnostic.Location.Path != "query.go" || !strings.Contains(diagnostic.Message, "UnprovenVideo") || !strings.Contains(diagnostic.Message, "relation Links") ||
		len(diagnostic.Evidence) != 1 || !strings.Contains(diagnostic.Evidence[0].Message, relationtopn.ReasonTargetUniqueness) {
		t.Fatalf("Diagnostic = %#v", diagnostic)
	}
}

func TestAnalyzeInputsReportsStructuralRelationTopNFallbacksFromSource(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository

import (
	"time"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type Video struct {
	model.Meta `+"`tidbgo:\"table=videos\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	DeletedAt time.Time `+"`tidbgo:\",soft_delete\"`"+`
	Links []VideoLink `+"`tidbgo:\"has_many,join=ID:VideoID\"`"+`
	Tags []Tag `+"`tidbgo:\"many_to_many,through=videos_tags,source=ID:video_id,target=tag_id:ID\"`"+`
}
type VideoLink struct {
	model.Meta `+"`tidbgo:\"table=video_links\"`"+`
	VideoID int64 `+"`tidbgo:\",pk\"`"+`
	GenreID int64 `+"`tidbgo:\",pk\"`"+`
}
type Tag struct { ID int64 `+"`tidbgo:\",pk\"`"+`; Name string }

func nested() { _, _, _ = orm.Query[Video]().Where(orm.And(orm.Has("Links", orm.Equal("GenreID", 7)), orm.Equal("ID", 1))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
func seek() { _, _, _ = orm.Query[Video]().WithDeleted().Where(orm.Has("Links", orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).SeekAfter(100).Limit(20).Build() }
func softDelete() { _, _, _ = orm.Query[Video]().Where(orm.Has("Links", orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
	func manyToMany() { _, _, _ = orm.Query[Video]().WithDeleted().Where(orm.Has("Tags", orm.Equal("Name", "drama"))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`)
	if got, want := analysis.Statistics.RelationTopNPatterns, 4; got != want {
		t.Fatalf("RelationTopNPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.AnalyzedRelationTopNPatterns, 4; got != want {
		t.Fatalf("AnalyzedRelationTopNPatterns = %d, want %d", got, want)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{
		querycheck.CodeRelationTopNFallback,
		querycheck.CodeRelationTopNFallback,
		querycheck.CodeRelationTopNFallback,
		querycheck.CodeRelationTopNFallback,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	joined := ""
	for _, diagnostic := range analysis.Diagnostics {
		for _, evidence := range diagnostic.Evidence {
			joined += evidence.Message + "\n"
		}
	}
	for _, reason := range []string{relationtopn.ReasonNestedCollection, relationtopn.ReasonSeekAfter, relationtopn.ReasonRootSoftDelete, relationtopn.ReasonTargetUniqueness} {
		if !strings.Contains(joined, reason) {
			t.Fatalf("evidence = %q, want reason %q", joined, reason)
		}
	}
}

func TestAnalyzeInputsChecksRelationTopNAssociationIndexFromSource(t *testing.T) {
	t.Parallel()

	source := `package repository

import (
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)
type Video struct {
	model.Meta ` + "`tidbgo:\"table=videos\"`" + `
	ID int64 ` + "`tidbgo:\",pk\"`" + `
	Links []VideoLink ` + "`tidbgo:\"has_many,join=ID:VideoID\"`" + `
}
type VideoLink struct {
	model.Meta ` + "`tidbgo:\"table=video_links\"`" + `
	ID int64 ` + "`tidbgo:\",pk\"`" + `
	VideoID int64 ` + "`tidbgo:\",unique=video_genre\"`" + `
	GenreID int64 ` + "`tidbgo:\",unique=video_genre\"`" + `
	Priority int64
}
func query() { _, _, _ = orm.Query[Video]().Where(orm.Has("Links", orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`
	matching := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, `CREATE TABLE videos (
  id BIGINT PRIMARY KEY
);
CREATE TABLE video_links (
  id BIGINT NOT NULL,
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  priority BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY video_genre (video_id, genre_id),
  KEY genre_video (genre_id, video_id)
);`)))
	if got, want := matching.Statistics.AnalyzedIndexPatterns, 1; got != want || len(matching.Diagnostics) != 0 {
		t.Fatalf("matching analysis = %#v", matching)
	}

	missing := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, `CREATE TABLE videos (
  id BIGINT PRIMARY KEY
);
CREATE TABLE video_links (
  id BIGINT NOT NULL,
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  priority BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY video_genre (video_id, genre_id)
);`)))
	if got, want := sourceDiagnosticCodes(missing), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if diagnostic := missing.Diagnostics[0]; diagnostic.Location.Path != "query.go" || !strings.Contains(diagnostic.Message, "Video.Links") {
		t.Fatalf("Diagnostic = %#v", diagnostic)
	}
}

func TestAnalyzeInputsChecksManyToManyRelationTopNJunctionIndexFromSource(t *testing.T) {
	t.Parallel()

	source := `package repository
import "github.com/mayahiro/go-tidb/orm"
type User struct {
	ID uint64 ` + "`tidbgo:\",pk\"`" + `
	Roles []Role ` + "`tidbgo:\"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID\"`" + `
}
type Role struct { ID uint64 ` + "`tidbgo:\",pk\"`" + `; Name string }
func query() { _, _, _ = orm.Query[User]().Where(orm.Has("Roles", orm.Equal("ID", uint64(7)))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`
	matching := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, `CREATE TABLE user_roles (
  user_id BIGINT UNSIGNED NOT NULL,
  role_id BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (user_id, role_id),
  KEY role_user (role_id, user_id)
);`)))
	if matching.Statistics.AnalyzedRelationTopNPatterns != 1 || matching.Statistics.AnalyzedIndexPatterns != 1 || len(matching.Diagnostics) != 0 {
		t.Fatalf("matching analysis = %#v", matching)
	}

	missing := analyzeSourceWithOptions(t, source, WithSchema(parseSourceSchema(t, `CREATE TABLE user_roles (
  user_id BIGINT UNSIGNED NOT NULL,
  role_id BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (user_id, role_id)
);`)))
	if got, want := sourceDiagnosticCodes(missing), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	diagnostic := missing.Diagnostics[0]
	if !strings.Contains(diagnostic.Message, "User.Roles") || !strings.Contains(diagnostic.Message, "user_roles") ||
		!strings.Contains(diagnostic.Evidence[0].Message, "(role_id, user_id)") {
		t.Fatalf("Diagnostic = %#v", diagnostic)
	}
}

func TestAnalyzeInputsCountsUncertainRelationTopNWithoutGuessing(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Video struct { ID int64; Links []VideoLink `+"`tidbgo:\"has_many,join=ID:VideoID\"`"+` }
type VideoLink struct { VideoID int64; GenreID int64 }
type InvalidVideo struct {
	ID int64 `+"`tidbgo:\",pk\"`"+`
	Tags []Tag `+"`tidbgo:\"many_to_many,through=videos_tags,source=ID:shared_id,target=shared_id:ID\"`"+`
}
type Tag struct { ID int64 `+"`tidbgo:\",pk\"`"+` }
func query(relation string) { _, _, _ = orm.Query[Video]().Where(orm.Has(relation, orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
func invalidRelation() { _, _, _ = orm.Query[InvalidVideo]().Where(orm.Has("Tags", orm.Equal("ID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`)
	if got, want := analysis.Statistics.RelationTopNPatterns, 2; got != want {
		t.Fatalf("RelationTopNPatterns = %d, want %d", got, want)
	}
	if got, want := analysis.Statistics.UncertainRelationTopNPatterns, 2; got != want {
		t.Fatalf("UncertainRelationTopNPatterns = %d, want %d", got, want)
	}
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("Diagnostics = %#v, want none", analysis.Diagnostics)
	}
}

func TestAnalyzeInputsDoesNotTreatToOneHasAsRelationTopN(t *testing.T) {
	t.Parallel()

	analysis := analyzeSource(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type Video struct {
	ID int64 `+"`tidbgo:\",pk\"`"+`
	MakerID int64
	Maker *Maker `+"`tidbgo:\"belongs_to\"`"+`
}
type Maker struct { ID int64 `+"`tidbgo:\",pk\"`"+` }
func query() { _, _, _ = orm.Query[Video]().Where(orm.Has("Maker", orm.Equal("ID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`)
	if analysis.Statistics.RelationTopNPatterns != 0 || analysis.Statistics.AnalyzedRelationTopNPatterns != 0 ||
		analysis.Statistics.UncertainRelationTopNPatterns != 0 || len(analysis.Diagnostics) != 0 {
		t.Fatalf("Analysis = %#v", analysis)
	}
}

func TestAnalyzePathResolvesModuleLocalModelAndSkipsNonProductionFiles(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeSourceTestFile(t, filepath.Join(directory, "go.mod"), "module \"example.test/application\" // test module\n\ngo 1.26\n")
	writeSourceTestFile(t, filepath.Join(directory, "domain", "user.go"), `package domain
type User struct { ID int64; Email string; Biography string }
`)
	writeSourceTestFile(t, filepath.Join(directory, "repository", "query.go"), `package repository
import (
	"example.test/application/domain"
	"github.com/mayahiro/go-tidb/orm"
)
func load() {
	users, _ := orm.Query[domain.User]().All(ctx, db)
	for _, user := range users { _ = user.ID }
}
`)
	writeSourceTestFile(t, filepath.Join(directory, "repository", "query_test.go"), `package repository
func ignoredTest() { users, _ := orm.Query[domain.User]().All(ctx, db); _ = users[0].ID }
`)
	writeSourceTestFile(t, filepath.Join(directory, "repository", "generated.go"), `// Code generated by test. DO NOT EDIT.
package repository
func ignoredGenerated() { users, _ := orm.Query[domain.User]().All(ctx, db); _ = users[0].ID }
`)

	analysis, err := AnalyzePath(directory)
	if err != nil {
		t.Fatalf("AnalyzePath() error = %v", err)
	}
	if got, want := analysis.Statistics.Files, 2; got != want {
		t.Fatalf("Files = %d, want %d", got, want)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one", analysis.Diagnostics)
	}
	if got, want := analysis.Diagnostics[0].Location.Path, "repository/query.go"; got != want {
		t.Fatalf("Location.Path = %q, want %q", got, want)
	}
}

func TestAnalyzePathChecksModuleLocalModelAgainstSchema(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeSourceTestFile(t, filepath.Join(directory, "go.mod"), "module example.test/application\n\ngo 1.26\n")
	writeSourceTestFile(t, filepath.Join(directory, "domain", "user.go"), `package domain
import "github.com/mayahiro/go-tidb/model"
type User struct {
	model.Meta `+"`tidbgo:\"table=user_accounts\"`"+`
	ID int64 `+"`tidbgo:\"user_id,pk\"`"+`
	TenantID int64
}
`)
	writeSourceTestFile(t, filepath.Join(directory, "repository", "query.go"), `package repository
import (
	"example.test/application/domain"
	"github.com/mayahiro/go-tidb/orm"
)
func build() {
	_, _, _ = orm.Query[domain.User]().Where(orm.Equal("TenantID", 7)).OrderBy(orm.Desc("ID")).Limit(20).Build()
}
`)
	catalog := parseSourceSchema(t, `CREATE TABLE user_accounts (
  user_id BIGINT PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  KEY tenant_only (tenant_id)
);`)
	analysis, err := AnalyzePath(directory, WithSchema(catalog))
	if err != nil {
		t.Fatalf("AnalyzePath() error = %v", err)
	}
	if got, want := analysis.Statistics.AnalyzedIndexPatterns, 1; got != want {
		t.Fatalf("AnalyzedIndexPatterns = %d, want %d", got, want)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	if got, want := analysis.Diagnostics[0].Location.Path, "repository/query.go"; got != want {
		t.Fatalf("Location.Path = %q, want %q", got, want)
	}
}

func TestAnalyzePathResolvesModuleLocalRelationTopNAndAssociationIndex(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeSourceTestFile(t, filepath.Join(directory, "go.mod"), "module example.test/application\n\ngo 1.26\n")
	writeSourceTestFile(t, filepath.Join(directory, "domain", "models.go"), `package domain
import "github.com/mayahiro/go-tidb/model"
type Video struct {
	model.Meta `+"`tidbgo:\"table=videos\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	Links []VideoLink `+"`tidbgo:\"has_many\"`"+`
}
type VideoLink struct {
	model.Meta `+"`tidbgo:\"table=video_links\"`"+`
	ID int64 `+"`tidbgo:\",pk\"`"+`
	VideoID int64 `+"`tidbgo:\",unique=video_genre\"`"+`
	GenreID int64 `+"`tidbgo:\",unique=video_genre\"`"+`
	Priority int64
}
`)
	writeSourceTestFile(t, filepath.Join(directory, "repository", "query.go"), `package repository
import (
	"example.test/application/domain"
	"github.com/mayahiro/go-tidb/orm"
)
func query() { _, _, _ = orm.Query[domain.Video]().Where(orm.Has("Links", orm.Equal("GenreID", 7))).OrderBy(orm.Desc("ID")).Limit(20).Build() }
`)
	catalog := parseSourceSchema(t, `CREATE TABLE videos (id BIGINT PRIMARY KEY);
CREATE TABLE video_links (
  id BIGINT NOT NULL,
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  priority BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY video_genre (video_id, genre_id),
  KEY genre_video (genre_id, video_id)
);`)
	analysis, err := AnalyzePath(directory, WithSchema(catalog))
	if err != nil {
		t.Fatalf("AnalyzePath() error = %v", err)
	}
	if analysis.Statistics.AnalyzedRelationTopNPatterns != 1 || analysis.Statistics.AnalyzedIndexPatterns != 1 || len(analysis.Diagnostics) != 0 {
		t.Fatalf("Analysis = %#v", analysis)
	}
}

func TestAnalyzePathExcludesNonProductionDirectoriesAndBuildFiles(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeSourceTestFile(t, filepath.Join(directory, "main.go"), "package application\n")
	writeSourceTestFile(t, filepath.Join(directory, "ignored.go"), "//go:build ignore\n\npackage broken\nfunc {\n")
	writeSourceTestFile(t, filepath.Join(directory, "vendor", "broken.go"), "package broken\nfunc {\n")
	writeSourceTestFile(t, filepath.Join(directory, "testdata", "broken.go"), "package broken\nfunc {\n")
	writeSourceTestFile(t, filepath.Join(directory, ".hidden", "broken.go"), "package broken\nfunc {\n")

	analysis, err := AnalyzePath(directory)
	if err != nil {
		t.Fatalf("AnalyzePath() error = %v", err)
	}
	if got, want := analysis.Statistics.Files, 1; got != want {
		t.Fatalf("Files = %d, want %d", got, want)
	}
}

func TestAnalyzePathRejectsMissingOrInvalidSource(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if _, err := AnalyzePath(directory); !errors.Is(err, ErrNoSourceFiles) {
		t.Fatalf("AnalyzePath(empty) error = %v, want ErrNoSourceFiles", err)
	}
	missing := filepath.Join(directory, "missing")
	if _, err := AnalyzePath(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AnalyzePath(missing) error = %v, want os.ErrNotExist", err)
	}
	bad := filepath.Join(directory, "bad.go")
	writeSourceTestFile(t, bad, "package broken\nfunc {\n")
	if _, err := AnalyzePath(bad); !errors.Is(err, ErrInvalidSource) || !strings.Contains(err.Error(), "bad.go") || strings.Contains(err.Error(), directory) {
		t.Fatalf("AnalyzePath(bad) error = %v, want relative parse location", err)
	}
}

func TestAnalyzeInputsKeepsCachedQueryHelperIndexMetadataImmutable(t *testing.T) {
	t.Parallel()

	analysis := analyzeSourceWithOptions(t, `package repository

import "github.com/mayahiro/go-tidb/orm"

type User struct { ID int64; TenantID int64; Name string }

func base() *orm.SelectQuery[User] {
	return orm.Query[User]().Where(orm.Equal("TenantID", 7))
}

func byID() {
	_, _, _ = base().OrderBy(orm.Desc("ID")).Limit(20).Build()
}

func byName() {
	_, _, _ = base().OrderBy(orm.Desc("Name")).Limit(20).Build()
}
`, WithSchema(parseSourceSchema(t, `CREATE TABLE user (
  id BIGINT PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  KEY tenant_id_id (tenant_id, id)
);`)))
	if got, want := analysis.Statistics.AnalyzedIndexPatterns, 2; got != want {
		t.Fatalf("AnalyzedIndexPatterns = %d, want %d", got, want)
	}
	if got, want := sourceDiagnosticCodes(analysis), []string{querycheck.CodeMissingIndexPrefix}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic codes = %#v, want %#v", got, want)
	}
	joined := analysis.Diagnostics[0].Message
	for _, evidence := range analysis.Diagnostics[0].Evidence {
		joined += "\n" + evidence.Message
	}
	if !strings.Contains(joined, "user(tenant_id, name)") || strings.Contains(joined, "tenant_id, id, name") {
		t.Fatalf("diagnostic text = %q, want isolated helper order", joined)
	}
}

func TestFormatStatistics(t *testing.T) {
	t.Parallel()

	statistics := Statistics{Files: 3, ModelTypes: 2, ResultQueries: 5, QueryPatterns: 6, ExplicitProjections: 1, Analyzed: 2, Uncertain: 2, AnalyzedPatterns: 4, UncertainPatterns: 2}
	const want = "source: files=3 model_types=2 result_queries=5 query_patterns=6 explicit_projections=1 analyzed=2 uncertain=2 analyzed_patterns=4 uncertain_patterns=2 relation_topn_patterns=0 analyzed_relation_topn_patterns=0 uncertain_relation_topn_patterns=0 index_patterns=0 analyzed_index_patterns=0 uncertain_index_patterns=0"
	if got := FormatStatistics(statistics); got != want {
		t.Fatalf("FormatStatistics() = %q, want %q", got, want)
	}
}

func analyzeSource(t testing.TB, source string) Analysis {
	return analyzeSourceWithOptions(t, source)
}

func analyzeSourceWithOptions(t testing.TB, source string, options ...AnalysisOption) Analysis {
	t.Helper()
	analysis, err := analyzeInputs([]sourceInput{{
		absolutePath: filepath.Join(t.TempDir(), "query.go"),
		displayPath:  "query.go",
		source:       []byte(source),
	}}, options...)
	if err != nil {
		t.Fatalf("analyzeInputs() error = %v", err)
	}
	return analysis
}

func parseSourceSchema(t testing.TB, source string) *physicalschema.Catalog {
	t.Helper()
	catalog, err := physicalschema.Parse(source)
	if err != nil {
		t.Fatalf("schema.Parse() error = %v", err)
	}
	return catalog
}

func sourceDiagnosticCodes(analysis Analysis) []string {
	codes := make([]string, len(analysis.Diagnostics))
	for index, diagnostic := range analysis.Diagnostics {
		codes[index] = diagnostic.Code
	}
	sort.Strings(codes)
	return codes
}

func writeSourceTestFile(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
