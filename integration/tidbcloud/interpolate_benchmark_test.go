package tidbcloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

const interpolateBenchmarkQuery = "SELECT id FROM tidbgo_it_users WHERE email = ?"

type lastQueryInfo struct {
	RUConsumption *float64 `json:"ru_consumption"`
}

func BenchmarkTiDBCloudStarterInterpolateParams(b *testing.B) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter benchmark")
	}
	config := parseTestDSN(b, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fixtureDatabase := openTestDatabase(b, dsn)
	if err := fixtureDatabase.PingContext(ctx); err != nil {
		fatalDatabaseError(b, dsn, "connect to TiDB Cloud Starter", err)
	}
	verifyConnectedTarget(b, ctx, fixtureDatabase, dsn)
	installFixture(b, ctx, fixtureDatabase, dsn)

	benchmarks := []struct {
		name        string
		interpolate bool
	}{
		{name: "prepared", interpolate: false},
		{name: "interpolated", interpolate: true},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			benchmarkInterpolateParams(b, ctx, config, benchmark.interpolate)
		})
	}
}

func benchmarkInterpolateParams(b *testing.B, ctx context.Context, baseConfig *mysql.Config, interpolate bool) {
	b.Helper()

	config := baseConfig.Clone()
	config.InterpolateParams = interpolate
	dsn := config.FormatDSN()
	database := openTestDatabase(b, dsn)
	if err := database.PingContext(ctx); err != nil {
		fatalDatabaseError(b, dsn, "connect for interpolateParams benchmark", err)
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve the benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close the benchmark connection: %v", err)
		}
	})

	for range 3 {
		if err := executeInterpolateBenchmarkQuery(ctx, connection); err != nil {
			fatalDatabaseError(b, dsn, "warm the interpolateParams benchmark", err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := executeInterpolateBenchmarkQuery(ctx, connection); err != nil {
			fatalDatabaseError(b, dsn, "execute the interpolateParams benchmark", err)
		}
	}
	b.StopTimer()

	const samples = 5
	var totalRU float64
	for range samples {
		if err := executeInterpolateBenchmarkQuery(ctx, connection); err != nil {
			fatalDatabaseError(b, dsn, "sample benchmark RU", err)
		}
		ru, err := lastStatementRU(ctx, connection)
		if err != nil {
			fatalDatabaseError(b, dsn, "read benchmark RU", err)
		}
		totalRU += ru
	}
	b.ReportMetric(totalRU/samples, "RU/op")
}

func executeInterpolateBenchmarkQuery(ctx context.Context, connection *sql.Conn) error {
	var id int64
	if err := connection.QueryRowContext(ctx, interpolateBenchmarkQuery, "ada@example.test").Scan(&id); err != nil {
		return err
	}
	if id != 1 {
		return fmt.Errorf("interpolateParams benchmark ID = %d, want 1", id)
	}
	return nil
}

func lastStatementRU(ctx context.Context, connection *sql.Conn) (float64, error) {
	var raw string
	if err := connection.QueryRowContext(ctx, "SELECT @@tidb_last_query_info").Scan(&raw); err != nil {
		return 0, err
	}
	var info lastQueryInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return 0, err
	}
	if info.RUConsumption == nil {
		return 0, fmt.Errorf("tidb_last_query_info did not contain ru_consumption")
	}
	return *info.RUConsumption, nil
}
