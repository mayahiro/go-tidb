// Package orm builds queries and mutations from application-owned Go structs
// without code generation or an implicit database connection.
//
// SQL placeholders and bind arguments remain separate. Native time.Time values
// are passed to the executor without literal formatting or timezone conversion;
// serialization follows the database driver and connection settings. Build does
// not invoke driver.Valuer. The package does not change connection time zones.
//
// Query.Build compiles SQL offline. All, ScanAll, First, Only, Exists, and Count
// perform I/O only through an explicitly supplied database/sql executor. Has
// compiles relation existence conditions without implicit loading.
// ScanAll reads a single column into a scalar slice or selected Go fields into
// a separate struct slice, retaining the source model's SQL and diagnostics.
// Aggregate groups source-model rows with relation filters and validated
// expressions, conditional counts/sums, daily/monthly calendar keys, output-name grouping,
// HAVING, to-one related fields, grouped windows, and optional TiKV/TiFlash and
// MPP hints. Its ScanAll
// maps output Go names to scalar or struct slices. Aggregate Explain and
// ExplainAnalyze return requested policy, plan rows, and same-session warnings.
// Aggregate Compare explicitly compares auto, TiKV, and TiFlash MPP with result
// checks, latency/ServerRU samples, and separate plans. Complete comparisons can
// export measured samples through WriteCapture for existing baseline checks.
// Nearest builds exact or explicitly approximate vector Top-K searches with
// prefilters, vector plan evidence, and offline schema/index checks. ReadFrom
// and MPP also apply to ordinary SELECTs and all their preload statements.
// Preload adds explicit nested hydration without lazy loading. Belongs-to and
// has-one relations use inline LEFT JOINs; has-many and many-to-many
// relations use deterministic secondary SELECTs, with unrestricted root
// collections loaded once and constrained collections loaded in bounded
// parameter batches. Read-only via relations reuse payload-bearing edge
// mappings and support edge-field ordering without hydrating the edge.
// Insert, automatically bounded InsertMany, Upsert,
// UpsertMany, Update, UpdateMany, UpdateWhere, Delete, and DeleteWhere provide typed model
// writes. Set and Increment provide safe conditional-update assignments.
// AddRelation, RemoveRelation, and ClearRelation provide pure many-to-many
// junction writes. Transaction groups application-defined work using a
// transaction-bound Executor without retrying it. Raw provides model-aware
// result scanning for explicit SQL. Observe configures a shared executor once,
// while WithStatementObserver supplies optional context overrides.
// NewStatementLogger provides automatic terminal colors, with an explicit
// StatementLoggerColor override for any writer. Bind values are excluded unless
// explicitly enabled. RuntimeCapture records actual typed queries, preloads,
// and bulk splits after it is installed at an
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
