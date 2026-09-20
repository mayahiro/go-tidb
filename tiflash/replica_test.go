package tiflash

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testConnector struct {
	query func(string, []driver.NamedValue) (driver.Rows, error)
}

func (c *testConnector) Connect(context.Context) (driver.Conn, error) { return &testConnection{c}, nil }
func (c *testConnector) Driver() driver.Driver                        { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

type testConnection struct{ *testConnector }

func (*testConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*testConnection) Close() error                        { return nil }
func (*testConnection) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *testConnection) QueryContext(_ context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(statement, args)
}

type testRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	failure error
	closed  bool
}

func (r *testRows) Columns() []string { return r.columns }
func (r *testRows) Close() error      { r.closed = true; return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		if r.failure != nil {
			return r.failure
		}
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
func replicaRows(values ...driver.Value) *testRows {
	r := &testRows{columns: []string{"schema", "table", "count", "available", "progress"}}
	if len(values) > 0 {
		r.values = [][]driver.Value{values}
	}
	return r
}

func TestReplicaIdentifiersAndInspection(t *testing.T) {
	ddl, err := BuildEnableReplica("data`base", "or`ders")
	if err != nil || ddl != "ALTER TABLE `data``base`.`or``ders` SET TIFLASH REPLICA 2" {
		t.Fatal(ddl, err)
	}
	for _, name := range []string{"", "trailing ", "nul\x00", strings.Repeat("a", 65)} {
		if _, err := BuildEnableReplica("db", name); err == nil {
			t.Fatal(name)
		}
	}
	for _, tc := range []struct {
		name                  string
		rows                  *testRows
		wantErr               error
		configured, available bool
	}{
		{"ready", replicaRows("db", "orders", int64(2), int64(1), float64(1)), nil, true, true},
		{"building", replicaRows("db", "orders", int64(2), int64(0), 0.4), nil, true, false},
		{"unconfigured", replicaRows("db", "orders", nil, nil, nil), nil, false, false},
		{"missing", replicaRows(), ErrTableNotFound, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(&testConnector{query: func(statement string, args []driver.NamedValue) (driver.Rows, error) {
				if statement != inspectReplicaSQL || len(args) != 2 || args[0].Value != "db" || args[1].Value != "orders" {
					t.Fatal(statement, args)
				}
				return tc.rows, nil
			}})
			defer db.Close()
			got, err := InspectReplica(context.Background(), db, "db", "orders")
			if !errors.Is(err, tc.wantErr) || got.Configured != tc.configured || got.Available != tc.available || !tc.rows.closed {
				t.Fatal(got, err)
			}
		})
	}
}

func TestReplicaWaitRetainsLastStatusAndDeadline(t *testing.T) {
	failure := errors.New("permission denied")
	calls := 0
	db := sql.OpenDB(&testConnector{query: func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
		calls++
		if calls > 1 {
			return nil, failure
		}
		return replicaRows("db", "orders", int64(2), int64(0), 0.5), nil
	}})
	defer db.Close()
	if _, err := WaitReplicaReady(context.Background(), db, "db", "orders", 0); err == nil || calls != 0 {
		t.Fatal("deadline validation", err, calls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := WaitReplicaReady(ctx, db, "db", "orders", time.Millisecond)
	if !errors.Is(err, failure) || got.Progress != 0.5 || !got.Configured {
		t.Fatal(got, err)
	}
	cancel()
	if _, err := WaitReplicaReady(ctx, db, "db", "orders", 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var nilDB *sql.DB
	if _, err := InspectReplica(context.Background(), nilDB, "db", "orders"); err == nil {
		t.Fatal("typed nil accepted")
	}
}

func TestReplicaWaitCompletionAndCancellation(t *testing.T) {
	for _, ready := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		calls := 0
		db := sql.OpenDB(&testConnector{query: func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
			calls++
			available := int64(0)
			if calls == 2 {
				if ready {
					available = 1
				} else {
					cancel()
				}
			}
			return replicaRows("db", "orders", int64(2), available, 0.75), nil
		}})
		got, err := WaitReplicaReady(ctx, db, "db", "orders", time.Millisecond)
		cancel()
		db.Close()
		if ready && (err != nil || !got.Available || calls != 2) {
			t.Fatal(got, err, calls)
		}
		if !ready && !errors.Is(err, context.Canceled) {
			t.Fatal(got, err)
		}
	}
}

func TestCapabilitiesKeepFailuresUnknown(t *testing.T) {
	failure := errors.New("unknown or denied")
	var statements []string
	db := sql.OpenDB(&testConnector{query: func(statement string, _ []driver.NamedValue) (driver.Rows, error) {
		statements = append(statements, statement)
		switch len(statements) {
		case 1:
			return nil, failure
		case 2:
			return &testRows{columns: []string{"Variable_name", "Value"}, values: [][]driver.Value{{"tidb_allow_mpp", "ON"}}}, nil
		default:
			return &testRows{columns: []string{"value"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
	}})
	defer db.Close()
	result, err := ProbeCapabilities(context.Background(), db)
	if !errors.Is(err, failure) || result.ReplicaMetadata.State != Unknown || result.MPPSettings.State != Unsupported || result.WindowFunctions.State != Supported || result.VectorFunctions.State != Supported || result.VectorIndexMetadata.State != Supported {
		t.Fatal(result, err)
	}
	if len(statements) != 5 || reflect.DeepEqual(result, Capabilities{}) {
		t.Fatal(statements, result)
	}
	for _, statement := range statements {
		if !strings.HasPrefix(statement, "SELECT ") && !strings.HasPrefix(statement, "SHOW ") && !strings.HasPrefix(statement, "EXPLAIN ") {
			t.Fatal("mutation probe", statement)
		}
	}
}
