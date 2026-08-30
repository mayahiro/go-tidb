package orm

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

type mutationBenchmarkExecutor struct {
	result sql.Result
}

func (executor mutationBenchmarkExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return executor.result, nil
}

var (
	mutationBenchmarkSQLSink      string
	mutationBenchmarkArgsSink     []any
	mutationBenchmarkAffectedSink int64
)

func BenchmarkInsertBuild(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkInsertExec(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Insert(&value)
	executor := mutationBenchmarkExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkUpsertBuild(b *testing.B) {
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Upsert(&value)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkUpdateBuildAllFields(b *testing.B) {
	value := mutationModel{ID: 8143, Name: "Ada", Amount: mutationValue{text: "12.30"}}
	query := Update(&value)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkUpdateWhereBuild(b *testing.B) {
	now := time.Date(2026, time.August, 30, 10, 0, 0, 0, time.UTC)
	query := UpdateWhere[conditionalUpdateModel](
		Set("LockOwner", "worker-a"),
		Set("LockUntil", now.Add(time.Minute)),
		Increment("RetryCount", int64(1)),
	).Where(
		Equal("ChannelID", int64(7)),
		Or(IsNull("LockUntil"), LessThanOrEqual("LockUntil", now)),
	)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkUpdateWhereExec(b *testing.B) {
	now := time.Date(2026, time.August, 30, 10, 0, 0, 0, time.UTC)
	query := UpdateWhere[conditionalUpdateModel](
		Set("LockOwner", "worker-a"),
		Set("LockUntil", now.Add(time.Minute)),
		Increment("RetryCount", int64(1)),
	).Where(
		Equal("ChannelID", int64(7)),
		Or(IsNull("LockUntil"), LessThanOrEqual("LockUntil", now)),
	)
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkRawExecConditionalUpdate(b *testing.B) {
	now := time.Date(2026, time.August, 30, 10, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(time.Minute)
	statement := "UPDATE `channel_leases` SET `lock_owner` = ?, `lock_until` = ?, `retry_count` = `retry_count` + ? WHERE `channel_id` = ? AND (`lock_until` IS NULL OR `lock_until` <= ?)"
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: 1}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = RawExec(ctx, executor, statement, "worker-a", leaseUntil, int64(1), int64(7), now)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkInsertManyBuild100Rows(b *testing.B) {
	values := make([]mutationModel, 100)
	for index := range values {
		values[index] = mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	}
	query := InsertMany(values)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkInsertManyBuild100PointerRows(b *testing.B) {
	models := make([]mutationModel, 100)
	values := make([]*mutationModel, len(models))
	for index := range models {
		models[index] = mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
		values[index] = &models[index]
	}
	query := InsertMany(values)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkInsertManyExec100Rows(b *testing.B) {
	values := make([]mutationModel, 100)
	for index := range values {
		values[index] = mutationModel{Name: "Ada", Amount: mutationValue{text: "12.30"}}
	}
	query := InsertMany(values)
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: int64(len(values))}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkUpsertManyBuild100Rows(b *testing.B) {
	values := make([]bulkMutationModel, 100)
	query := UpsertMany(values)
	var sqlText string
	var arguments []any
	var err error

	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err = query.Build()
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkSQLSink = sqlText
	mutationBenchmarkArgsSink = arguments
}

func BenchmarkInsertManyExecAutomaticSplit(b *testing.B) {
	values := make([]bulkMutationModel, maxMutationParameters/2+1)
	query := InsertMany(values)
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: int64(len(values))}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}

func BenchmarkUpsertManyExecAutomaticSplit(b *testing.B) {
	values := make([]bulkMutationModel, maxMutationParameters/2+1)
	query := UpsertMany(values)
	executor := mutationBenchmarkExecutor{result: mutationResult{rowsAffected: int64(len(values))}}
	ctx := context.Background()
	var affected int64
	var err error

	b.ReportAllocs()
	for b.Loop() {
		affected, err = query.Exec(ctx, executor)
		if err != nil {
			b.Fatal(err)
		}
	}
	mutationBenchmarkAffectedSink = affected
}
