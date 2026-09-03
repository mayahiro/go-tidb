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

func BenchmarkAnalyzePathHundredResolvedRelationTopNPatterns(b *testing.B) {
	directory := b.TempDir()
	writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
	var source strings.Builder
	source.WriteString(`package benchmark
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
	VideoID int64 ` + "`tidbgo:\",pk\"`" + `
	GenreID int64 ` + "`tidbgo:\",pk\"`" + `
}
`)
	for index := range 100 {
		fmt.Fprintf(&source, `
func query%d() (string, []any, error) {
	return orm.Query[Video]().
		Where(orm.Has("Links", orm.Equal("GenreID", %d))).
		OrderBy(orm.Desc("ID")).
		Limit(20).
		Build()
}
`, index, index)
	}
	writeSourceTestFile(b, filepath.Join(directory, "queries.go"), source.String())
	catalog, err := physicalschema.Parse(`CREATE TABLE videos (
  id BIGINT PRIMARY KEY
);
CREATE TABLE video_links (
  video_id BIGINT NOT NULL,
  genre_id BIGINT NOT NULL,
  PRIMARY KEY (video_id, genre_id),
  KEY genre_video (genre_id, video_id)
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
	if len(analysis.Diagnostics) != 0 || analysis.Statistics.AnalyzedRelationTopNPatterns != 100 || analysis.Statistics.AnalyzedIndexPatterns != 100 {
		b.Fatalf("Analysis = %#v", analysis)
	}
}

func BenchmarkAnalyzePathHundredResolvedManyToManyRelationTopNPatterns(b *testing.B) {
	directory := b.TempDir()
	writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
	var source strings.Builder
	source.WriteString(`package benchmark
import (
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)
type User struct {
	model.Meta ` + "`tidbgo:\"table=users\"`" + `
	ID int64 ` + "`tidbgo:\",pk\"`" + `
	Roles []Role ` + "`tidbgo:\"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID\"`" + `
}
type Role struct {
	model.Meta ` + "`tidbgo:\"table=roles\"`" + `
	ID int64 ` + "`tidbgo:\",pk\"`" + `
}
`)
	for index := range 100 {
		fmt.Fprintf(&source, `
func query%d() (string, []any, error) {
	return orm.Query[User]().
		Where(orm.Has("Roles", orm.Equal("ID", %d))).
		OrderBy(orm.Desc("ID")).
		Limit(20).
		Build()
}
`, index, index)
	}
	writeSourceTestFile(b, filepath.Join(directory, "queries.go"), source.String())
	catalog, err := physicalschema.Parse(`CREATE TABLE users (
  id BIGINT PRIMARY KEY
);
CREATE TABLE roles (
  id BIGINT PRIMARY KEY
);
CREATE TABLE user_roles (
  user_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, role_id),
  KEY role_user (role_id, user_id)
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
	if len(analysis.Diagnostics) != 0 || analysis.Statistics.AnalyzedRelationTopNPatterns != 100 || analysis.Statistics.AnalyzedIndexPatterns != 100 {
		b.Fatalf("Analysis = %#v", analysis)
	}
}
