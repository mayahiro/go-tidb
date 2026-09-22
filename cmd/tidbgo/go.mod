module github.com/mayahiro/go-tidb/cmd/tidbgo

go 1.26

require (
	github.com/go-sql-driver/mysql v1.10.0
	github.com/mayahiro/go-tidb v0.0.0
	github.com/mayahiro/nagicli-go v0.4.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/mayahiro/nagi-go v0.4.0 // indirect
)

replace github.com/mayahiro/go-tidb => ../..
