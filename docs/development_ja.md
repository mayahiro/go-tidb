# Development

[English](development.md)

このguideは `go-tidb` contributor向けのcommand、repository構成、integration test設定、benchmark手順を記載します

## Local check

repository rootからofflineで完結する全確認を実行します

```sh
go -C tools tool goimports -w ..
go test ./...
go -C integration test ./...
go vet ./...
go -C integration vet ./...
go build ./...
go -C integration build ./...
```

root test commandはnested `integration` moduleへ入りません

## CLI development

checkoutから現在のcommandを直接実行します

```sh
go run ./cmd/tidbgo version
go run ./examples/starter-app/cmd/check | go run ./cmd/tidbgo check
```

release artifactをbuildする場合はGo linkerでversionを設定します

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/tidbgo
```

## Package boundary

- `model`: application-owned Go structのcached offline metadata
- `orm`: offline queryとmutation構築、明示的な `database/sql` 実行、Relation loading、typed raw result scan
- `schema`: TiDB CREATE TABLE snapshotからparseするimmutable offline catalog
- `check`: shared diagnostic data type、reason付きreportとsuppression、offline model、query、physical schema check
- `migrate`: 独立したMigration tooling用に予約した境界
- `cmd/tidbgo`: CLI entry point
- `internal`: 非公開のloggingとredaction support
- `examples`: 実行可能なpublic API example
- `integration`: actual TiDB Cloud Starterを検証する独立module

`integration` moduleが[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql) dependencyを所有し、local module replacementで現在のroot checkoutを使用します

root moduleとその利用者へtest dependencyは伝播しません

## Schema compatibility client benchmark

CREATE TABLE parseとparse済みcatalogに対する1 model compatibility checkを計測します

```sh
go test ./schema -run '^$' -bench '^BenchmarkParse$' -benchmem -count=5
go test ./check -run '^$' -bench '^BenchmarkSchema$' -benchmem -count=5
```

どちらのbenchmarkもofflineで動作し、SQL実行、connection open、actual RU消費を行いません

`BenchmarkParse` はlexical analysisとcatalog constructionを含みます

`BenchmarkSchema` はparse済みcatalogとcached model metadataを再利用します

## TiDB Cloud Starter integration test

connected suiteはopt-inです

`TIDBGO_TEST_DSN` がない場合はconnected testだけをskipし、driverとtest harnessはcompileします

小文字のdatabase名が `tidbgo_test_` で始まる空の専用databaseとTLS DSNを使用します

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -count=1 ./tidbcloud
```

suiteはendpointがTiDBを名乗ることを検証します

Starter endpointであることはenvironment ownerが保証し、[TiDB Cloud Starter connection requirements](https://docs.pingcap.com/tidbcloud/connect-to-tidb-cluster-serverless/?plan=starter)に従います

fixtureは `DATETIME(6)` を `time.Time` へscanするため `parseTime=true` が必要です

同じscanが `parseTime=false` では失敗することも確認します

現行の短命なparameterized query workloadでは `interpolateParams=true` を使います

interpolationを使わない場合、driverはcallごとにstatementのprepare、execute、closeを行います

明示的にprepareして再利用するstatementは異なるworkloadです

driver documentationはSQL injection riskを理由に、interpolationとBIG5、CP932、GB2312、GBK、SJISを併用しないよう求めています

suiteのconnection character setは `utf8mb4` のまま使用します

driverの[`interpolateParams` documentation](https://github.com/go-sql-driver/mysql/blob/v1.10.0/README.md#interpolateparams)も参照してください

suiteはconnection poolを1 connectionに制限します

scalar terminal、slice predicate、application-selected DECIMAL type、temporal field、Relation predicateとpreload、CRUD、bulk insertとupsert、`AUTO_RANDOM`、typed raw SQL、soft delete、restore、transactionのcommitとrollback、typed SELECT EXPLAINとEXPLAIN ANALYZE、same-session ServerRU取得、rootとpreload SELECTをまとめるoperation debug reportを確認します

固定された18個の `tidbgo_it_*` tableを作成し、現在のrunが作成したtableだけを削除します

既存fixture tableを検出した場合は削除せず失敗します

同じdatabaseに対する複数suiteを同時実行しません

## Debug report client benchmark

完了した2 statement eventをまとめるclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkDebugReportTwoStatements$' -benchmem -count=5
```

local mutation executorを使い、MySQL driver、network call、TiDB execution、actual RU consumptionは含みません

## EXPLAIN client benchmark

1個のtyped SELECTをcompileし、3 operatorのTiDB row-format planをscanするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplain$' -benchmem -count=5
```

local `database/sql` test driverを使い、MySQL driver、network round trip、TiDB optimization、actual RU consumptionは含みません

## EXPLAIN ANALYZE client benchmark

1個のtyped SELECTをcompileし、3 operatorのTiDB runtime planをscanするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplainAnalyze$' -benchmem -count=5
```

local `database/sql` test driverを使い、SELECT executionとTiDB runtime costを計測せずactual RUも消費しません

## ServerRU client benchmark

1個のServerRU valueを取得してdecodeするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkLastServerRU$' -benchmem -count=5
```

local `database/sql` test driverを使い、`database/sql` row pathとJSON decodeを含みます

MySQL driver、network round trip、TiDB execution、actual RU consumptionは含みません

## Driver transport benchmark

同じStarter point queryで `interpolateParams` の両modeを比較します

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterInterpolateParams$' \
    -benchmem -benchtime=20x -count=5 ./tidbcloud
```

benchmarkはDSNを出力せず両modeを派生し、modeごとに1 connectionを使います

latency、Go allocation、timer外の `@@tidb_last_query_info.ru_consumption` sample 5個を報告します

結果にはnetworkとStarterの変動が含まれるため、portableな性能保証や請求RU計測として扱いません

## Relation graph benchmark

同じ専用databaseで代表Relation graphを計測します

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterPreloadRelationGraph$' \
    -benchmem -benchtime=5x -count=5 ./tidbcloud
```

benchmarkは5個のinline to-one joinを持つparent SELECT、nested to-one joinを持つmany-to-many batch、nested to-one joinを持つhas-many batchの正確に3 application statementを検証します

1本のpinned connectionを使い、elapsed time、Go allocation、statement単位のsampled RUをoperationごとに合計します

setup、RU sampling query、cleanupは計測時間とapplication statement countに含めません
