# Development

[English](development.md)

このguideは `go-tidb` contributor向けのcommand、repository構成、integration test設定、benchmark手順を記載します

## 集計とTiFlashの検証

[集計API](aggregates_ja.md)のoffline testはgrouping、alias、NULLと型変換error、結果の所有権、hintの競合、未知のplan task、警告回収の失敗、connectionとcallbackの順序を確認します

```sh
go test ./orm -run '^TestAggregate|^TestPlanTask|^TestScanAll'
go test ./orm -run '^$' -bench '^BenchmarkAggregate$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregatePeriod$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateConditional$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateRelation$' -benchmem -benchtime=100ms -count=3
go test ./orm -run '^$' -bench '^BenchmarkAggregateComparison(Manual)?$' -benchmem -benchtime=100ms -count=3
```

benchmarkは同じSQLと結果値を使い、同じlocal test driverで集計の `ScanAll`、typed `Raw`、直接の `database/sql` collectorを比較します
出力group数は0、1、100、10,000です
TiDB、network、driverのargument変換、RUは測定しません
固定raw queryに対し、集計SQLの構築と結果mapping検証のcallごとの処理が加わります

`BenchmarkAggregatePeriod` は同じgroup数で、期間keyへのHAVINGを含む `Date` と `YearMonth` を、同等のtyped rawと直接collectorで比較します
すべて同じSQLと結果型を使い、local driverは日時式を評価しません
日別経路のprofileには `-bench '^BenchmarkAggregatePeriod$/^date$/^rows_100$/^aggregate$'` を使い、代替経路では `raw` を指定します

`BenchmarkAggregateConditional` は `BenchmarkAggregate` と同じgroup数と結果型を使い、条件付き件数／合計とHAVINGでの条件付き出力の再参照を含みます
代替方式は同じSQLとbind値を使い、driverはSQL条件を評価しません
profileには `-bench '^BenchmarkAggregateConditional$/^rows_100$/^aggregate$'` を使い、`raw` と比較します

`BenchmarkAggregateRelation` は入れ子の `Has` を含む集計を、同じ0／1／100／10,000 group、SQL、bind値、結果型でaggregate／raw／直接collectorと比較します
driverは関連先の検索を実行しません
profileには以下のcommandで `-bench '^BenchmarkAggregateRelation$/^rows_100$/^aggregate$'` と対応する `raw` 経路を指定します

比較benchmarkは同じ0／1／100／10,000行のdriver data、21組のSELECT／RU取得、3組のplan／警告を使います
代替となる手書き方式はtypedな `ScanAll` と結果照合を使い、`Compare` は入力固定、loop前のcompile、raw結果bufferの再利用、sample統計を含みます
診断全体の処理を測定し、通常ORM queryの性能変化や同じ結果mappingのcostを測るものではありません

代表経路のprofileを取得し、`raw` または `database_sql` と比較できます

```sh
aggregate_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkAggregate$/^rows_100$/^aggregate$' -benchtime=2s -cpuprofile "$aggregate_profile_dir/cpu" -memprofile "$aggregate_profile_dir/mem" -o "$aggregate_profile_dir/orm.test"
go -C tools tool pprof -top "$aggregate_profile_dir/orm.test" "$aggregate_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$aggregate_profile_dir/orm.test" "$aggregate_profile_dir/mem"
```

比較のprofileは `-bench '^BenchmarkAggregateComparison$/^rows_100$'` と `-bench '^BenchmarkAggregateComparisonManual$/^rows_100$'` を別fileへ出力し、同じprofile commandで確認します
確認後は自分で作成した一時profile directoryを削除してください

