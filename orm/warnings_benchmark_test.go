package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

type warningBenchmarkConnector struct{ values, warnings [][]driver.Value }

func (c *warningBenchmarkConnector) Connect(context.Context) (driver.Conn, error) {
	return &warningBenchmarkConn{connector: c}, nil
}
func (*warningBenchmarkConnector) Driver() driver.Driver { return allTestDriver{} }

type warningBenchmarkConn struct {
	connector *warningBenchmarkConnector
	target    *allTestState
}

func (*warningBenchmarkConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*warningBenchmarkConn) Begin() (driver.Tx, error)           { return allTestTx{}, nil }
func (*warningBenchmarkConn) Close() error                        { return nil }
func (c *warningBenchmarkConn) QueryContext(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
	if statement == "SHOW WARNINGS" {
		if c.target == nil || c.target.closeCalls != 1 {
			return nil, errors.New("target rows still open")
		}
		return &allTestRows{state: &allTestState{columns: []string{"Level", "Code", "Message"}, values: c.connector.warnings}}, nil
	}
	c.target = &allTestState{columns: []string{"ID"}, values: c.connector.values}
	return &allTestRows{state: c.target}, nil
}

// BenchmarkWarningCollection compares opt-in observation with manual pinning
// and warning collection, and with no collection. The driver excludes network.
func BenchmarkWarningCollection(b *testing.B) {
	for _, count := range []int{0, 1, 1000} {
		for _, rows := range []int{1, 100} {
			b.Run(fmt.Sprintf("warnings_%d/rows_%d", count, rows), func(b *testing.B) {
				c := &warningBenchmarkConnector{values: make([][]driver.Value, rows), warnings: make([][]driver.Value, count)}
				for i := range c.values {
					c.values[i] = []driver.Value{int64(i)}
				}
				for i := range c.warnings {
					c.warnings[i] = []driver.Value{"Warning", int64(1105), "MPP mode may be blocked because private-value"}
				}
				db := sql.OpenDB(c)
				b.Cleanup(func() { _ = db.Close() })
				q := Aggregate[warningSource]().Select(Field("ID")).GroupBy("ID")
				for _, method := range []string{"none", "collect", "manual"} {
					b.Run(method, func(b *testing.B) {
						ctx := context.Background()
						if method == "collect" {
							ctx = WithStatementObserver(ctx, func(e StatementEvent) {
								if e.Warnings == nil || !e.Warnings.Known || e.Warnings.Error != nil || len(e.Warnings.Warnings) != count {
									b.Fatal("invalid observation")
								}
							}, CollectWarnings())
						}
						b.ReportAllocs()
						for b.Loop() {
							var values []int64
							if method == "manual" {
								conn, err := db.Conn(ctx)
								if err != nil {
									b.Fatal(err)
								}
								err = q.ScanAll(ctx, conn, &values)
								if err != nil {
									b.Fatal(err)
								}
								warnings, err := collectStatementWarnings(ctx, conn)
								closeErr := conn.Close()
								if err != nil || closeErr != nil || len(warnings) != count {
									b.Fatal(err, closeErr)
								}
							} else if err := q.ScanAll(ctx, db, &values); err != nil {
								b.Fatal(err)
							}
							if len(values) != rows {
								b.Fatal("result count mismatch")
							}
						}
					})
				}
			})
		}
	}
}
