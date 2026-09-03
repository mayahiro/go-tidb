package sourcecheck

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	physicalschema "github.com/mayahiro/go-tidb/schema"
)

func BenchmarkAnalyzePathHundredLocalQueries(b *testing.B) {
	directory := b.TempDir()
	writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
	var source strings.Builder
	source.WriteString(`package benchmark
import "github.com/mayahiro/go-tidb/orm"
type User struct { ID int64; Email string; Biography string; Payload []byte }
`)
	for index := range 100 {
		fmt.Fprintf(&source, `
func query%d() int {
	users, _ := orm.Query[User]().All(ctx, db)
	for index := range users { _ = users[index].ID; _ = users[index].Email }
	return len(users)
}
`, index)
	}
	writeSourceTestFile(b, filepath.Join(directory, "queries.go"), source.String())

	var analysis Analysis
	var err error
	b.ReportAllocs()
	for b.Loop() {
		analysis, err = AnalyzePath(directory)
		if err != nil {
			b.Fatal(err)
		}
	}
	if len(analysis.Diagnostics) != 100 {
		b.Fatalf("Diagnostics = %d, want 100", len(analysis.Diagnostics))
	}
}

func BenchmarkAnalyzePathHundredResolvedPatterns(b *testing.B) {
	directory := b.TempDir()
	writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
	var source strings.Builder
	source.WriteString(`package benchmark
import "github.com/mayahiro/go-tidb/orm"
type User struct { ID int64; Name string }
`)
	for index := range 100 {
		fmt.Fprintf(&source, `
func query%d() (string, []any, error) {
	return orm.Query[User]().
		Where(orm.Contains("Name", "needle")).
		OrderBy(orm.Desc("ID")).
		Limit(20).
		Offset(40).
		Build()
}
`, index)
	}
	writeSourceTestFile(b, filepath.Join(directory, "queries.go"), source.String())

	var analysis Analysis
	var err error
	b.ReportAllocs()
	for b.Loop() {
		analysis, err = AnalyzePath(directory)
		if err != nil {
			b.Fatal(err)
		}
	}
	if len(analysis.Diagnostics) != 200 {
		b.Fatalf("Diagnostics = %d, want 200", len(analysis.Diagnostics))
	}
}

func BenchmarkAnalyzePathHundredResolvedIndexPatterns(b *testing.B) {
	directory := b.TempDir()
	writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
	var source strings.Builder
	source.WriteString(`package benchmark
import "github.com/mayahiro/go-tidb/orm"
type User struct { ID int64; TenantID int64; Payload []byte }
`)
	for index := range 100 {
		fmt.Fprintf(&source, `
func query%d() (string, []any, error) {
	return orm.Query[User]().
		Where(orm.Equal("TenantID", %d)).
		OrderBy(orm.Desc("ID")).
		Limit(20).
		Build()
}
`, index, index)
	}
	writeSourceTestFile(b, filepath.Join(directory, "queries.go"), source.String())
	catalog, err := physicalschema.Parse(`CREATE TABLE user (
  id BIGINT NOT NULL PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  payload BLOB NOT NULL,
  KEY tenant_only (tenant_id)
);`)
	if err != nil {
		b.Fatal(err)
	}

	var analysis Analysis
	b.ReportAllocs()
	for b.Loop() {
		analysis, err = AnalyzePath(directory, WithSchema(catalog))
		if err != nil {
			b.Fatal(err)
		}
	}
	if len(analysis.Diagnostics) != 100 {
		b.Fatalf("Diagnostics = %d, want 100", len(analysis.Diagnostics))
	}
}
