# TiFlashの実行方針と明示的な準備

[English](tiflash.md) | [集計](aggregates_ja.md) | [ベクトル検索](vector-search_ja.md)

`ReadFrom(orm.TiKV/orm.TiFlash)` と `MPP(orm.MPPAuto/orm.MPPEnforce)` は通常の `Query`、`Aggregate`、`Nearest` で使えます
optimizer hintによる要求であり実行を保証しません。省略した方針はsessionの挙動を維持します
session SETや自動capability確認は実行しません
TiKVとMPPEnforce、通常queryのForceIndexとTiFlashの併用は拒否します

通常queryでは `All`、`First`、`Only`、`ScanAll`、`Count`、`Exists`、明示的なplan取得に適用します
Relation書き換え後の物理alias、inline JOIN、入れ子のEXISTS、個別preload SELECTにも引き継ぎます
MPP設定はstatementごとに1回です。root lookupを省いたCountは残ったassociation tableへhintを付けます
cached SQLや別builderは変更しません。runtime query-shape fingerprintは要求方針を含みます
明示的なTiFlash指定にはscalarのrow-index prefix検証を適用しません

## レプリカの準備

package `tiflash` は通常queryとは独立して、明示的に準備します

```go
ddl, err := tiflash.BuildEnableReplica("app", "orders")
if err != nil { return err }
// The caller decides when to execute this DDL, including its operational cost.
if _, err = db.ExecContext(ctx, ddl); err != nil { return err }

prepareCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
defer cancel()
replica, err := tiflash.WaitReplicaReady(prepareCtx, db, "app", "orders", time.Second)
if err != nil { return err }
_ = replica
```

DDL生成はschema／tableを別々にquoteし、Starterの2レプリカを要求します
`InspectReplica` は1回のmetadata queryで、不在／不可視のbase tableの `ErrTableNotFound` と、存在するtableの `Configured=false` を区別します
権限、接続、非対応metadataのエラーはそのままエラーとして扱います
`WaitReplicaReady` はdeadline付きcontextを必須とし、初期利用可能状態をpollします
replica未設定なら直ちに `ErrReplicaNotConfigured` を返します
intervalが0なら1秒、負数は拒否します。後続のエラー時には最後の成功した確認結果を保持します

`Available` は一度有効になると維持される初期準備metadataであり、継続的な健全性や鮮度の確認ではありません
`Progress=1` も全replicaの同期完了を証明しません
Relation targetや中間tableを含む全参照tableを準備します
[レプリカの仕様](https://docs.pingcap.com/tidb/stable/create-tiflash-replicas/)を参照してください

## 機能とplanの確認

`tiflash.ProbeCapabilities(ctx, executor)` はreplica metadata、MPP設定の存在、window関数、vector関数、vector index metadataを5回の小さなread-only queryで明示的に確認します
設定は変更しません。`Supported` はその限定的なprobeの成功を示し、全operator／tableの対応を保証しません
成功したSHOWにMPP設定がなければ `Unsupported`、失敗、拒否、未確認は `Unknown` です
個別エラーと結合したエラーを返し、成功済みの結果は維持します。エラーは未redactのserver文言を含む場合があります

同一sessionの警告は集計／vectorの `Explain`、`ExplainAnalyze` で確認します
通常Selectのplan取得は既存の戻り値型を維持します
`AggregatePlan.Summary()` と `VectorPlan.Summary()` は各operatorの処理task、推定／実測の出力行数、直下の子operatorの出力を返します
実測値はAnalyzeだけが持ちます。子の出力は利用可能な入力であり、消費した行数の実測や全子孫scanの合計ではありません
未知のtree形式では個別operatorを残し、`TreeKnown=false` として子の関係とresultを未確定にします
未知のtaskも未確定のままです。転送byte数、通常queryのlatency、請求RU、rootの最終集計だけからのpushdown不足は推定しません
