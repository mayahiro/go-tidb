package orm

import "github.com/mayahiro/go-tidb/check"

var queryDiagnosticSink []check.Diagnostic

func queryDiagnosticCodes(diagnostics []check.Diagnostic) []string {
	codes := make([]string, len(diagnostics))
	for index := range diagnostics {
		codes[index] = diagnostics[index].Code
	}
	return codes
}
