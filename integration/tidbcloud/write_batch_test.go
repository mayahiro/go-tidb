package tidbcloud

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/orm"
)

type writeBenchmarkExecFunc func(context.Context, string, ...any) (sql.Result, error)

func (execute writeBenchmarkExecFunc) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return execute(ctx, query, args...)
}

func TestWriteBenchmarkBatchesPreserveValuesAndRemainder(t *testing.T) {
	t.Parallel()
	for _, upsert := range []bool{false, true} {
		for _, size := range []int{0, 100, 500, 1000} {
			t.Run(fmt.Sprintf("upsert_%t/batch_%d", upsert, size), func(t *testing.T) {
				values := makeWriteBenchmarkRows(1001, 32)
				pointers := make([]*writeBenchmarkRow, len(values))
				for i := range values {
					pointers[i] = &values[i]
				}
				ctx := context.Background()
				calls, offset := 0, 0
				executor := writeBenchmarkExecFunc(func(gotContext context.Context, query string, args ...any) (sql.Result, error) {
					if gotContext != ctx {
						t.Fatal("batch changed the caller context")
					}
					end := len(values)
					if size > 0 {
						end = min(offset+size, end)
					}
					var wantSQL string
					var wantArgs []any
					var err error
					if upsert {
						wantSQL, wantArgs, err = orm.UpsertMany(pointers[offset:end]).Build()
					} else {
						wantSQL, wantArgs, err = orm.InsertMany(pointers[offset:end]).Build()
					}
					if err != nil || query != wantSQL || !reflect.DeepEqual(args, wantArgs) {
						t.Fatalf("batch %d differs from its input: error=%v", calls, err)
					}
					if strings.Count(query, "?") != 6*(end-offset) {
						t.Fatalf("batch %d has an unexpected bind count", calls)
					}
					rows := end - offset
					offset = end
					calls++
					return driver.RowsAffected(rows), nil
				})
				affected, err := executeWriteBenchmark(ctx, executor, pointers, writeBenchmarkCase{batchSize: size, upsert: upsert})
				wantCalls := 1
				if size > 0 {
					wantCalls = (len(values)-1)/size + 1
				}
				if err != nil || affected != int64(len(values)) || offset != len(values) || calls != wantCalls {
					t.Fatalf("affected/offset/calls = %d/%d/%d, error=%v", affected, offset, calls, err)
				}
				for _, row := range values {
					if row.ID != 0 {
						t.Fatal("batch changed an input AUTO_RANDOM field")
					}
				}
			})
		}
	}
}

func TestWriteBenchmarkBatchesStopAtError(t *testing.T) {
	t.Parallel()
	failure := errors.New("batch failure")
	for _, upsert := range []bool{false, true} {
		values := makeWriteBenchmarkRows(3, 32)
		pointers := []*writeBenchmarkRow{&values[0], &values[1], &values[2]}
		calls := 0
		executor := writeBenchmarkExecFunc(func(context.Context, string, ...any) (sql.Result, error) {
			calls++
			if calls == 2 {
				return nil, failure
			}
			return driver.RowsAffected(1), nil
		})
		affected, err := executeWriteBenchmark(context.Background(), executor, pointers, writeBenchmarkCase{batchSize: 1, upsert: upsert})
		if !errors.Is(err, failure) || affected != 1 || calls != 2 {
			t.Fatalf("upsert=%t: affected=%d calls=%d error=%v", upsert, affected, calls, err)
		}
	}
}

func TestWriteBenchmarkRejectsInvalidBatchAndAcceptsEmptyInput(t *testing.T) {
	t.Parallel()
	executor := writeBenchmarkExecFunc(func(context.Context, string, ...any) (sql.Result, error) {
		t.Fatal("invalid or empty input executed a statement")
		return nil, nil
	})
	for _, test := range []writeBenchmarkCase{{batchSize: -1}, {single: true}} {
		if _, err := executeWriteBenchmark(context.Background(), executor, nil, test); err == nil {
			t.Fatalf("input %+v must fail", test)
		}
	}
	for _, size := range []int{0, 100} {
		if affected, err := executeWriteBenchmark(context.Background(), executor, nil, writeBenchmarkCase{batchSize: size}); err != nil || affected != 0 {
			t.Fatalf("empty input: affected=%d error=%v", affected, err)
		}
	}
}

