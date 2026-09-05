# Relation同期のbenchmark

integration moduleでは、既存のpublic mutation APIを使用し、指定したRelationの構成とpayloadへ更新する3方式を比較します

| Candidate | 親行lock後の処理 |
| --- | --- |
| `replace` | sourceのedgeを全削除し、指定したedgeをinsert |
| `set_based` | 指定target集合に含まれないedgeだけを削除し、指定したedgeをupsert |
| `read_diff` | 現在のedgeを読み、fixtureの値をGoで比較して、不要なtargetの削除と変更されたedgeのinsertまたはupsertを実行 |

これらはtest用candidateであり、追加のpublic APIや自動compiler rewriteではありません

全方式はcaller-ownedな1個のpessimistic transactionで実行し、Relationが空の場合も同じ既存の親行を `SELECT ... FOR UPDATE` でlockします

read-diff方式はedgeの取得にもcurrent readの `FOR UPDATE` を使い、それ以前のtransaction snapshotを差分の基準にしません

TiDBにはgap lockがないため、既存edgeの範囲だけをlockしても、その範囲へ新しいedgeをinsertするwriterを直列化できません。協調する全writerが同じ親行lockの規則を守る必要があります。[TiDB pessimistic transactions](https://docs.pingcap.com/tidb/stable/pessimistic-transaction/)を参照してください

fixtureはprimary keyが `(s, t)` のpure junction、または `AUTO_RANDOM` primary key、unique `(s, t)`、整数payload、32 byteの文字列payloadを持つedge modelです

全方式で同じ構成とpayloadになり、別sourceが変更されないことを検証します。差分方式では既存edge IDの維持も検証します。全削除・再挿入はIDを再生成し得るため、identityやDB管理値が重要な場合に相互置換できる方式ではありません

fixtureのUpsertは生成primary keyをinputへ含めず、Relation pairだけに衝突します。任意のunique constraintへ一般化しないでください。TiDBは任意のprimaryまたはunique keyの衝突で行を更新し得ます。[TiDB update guidance](https://docs.pingcap.com/developer/dev-guide-update-data/)を参照してください

## 比較の実行

[Development](development_ja.md#tidb-cloud-starter-integration-test)に記載した専用TiDB test databaseとDSNを使用します

harnessは選択中のdatabase prefixとTiDB versionを検証し、既存設定が `pessimistic` modeかつsession autocommit有効であることを要求します。設定自体は変更しません

`tidbgo_it_sync_roots`、`tidbgo_it_sync_pairs`、`tidbgo_it_sync_edges` を作成し、自身が作成に成功したtableだけを削除します。既存tableがある場合はerrorとし、cleanupの対象にしません

```sh
# TIDBGO_TEST_DSN must already point to the dedicated test database.
go -C integration test -run '^TestTiDBCloudStarterRelationSyncCandidates$' -count=1 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterRelationSync$' -benchmem -benchtime=1x -count=1 ./tidbcloud
```

matrixは42 caseです。edge数は10または100、pureまたはpayload付き、3方式、無変更・一部変更・全変更の構成を比較します。payload付きではpayloadだけを変更するcaseも含みます

一部変更はtarget keyの10%を入れ替え、別の10%の整数payloadを更新します。payloadのみの変更は全targetを維持し、10%の整数payloadを更新します

caseごとにwarm-up、各timed operation、独立した3回のRU sampleの前にdataをreset・seedします。setup、反復write、検証、RU probeもresourceを消費するため、固定iteration数と絞り込んだfilterを使用してください

```sh
go -C integration test -run '^$' -bench '^BenchmarkTiDBCloudStarterRelationSync$/^rows_100$/^payload_true$' -benchmem -benchtime=3x -count=3 ./tidbcloud
```

connected testは空の指定集合、再実行、rollback、別sourceの維持、空Relationからの親行lock競合、古いsnapshotを持つ別transactionでの処理も確認します。snapshotのtestは明示的に `REPEATABLE READ` を指定し、同期直前の通常SELECTではまだ古い空集合が見えることも検証します

## 計測境界

- `ns/op` と `B/op` はtransaction、親行lock、必要な場合のedge読取と比較、mutation、commitを含み、setup、検証、RU probeを除外します
- `Statement-ServerRU/op` は親行lockと差分読取を含む全対象SELECTとDMLの同一session上のServerRUを合算し、独立した3 sampleで平均します
- このRU metricはBEGIN、COMMIT、diagnostic query、setup、検証、cleanupを除外します。transaction全体RUでも請求RUでもなく、commitを含む削減効果は判断できません
- `statements/op` はSELECTとDML、`writes/op` は試行したmutation statementを数えます。変更行数や物理storage write数ではありません。`tx-controls/op` はBEGINとCOMMITを別に数えます
- `B/op` はGoの総allocationであり、peak heapやserver memoryではありません
- timingは逐次実行・single clientの実験結果であり、同時刻のrandomized trialや並行throughput testではありません

## 確認されたtrade-off

2026-09-05、Go 1.27.1、darwin/arm64、Apple M1 Max、default並列度10、project専用TiDB、mysql driver v1.10.0、`interpolateParams=true`、`clientFoundRows=false` で、100 edgeのpayload付きworkloadは次の結果でした

各値は3 runそれぞれの3 sample平均 `Statement-ServerRU/op` の中央値です

| 変更 | Replace | Set-based | Read-diff |
| --- | ---: | ---: | ---: |
| 無変更 | 10.83 | 6.937 | 6.808 |
| 構成とpayloadの一部変更 | 11.55 | 12.17 | 13.53 |
| 構成の全変更 | 11.02 | 11.71 | 13.86 |
| payloadのみ | 11.03 | 7.065 | 10.69 |

差分方式は既存IDを維持し、payload付きedgeが無変更の場合に対象statement RUを削減しました。read-diffは無変更時にmutationを発行しませんでしたが、構成変更に削除と追加が必要な場合は他方式の3 statementに対して4 statementを発行しました

どちらの差分方式も全shapeで対象RUを削減する結果ではありません。この計測から常に高速な置換方針を決めたり、edge数だけでapplicationの全体RUを予測したりはできません

## Offline planningとprofile

```sh
go -C integration test -run '^$' -bench '^BenchmarkRelationSyncPlanning$' -benchmem -benchtime=100ms -count=3 ./tidbcloud
go -C integration test -run '^$' -bench '^BenchmarkRelationSyncPlanning$/^rows_100$/^payload_true$/^partial$/^set_based$' -benchmem -benchtime=3s -cpuprofile /tmp/tidbgo-relation-sync.cpu -memprofile /tmp/tidbgo-relation-sync.mem -o /tmp/tidbgo-relation-sync.test ./tidbcloud
go -C tools tool pprof -top /tmp/tidbgo-relation-sync.test /tmp/tidbgo-relation-sync.cpu
go -C tools tool pprof -top -alloc_space /tmp/tidbgo-relation-sync.test /tmp/tidbgo-relation-sync.mem
```

別candidateでは `set_based` を `replace` または `read_diff` へ置き換えます

offline planningはkeyの準備、fixture専用のGo差分計算、mutation SQL構築を含み、driver変換、row scan、lock、transaction control、network I/Oを含みません

Goの比較処理はfixtureの整数keyとpayloadだけを対象とし、DB collation、NULL、custom Scanner/Valuer型、serverがnormalizeする値を含む汎用的な等値性の契約ではありません
