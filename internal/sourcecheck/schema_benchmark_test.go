package sourcecheck

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkAnalyzePathSchemaModels(b *testing.B) {
	for _, mode := range []string{"matching", "missing_columns", "embedded"} {
		b.Run(mode, func(b *testing.B) {
			directory := b.TempDir()
			writeSourceTestFile(b, filepath.Join(directory, "go.mod"), "module example.test/benchmark\n\ngo 1.26\n")
			var source, sql strings.Builder
			source.WriteString("package benchmark\nimport \"github.com/mayahiro/go-tidb/model\"\ntype Embedded struct { Extra string }\n")
			for index := range 100 {
				fmt.Fprintf(&source, "type Item%d struct {\n model.Meta `tidbgo:\"table=items_%d\"`\n ID int64 `tidbgo:\",pk\"`\n Code string `tidbgo:\",unique=code\"`\n Payload []byte\n", index, index)
				if mode == "embedded" {
					source.WriteString(" Embedded\n")
				}
				source.WriteString("}\n")
				fmt.Fprintf(&sql, "CREATE TABLE items_%d (id BIGINT PRIMARY KEY, code VARCHAR(32) NOT NULL UNIQUE", index)
				if mode != "missing_columns" {
					sql.WriteString(", payload BLOB")
				}
				sql.WriteString(");\n")
			}
			writeSourceTestFile(b, filepath.Join(directory, "models.go"), source.String())
			catalog := parseSourceSchema(b, sql.String())
			b.ReportAllocs()
			for b.Loop() {
				if _, err := AnalyzePath(directory, WithSchema(catalog)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
