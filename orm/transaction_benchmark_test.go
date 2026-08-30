package orm

import (
	"context"
	"database/sql"
	"testing"
)

func BenchmarkTransaction(b *testing.B) {
	state := &transactionTestState{}
	database := openTransactionTestDB(b, state)
	ctx := context.Background()
	callback := func(*sql.Tx) error { return nil }

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := Transaction(ctx, database, callback); err != nil {
			b.Fatal(err)
		}
	}
}