後述の専用database用に `TIDBGO_TEST_DSN` を設定して実行します

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterTiFlash$' -count=1 -v
```

このopt-in testは自分が所有する `tidbgo_it_tiflash_aggregates` tableを作り、NULLを含むDECIMALの20,000行を投入し、statisticsを解析してTiFlash replicaを2つ要求し、初期 `AVAILABLE=1` を待ちます
既存tableは削除しません。作成したtableは失敗時もcleanupします

固定した `aggregate_v1` datasetで、広いscan、主キーの20行range、選択性の高いsecondary index、20,000出力groupを比較します
Auto、TiKV、TiFlash MPPに対し、手書きSQLと集計builderを使います
両実装を2回warm-upし、3 roundでengineと実装の順序を交代します
全結果値、NULL、順序の一致を要求します
latencyはrowsのcloseまでを測定し、同じconnectionで直後に読むRU probeはその区間に含めません
警告とruntime planは別の明示的なplan実行から取得します
普遍的なlatency／RU閾値は設けません。free planの制約、cache、statistics、network、共有serviceの負荷が測定へ影響します

各workloadでは公開 `Compare` も2回warmupし、5 round測定します
全結果の照合と、3 variantのexportから既存baseline analyzerへの連携を確認します
レプリカ欠如時は未完了statusと警告の保持を確認します
offlineの比較testでは値と順序の変化、Valuerの固定、float許容誤差、driver数値型、行数上限、cancel、部分report、capture writerのerrorも確認します

レプリカ不在の警告、空入力、nullable結果、7種類の集計関数、HAVINGとpaging、sourceのsoft-delete、physical tableの解決、MPPのsession設定復元も確認します
通常実行とEXPLAIN ANALYZEは別の観測です
sampleは請求RUやTiFlashが速い・安いことの保証ではありません
レプリカ準備、storage、seed、warm-up、probe、cleanupは報告するSELECT測定外のcostを生みます
このdatabaseで複数の接続testを同時に実行しないでください

期間集計のintegration testは同じ専用databaseのguardを使います

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterPeriodAggregates$' -count=1 -v
```

自分が所有する `tidbgo_it_period_aggregates` tableを作成し、20,000行と2つのTiFlash replicaを準備して、終了時にcleanupします
日別・月別の結果を独立したGoの計算と比較し、UTC／JSTのsessionとdriver location、interpolationの有無、NULL、年末・月末・うるう日、alias衝突、HAVINGとpaging、`parseTime=false`、空結果を確認します
TIMESTAMPとDATETIMEは分けて検証します

4 workloadは1週間のtimestamp範囲、NULL以外の全日、全月、日付と店舗のgroupを扱います
元のcolumnへのrange条件により、入力filterと期間抽出を分けます
`DATE` と `EXTRACT(YEAR_MONTH ...)` を同等の `DATE_FORMAT` とcastに対し、2回のwarmupと実装順を交代する5 roundで比較します
両実装ともtyped rawでscanし、全結果を照合して同じsessionで直後にServerRUを読みます
各workloadで公開 `Compare` も実行します。そのplanとstorage側の集計operatorは通常実行とは別の観測です
これらのworkloadは普遍的なlatencyやRUの優位性を示すものではありません

