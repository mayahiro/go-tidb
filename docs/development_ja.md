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
go run ./cmd/tidbgo lint ./examples/starter-app
```

release artifactをbuildする場合はGo linkerでversionを設定します

```sh
go build -ldflags "-X main.version=v0.1.0" ./cmd/tidbgo
```

## Package boundary

- `model`: application-owned Go structのcached offline metadata
- `orm`: offline queryとmutation構築、明示的な `database/sql` 実行、Relation loading、typed raw result scan
- `schema`: TiDB CREATE TABLE snapshotからparseするimmutable offline catalog
- `check`: shared diagnostic data typeとoffline modelおよびphysical schema check
- `migrate`: 独立したMigration tooling用に予約した境界
- `cmd/tidbgo`: CLI entry point
- `internal`: 非公開のcompiler、analysis、logging、redaction support
- `examples`: 実行可能なpublic API example
- `integration`: actual TiDB Cloud Starterを検証する独立module

`integration` moduleが[`go-sql-driver/mysql`](https://github.com/go-sql-driver/mysql) dependencyを所有し、local module replacementで現在のroot checkoutを使用します

root moduleとその利用者へtest dependencyは伝播しません

## Source解析benchmark

100個のlocal queryを含むfileについて再帰的な収集、Go parse、model index、query flow解析、diagnostic構築を計測します

```sh
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredLocalQueries$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedIndexPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedRelationTopNPatterns$' -benchmem -count=5
go test ./internal/sourcecheck -run '^$' -bench '^BenchmarkAnalyzePathHundredResolvedManyToManyRelationTopNPatterns$' -benchmem -count=5
```

offline benchmarkであり、package load、application code実行、database connection open、RU消費を行いません

temporary fixture作成はtimer開始前に完了します

2番目のworkloadはconstant pagination、order、nested predicate解析、source location、deduplication、query-pattern diagnosticを実行します

3番目はparse済みschema metadata、物理model名の解決、100個のordered-limit queryに対するshared index-prefix checkerを追加します

4番目はdirect Relation metadataを解決し、共通のrelation-first TopN compiler decisionを適用して100個のassociation index accessを照合します

5番目はpure many-to-many Relationとjunction metadataを解決し、同じcompiler decisionを適用して100個のjunction index accessを照合します

## Schema compatibility client benchmark

CREATE TABLE parseとparse済みcatalogに対する1 model compatibility checkを計測します

```sh
go test ./schema -run '^$' -bench '^BenchmarkParse$' -benchmem -count=5
go test ./check -run '^$' -bench '^BenchmarkSchema$' -benchmem -count=5
```

どちらのbenchmarkもofflineで動作し、SQL実行、connection open、actual RU消費を行いません

`BenchmarkParse` はlexical analysisとcatalog constructionを含みます

`BenchmarkSchema` はparse済みcatalogとcached model metadataを再利用します

## Query analysis client benchmark

query shape compile、neutral query check、schema-aware index prefix check、runtime artifact解析、ServerRU比較を計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryShapeIndexDiagnostics$' -benchmem -count=5
go test ./internal/querycheck -run '^$' -bench '^BenchmarkDiagnostics$' -benchmem -count=5
go test ./internal/queryshape -run '^$' -bench '^BenchmarkQueryFingerprint$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeCapturedQueryShapes$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeServerRUOneFingerprint$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkNewServerRUBaseline$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkCompareServerRU$' -benchmem -count=5
```

全てoffline benchmarkであり、SQL execution、network call、TiDB optimization、actual RU consumptionを含みません

schema-aware benchmarkはQueryShape構築と物理index prefix照合を含みます

fingerprintはevidenceが不要な場合に遅延され、独立したbenchmarkで計測します

comparison benchmarkは同じbuilderを両diagnostic pathで使用します

neutral query check benchmarkはbuilder compileを含みません

runtime benchmarkはJSON decodeとDB accessなしで100個のcaptured typed query recordを解析します

ServerRU benchmarkは1 fingerprintの1 sampleと10,000 sampleを比較し、保持bytesとallocation数がsample数へ依存しないことを確認します

baseline benchmarkは保存対象のfingerprint aggregateが1個の場合と10,000個の場合を比較します

出力自体がfingerprintごとに1 entryを持つため、このpathのmemoryはfingerprint数に応じて増えます

comparison benchmarkは一致する1件と10,000件のbaselineとcurrent fingerprint setを使い、validationとdeterministic mergeを含みますがJSON decodeとreport encodeは含みません

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

scalar terminal、slice predicate、application-selected DECIMAL type、temporal field、Relation predicateとpreload、CRUD、bulk insertとupsert、`AUTO_RANDOM`、typed raw SQL、soft delete、restore、transactionのcommitとrollback、typed SELECT EXPLAINとEXPLAIN ANALYZE、same-session ServerRU取得、rootとpreload SELECTのstatement observationを確認します

