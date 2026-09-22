package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func BenchmarkLoad(b *testing.B) {
	for _, workload := range []struct {
		name                               string
		versions, statements, literalBytes int
	}{{"one_version", 1, 1, 0}, {"hundred_versions", 100, 1, 0}, {"hundred_statements", 1, 100, 0}, {"large_literal", 1, 1, 1 << 20}} {
		b.Run(workload.name, func(b *testing.B) {
			dir := b.TempDir()
			start := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
			bytes := 0
			for i := 0; i < workload.versions; i++ {
				name := strings.ReplaceAll(start.Add(time.Duration(i)*time.Second).Format(timestampLayout), ".", "") + "_create_table.sql"
				var source strings.Builder
				source.WriteString("-- tidbgo:up\n")
				for statement := 0; statement < workload.statements; statement++ {
					fmt.Fprintf(&source, "CREATE TABLE t%d (id BIGINT PRIMARY KEY, note TEXT DEFAULT '%s');\n", i*workload.statements+statement, strings.Repeat("a;''bc", workload.literalBytes/6))
				}
				source.WriteString("-- tidbgo:down\n")
				for statement := workload.statements - 1; statement >= 0; statement-- {
					fmt.Fprintf(&source, "DROP TABLE t%d;\n", i*workload.statements+statement)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(source.String()), 0644); err != nil {
					b.Fatal(err)
				}
				bytes += source.Len()
			}
			if files, err := Load(dir); err != nil || len(files) != workload.versions {
				b.Fatalf("fixture load: %d, %v", len(files), err)
			}
			b.SetBytes(int64(bytes))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Load(dir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
