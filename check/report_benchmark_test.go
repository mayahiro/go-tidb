package check

import "testing"

var reportBenchmarkSink Report

func BenchmarkNewReport100Diagnostics(b *testing.B) {
	diagnostics := make([]Diagnostic, 100)
	for index := range diagnostics {
		code := "WRN001"
		if index%2 != 0 {
			code = "WRN002"
		}
		diagnostics[index] = Diagnostic{
			Code:         code,
			Severity:     SeverityWarning,
			Title:        "Benchmark diagnostic",
			Message:      "A deterministic warning used to measure report construction",
			Evidence:     []Evidence{{Message: "benchmark evidence"}},
			Suppressible: true,
		}
	}
	suppression := Allow("WRN001", "accepted in this benchmark")
	var report Report
	var err error

	b.ReportAllocs()
	for b.Loop() {
		report, err = NewReport(diagnostics, suppression)
		if err != nil {
			b.Fatal(err)
		}
	}
	reportBenchmarkSink = report
}
