module github.com/mayahiro/go-tidb/integration

go 1.26

require (
	github.com/go-sql-driver/mysql v1.10.0
	github.com/mayahiro/go-tidb v0.0.0
)

require filippo.io/edwards25519 v1.2.0 // indirect

replace github.com/mayahiro/go-tidb => ..
