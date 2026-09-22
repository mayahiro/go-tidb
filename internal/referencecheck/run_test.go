package referencecheck

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type probeStep struct {
	values                      []driver.Value
	queryErr, rowsErr, closeErr error
}
type probeConnector struct {
	steps   []probeStep
	queries []string
	closed  int
}

func (c *probeConnector) Connect(context.Context) (driver.Conn, error) {
	return &probeConn{connector: c}, nil
}
func (*probeConnector) Driver() driver.Driver { return probeDriver{} }

type probeDriver struct{}

func (probeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unexpected Open") }

type probeConn struct{ connector *probeConnector }

func (*probeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (*probeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected Begin") }
func (*probeConn) Close() error                        { return nil }
func (c *probeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "SELECT EXISTS (") || len(args) != 0 {
		return nil, errors.New("unexpected query")
	}
	index := len(c.connector.queries)
	c.connector.queries = append(c.connector.queries, query)
	if index >= len(c.connector.steps) {
		return nil, errors.New("unexpected extra query")
	}
	step := c.connector.steps[index]
	if step.queryErr != nil {
		return nil, step.queryErr
	}
	return &probeRows{step: step, connector: c.connector}, nil
}

type probeRows struct {
	step      probeStep
	index     int
	connector *probeConnector
}

func (*probeRows) Columns() []string { return []string{"orphan"} }
func (r *probeRows) Close() error    { r.connector.closed++; return r.step.closeErr }
func (r *probeRows) Next(dest []driver.Value) error {
	if r.index == len(r.step.values) {
		if r.step.rowsErr != nil {
			return r.step.rowsErr
		}
		return io.EOF
	}
	dest[0] = r.step.values[r.index]
	r.index++
	return nil
}

func TestRunReportsOrphansAndStopsOnErrors(t *testing.T) {
	for _, tc := range []struct {
		name           string
		steps          []probeStep
		checked        int
		orphan, failed bool
	}{
		{"clean", []probeStep{{values: []driver.Value{int64(0)}}, {values: []driver.Value{int64(0)}}}, 2, false, false},
		{"orphan", []probeStep{{values: []driver.Value{int64(1)}}, {values: []driver.Value{int64(0)}}}, 2, true, false},
		{"partial", []probeStep{{values: []driver.Value{int64(1)}}, {queryErr: errors.New("secret database address")}}, 1, true, true},
		{"empty", []probeStep{{}}, 0, false, true},
		{"multiple", []probeStep{{values: []driver.Value{int64(0), int64(1)}}}, 0, false, true},
		{"scan", []probeStep{{values: []driver.Value{"secret value"}}}, 0, false, true},
		{"rows", []probeStep{{values: []driver.Value{int64(0)}, rowsErr: errors.New("secret rows error")}}, 0, false, true},
		{"close", []probeStep{{values: []driver.Value{int64(0)}, closeErr: errors.New("secret close error")}}, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector := &probeConnector{steps: tc.steps}
			db := sql.OpenDB(connector)
			defer db.Close()
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ref := Reference{Name: "Child.Parent", ChildTable: "child", ChildColumns: []string{"parent_id"}, ParentTable: "parent", ParentColumns: []string{"id"}}
			results, err := Run(ctx, db, []Reference{ref, ref})
			if (err != nil) != tc.failed {
				t.Fatalf("err=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked driver details")
			}
			checked := 0
			for _, result := range results {
				if result.Checked {
					checked++
				}
			}
			if checked != tc.checked || len(results) != 2 || results[0].Orphan != tc.orphan {
				t.Fatalf("results=%+v", results)
			}
			if connector.closed < checked {
				t.Fatal("rows not closed")
			}
		})
	}
}

func TestRunRequiresDeadlineAndValidatesBeforeIO(t *testing.T) {
	connector := &probeConnector{}
	db := sql.OpenDB(connector)
	defer db.Close()
	if _, err := Run(context.Background(), db, nil); err == nil {
		t.Fatal("missing deadline accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ref := Reference{Name: "Child.Parent", ChildTable: "child", ChildColumns: []string{"parent_id"}, ParentTable: "parent", ParentColumns: []string{"id"}}
	if _, err := Run(ctx, db, []Reference{ref, {}}); err == nil {
		t.Fatal("invalid mapping accepted")
	}
	cancel()
	if _, err := Run(ctx, db, []Reference{ref}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if len(connector.queries) != 0 {
		t.Fatal("executed before validation or after cancellation")
	}
}
