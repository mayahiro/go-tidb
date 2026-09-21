package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
)

// The streaming alternative produces the same sorted canonical byte sequence
// but avoids joining it into one large intermediate string.
func streamingSnapshotHash(source string) (string, error) {
	statements, err := splitSQL(source)
	if err != nil {
		return "", err
	}
	for i, statement := range statements {
		statements[i], err = canonicalSQL(statement)
		if err != nil {
			return "", err
		}
	}
	sort.Strings(statements)
	h := sha256.New()
	for i, statement := range statements {
		if i > 0 {
			io.WriteString(h, ";\n")
		}
		io.WriteString(h, statement)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotWorkload(tables, columns, literalBytes int) string {
	var b strings.Builder
	b.WriteString(snapshotHeader)
	for table := tables - 1; table >= 0; table-- {
		fmt.Fprintf(&b, "CREATE TABLE `t_%04d` (`id` BIGINT NOT NULL AUTO_INCREMENT", table)
		for col := 0; col < columns; col++ {
			fmt.Fprintf(&b, ", `c_%04d` DECIMAL(18,6) DEFAULT 1.250000", col)
		}
		fmt.Fprintf(&b, ", `note` TEXT DEFAULT '%s', PRIMARY KEY (`id`)) ENGINE=InnoDB AUTO_INCREMENT=123 DEFAULT CHARSET=utf8mb4;\n", strings.Repeat("a;''bc", literalBytes/6))
	}
	return b.String()
}

func BenchmarkSnapshotHash(b *testing.B) {
	for _, workload := range []struct {
		name                          string
		tables, columns, literalBytes int
	}{
		{"one_table", 1, 5, 0},
		{"hundred_tables", 100, 12, 0},
		{"many_columns", 4, 1000, 0},
		{"large_literal", 1, 5, 1 << 20},
	} {
		source := snapshotWorkload(workload.tables, workload.columns, workload.literalBytes)
		expected, err := snapshotHash(source)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(workload.name, func(b *testing.B) {
			for _, method := range []struct {
				name string
				hash func(string) (string, error)
			}{{"join", snapshotHash}, {"stream", streamingSnapshotHash}} {
				b.Run(method.name, func(b *testing.B) {
					if got, err := method.hash(source); err != nil || got != expected {
						b.Fatalf("non-equivalent hash: %q,%v", got, err)
					}
					b.ReportAllocs()
					b.SetBytes(int64(len(source)))
					b.ResetTimer()
					for b.Loop() {
						if _, err := method.hash(source); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
