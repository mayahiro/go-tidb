package tidbcloud

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

const (
	preloadBatchParentCount = 10000
	preloadBatchAliasStride = 5
	preloadBatchInsertSize  = 1000
)

type preloadBatchParent struct {
	model.Meta `tidbgo:"table=tidbgo_it_preload_parents"`
	ID         int64 `tidbgo:",pk"`
	Name       string
	Aliases    []preloadBatchAlias `tidbgo:"has_many,join=ID:ParentID"`
}

type preloadBatchAlias struct {
	model.Meta `tidbgo:"table=tidbgo_it_preload_aliases"`
	ID         int64 `tidbgo:",pk"`
	ParentID   int64
	Name       string
}

func BenchmarkTiDBCloudStarterPreloadBatchSizes(b *testing.B) {
	baseDSN := os.Getenv(testDSNEnvironment)
	if baseDSN == "" {
		b.Skip("TIDBGO_TEST_DSN is not set; skipping the connected TiDB Cloud Starter benchmark")
	}
	baseConfig := parseTestDSN(b, baseDSN)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fixtureDatabase := openTestDatabase(b, baseDSN)
	if err := fixtureDatabase.PingContext(ctx); err != nil {
		fatalDatabaseError(b, baseDSN, "connect for preload batch benchmark fixture", err)
	}
	verifyConnectedTarget(b, ctx, fixtureDatabase, baseDSN)
	installPreloadBatchFixture(b, ctx, fixtureDatabase, baseDSN)

	transports := []struct {
		name        string
		interpolate bool
	}{
		{name: "prepared", interpolate: false},
		{name: "interpolated", interpolate: true},
	}
	for _, transport := range transports {
		b.Run(transport.name, func(b *testing.B) {
			config := baseConfig.Clone()
			config.InterpolateParams = transport.interpolate
			benchmarkPreloadBatchTransport(b, ctx, config)
		})
	}
}

func benchmarkPreloadBatchTransport(b *testing.B, ctx context.Context, config *mysql.Config) {
	b.Helper()

	dsn := config.FormatDSN()
	database := openTestDatabase(b, dsn)
	if err := database.PingContext(ctx); err != nil {
		fatalDatabaseError(b, dsn, "connect for preload batch benchmark", err)
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		fatalDatabaseError(b, dsn, "reserve the preload batch benchmark connection", err)
	}
	b.Cleanup(func() {
		if err := connection.Close(); err != nil {
			b.Errorf("close the preload batch benchmark connection: %v", err)
		}
	})
	executor := &queryMetricExecutor{connection: connection}

	b.Run("orm_current", func(b *testing.B) {
		benchmarkPreloadBatchStrategy(b, ctx, dsn, executor, 0, func() ([]preloadBatchParent, error) {
			return preloadBatchORMQuery().All(ctx, executor)
		})
	})
	b.Run("orm_constrained", func(b *testing.B) {
		benchmarkPreloadBatchStrategy(b, ctx, dsn, executor, 0, func() ([]preloadBatchParent, error) {
			return preloadBatchORMQuery().Limit(preloadBatchParentCount).All(ctx, executor)
		})
	})
	b.Run("manual_all", func(b *testing.B) {
		benchmarkPreloadBatchStrategy(b, ctx, dsn, executor, 0, func() ([]preloadBatchParent, error) {
			return loadAllPreloadBatchRelations(ctx, executor)
		})
	})
	for _, batchSize := range []int{500, 1000, 2000, 5000, 10000} {
		b.Run(fmt.Sprintf("manual_%d", batchSize), func(b *testing.B) {
			benchmarkPreloadBatchStrategy(b, ctx, dsn, executor, batchSize, func() ([]preloadBatchParent, error) {
				return loadPreloadBatchManually(ctx, executor, batchSize)
			})
		})
	}
}

