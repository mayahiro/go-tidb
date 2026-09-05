package runtimecapture

import "testing"

// Records are prepared before timing. The benchmark isolates offline analysis
// rather than runtime capture, JSON decoding, driver conversion, or DB work.
func BenchmarkAnalyzeRepeatedWrites(b *testing.B) {
	for _, test := range []struct {
		name      string
		operation string
		terminal  string
		ru        bool
		scopes    bool
		batch     bool
	}{
		{name: "insert", operation: "INSERT", terminal: "insert"},
		{name: "upsert_ru", operation: "UPSERT", terminal: "upsert", ru: true},
		{name: "separate_scopes", operation: "INSERT", terminal: "insert", scopes: true},
		{name: "bulk", operation: "INSERT", terminal: "insert_many", batch: true},
	} {
		b.Run(test.name, func(b *testing.B) {
			records := make([]Record, 1000)
			for index := range records {
				records[index] = runtimeAnalysisRecord(uint64(index+1), SourceTypedMutation, "s1:write")
				records[index].Operation = test.operation
				records[index].Terminal = test.terminal
				records[index].SQL = "INSERT INTO `users` (`email`) VALUES (?)"
				if test.operation == "UPSERT" {
					records[index].SQL += " ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)"
				}
				if test.ru {
					records[index].ServerRU = &ServerRU{Known: true, Value: float64(index%5) / 4, AuxiliaryStatements: 1}
				}
				if test.scopes {
					records[index].ScopeID = uint64(index + 1)
				}
				if test.batch {
					records[index].Batch = &Batch{Group: 1, Index: index + 1, Count: len(records), Rows: 100, TotalRows: len(records) * 100}
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				result := Analyze(records)
				if result.Statistics.Statements != len(records) {
					b.Fatalf("statement count = %d, want %d", result.Statistics.Statements, len(records))
				}
			}
		})
	}
}
