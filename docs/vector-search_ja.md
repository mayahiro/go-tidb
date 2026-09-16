# ベクトル型と近傍検索

[English](vector-search.md)

package `vector` は有限の `float32` 要素を所有し、`sql.Scanner` と `driver.Valuer` を実装します
`New` と `Values` はsliceをコピーします。ゼロ値はSQL NULL、`New(nil)` は空ベクトルです
text／byteのscanは数値JSON配列を受け入れ、成功した場合だけ宛先を変更します
NULL要素、非有限値、不正な入力、16,383次元を超える値は拒否します
`ValidateDimensions(n)` はNULLではない正の固定次元数を確認します

```go
type Document struct {
    model.Meta `tidbgo:"table=documents"`
    ID         int64 `tidbgo:",pk"`
    TenantID   int64
    Title      string
    Embedding  vector.Vector
}

input, err := vector.New([]float32{0.2, 0.4, 0.8})
if err != nil { return err }
q := orm.Nearest[Document]("Embedding", input, vector.L2, 10).
    Select("ID", "Title").Where(orm.Equal("TenantID", tenantID))
var hits []struct { ID int64; Title string; Distance sql.NullFloat64 }
err = q.ScanAll(ctx, db, &hits)
```

この例では `embedding VECTOR(3)` を用意します。通常のORM書き込みにも `vector.Vector` を使えます
検索は `vector.L2` と `vector.Cosine`、mappedな `vector.Vector` またはpointer field、1から4,294,967,295のTop-Kに対応します
sourceはpointerではないstructとします。`Build` はofflineで検証しquery vectorをbindします
物理columnとの次元数の照合にはschema snapshotまたはserverによる検証が必要です

## 正確検索と近似検索

`Nearest` の初期値は `VectorExact` です。距離、続いてmodelの宣言済みprimary keyで並べます
このmodeではprimary keyが必須で、追加の並び順によりTiDBの現在の単一orderによるANN経路を除外します
対象集合が大きいと広い範囲をscanするため、低latency／低RUは保証しません
`Mode(orm.VectorApproximate)` は距離だけで並べ、ANN経路を許可します
再現率や同距離の順序は保証せず、TiDBが通常scanを選ぶ場合もあります
storage／MPP hintは検索modeとは独立した実行方針の要求です

`Has` を含む `Where` とactiveなsoft-delete scopeは必ずTop-K前に適用します
`WithDeleted` が含めるのは削除済みsource行だけです。条件をLIMITの後へ移動しません
現在のTiDBは事前filter付きではANN indexを使えず、tenant／soft-delete条件もindex利用を妨げます
公式の[index利用条件](https://docs.pingcap.com/ai/vector-search-index/)を参照してください

初期projectionは全mapped source fieldと `Distance` です。embeddingの取得を避けるには `Select` を指定します
距離出力名が選択済みfieldと衝突する場合は `DistanceAs` で変更します
`ScanAll` は正確なGo名を照合し、集計と同じ宛先の一括置換とNULL規則を使います
SQL NULLの距離は維持され、SQLの昇順では先頭になります
nullableなembeddingではnullableな宛先を使い、必要なら `Where(orm.IsNotNull("Embedding"))` で除外します
検索用データには非NULLの固定次元embeddingを用い、cosineではnormが0にならないようにします

## Index定義と確認

```go
ddl, err := orm.BuildVectorIndex[Document]("Embedding", "embedding_l2", vector.L2)
// CREATE VECTOR INDEX `embedding_l2` ON `documents`
// ((VEC_L2_DISTANCE(`embedding`))) USING HNSW
```

返すのはSQLだけです。[TiFlashレプリカ](tiflash_ja.md)と固定次元VECTOR columnを準備してから、呼び出し側で明示的に実行します
通常queryにindex／replica／capability確認は追加しません
TiDBのvector機能は現在public previewであり、提供状況は変わる可能性があります

`schema.Parse` は `VECTOR(n)` と単一columnのL2／cosine vector indexを認識します
`Column.VectorDimensions` と `Index.Vector` からmetadataを取得できます
vector indexはscalarのlookup／uniquenessを満たすindexとして扱いません
`q.SchemaDiagnostics(catalog)` はofflineで次を確認します

| Code | 根拠 |
| --- | --- |
| `VEC001` | snapshotなし、columnの欠落／型不一致、次元数の不一致 |
| `VEC002` | 対応する固定次元vector indexなし、またはANNを妨げる事前filter |
| `VEC003` | `VectorPlan.Diagnostics` が近似modeの認識済みscanにANNを確認できない |

snapshotなし、index／planの助言はinfo、指定schemaのcolumn／次元数不一致はwarningです
metadataだけでは稼働中のindex準備状態は証明できません
構築状況は公式[indexガイド](https://docs.pingcap.com/ai/vector-search-index/)に従い `INFORMATION_SCHEMA.TIFLASH_INDEXES` で確認します

`q.Explain` と `q.ExplainAnalyze` は要求方針、同一sessionの警告、`Summary`、`IndexUsage` を持つ `VectorPlan` を返します
Analyzeは検索を明示的に実行します
`VectorIndexUsed` はTiFlash scanの `annIndex:`、`VectorIndexNotUsed` は認識済みscanを根拠にし、不明な場合は `VectorIndexUnknown` です
plannedは最適化時、executedはそのAnalyze実行の利用状況であり、再現率や別のSELECTの挙動を証明しません

RuntimeCaptureは `source=typed_vector` と `s1:` SQL-template fingerprintを使います
hintとmodeによる並び順を含み、bind値を除外します
同じ入力条件のRU baseline比較に利用できますが、scalar query-shape lintは推定しません