func benchmarkPreloadBatchStrategy(
	b *testing.B,
	ctx context.Context,
	dsn string,
	executor *queryMetricExecutor,
	batchSize int,
	load func() ([]preloadBatchParent, error),
) {
	b.Helper()

	executor.reset(false)
	parents, err := load()
	if err != nil {
		fatalDatabaseError(b, dsn, "warm the preload batch benchmark", err)
	}
	validatePreloadBatchResult(b, parents)
	warmStatements := executor.statements
	warmQueryBytes := executor.queryBytes
	warmParameters := executor.parameters
	b.ReportAllocs()

	b.ResetTimer()
	for range b.N {
		executor.reset(false)
		parents, err = load()
		if err != nil {
			fatalDatabaseError(b, dsn, "execute the preload batch benchmark", err)
		}
		if executor.statements != warmStatements || executor.queryBytes != warmQueryBytes || executor.parameters != warmParameters {
			b.Fatalf(
				"preload metrics = %d statements, %d query bytes, %d parameters, want %d, %d, %d",
				executor.statements,
				executor.queryBytes,
				executor.parameters,
				warmStatements,
				warmQueryBytes,
				warmParameters,
			)
		}
		validatePreloadBatchResult(b, parents)
	}
	b.StopTimer()

	executor.reset(true)
	parents, err = load()
	if err != nil {
		fatalDatabaseError(b, dsn, "sample preload batch benchmark RU", err)
	}
	if err := executor.finishRU(ctx); err != nil {
		fatalDatabaseError(b, dsn, "finish preload batch benchmark RU", err)
	}
	validatePreloadBatchResult(b, parents)
	b.ReportMetric(executor.ru, "RU/op")
	b.ReportMetric(float64(warmStatements), "statements/op")
	b.ReportMetric(float64(warmQueryBytes), "query-bytes/op")
	b.ReportMetric(float64(warmParameters), "parameters/op")
	if batchSize != 0 {
		b.ReportMetric(float64(batchSize), "keys/batch")
	}
}

func preloadBatchORMQuery() *orm.SelectQuery[preloadBatchParent] {
	return orm.Query[preloadBatchParent]().
		Select("ID", "Name").
		Preload("Aliases", orm.PreloadOrderBy(orm.Asc("ID"))).
		OrderBy(orm.Asc("ID"))
}

func loadPreloadBatchManually(ctx context.Context, executor orm.QueryExecutor, batchSize int) ([]preloadBatchParent, error) {
	parents, indexes, err := loadPreloadBatchParents(ctx, executor)
	if err != nil {
		return nil, err
	}

	for start := 0; start < len(parents); start += batchSize {
		end := min(start+batchSize, len(parents))
		arguments := make([]any, end-start)
		for index := start; index < end; index++ {
			arguments[index-start] = parents[index].ID
		}
		aliases, queryErr := orm.Raw[preloadBatchAlias](preloadBatchAliasSQL(end-start), arguments...).All(ctx, executor)
		if queryErr != nil {
			return nil, queryErr
		}
		for _, alias := range aliases {
			parentIndex, ok := indexes[alias.ParentID]
			if !ok {
				return nil, fmt.Errorf("preload batch alias %d has unrequested parent %d", alias.ID, alias.ParentID)
			}
			parents[parentIndex].Aliases = append(parents[parentIndex].Aliases, alias)
		}
	}
	return parents, nil
}

func loadAllPreloadBatchRelations(ctx context.Context, executor orm.QueryExecutor) ([]preloadBatchParent, error) {
	parents, indexes, err := loadPreloadBatchParents(ctx, executor)
	if err != nil {
		return nil, err
	}
	aliases, err := orm.Raw[preloadBatchAlias](
		"SELECT `id`, `parent_id`, `name` FROM `tidbgo_it_preload_aliases` ORDER BY `id` ASC",
	).All(ctx, executor)
	if err != nil {
		return nil, err
	}
	for _, alias := range aliases {
		parentIndex, ok := indexes[alias.ParentID]
		if !ok {
			continue
		}
		parents[parentIndex].Aliases = append(parents[parentIndex].Aliases, alias)
	}
	return parents, nil
}

func loadPreloadBatchParents(ctx context.Context, executor orm.QueryExecutor) ([]preloadBatchParent, map[int64]int, error) {
	parents, err := orm.Query[preloadBatchParent]().
		Select("ID", "Name").
		OrderBy(orm.Asc("ID")).
		All(ctx, executor)
	if err != nil {
		return nil, nil, err
	}
	indexes := make(map[int64]int, len(parents))
	for index := range parents {
		indexes[parents[index].ID] = index
	}
	return parents, indexes, nil
}

