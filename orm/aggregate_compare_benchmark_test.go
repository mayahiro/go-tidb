package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type aggregateCompareTestState struct {
	values      [][]driver.Value
	columns     []string
	queries     []string
	arguments   [][]driver.NamedValue
	record      bool
	connections int
	last        string
	variant     int
	selects     int
	filter      func(context.Context, string, []driver.NamedValue) (driver.Rows, error, bool)
}

type aggregateCompareTestConnector struct{ state *aggregateCompareTestState }

func (c *aggregateCompareTestConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.connections++
	return &aggregateCompareTestConn{state: c.state}, nil
}
func (*aggregateCompareTestConnector) Driver() driver.Driver { return allTestDriver{} }

type aggregateCompareTestConn struct{ state *aggregateCompareTestState }

func (*aggregateCompareTestConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*aggregateCompareTestConn) Close() error                        { return nil }
func (*aggregateCompareTestConn) Begin() (driver.Tx, error)           { return allTestTx{}, nil }
func (c *aggregateCompareTestConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	if s.record {
		s.queries = append(s.queries, query)
		s.arguments = append(s.arguments, append([]driver.NamedValue(nil), args...))
	}
	if s.filter != nil {
		rows, err, handled := s.filter(ctx, query, args)
		if handled {
			return rows, err
		}
	}
	if query == lastServerRUQuery {
		if s.last != "select_closed" {
			return nil, fmt.Errorf("RU probe preceded target closure or followed another statement: %s", s.last)
		}
		s.last = "ru"
		return &aggregateCompareTestRows{columns: []string{"info"}, values: [][]driver.Value{{fmt.Sprintf(`{"ru_consumption":%d}`, s.variant+1)}}}, nil
	}
	if query == "SHOW WARNINGS" {
		if s.last != "plan_closed" {
			return nil, fmt.Errorf("warnings followed another statement: %s", s.last)
		}
		s.last = "warnings"
		return &aggregateCompareTestRows{columns: []string{"Level", "Code", "Message"}}, nil
	}
	s.variant = 0
	if strings.Contains(query, "TIKV[a]") {
		s.variant = 1
	}
	if strings.Contains(query, "TIFLASH[a]") {
		s.variant = 2
	}
	if strings.HasPrefix(query, "EXPLAIN ANALYZE ") {
		s.last = "plan"
		task := "cop[tikv]"
		if s.variant == 2 {
			task = "mpp[tiflash]"
		}
		return &aggregateCompareTestRows{columns: explainAnalyzeColumnNames[:], values: [][]driver.Value{{"TableFullScan_1", "100", int64(len(s.values)), task, "table:a", "time:1ms", "", "N/A", "N/A"}}, closed: func() { s.last = "plan_closed" }}, nil
	}
	s.last = "select"
	s.selects++
	return &aggregateCompareTestRows{columns: s.columns, values: s.values, closed: func() { s.last = "select_closed" }}, nil
}

type aggregateCompareTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	closed  func()
}

func (r *aggregateCompareTestRows) Columns() []string { return r.columns }
func (r *aggregateCompareTestRows) Close() error {
	if r.closed != nil {
		r.closed()
		r.closed = nil
	}
	return nil
}
func (r *aggregateCompareTestRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func aggregateCompareBenchmarkData(rows int) (*AggregateQuery[aggregateBenchmarkSource], *aggregateCompareTestState) {
	q := Aggregate[aggregateBenchmarkSource]().Select(Field("ShopID"), CountAll().As("Count"), Sum("Amount").As("Total")).GroupBy("ShopID").OrderBy(Asc("ShopID"))
	s := &aggregateCompareTestState{columns: []string{"ShopID", "Count", "Total"}, values: make([][]driver.Value, rows)}
	for i := range s.values {
		s.values[i] = []driver.Value{int64(i), int64(2), int64(42)}
	}
	return q, s
}

// benchmarkManualAggregateComparison models an application-owned comparison
// using typed ScanAll, exact values, one pinned connection, immediate RU,
// two warmups, five rotated samples, and separate runtime plans.
func benchmarkManualAggregateComparison(ctx context.Context, db *sql.DB, q *AggregateQuery[aggregateBenchmarkSource]) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	variants := []*AggregateQuery[aggregateBenchmarkSource]{q, qCopy(q).ReadFrom(TiKV), qCopy(q).ReadFrom(TiFlash).MPP(MPPEnforce)}
	var reference []aggregateBenchmarkResult
	for round := range 7 {
		for step := range 3 {
			variant := variants[(round+step)%3]
			var values []aggregateBenchmarkResult
			if err := variant.ScanAll(ctx, conn, &values); err != nil {
				return err
			}
			if _, err := LastServerRU(ctx, conn); err != nil {
				return err
			}
			if reference == nil {
				reference = values
			} else if !reflect.DeepEqual(reference, values) {
				return fmt.Errorf("unequal results")
			}
		}
	}
	for _, variant := range variants {
		plan, err := variant.ExplainAnalyze(ctx, conn)
		if err != nil {
			return err
		}
		if plan.WarningsError != nil {
			return plan.WarningsError
		}
	}
	return nil
}

func qCopy[T any](q *AggregateQuery[T]) *AggregateQuery[T] { copy := *q; return &copy }

func BenchmarkAggregateComparisonManual(b *testing.B) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			q, state := aggregateCompareBenchmarkData(count)
			db := sql.OpenDB(&aggregateCompareTestConnector{state: state})
			b.Cleanup(func() { _ = db.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if err := benchmarkManualAggregateComparison(context.Background(), db, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAggregateComparison measures the complete public diagnostic using
// the same driver, values, and 21 SELECT/three plan workload as the manual path.
func BenchmarkAggregateComparison(b *testing.B) {
	for _, count := range []int{0, 1, 100, 10000} {
		b.Run(fmt.Sprintf("rows_%d", count), func(b *testing.B) {
			q, state := aggregateCompareBenchmarkData(count)
			db := sql.OpenDB(&aggregateCompareTestConnector{state: state})
			b.Cleanup(func() { _ = db.Close() })
			b.ReportAllocs()
			for b.Loop() {
				if _, err := q.Compare(context.Background(), db, AggregateCompareOptions{Case: "fixed-v1"}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