func TestWriteBenchmarkAffectedRows(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		seed      int
		unchanged bool
		foundRows bool
		want      int64
	}{
		{want: 1000},
		{seed: 500, want: 1500},
		{seed: 1000, want: 2000},
		{seed: 1000, unchanged: true, want: 0},
		{seed: 1000, unchanged: true, foundRows: true, want: 1000},
		{seed: 500, unchanged: true, want: 500},
		{seed: 500, unchanged: true, foundRows: true, want: 1000},
	} {
		got := writeBenchmarkAffectedRows(writeBenchmarkCase{rows: 1000, seedRows: test.seed, unchanged: test.unchanged}, test.foundRows)
		if got != test.want {
			t.Fatalf("%+v: affected=%d", test, got)
		}
	}
}

func TestWriteBenchmarkObservationCountsDMLRUOnly(t *testing.T) {
	t.Parallel()
	for _, transaction := range []bool{false, true} {
		var metrics writeBenchmarkObservation
		if transaction {
			metrics.observe(orm.StatementEvent{Operation: orm.StatementBegin, ServerRU: &orm.ServerRUObservation{Known: true, Value: 1000}})
		}
		metrics.observe(orm.StatementEvent{Operation: orm.StatementInsert, ArgumentCount: 12, SQL: "INSERT 1", ServerRU: &orm.ServerRUObservation{Known: true, Value: 1.25}})
		metrics.observe(orm.StatementEvent{Operation: orm.StatementUpsert, ArgumentCount: 6, SQL: "INSERT 2 UPSERT", ServerRU: &orm.ServerRUObservation{Known: true, Value: 2.5}})
		if transaction {
			metrics.observe(orm.StatementEvent{Operation: orm.StatementCommit, ServerRU: &orm.ServerRUObservation{Known: true, Value: 1000}})
		}
		if err := metrics.validate(2, transaction); err != nil || metrics.ru != 3.75 || metrics.maxArguments != 12 || metrics.maxSQLBytes != len("INSERT 2 UPSERT") {
			t.Fatalf("transaction=%t metrics=%+v error=%v", transaction, metrics, err)
		}
		if err := metrics.validate(3, transaction); err == nil {
			t.Fatal("wrong statement count was accepted")
		}
		if err := metrics.validate(2, !transaction); err == nil {
			t.Fatal("wrong transaction boundary was accepted")
		}
	}
}

func TestWriteBenchmarkObservationRejectsMissingRUAndInvalidOrder(t *testing.T) {
	t.Parallel()
	dml := orm.StatementEvent{Operation: orm.StatementUpsert, ServerRU: &orm.ServerRUObservation{Known: true, Value: 1}}
	begin := orm.StatementEvent{Operation: orm.StatementBegin}
	commit := orm.StatementEvent{Operation: orm.StatementCommit}
	failure := errors.New("observation failure")
	for _, events := range [][]orm.StatementEvent{
		{{Operation: orm.StatementInsert}},
		{{Operation: orm.StatementInsert, ServerRU: &orm.ServerRUObservation{}}},
		{{Operation: orm.StatementInsert, ServerRU: &orm.ServerRUObservation{Known: true, Error: failure}}},
		{{Operation: orm.StatementInsert, Error: failure}},
		{{Operation: orm.StatementRollback}},
		{dml, begin},
		{commit},
		{begin, begin},
		{begin, dml, commit, commit},
		{begin, dml, commit, dml},
	} {
		var metrics writeBenchmarkObservation
		for _, event := range events {
			metrics.observe(event)
		}
		if metrics.err == nil {
			t.Fatalf("invalid event sequence was accepted: %+v", events)
		}
	}
}
