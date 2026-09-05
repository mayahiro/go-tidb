package tidbcloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

type writeBenchmarkRow struct {
	model.Meta `tidbgo:"table=tidbgo_it_write_benchmark"`
	ID         int64 `tidbgo:",pk,auto_random"`
	K          int64
	V0         int64
	V1         bool
	V2         string
	V3         string
	V4         time.Time
}

type writeBenchmarkCase struct {
	name        string
	rows        int
	seedRows    int
	upsert      bool
	unchanged   bool
	single      bool
	batchSize   int
	transaction bool
}

func BenchmarkTiDBCloudStarterWrite(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter benchmark")
	}
	config := parseTestDSN(b, dsn)
	if !config.ParseTime {
		b.Fatal("TIDBGO_TEST_DSN must include parseTime=true for the write benchmark")
	}
	b.Logf("write transport: interpolateParams=%t clientFoundRows=%t", config.InterpolateParams, config.ClientFoundRows)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	database := openTestDatabase(b, dsn)
	verifyConnectedTarget(b, ctx, database, dsn)
	installWriteBenchmarkFixture(b, ctx, database, dsn)
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve the write benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close write benchmark connection: %s", redact.Error(err, dsn))
		}
	})
	for _, payloadBytes := range []int{32, 2048} {
		b.Run(fmt.Sprintf("payload_%d", payloadBytes), func(b *testing.B) {
			for _, test := range []writeBenchmarkCase{
				{name: "insert", rows: 1, single: true},
				{name: "upsert_new", rows: 1, single: true, upsert: true},
				{name: "upsert_changed", rows: 1, seedRows: 1, single: true, upsert: true},
				{name: "upsert_unchanged", rows: 1, seedRows: 1, single: true, upsert: true, unchanged: true},
				{name: "insert_many", rows: 100},
				{name: "upsert_many_mixed", rows: 100, seedRows: 50, upsert: true},
				{name: "upsert_many_changed", rows: 100, seedRows: 100, upsert: true},
				{name: "upsert_many_unchanged", rows: 100, seedRows: 100, upsert: true, unchanged: true},
			} {
				b.Run(test.name, func(b *testing.B) {
					benchmarkWriteCase(b, ctx, connection, dsn, test, payloadBytes, config.ClientFoundRows, "ServerRU")
				})
			}
		})
	}
}

func installWriteBenchmarkFixture(b *testing.B, ctx context.Context, database *sql.DB, dsn string) {
	b.Helper()
	_, err := database.ExecContext(ctx, `CREATE TABLE tidbgo_it_write_benchmark (
  id BIGINT NOT NULL AUTO_RANDOM PRIMARY KEY,
  k BIGINT NOT NULL,
  v0 BIGINT NOT NULL,
  v1 BOOLEAN NOT NULL,
  v2 VARCHAR(64) NOT NULL,
  v3 JSON NOT NULL,
  v4 DATETIME(6) NOT NULL,
  UNIQUE KEY write_benchmark_k (k)
)`)
	if err != nil {
		fatalDatabaseError(b, dsn, "create write benchmark table; a pre-existing table is never removed", err)
	}
	b.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := database.ExecContext(cleanupContext, "DROP TABLE tidbgo_it_write_benchmark"); err != nil {
			b.Errorf("drop write benchmark table: %s", redact.Error(err, dsn))
		}
	})
}

func makeWriteBenchmarkRows(count, payloadBytes int) []writeBenchmarkRow {
	values := make([]writeBenchmarkRow, count)
	for i := range values {
		values[i] = writeBenchmarkRow{
			K: int64(i + 1000), V0: int64(i + 2000), V1: i%2 == 0,
			V2: fmt.Sprintf("value-%08x", i),
			V3: fmt.Sprintf(`{"v":%q}`, strings.Repeat(string(rune('a'+i%26)), payloadBytes)),
			V4: time.Date(2026, time.September, 5, 0, 0, i%60, 123456000, time.UTC),
		}
	}
	return values
}

func benchmarkWriteCase(b *testing.B, ctx context.Context, connection *sql.Conn, dsn string, test writeBenchmarkCase, payloadBytes int, foundRows bool, ruMetric string) {
	b.Helper()
	values := makeWriteBenchmarkRows(test.rows, payloadBytes)
	pointers := make([]*writeBenchmarkRow, len(values))
	for i := range values {
		pointers[i] = &values[i]
	}
	execute := func(ctx context.Context) (int64, error) {
		if !test.transaction {
			return executeWriteBenchmark(ctx, connection, pointers, test)
		}
		var affected int64
		err := orm.Transaction(ctx, connection, func(tx *sql.Tx) error {
			var err error
			affected, err = executeWriteBenchmark(ctx, tx, pointers, test)
			return err
		})
		return affected, err
	}
	wantAffected := writeBenchmarkAffectedRows(test, foundRows)
	checkAffected := func(affected int64, err error) {
		b.Helper()
		if err != nil {
			fatalDatabaseError(b, dsn, "execute write benchmark", err)
		}
		if affected != wantAffected {
			b.Fatalf("affected rows = %d, want %d", affected, wantAffected)
		}
	}
	seedIDs := prepareWriteBenchmark(b, ctx, connection, dsn, values, test)
	checkAffected(execute(ctx))
	verifyWriteBenchmark(b, ctx, connection, dsn, values, seedIDs, test)

	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for range b.N {
		prepareWriteBenchmark(b, ctx, connection, dsn, values, test)
		b.StartTimer()
		affected, err := execute(ctx)
		b.StopTimer()
		checkAffected(affected, err)
	}

	// Sample separately so the extra RU round trip is absent from ns/op.
	const samples = 3
	var totalRU float64
	var metrics writeBenchmarkObservation
	wantStatements := 1
	if test.batchSize > 0 {
		wantStatements = (test.rows-1)/test.batchSize + 1
	}
	for range samples {
		seedIDs := prepareWriteBenchmark(b, ctx, connection, dsn, values, test)
		metrics = writeBenchmarkObservation{}
		observed := orm.WithStatementObserver(ctx, metrics.observe, orm.CollectServerRU())
		checkAffected(execute(observed))
		if err := metrics.validate(wantStatements, test.transaction); err != nil {
			fatalDatabaseError(b, dsn, "validate write benchmark observations", err)
		}
		totalRU += metrics.ru
		verifyWriteBenchmark(b, ctx, connection, dsn, values, seedIDs, test)
	}
	b.ReportMetric(totalRU/samples, ruMetric+"/op")
	b.ReportMetric(totalRU/samples/float64(test.rows), ruMetric+"/row")
	b.ReportMetric(float64(metrics.statements), "statements/op")
	b.ReportMetric(float64(metrics.begins+metrics.commits), "tx-controls/op")
	b.ReportMetric(float64(metrics.maxArguments), "max-args/statement")
	b.ReportMetric(float64(metrics.maxSQLBytes), "max-SQL-bytes/statement")
	b.ReportMetric(float64(test.rows), "rows/op")
}