条件付き集計のintegration testも専用databaseのguardと明示的なTiFlash opt-inを使います

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterConditionalAggregates$' -count=1 -v
```

自分が所有する `tidbgo_it_conditional_aggregates` tableを作成・cleanupし、20,000行の投入とstatistics解析、2つのTiFlash replicaの準備を行います
TRUE／FALSE／NULLの条件、空と一致行のないgroup、正確なDECIMAL、soft-delete、escape付きLIKE、alias衝突、HAVING／順序、日別・月別のpaging、interpolationの有無を明示した期待値と照合します

4 workloadは複数指標、日別group、月別group、status indexに対する少数の一致行を扱います
独立した手書きCASE、IF、filter付きSQLを同じtyped raw collectorで比較します
filter付きSQLは全入力件数のqueryと条件一致の件数／合計のqueryを使い、欠けたgroupを件数0・合計NULLとして統合します
少数一致のworkloadは条件付き指標だけを要求するため、代替はindexを持つstatus columnへ条件を指定する1つのWHERE queryになります

auto、TiKV、TiFlash MPPの指定ごとに各方式を2回warmupし、方式の順序を交代する5 roundを測定します
latencyはSELECTからrowsのcloseまでと結果統合の時間を合計し、直後の同じsessionでのRU probeは含みません
ServerRUは1つの結果に必要なstatementの分を合計します
全結果値と順序の一致を要求します
公開 `Compare` はworkloadごとに別途実行し、そのplanとstorage側の集計operatorを記録します
指標の統合やTiFlashによるlatency／RUの削減は保証しません

Relation集計のtestも専用test databaseと明示的なopt-inを使います

```sh
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterAggregateRelations$' -count=1 -v
```

`tidbgo_it_aggregate_relation_nodes` のsource 10,000行と関連20,000行、`tidbgo_it_aggregate_relation_edges` の20,000行を所有し、終了時にcleanupします
statisticsを解析し、各tableに2つのTiFlash replicaを要求します
重複一致、NULL／欠落key、source／target／edgeのsoft-delete、入れ子・否定・Or、空／全NULL集計、期間key、条件付き集計、HAVING、paging、interpolationの有無を明示した期待値と照合します

広い月別、選択性の高い条件、多数group、viaの4 workloadで、独立した手書きhint付きEXISTS、通常EXISTS、重複を除いた一致keyとのJOINを比較します
typed Rawと同じ結果型を使い、全結果値と順序を照合します
auto、TiKV、TiFlash MPPごとに各方式を2回warmupし、方式順を交代して5 sampleを測定します
latencyはRaw構築、SELECT、scan、rows closeを含み、直後の同じsessionによるRU probeを除きます
公開 `Compare` を別途実行し、結果、関連tableのplan対応、engine要求、警告を検証します
hintやJOINへの書き換えによる高速化は保証しません

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
- `orm`: offline query、aggregateとmutation構築、明示的な `database/sql` 実行、Relation loading、typed raw result scan
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

## Via Relation compiler検証

source-target candidate keyを宣言したpayload付きedgeの、metadata warm済みoffline SQL compileを計測します

workloadはrelation-first List、association-only Count、root predicateによるfallbackです

```sh
go test ./orm -run '^$' -bench '^BenchmarkViaRelationCompiler$' -benchmem -count=5
via_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkViaRelationCompiler$' -benchtime=2s -cpuprofile "$via_profile_dir/cpu" -memprofile "$via_profile_dir/mem" -o "$via_profile_dir/orm.test"
go -C tools tool pprof -top "$via_profile_dir/orm.test" "$via_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$via_profile_dir/orm.test" "$via_profile_dir/mem"
```

DB実行とRUは含みません。変換後ListはEXISTSより大きいSQLになるため、compiler allocationとDB側の削減を分けて計測します

後述する専用test DBを設定した後、次を実行します

```sh
go -C integration test -run '^TestTiDBCloudStarterVia(Compiler|Preload)$' -count=1 -v ./tidbcloud
```

compiler fixtureは200 parent、nullable edge key、surrogate edge primary key、source-target unique key、必須payload、soft-delete scopeを含みます

reference EXISTS queryと結果、順序、件数を比較し、`SHOW WARNINGS` を確認し、`ExplainAnalyze` とhint付きEXISTS／rewriteの小規模な交互RU sampleを出力します

latencyはstatement直後の同一connectionによるRU取得を含みません。testが作成した固定名tableだけを削除し、既存tableがあれば拒否します

このtestはRUを消費します。小規模dataとoptimizer statisticsによる結果はproduction性能やRU regression gateの根拠とはせず、application側の実測とbaseline確認は別途行ってください

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
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeRepeatedWrites$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeMutationIndexes$' -benchmem -count=5
go test ./orm -run '^$' -bench '^BenchmarkConditionalMutationObservation$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkNewServerRUBaseline$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkCompareServerRU$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkAnalyzeWorkload$' -benchmem -count=5
go test ./internal/runtimecapture -run '^$' -bench '^BenchmarkWorkloadBaselineAndComparison$' -benchmem -count=5
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

write反復のbenchmarkはiterationごとに構築済みstatement record 1,000件を解析します

単行insert、known RU付きupsert、主キーupdate、known RU付き条件update、insertとupdateの独立scope、対象外のbulk分割とsoft deleteを含みます

record構築、JSON decode、report encode、runtime captureは計測区間に含めません

集計はattemptまたはRU sampleごとではなく異なるwrite groupごとにcounterを保持するため、解析時間と合わせてallocationも比較します

mutation index benchmarkは同じSQL fingerprintのrecordが1件と1,000件の場合をparse済みschemaで比較します

index照合結果はfingerprintごとにcacheし、coverageはrecordごとに数えます

conditional observation benchmarkはUpdateWhereとDeleteWhereをobserverなし、通常observer、runtime captureの3種類で比較します

captureではscalar metadata生成、JSON encode、discard writerを含みますが、driver変換とDB I/Oは含めません

workload benchmarkは同一の構築済みUPDATE recordをscope budget集計の無効・有効で比較します

1 statement、1 scope内の10,000 statement、各10 statementの1,000 scopeを含み、scope counterはscope内のattempt数ではなく異なるscope数に応じて増えます

workload baselineとcomparisonのbenchmarkは各10 statementの5 scopeを使いvalidationも含みますが、JSON、runtime observation、実DBのRUは計測しません

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

日時引数のtestではUTC/JSTの入力、UTC/JSTのdriver location、interpolationの有無、UTC/JSTのsession timezoneを組み合わせ、typed mutation、raw SQL、`database/sql` による直接実行を比較します

`DATETIME(6)` と `TIMESTAMP(6)` の保存・更新・範囲検索を、隣接するmicrosecond、nullable pointer、application独自のwall-clock Valuer、引用符・backslash・NUL・Unicodeを含む文字列で検証します

同じ接続による読み戻しで隠れる差を検出するため、sessionをUTCにした状態の保存済み日時表現も確認します

test自身の接続では `parseTime=true` とし、driverの `timeTruncate` を無効にします

`TIDBGO_TEST_DSN` を設定した状態で、このtestだけを実行するcommandは次のとおりです

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterArguments$' -count=1 -v
```

