# Vector values and nearest-neighbor search

[日本語](vector-search_ja.md)

Package `vector` provides an owned finite `float32` vector implementing
`sql.Scanner` and `driver.Valuer`. `New` and `Values` copy slices. The zero value
is SQL NULL; `New(nil)` is an empty vector. Text/byte scanning accepts numeric
JSON arrays and changes the destination only after success. NULL elements,
nonfinite values, malformed input, and more than 16,383 dimensions are rejected.
`ValidateDimensions(n)` requires a non-NULL vector with a positive fixed size.

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

Provision `embedding VECTOR(3)` for this example. Ordinary ORM writes accept
`vector.Vector` values. Search supports `vector.L2` and `vector.Cosine`, a mapped
`vector.Vector` or pointer field, and Top-K between 1 and 4,294,967,295. The source
must be a non-pointer struct. `Build` validates offline and binds the query vector;
physical dimensions require a schema snapshot or server validation.

## Exact and approximate modes

`Nearest` defaults to `VectorExact`. It orders by distance, then the model's
declared primary key, which is required in this mode. The additional order terms
exclude TiDB's current single-order ANN path. Exact search can scan a large
eligible set and does not imply low latency or RU. `Mode(orm.VectorApproximate)`
permits the ANN path using distance-only ordering; recall and tied ordering are
not guaranteed, and TiDB can still choose a full scan. Storage/MPP hints request
execution policy independently of search mode.

`Where`, including `Has`, and the active soft-delete scope always run before
Top-K. `WithDeleted` includes deleted source rows only. No condition is moved
after LIMIT. TiDB currently cannot use its ANN index with prefilters; tenant and
soft-delete conditions can therefore prevent index use. See the official
[index conditions](https://docs.pingcap.com/ai/vector-search-index/).

Default projection retrieves all mapped source fields plus `Distance`; use
`Select` to avoid fetching embeddings. `DistanceAs` changes the distance output
name when it collides with a selected field. `ScanAll` matches exact Go names and
uses aggregate scanning's atomic destination and NULL rules. SQL NULL distances
are preserved and sort first in ascending SQL order. For nullable embeddings,
use a nullable destination and, when required, `Where(orm.IsNotNull("Embedding"))`.
Prefer non-NULL fixed-dimensional embeddings and nonzero norms for cosine search.

## Index definition and evidence

```go
ddl, err := orm.BuildVectorIndex[Document]("Embedding", "embedding_l2", vector.L2)
// CREATE VECTOR INDEX `embedding_l2` ON `documents`
// ((VEC_L2_DISTANCE(`embedding`))) USING HNSW
```

This only returns SQL. The caller explicitly executes it after preparing
[TiFlash replicas](tiflash.md) and a fixed-dimensional VECTOR column. No index,
replica, or capability check is added to normal queries. TiDB's vector feature
is currently public preview; deployment support can change.

`schema.Parse` recognizes `VECTOR(n)` and single-column L2/cosine vector indexes.
`Column.VectorDimensions` and `Index.Vector` expose that metadata; vector indexes
never count as scalar lookup or uniqueness coverage. `q.SchemaDiagnostics(catalog)`
is offline:

| Code | Evidence |
| --- | --- |
| `VEC001` | No snapshot, missing/non-vector column, or dimension mismatch |
| `VEC002` | No matching fixed-dimensional vector index or a prefilter preventing ANN |
| `VEC003` | `VectorPlan.Diagnostics` sees recognized table scans without ANN in approximate mode |

Missing snapshot and index/plan advisories are informational; a supplied schema
with incompatible columns/dimensions produces a warning. Metadata never proves
live index readiness. Inspect `INFORMATION_SCHEMA.TIFLASH_INDEXES` for build
progress as described in the [index guide](https://docs.pingcap.com/ai/vector-search-index/).

`q.Explain` and `q.ExplainAnalyze` return `VectorPlan`, including requested policy,
same-session warnings, `Summary`, and `IndexUsage`. Analyze explicitly executes
the search. `VectorIndexUsed` requires a TiFlash scan with `annIndex:`;
`VectorIndexNotUsed` requires recognized scans; unfamiliar evidence stays
`VectorIndexUnknown`. Planned use describes optimization, observed use describes
that Analyze execution. Neither proves recall or another SELECT's behavior.

RuntimeCapture uses `source=typed_vector` and an `s1:` SQL-template fingerprint
including hints and mode-specific order, excluding bind values. Existing RU
baselines can compare equivalent inputs; no scalar query-shape lint is inferred.
