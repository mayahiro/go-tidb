package runtimecapture

import "github.com/mayahiro/go-tidb/internal/querycheck"

func (analyzer *analyzer) analyzeMutation(record Record) {
	analyzer.analysis.Statistics.MutationShapeStatements++
	if !analyzer.configuration.schemaEnabled {
		return
	}
	analyzer.analysis.Statistics.SchemaCheckedStatements++
	if analyzer.mutationPatterns == nil {
		analyzer.mutationPatterns = make(map[string]querycheck.MutationIndexStatus)
	}
	status, exists := analyzer.mutationPatterns[record.Fingerprint]
	if !exists {
		result := querycheck.MutationIndexDiagnostics(*record.Mutation, analyzer.configuration.catalog)
		status = result.Status
		analyzer.mutationPatterns[record.Fingerprint] = status
		analyzer.appendQueryDiagnostics(record, result.Diagnostics, true)
	}
	switch status {
	case querycheck.MutationIndexChecked:
		analyzer.analysis.Statistics.MutationIndexCheckedStatements++
	case querycheck.MutationIndexUncertain:
		analyzer.analysis.Statistics.MutationIndexUncertainStatements++
	}
}
