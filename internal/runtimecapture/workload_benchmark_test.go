package runtimecapture

import "testing"

// Workload records are prepared before timing. Both modes run the same input;
// only the enabled mode keeps per-scope budget counters. No JSON or DB I/O is
// included. Memory scales with distinct scopes, not attempts within one scope.
func BenchmarkAnalyzeWorkload(b *testing.B) {
	for _, test := range []struct {
		name               string
		scopes, statements int
	}{
		{"one_scope", 1, 1}, {"one_scope_10000_statements", 1, 10000}, {"1000_scopes", 1000, 10},
	} {
		b.Run(test.name, func(b *testing.B) {
			records := workloadRecords(test.scopes, test.statements, 1)
			for _, enabled := range []bool{false, true} {
				mode := "disabled"
				var options []AnalysisOption
				if enabled {
					mode, options = "enabled", []AnalysisOption{WithWorkload("benchmark")}
				}
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						analysis := Analyze(records, options...)
						if analysis.Statistics.Statements != len(records) || (analysis.Workload != nil) != enabled {
							b.Fatal("invalid aggregation")
						}
					}
				})
			}
		})
	}
}

func BenchmarkWorkloadBaselineAndComparison(b *testing.B) {
	analysis := Analyze(workloadRecords(5, 10, 1), WithWorkload("benchmark"))
	baseline, err := NewServerRUBaseline(analysis)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("baseline", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := NewServerRUBaseline(analysis); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("comparison", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := CompareServerRU(analysis, baseline); err != nil {
				b.Fatal(err)
			}
		}
	})
}