suiteはconnection poolを1 connectionに制限します

scalar terminal、slice predicate、application-selected DECIMAL type、temporal field、Relation predicateとpreload、CRUD、bulk insertとupsert、`AUTO_RANDOM`、typed raw SQL、soft delete、restore、transactionのcommitとrollback、typed SELECT EXPLAINとEXPLAIN ANALYZE、same-session ServerRU取得、rootとpreload SELECTのstatement observationを確認します

接続testは固定名の `tidbgo_it_*` fixture tableを作成し、現在のrunが作成したtableだけを削除します

既存fixture tableを検出した場合は削除せず失敗します

同じdatabaseに対する複数suiteを同時実行しません

## 部分取得結果のscan

同じofflineの`database/sql` driverとSQLで、`All`後の変換loop、`ScanAll`、benchmark専用のgeneric collectorを比較します

```sh
go test ./orm -run '^$' -bench '^BenchmarkScanAll$' -benchmem -benchtime=100ms -count=3
```

IDのscalar、小さいstruct、nullable/Scanner field、幅の広い結果を0、1、100、10,000行で測定します

幅の広い`all_map`は結果型が一致するため`All`をそのまま使います。genericの代替方式は取得元のcompileと診断を共有しますが、destination pointerの検証を省いており、public APIではありません

測定対象はclientの時間とallocationであり、RUやnetwork costではありません。`rows_10000/dto/all_map`と`rows_10000/dto/scan_all`へ`-cpuprofile`、`-memprofile`を指定し、`go -C tools tool pprof`でCPUとallocationを確認できます

専用test DSNを設定した状態で、実TiDBの結果、取得元SQL、pagination、Relation条件、soft-deleteのNULL、ServerRU収集を検証します

```sh
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterScanAll$' -count=1 -v
```

test DBを検証してから、自分で作成した`tidbgo_it_projection_*`のfixture tableだけを削除します。既存のfixture tableがある場合は失敗し、そのtableを変更しません

## 一覧SQLの比較

上記の専用テストDBを設定してから、比較を明示的に有効にします

```sh
TIDBGO_TEST_ORDERED_LIST=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterOrderedListSQLShapes$' -count=1 -v
```

専用tableへ12,000件のlinkと600件のtargetを作成し、既存tableがある場合は拒否し、今回作成したtableだけを削除します

既定SELECT、`FORCE INDEX`、derived tableでIDを先に絞る方式を `Raw[T]` で実行し、`Query` / `ForceIndex` / `Preload` / `All` のcompiler経路も比較します

10・50・100件の先頭ページ、2ページ目・中間・末尾・末尾超え、昇順、大きいLIMIT、少数件または0件、非index filterを含みます。最後の条件では今回作成したfixtureのordered indexをDROPし、明示指定がDB errorとなることと未指定での実行を検証します

fixtureから計算したID、順序、値、参照先なしと削除済みtargetの扱いを確認したうえで、各方式の結果を比較します

