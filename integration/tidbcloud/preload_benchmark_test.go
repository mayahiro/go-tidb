package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

type queryMetricExecutor struct {
	connection *sql.Conn
	statements int
	queryBytes int64
	parameters int64
	sampleRU   bool
	pendingRU  bool
	ru         float64
}

func (executor *queryMetricExecutor) QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error) {
	if executor.sampleRU && executor.pendingRU {
		if err := executor.collectPendingRU(ctx); err != nil {
			return nil, err
		}
	}
	rows, err := executor.connection.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	executor.statements++
	executor.queryBytes += int64(len(query))
	executor.parameters += int64(len(arguments))
	executor.pendingRU = executor.sampleRU
	return rows, nil
}

func (executor *queryMetricExecutor) reset(sampleRU bool) {
	executor.statements = 0
	executor.queryBytes = 0
	executor.parameters = 0
	executor.sampleRU = sampleRU
	executor.pendingRU = false
	executor.ru = 0
}

func (executor *queryMetricExecutor) finishRU(ctx context.Context) error {
	if !executor.pendingRU {
		return nil
	}
	return executor.collectPendingRU(ctx)
}

func (executor *queryMetricExecutor) collectPendingRU(ctx context.Context) error {
	ru, err := lastStatementRU(ctx, executor.connection)
	if err != nil {
		return fmt.Errorf("read statement RU: %w", err)
	}
	executor.ru += ru
	executor.pendingRU = false
	return nil
}

func BenchmarkTiDBCloudStarterPreloadRelationGraph(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter benchmark")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	database := openTestDatabase(b, dsn)
	if err := database.PingContext(ctx); err != nil {
		fatalDatabaseError(b, dsn, "connect for relation graph benchmark", err)
	}
	verifyConnectedTarget(b, ctx, database, dsn)
	installFixture(b, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve the relation graph benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close the relation graph benchmark connection: %v", err)
		}
	})
	executor := &queryMetricExecutor{connection: connection}

	for range 3 {
		executor.reset(false)
		if _, err := relationGraphQuery().Only(ctx, executor); err != nil {
			fatalDatabaseError(b, dsn, "warm the relation graph benchmark", err)
		}
		if executor.statements != 3 {
			b.Fatalf("warm relation graph statement count = %d, want 3", executor.statements)
		}
	}

	b.ReportAllocs()
	b.ReportMetric(3, "statements/op")
	b.ResetTimer()
	for range b.N {
		executor.reset(false)
		if _, err := relationGraphQuery().Only(ctx, executor); err != nil {
			fatalDatabaseError(b, dsn, "execute the relation graph benchmark", err)
		}
		if executor.statements != 3 {
			b.Fatalf("relation graph statement count = %d, want 3", executor.statements)
		}
	}
	b.StopTimer()

	const samples = 5
	var totalRU float64
	for range samples {
		executor.reset(true)
		if _, err := relationGraphQuery().Only(ctx, executor); err != nil {
			fatalDatabaseError(b, dsn, "sample relation graph benchmark RU", err)
		}
		if err := executor.finishRU(ctx); err != nil {
			fatalDatabaseError(b, dsn, "finish relation graph benchmark RU", err)
		}
		if executor.statements != 3 {
			b.Fatalf("RU sample statement count = %d, want 3", executor.statements)
		}
		totalRU += executor.ru
	}
	b.ReportMetric(totalRU/samples, "RU/op")
}
