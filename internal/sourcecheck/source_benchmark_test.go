package sourcecheck

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
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