各方式のwarmupを1回行い、実行順を入れ替えた3 sampleについて、同じ固定connectionから直後にServerRUを読みます

logには各sample、中央値、runtime plan、hint warningの確認結果を残します

所要時間はclientのscanと、compiler経路では比較用にhydrate済みresultを平坦化する処理も含み、RU probeを含みません。setup、cleanup、EXPLAINは報告するSELECTのRUへ含めません。ORM単体のoverheadを切り分ける計測ではありません

この比較は明示的に有効にする実験であり、RU regression gateではありません

applicationのstatisticsを再現するものではなく、特定のoptimizer判断や普遍的なRU改善を保証しません

index指定付き一覧のページサイズとOFFSETを変えた場合や、scalar queryのoffline compileを、DB側の効果と分けて比較できます

```sh
go test ./orm -run '^$' -bench '^BenchmarkOrderedListCompiler$' -benchmem -benchtime=200ms -count=3
ordered_list_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkOrderedListCompiler/first_50$' -benchtime=1s -cpuprofile "$ordered_list_profile_dir/cpu" -memprofile "$ordered_list_profile_dir/mem" -o "$ordered_list_profile_dir/orm.test"
go -C tools tool pprof -top "$ordered_list_profile_dir/orm.test" "$ordered_list_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$ordered_list_profile_dir/orm.test" "$ordered_list_profile_dir/mem"
rm -rf "$ordered_list_profile_dir"
```

benchmarkはmodel metadataを再利用して `Build` を計測し、driver、network、RUを含みません。compiler変更の前後でCPUとallocationのprofileを比較します。SQL templateが長くてもallocation量が増えるとは限りません

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

## 行ごとのUPDATEの検証とbenchmark

一括UPDATEの正当性testはnullable値、JSON、applicationが選択したDECIMAL値、複合主キーと上位bitが立つunsigned主キー、soft delete、restore、UNIQUE key error、未存在row、transactionのcommitとrollbackを確認します

`interpolateParams` と `clientFoundRows` の両設定を検証し、今回作成した3個の `tidbgo_it_update_many*` tableだけを削除します

```sh
# Set TIDBGO_TEST_DSN to the dedicated database described above.
go -C integration test -run '^TestTiDBCloudStarterUpdateMany$' -count=1 -v ./tidbcloud
```

DBなしでclient側compiler costを比較し、同じ入力と選択fieldに対する単行 `Update` 反復と `UpdateMany` をprofileします

```sh
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$' -benchmem -benchtime=200ms -count=5
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$/^rows_1000$/^selected_true$/^loop$' -benchtime=3s -cpuprofile /tmp/tidbgo-update-loop.cpu -memprofile /tmp/tidbgo-update-loop.mem -o /tmp/tidbgo-update-loop.test
go test ./orm -run '^$' -bench '^BenchmarkUpdateMany$/^rows_1000$/^selected_true$/^values$' -benchtime=3s -cpuprofile /tmp/tidbgo-update-many.cpu -memprofile /tmp/tidbgo-update-many.mem -o /tmp/tidbgo-update-many.test
go -C tools tool pprof -top /tmp/tidbgo-update-loop.test /tmp/tidbgo-update-loop.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-update-loop.test /tmp/tidbgo-update-loop.mem
go -C tools tool pprof -top /tmp/tidbgo-update-many.test /tmp/tidbgo-update-many.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-update-many.test /tmp/tidbgo-update-many.mem
```

offline workloadはwarm済みmetadata、native scalar、pointer、byte slice、time、実行しないcustom Valuerを使います

選択fieldと全writable field、value／pointer slice、自動分割を含み、networkやRUではなくcompileとargument準備を測定します

CASE statementはkey引数を反復するため、statement数やallocation数が減っても、全projectionでallocation byte数が減るとは限りません

接続比較は新規 `tidbgo_it_update_shapes` tableに1000行を作成し、既存tableがあれば拒否します

Update loop、`UpdateMany`、事前compile済みの派生table JOIN、hint付きJOINを比較し、各sampleは25、100、500行をtransaction内で更新してrollbackします

costを限定するためiteration数を固定してください

