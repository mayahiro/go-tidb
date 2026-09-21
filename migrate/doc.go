// Package migrate provides explicit versioned SQL migrations for TiDB Cloud
// Starter. It is deployment tooling, independent of the ORM runtime.
//
// Load and Create work offline. Runner uses a caller-owned database/sql pool,
// pins one connection, and serializes operations with a database-scoped advisory
// lock. DDL is not transactional: interrupted operations require explicit repair.
// Successful changes refresh a SQL snapshot from the actual database, including
// after a down migration. SQL files are trusted deployment code, not a sandbox.
package migrate
