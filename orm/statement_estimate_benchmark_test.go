package orm

import "testing"

var (
	statementCountBenchmarkSink    int
	statementEstimateBenchmarkSink StatementCountEstimate
)

func BenchmarkInsertManyStatementCountAutomaticSplit(b *testing.B) {
	values := make([]bulkMutationModel, maxMutationParameters/2+1)
	query := InsertMany(values)
	var count int
	var err error

	b.ReportAllocs()
	for b.Loop() {
		count, err = query.StatementCount()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(count), "planned-statements/op")
	statementCountBenchmarkSink = count
}

func BenchmarkInsertManyPointerStatementCountAutomaticSplit(b *testing.B) {
	values := make([]*bulkMutationModel, maxMutationParameters/2+1)
	query := InsertMany(values)
	var count int
	var err error

	b.ReportAllocs()
	for b.Loop() {
		count, err = query.StatementCount()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(count), "planned-statements/op")
	statementCountBenchmarkSink = count
}

func BenchmarkUpsertManyStatementCountAutomaticSplit(b *testing.B) {
	values := make([]bulkMutationModel, maxMutationParameters/2+1)
	query := UpsertMany(values, "Value")
	var count int
	var err error

	b.ReportAllocs()
	for b.Loop() {
		count, err = query.StatementCount()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(count), "planned-statements/op")
	statementCountBenchmarkSink = count
}

func BenchmarkSelectQueryEstimateAllStatements(b *testing.B) {
	query := Query[preloadUser]().
		Select("ID").
		Preload("Orders.User").
		Limit(10_001)
	var estimate StatementCountEstimate
	var err error

	b.ReportAllocs()
	for b.Loop() {
		estimate, err = query.EstimateAllStatements()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(estimate.Maximum), "max-statements/op")
	statementEstimateBenchmarkSink = estimate
}
