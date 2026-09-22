// Package migrate provides explicit versioned SQL migrations for TiDB Cloud
// Starter. It is deployment tooling, independent of the ORM runtime.
//
// Load, Lint, and Create work offline. Each up/down section can contain multiple
// SQL statements. A version is the complete filename without .sql. Runner uses
// a caller-owned database/sql pool, pins one connection, and serializes operations
// with a database-scoped advisory lock. The database stores only applied versions
// and database-generated registration times. Down reverses registration order.
// DDL and record writes are not atomic: OnEvent reports statement progress for
// manual recovery, and the CLI persists it under log/tidbgo.
// Successful changes refresh a SQL snapshot from the actual database, including
// after a down migration. SQL files are trusted deployment code, not a sandbox.
package migrate