固定された18個の `tidbgo_it_*` tableを作成し、現在のrunが作成したtableだけを削除します

既存fixture tableを検出した場合は削除せず失敗します

同じdatabaseに対する複数suiteを同時実行しません

## Write compiler benchmark

単行CRUD、field指定更新、valueとpointerのbulk writeを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkMutationWrite$' -benchmem -benchtime=200ms -count=5
go test ./orm -run '^$' -bench '^BenchmarkMutationWrite$/^upsert_values$/^rows_24580$' -benchtime=3s -cpuprofile /tmp/tidbgo-write.cpu -memprofile /tmp/tidbgo-write.mem -o /tmp/tidbgo-write.test
go -C tools tool pprof -top /tmp/tidbgo-write.test /tmp/tidbgo-write.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-write.test /tmp/tidbgo-write.mem
```

offline workloadはnative scalar、nullable pointer、byte slice、time value、pointer receiverの `driver.Valuer` を含みます

100行と24,580行を対象とし、後者は8 columnのfull batch 3個と残り7行へ分割します

各operationでbuilderを作成し、warm済みmodel metadataを使用します

`Value` の実行とDB接続は行わず、compilerと引数準備のcostを計測し、driver conversion、network latency、RUは含みません

mutation planはfield accessとValuer receiverの選択、およびmodelごとに1個のdefault single-row Upsert SQLをcacheします

bulk実行は同じ行数のbatch SQLをその実行内で再利用し、batch sizeや選択fieldをkeyとするglobal cacheは保持しません

各batchのargument sliceは独立しています

## Connected write baseline

前述の専用databaseと固定iteration数を使用します

```sh
TIDBGO_TEST_DSN='<user>:<password>@tcp(<host>:4000)/tidbgo_test_ci?tls=true&parseTime=true&interpolateParams=true' \
  go -C integration test -run '^$' \
    -bench '^BenchmarkTiDBCloudStarterWrite$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

opt-in benchmarkは `tidbgo_it_write_benchmark` だけを作成し、既存tableを拒否し、自身で作成したtableをcleanup時に削除します

1個のpinned connectionを使用し、DSNの `interpolateParams` と `clientFoundRows` を維持するため、比較時はdriver設定を揃えます

32 byteと2,048 byteのJSON string payloadで、単行Insert、新規・変更・同値のUpsert、100行Insert、混在・変更・同値のBulk Upsertを計測します

tableは `AUTO_RANDOM` primary keyと別のunique keyを持ちます

trialごとにtimer外でrowをresetして同じconflictをseedし、反復Upsertが別のworkloadへ変わることを防ぎます

warm-upとRU sampleでは最終値、affected row数、既存ID、generated IDの反映contractを検証します

latencyとGo allocationにsetup、検証、RU queryを含めません

計測後の独立した3 sampleから `ServerRU/op` と `ServerRU/row` を報告し、各sampleはpinned connectionで対象statement直後に自動収集します

`statements/op` は対象DMLだけを数え、setup、seed、検証、RU収集、cleanupはこれらのmetric外で追加resourceを消費します

autocommit DMLの計測であり、明示transaction全体または請求RUを表しません

各trialは実データを書き込むためiteration数を制限してください

## EXPLAIN client benchmark

1個のtyped SELECTをcompileし、3 operatorのTiDB row-format planをscanするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplain$' -benchmem -count=5
```

local `database/sql` test driverを使い、MySQL driver、network round trip、TiDB optimization、actual RU consumptionは含みません

## EXPLAIN ANALYZE client benchmark

typed SELECTをcompileし、plan access metadataを解決してTiDB runtime planをscanするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryExplainAnalyze($|RelationAliases$)' -benchmem -count=5
```

1個目のworkloadは3個のphysical table operatorをscanします

Relation workloadは4 operatorをscanし、root、direct Relation、many-to-many junction、targetのaliasを解決します

いずれもlocal `database/sql` test driverを使い、SELECT executionとTiDB runtime costを計測せずactual RUも消費しません

取得済みplanをdiagnosticへ変換するcostは別に計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkExplainAnalyzePlanDiagnostics' -benchmem -count=5
```

clean caseはdiagnosticなし、warning caseは不完全なstatistics、大規模full scan、disk usageのevidenceを生成し、resolved access caseはphysical table、model、Relation metadataを含めます

どちらもDB I/Oを行わずtimingとRU textをparseしません

## ServerRU client benchmark

1個のServerRU valueを取得してdecodeするclient-side costを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkLastServerRU$' -benchmem -count=5
```

automatic connection pinning、1 target `RawExec`、auxiliary query、decode、通常observerまたはruntime capture deliveryを計測します

```sh
go test ./orm -run '^$' -bench '^BenchmarkRawExecWith(ServerRUCollection|RuntimeCaptureAndServerRU)$' -benchmem -count=5
```

local `database/sql` test driverを使い、該当するclient pathを含みます

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
