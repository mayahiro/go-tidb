package sourcecheck

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
		Files:         1,
		ModelTypes:    1,
		ResultQueries: 1,
		Analyzed:      1,
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
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, Analyzed: 1}); !reflect.DeepEqual(got, want) {
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
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, Analyzed: 1}); !reflect.DeepEqual(got, want) {
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
		ExplicitProjections: 1,
		Analyzed:            2,
		Uncertain:           4,
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
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, Uncertain: 1}); !reflect.DeepEqual(got, want) {
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
	if got, want := analysis.Statistics, (Statistics{Files: 1, ModelTypes: 1, ResultQueries: 1, Analyzed: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Statistics = %#v, want %#v", got, want)
	}
	if len(analysis.Diagnostics) != 1 {
		t.Fatalf("Diagnostics = %#v, want one", analysis.Diagnostics)
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

func TestFormatStatistics(t *testing.T) {
	t.Parallel()

	statistics := Statistics{Files: 3, ModelTypes: 2, ResultQueries: 5, ExplicitProjections: 1, Analyzed: 2, Uncertain: 2}
	const want = "source: files=3 model_types=2 result_queries=5 explicit_projections=1 analyzed=2 uncertain=2"
	if got := FormatStatistics(statistics); got != want {
		t.Fatalf("FormatStatistics() = %q, want %q", got, want)
	}
}

func analyzeSource(t testing.TB, source string) Analysis {
	t.Helper()
	analysis, err := analyzeInputs([]sourceInput{{
		absolutePath: filepath.Join(t.TempDir(), "query.go"),
		displayPath:  "query.go",
		source:       []byte(source),
	}})
	if err != nil {
		t.Fatalf("analyzeInputs() error = %v", err)
	}
	return analysis
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