```sh
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterUpdateMany$' -benchmem -benchtime=3x -count=3 ./tidbcloud
# SQL-only shape comparison, one warm-up and three samples per case:
go -C integration test -run '^TestTiDBCloudStarterUpdateManySQLShapes$' -count=1 -v ./tidbcloud
```

`ns/op` はDMLだけを測定し、setup、BEGIN、ROLLBACK、結果確認、別試行で3回取得するsame-session RU sampleを除外します

`DML-ServerRU/op` はcaptured UPDATE RUの合計であり、請求RUやcommit済みtransaction全体のcostではありません

`DML-statements/op` にtransaction controlとRU probeは含まず、fixtureには更新対象のsecondary indexがないため、実applicationのindex、値、並行性、batch、commit経路で再測定してから一般化してください

普遍的な速度やRUの閾値をtestで要求しません

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

## Write batch size比較

同じ1,000行のinputを100、500、1,000行ずつ書き込む場合を比較します

```sh
# Set TIDBGO_TEST_DSN to the dedicated database described above.
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterWriteBatchSizes$' -benchmem -benchtime=1x -count=1 ./tidbcloud
# Repeat a narrower comparison after the initial matrix.
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterWriteBatchSizes$/^payload_2048$/^autocommit$/^upsert_changed$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

60 caseで32 byteと2,048 byteのJSON string payload、Insert、新規・混在・変更・同値Upsert、2種類のtransaction境界を比較します

`autocommit` はDML statementごとに独立してcommitし、`transaction` はpinned connection上の1回の `orm.Transaction` へ全batchをまとめ、latencyとGo allocationにBEGINとCOMMITを含めます

sessionのautocommit有効を必須とし、既存のTiDB transaction modeを変更せず記録します

mode間ではatomicityが異なるため、同じmode内でbatch sizeを比較してください

各caseは同じ値とconflictから開始し、混在caseはinputの前半をseedし、変更caseは整数fieldを1個変更します

commit後に結果と既存IDを検証します

writable columnは6個で各candidate batchは1 statementに収まるため、`batch_1000` はこのworkloadでの現行自動分割policyと同じDML形状です

比較用の分割はpublic mutation APIへ渡すinput sliceで行い、ORMのpolicyは変更しません

3種類のsizeは計測candidateであり、推奨defaultやpublic batch size optionではありません

`Exec` の自動分割は引き続きplaceholder数に基づきます

`DML-ServerRU/op` は1,000行のoperationに含まれる全batchのstatement別ServerRUを合算し、独立してresetした3 sampleを平均します

`DML-ServerRU/row` はその合計をinput行数で割った値です

RU probeは同じconnectionまたはactive transaction上で各DML直後に実行し、latencyとallocation計測に含めません

BEGIN・COMMITのRU、setup、seed、検証、probe、cleanupはmetricに含めないため、transaction全体RUとautocommit RUの比較や請求RUとして使用できません

`statements/op` は対象DMLの10、2、1 statement、`tx-controls/op` は明示BEGIN・COMMITの0または2 statementを数え、driverとnetworkの全round trip数ではありません

`max-args/statement` と `max-SQL-bytes/statement` は最大のbind listとplaceholder SQL templateを表し、interpolation後のpacket sizeやpeak memoryではありません

`B/op` はGoの総allocationでありretained heapやpeak heapではなく、接続準備、source data、検証を含めません

full matrixの `1x` 実行ではwarm-up、計測、RU sampleで対象inputを合計300,000行処理し、resetとseedの追加writeも発生します

resource使用量を制限するためfilterと固定iteration数を使用してください

write baselineと同じ使い捨てtableを使用するため、同じDBで同時実行しません

固定行数・single clientの結果から、より大きい行、placeholder上限に達するbatch、並行writeでの最適sizeは判断できません

同じbatch処理を完全offlineでprofileできます

```sh
go -C integration test -run '^$' -bench '^BenchmarkWriteBatchCompiler$' -benchmem -benchtime=200ms -count=5 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkWriteBatchCompiler$/^payload_2048$/^upsert_true$/^batch_1000$' -benchtime=3s -cpuprofile /tmp/tidbgo-batch.cpu -memprofile /tmp/tidbgo-batch.mem -o /tmp/tidbgo-batch.test ./tidbcloud
go -C tools tool pprof -top /tmp/tidbgo-batch.test /tmp/tidbgo-batch.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-batch.test /tmp/tidbgo-batch.mem
```

小さいbatch candidateをprofileする場合は `batch_1000` を `batch_100` に置き換えます

offline executorはTiDBへ接続せず、driver引数変換と引数の保持も行いません

dataとmetadataを計測前に準備するため、このbenchmarkでpayload長を変えても転送やJSON処理は計測しません

## Relation同期の比較

全削除・再挿入、集合差分、read-diffの実接続比較、identityとlockの前提、計測境界、offline profileは [Relation同期のbenchmark](relation-sync-benchmarks_ja.md) を参照してください

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

DBを使わずclient側の処理を分けて計測します

```sh
go test ./orm -run '^$' -bench '^(BenchmarkSelectQueryBuildPreload.*|BenchmarkSelectQueryPreloadRelationGraphThreeStatements|BenchmarkSelectQueryPreloadHasMany100Parents300Children|BenchmarkSelectQueryPreloadManyToMany100Parents300Targets|BenchmarkSelectQueryPreloadNested100Parents300Children|BenchmarkViaPreload100Parents300Targets)$' -benchmem -count=5
preload_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkSelectQueryBuildPreloadRelationGraph$' -benchtime=2s -cpuprofile "$preload_profile_dir/build.cpu" -memprofile "$preload_profile_dir/build.mem" -o "$preload_profile_dir/build.test"
go test ./orm -run '^$' -bench '^BenchmarkViaPreload100Parents300Targets$/^via$' -benchtime=2s -cpuprofile "$preload_profile_dir/via.cpu" -memprofile "$preload_profile_dir/via.mem" -o "$preload_profile_dir/via.test"
go -C tools tool pprof -top "$preload_profile_dir/build.test" "$preload_profile_dir/build.cpu"
go -C tools tool pprof -top -alloc_space "$preload_profile_dir/build.test" "$preload_profile_dir/build.mem"
go -C tools tool pprof -top "$preload_profile_dir/via.test" "$preload_profile_dir/via.cpu"
go -C tools tool pprof -top -alloc_space "$preload_profile_dir/via.test" "$preload_profile_dir/via.mem"
```

Build workloadはofflineのplanとSQL構築を反復します

実行workloadはlocal database/sql test driverを使い、result decodeとRelation hydrationを含みます。MySQL driverの処理、network latency、TiDBのRUは計測しません

allocationや時間の差を評価する前に、SQL、statement数、結果が等価であることを確認してください

cached default target scan planはprojectionの順序も一致する場合だけ再利用し、queryのalias、scope、result sliceは独立させます

### Via Preloadのcost切り分け

value targetを直接読むviaと、edgeを取得してtargetを取り出す方式を比較します

```sh
go test ./orm -run '^TestViaCostFixtureResults$|^TestManyToManyReusableScan' -count=1
go test ./orm -run '^$' -bench '^(BenchmarkViaPreloadCost|BenchmarkViaPreloadPointerFallback|BenchmarkSelectQueryPreloadNested100Parents300Children)$' -benchmem -benchtime=200ms -count=5
via_cost_profile_dir=$(mktemp -d)
go test ./orm -run '^$' -bench '^BenchmarkViaPreloadCost$/^shared$/^via_true$' -benchtime=2s -cpuprofile "$via_cost_profile_dir/cpu" -memprofile "$via_cost_profile_dir/mem" -o "$via_cost_profile_dir/orm.test"
go -C tools tool pprof -top "$via_cost_profile_dir/orm.test" "$via_cost_profile_dir/cpu"
go -C tools tool pprof -top -alloc_space "$via_cost_profile_dir/orm.test" "$via_cost_profile_dir/mem"
```

offline matrixは空／1 edge、20 parent・100 edgeで12 targetを共有する入力、狭いprojection、共有のないtarget、2,000 edgeを含みます

column decodeと最終targetの取り出しを含み、MySQL driverとnetworkの処理は含みません。pointerとnested workloadは同じscan targetを再利用できない経路を確認します

直接fieldを持ち、targetのinline Relationがないvalue collectionではbatchごとにscan先を一度だけ設定します。pointer collectionと埋め込みfieldの経路は行ごとの設定を維持します。返却値の所有権と生成SQLは変わりません

接続ありの診断では、先に上記の専用test databaseを設定します

```sh
TIDBGO_TEST_VIA_COST=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterViaCost$' -count=1 -v
```

このopt-in testは自分で作成した `tidbgo_it_cost_*` fixtureだけを削除し、既存tableがある場合は拒否します

最終結果が同じ3 statementのedge／via queryを比較し、別途それぞれの生成SQLをdatabase/sqlで全行読み取ります。rawの読取では行数を検証しますがORMの結果graphは組み立てないため、SQL／driverの診断であり同等のrepository実装ではありません

2回のwarm-up後、4方式の順番を交互にして6回測定します。観測なしの全体latency、別試行のstatement別観測時間、操作ごとのDML ServerRUを3 sample、代表Relation planを分けて出力します

RU probe、結果検証、setup、EXPLAIN ANALYZEは観測なしlatencyの計測区間外です。latencyやRUの閾値によるassertは行いません

neutralなfixtureは再現の補助でありproduction dataの複製ではありません。生sampleを保存し、有利／不利な入力を比較してから変更を採用してください。別実行のplanやallocation削減だけではend-to-end latencyの改善は証明できません

別々に測ったrawとORMの中央値を引き算して、正確なORM overheadとしないでください

### 接続ありのRelation graph

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

## Window、vector、準備機能の検証

```sh
go test ./orm ./schema ./tiflash ./vector ./internal/sourcecheck ./examples/starter-app
go test ./orm -run '^$' -bench '^(BenchmarkAggregateWindow|BenchmarkAggregateRelatedBuild|BenchmarkVectorSearchBuild)$' -benchmem -count=3
go test ./vector -run '^$' -bench '^(BenchmarkVectorRoundTrip|BenchmarkVectorDecoderAlternatives)$' -benchmem -count=3
TIDBGO_TEST_TIFLASH=1 go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterTiFlashExtensions$' -count=1 -v
```

接続する拡張testにも他のStarter testと同じ専用DSNの制約があります
`tidbgo_it_tiflash_extensions` を所有・削除し、不在／削除済み関連を含む5,000行、2レプリカ、L2 vector indexを用意します
capability確認とreplica待機、通常query／preload、関連groupingと条件付きEXISTS指標、group後のROW_NUMBER／LAG／累積SUMと手動JOINの一致を確認します
prepared／interpolationの両方式、TiKV参照との正確vector結果一致、ANN planと事前filter時のfallback、nullable cosine距離、次元エラー時の宛先保持、実際のSHOW CREATE TABLE metadataも確認します
全window operatorのMPP対応を仮定せずserver警告を残します
記録する単発sampleは観測値であり、再現可能な性能保証や再現率benchmarkではありません

fake driverのwindow benchmarkは0、1、100、10,000groupでbuilder／Raw／database/sqlの同一結果を比較します
vector decoder比較は3、768、16,383次元で上限付きtyped-array解析とtoken解析を比較します
これはclient CPU／allocationの測定であり、ANN品質、TiFlash indexing、network cost、production throughputは測りません
上記profile手順のbenchmark名を置き換えて利用できます
近似検索の採用前にapplication dataの大きいembeddingと代表的なfilterを測定します

## 警告の検証

```sh
go test ./orm ./internal/warningcheck ./internal/runtimecapture ./cmd/tidbgo
go test ./orm -run '^$' -bench '^BenchmarkWarningCollection$' -benchmem -benchtime=100ms -count=3
go -C integration test ./tidbcloud -run '^TestTiDBCloudStarterWarningState$' -count=1 -v
```

接続testには前述の専用 `TIDBGO_TEST_DSN` が必要です
自身で作成した `tidbgo_it_warning_state` だけを使用・削除します
警告とRUの干渉、SELECTとmutationの警告観測、安全なcapture解析、収集有無を交互にした小queryのlatencyを確認します
既存のTiFlash拡張testでもwindow planの警告通知と通常SELECTの警告確認範囲を検証します
offline benchmarkは1／100結果行、0／1／1,000警告行で、収集なし、任意収集、手動での接続固定とSHOW WARNINGSを比較します
networkとTiDBの時間は含みません
profileは前述のコマンドで `warnings_1/rows_100/collect` と `warnings_1000/rows_1/collect` を対象にします