func writeBenchmarkAffectedRows(test writeBenchmarkCase, foundRows bool) int64 {
	if !test.unchanged {
		return int64(test.rows + test.seedRows)
	}
	affected := int64(test.rows - test.seedRows)
	if foundRows {
		affected += int64(test.seedRows)
	}
	return affected
}

// executeWriteBenchmark changes batch boundaries only; the caller owns any transaction.
func executeWriteBenchmark(ctx context.Context, executor orm.ExecExecutor, values []*writeBenchmarkRow, test writeBenchmarkCase) (int64, error) {
	if test.batchSize < 0 {
		return 0, fmt.Errorf("write benchmark batch size must not be negative")
	}
	if test.single {
		if len(values) != 1 {
			return 0, fmt.Errorf("single write benchmark requires exactly one row")
		}
		if test.upsert {
			return orm.Upsert(values[0]).Exec(ctx, executor)
		}
		return orm.Insert(values[0]).Exec(ctx, executor)
	}
	batchSize := test.batchSize
	if batchSize == 0 {
		batchSize = len(values)
	}
	var total int64
	for start := 0; start < len(values); start += batchSize {
		end := min(start+batchSize, len(values))
		var affected int64
		var err error
		if test.upsert {
			affected, err = orm.UpsertMany(values[start:end]).Exec(ctx, executor)
		} else {
			affected, err = orm.InsertMany(values[start:end]).Exec(ctx, executor)
		}
		total += affected
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func prepareWriteBenchmark(b *testing.B, ctx context.Context, connection *sql.Conn, dsn string, values []writeBenchmarkRow, test writeBenchmarkCase) map[int64]int64 {
	b.Helper()
	if _, err := connection.ExecContext(ctx, "DELETE FROM tidbgo_it_write_benchmark"); err != nil {
		fatalDatabaseError(b, dsn, "reset write benchmark rows", err)
	}
	for i := range values {
		values[i].ID = 0
	}
	if test.seedRows == 0 {
		return nil
	}
	seed := append([]writeBenchmarkRow(nil), values[:test.seedRows]...)
	if !test.unchanged {
		for i := range seed {
			seed[i].V0--
		}
	}
	if _, err := orm.InsertMany(seed).Exec(ctx, connection); err != nil {
		fatalDatabaseError(b, dsn, "seed write benchmark conflicts", err)
	}
	rows, err := orm.Query[writeBenchmarkRow]().Select("ID", "K").All(ctx, connection)
	if err != nil {
		fatalDatabaseError(b, dsn, "read original write benchmark IDs", err)
	}
	ids := make(map[int64]int64, len(rows))
	for _, row := range rows {
		ids[row.K] = row.ID
	}
	return ids
}

func verifyWriteBenchmark(b *testing.B, ctx context.Context, connection *sql.Conn, dsn string, values []writeBenchmarkRow, seedIDs map[int64]int64, test writeBenchmarkCase) {
	b.Helper()
	rows, err := orm.Query[writeBenchmarkRow]().OrderBy(orm.Asc("K")).All(ctx, connection)
	if err != nil {
		fatalDatabaseError(b, dsn, "verify write benchmark result", err)
	}
	if len(rows) != len(values) {
		b.Fatalf("write rows = %d, want %d", len(rows), len(values))
	}
	for i, row := range rows {
		want := values[i]
		if row.ID <= 0 || (seedIDs[row.K] != 0 && row.ID != seedIDs[row.K]) {
			b.Fatalf("row %d did not preserve its generated identity", i)
		}
		if test.single && !test.upsert {
			if want.ID != row.ID {
				b.Fatal("single Insert did not populate AUTO_RANDOM")
			}
		} else if want.ID != 0 {
			b.Fatal("upsert or bulk write changed an input AUTO_RANDOM field")
		}
		var actualJSON, expectedJSON any
		if err := json.Unmarshal([]byte(row.V3), &actualJSON); err != nil {
			b.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want.V3), &expectedJSON); err != nil {
			b.Fatal(err)
		}
		if row.K != want.K || row.V0 != want.V0 || row.V1 != want.V1 || row.V2 != want.V2 || !row.V4.Equal(want.V4) || !reflect.DeepEqual(actualJSON, expectedJSON) {
			b.Fatalf("write benchmark row %d does not match the input", i)
		}
	}
}
