package tidbcloud

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/orm"
)

const writeBatchBenchmarkRows = 1000

func BenchmarkTiDBCloudStarterWriteBatchSizes(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter benchmark")
	}
	config := parseTestDSN(b, dsn)
	if !config.ParseTime {
		b.Fatal("TIDBGO_TEST_DSN must include parseTime=true for the write benchmark")
	}
	b.Logf("write transport: interpolateParams=%t clientFoundRows=%t", config.InterpolateParams, config.ClientFoundRows)
	b.Log("DML-ServerRU excludes BEGIN/COMMIT and is not a transaction-total or billing metric")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	database := openTestDatabase(b, dsn)
	verifyConnectedTarget(b, ctx, database, dsn)
	var transactionMode string
	var autocommit bool
	if err := database.QueryRowContext(ctx, "SELECT @@autocommit, @@tidb_txn_mode").Scan(&autocommit, &transactionMode); err != nil {
		fatalDatabaseError(b, dsn, "read the write benchmark transaction settings", err)
	}
	if !autocommit {
		b.Fatal("write batch comparison requires session autocommit to be enabled")
	}
	b.Logf("write transaction mode: %s", transactionMode)
	installWriteBenchmarkFixture(b, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve the write batch benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close write batch benchmark connection: %s", redact.Error(err, dsn))
		}
	})
	for _, payloadBytes := range []int{32, 2048} {
		b.Run(fmt.Sprintf("payload_%d", payloadBytes), func(b *testing.B) {
			for _, mode := range []string{"autocommit", "transaction"} {
				b.Run(mode, func(b *testing.B) {
					for _, test := range []writeBenchmarkCase{
						{name: "insert", rows: writeBatchBenchmarkRows},
						{name: "upsert_new", rows: writeBatchBenchmarkRows, upsert: true},
						{name: "upsert_mixed", rows: writeBatchBenchmarkRows, seedRows: writeBatchBenchmarkRows / 2, upsert: true},
						{name: "upsert_changed", rows: writeBatchBenchmarkRows, seedRows: writeBatchBenchmarkRows, upsert: true},
						{name: "upsert_unchanged", rows: writeBatchBenchmarkRows, seedRows: writeBatchBenchmarkRows, upsert: true, unchanged: true},
					} {
						test.transaction = mode == "transaction"
						b.Run(test.name, func(b *testing.B) {
							for _, size := range []int{100, 500, writeBatchBenchmarkRows} {
								test.batchSize = size
								b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
									benchmarkWriteCase(b, ctx, connection, dsn, test, payloadBytes, config.ClientFoundRows, "DML-ServerRU")
								})
							}
						})
					}
				})
			}
		})
	}
}

// The compiler comparison uses the same data and batching loop without a driver or DB.
func BenchmarkWriteBatchCompiler(b *testing.B) {
	ctx := context.Background()
	for _, payloadBytes := range []int{32, 2048} {
		values := makeWriteBenchmarkRows(writeBatchBenchmarkRows, payloadBytes)
		pointers := make([]*writeBenchmarkRow, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		for _, upsert := range []bool{false, true} {
			for _, size := range []int{100, 500, writeBatchBenchmarkRows} {
				b.Run(fmt.Sprintf("payload_%d/upsert_%t/batch_%d", payloadBytes, upsert, size), func(b *testing.B) {
					test := writeBenchmarkCase{rows: len(values), batchSize: size, upsert: upsert}
					executor := discardWriteBenchmarkExecutor{}
					if _, err := executeWriteBenchmark(ctx, executor, pointers, test); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					for b.Loop() {
						if _, err := executeWriteBenchmark(ctx, executor, pointers, test); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

type discardWriteBenchmarkExecutor struct{}

func (discardWriteBenchmarkExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return driver.RowsAffected(1), nil
}

type writeBenchmarkObservation struct {
	statements   int
	begins       int
	commits      int
	maxArguments int
	maxSQLBytes  int
	ru           float64
	err          error
}

func (metrics *writeBenchmarkObservation) observe(event orm.StatementEvent) {
	if metrics.err != nil {
		return
	}
	if event.Error != nil {
		metrics.err = event.Error
		return
	}
	switch event.Operation {
	case orm.StatementBegin:
		if metrics.statements != 0 || metrics.begins != 0 || metrics.commits != 0 {
			metrics.err = fmt.Errorf("unexpected BEGIN order in write benchmark")
		}
		metrics.begins++
		return
	case orm.StatementCommit:
		if metrics.begins != 1 || metrics.statements == 0 || metrics.commits != 0 {
			metrics.err = fmt.Errorf("unexpected COMMIT order in write benchmark")
		}
		metrics.commits++
		return
	case orm.StatementInsert, orm.StatementUpsert:
		if metrics.commits != 0 {
			metrics.err = fmt.Errorf("unexpected DML after COMMIT in write benchmark")
			return
		}
	default:
		metrics.err = fmt.Errorf("unexpected write benchmark operation %s", event.Operation)
		return
	}
	metrics.statements++
	metrics.maxArguments = max(metrics.maxArguments, event.ArgumentCount)
	metrics.maxSQLBytes = max(metrics.maxSQLBytes, len(event.SQL))
	if event.ServerRU != nil && event.ServerRU.Error != nil {
		metrics.err = event.ServerRU.Error
		return
	}
	if event.ServerRU == nil || !event.ServerRU.Known {
		metrics.err = fmt.Errorf("write benchmark requires a known ServerRU sample for every DML statement")
		return
	}
	metrics.ru += event.ServerRU.Value
}

func (metrics writeBenchmarkObservation) validate(statements int, transaction bool) error {
	if metrics.err != nil {
		return metrics.err
	}
	wantControls := 0
	if transaction {
		wantControls = 1
	}
	if metrics.statements != statements || metrics.begins != wantControls || metrics.commits != wantControls {
		return fmt.Errorf("write benchmark observed DML/BEGIN/COMMIT = %d/%d/%d, want %d/%d/%d", metrics.statements, metrics.begins, metrics.commits, statements, wantControls, wantControls)
	}
	return nil
}