func preloadBatchAliasSQL(count int) string {
	const prefix = "SELECT `id`, `parent_id`, `name` FROM `tidbgo_it_preload_aliases` WHERE `parent_id` IN ("
	const suffix = ") ORDER BY `id` ASC"

	var query strings.Builder
	query.Grow(len(prefix) + len(suffix) + count*3)
	query.WriteString(prefix)
	for index := range count {
		if index != 0 {
			query.WriteString(", ")
		}
		query.WriteByte('?')
	}
	query.WriteString(suffix)
	return query.String()
}

func validatePreloadBatchResult(b *testing.B, parents []preloadBatchParent) {
	b.Helper()
	if len(parents) != preloadBatchParentCount {
		b.Fatalf("preload parent count = %d, want %d", len(parents), preloadBatchParentCount)
	}
	aliasCount := 0
	for index := range parents {
		aliasCount += len(parents[index].Aliases)
	}
	wantAliases := (preloadBatchParentCount + preloadBatchAliasStride - 1) / preloadBatchAliasStride
	if aliasCount != wantAliases {
		b.Fatalf("preload alias count = %d, want %d", aliasCount, wantAliases)
	}
}

func installPreloadBatchFixture(b *testing.B, ctx context.Context, database *sql.DB, dsn string) {
	b.Helper()

	tables := []fixtureTable{
		{
			name: "tidbgo_it_preload_parents",
			create: `CREATE TABLE tidbgo_it_preload_parents (
  id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (id)
)`,
			drop: "DROP TABLE tidbgo_it_preload_parents",
		},
		{
			name: "tidbgo_it_preload_aliases",
			create: `CREATE TABLE tidbgo_it_preload_aliases (
  id BIGINT NOT NULL,
  parent_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (id),
  KEY tidbgo_it_preload_aliases_parent_id (parent_id)
)`,
			drop: "DROP TABLE tidbgo_it_preload_aliases",
		},
	}
	created := make([]fixtureTable, 0, len(tables))
	b.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for index := len(created) - 1; index >= 0; index-- {
			if _, err := database.ExecContext(cleanupContext, created[index].drop); err != nil {
				b.Errorf("drop preload batch fixture table %s: %s", created[index].name, redact.Error(err, dsn))
			}
		}
	})
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, table.create); err != nil {
			fatalDatabaseError(b, dsn, "create preload batch fixture table "+table.name+"; the benchmark never removes a pre-existing table", err)
		}
		created = append(created, table)
	}
	insertPreloadBatchParents(b, ctx, database, dsn)
	insertPreloadBatchAliases(b, ctx, database, dsn)
}

func insertPreloadBatchParents(b *testing.B, ctx context.Context, database *sql.DB, dsn string) {
	b.Helper()

	for start := 0; start < preloadBatchParentCount; start += preloadBatchInsertSize {
		end := min(start+preloadBatchInsertSize, preloadBatchParentCount)
		var query strings.Builder
		query.WriteString("INSERT INTO `tidbgo_it_preload_parents` (`id`, `name`) VALUES ")
		arguments := make([]any, 0, (end-start)*2)
		for index := start; index < end; index++ {
			if index != start {
				query.WriteString(", ")
			}
			query.WriteString("(?, ?)")
			arguments = append(arguments, int64(index+1), "parent")
		}
		if _, err := database.ExecContext(ctx, query.String(), arguments...); err != nil {
			fatalDatabaseError(b, dsn, "insert preload batch parent rows", err)
		}
	}
}

func insertPreloadBatchAliases(b *testing.B, ctx context.Context, database *sql.DB, dsn string) {
	b.Helper()

	aliasCount := (preloadBatchParentCount + preloadBatchAliasStride - 1) / preloadBatchAliasStride
	for start := 0; start < aliasCount; start += preloadBatchInsertSize {
		end := min(start+preloadBatchInsertSize, aliasCount)
		var query strings.Builder
		query.WriteString("INSERT INTO `tidbgo_it_preload_aliases` (`id`, `parent_id`, `name`) VALUES ")
		arguments := make([]any, 0, (end-start)*3)
		for index := start; index < end; index++ {
			if index != start {
				query.WriteString(", ")
			}
			query.WriteString("(?, ?, ?)")
			arguments = append(arguments, int64(index+1), int64(index*preloadBatchAliasStride+1), "alias")
		}
		if _, err := database.ExecContext(ctx, query.String(), arguments...); err != nil {
			fatalDatabaseError(b, dsn, "insert preload batch alias rows", err)
		}
	}
}
