// Package orm builds queries and mutations from application-owned Go structs
// without code generation or an implicit database connection.
//
// Query.Build compiles SQL offline. All, First, Only, Exists, and Count perform
// I/O only through an explicitly supplied database/sql executor. Has compiles
// relation existence conditions without implicit loading.
// Preload adds explicit nested hydration without lazy loading. Belongs-to and
// has-one relations use inline LEFT JOINs; has-many and pure many-to-many
// relations use deterministic secondary SELECTs, with unrestricted root
// collections loaded once and constrained collections loaded in bounded
// parameter batches. Insert, automatically bounded InsertMany, Upsert,
// UpsertMany, Update, UpdateWhere, Delete, and DeleteWhere provide typed model
// writes. Set and Increment provide safe conditional-update assignments.
// AddRelation, RemoveRelation, and ClearRelation provide pure many-to-many
// junction writes. Transaction groups application-defined work using a
// concrete *sql.Tx without retrying it. Raw provides model-aware result
// scanning for explicit SQL. WithStatementObserver adds context-scoped
// execution events, and NewStatementLogger provides automatic terminal colors
// with bind values excluded unless explicitly enabled. RuntimeCapture records
// actual typed queries, preloads, and bulk splits after it is installed at an
// operation boundary, without per-query registration. CollectServerRU is an
// explicit high-cost observer option that pins pooled statements as needed and
// keeps diagnostic cost separate from target cost. Explain inspects the TiDB
// execution plan of a typed SELECT without executing it, and ExplainAnalyze
// explicitly executes that SELECT to collect runtime plan data.
// ExplainAnalyzePlan.Diagnostics checks the returned
// plan without another database statement. LastServerRU reads TiDB's ServerRU
// for one completed DML statement from the same *sql.Conn or active *sql.Tx.
// The package intentionally does not expose runtime DDL operations.
// Models with a soft-delete field are scoped to active rows by default;
// WithDeleted and PreloadWithDeleted opt into deleted rows at the root and
// relation-path boundaries respectively.
package orm
