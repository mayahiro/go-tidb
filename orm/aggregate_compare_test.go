package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

func openAggregateCompareTestDB(t *testing.T, state *aggregateCompareTestState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&aggregateCompareTestConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAggregateCompareMeasurementsObservationAndBaseline(t *testing.T) {
	q, state := aggregateCompareBenchmarkData(2)
	q.Where(Equal("Amount", int64(987654321))).ReadFrom(TiFlash).MPP(MPPEnforce)
	before, argsBefore, err := q.Build()
	if err != nil {
		t.Fatal(err)
	}
	state.record = true
	db := openAggregateCompareTestDB(t, state)
	var events []StatementEvent
	observed := Observe(db, func(event StatementEvent) {
		if db.Stats().InUse != 0 {
			t.Error("callback ran before connection release")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Error("callback cannot acquire connection", err)
		} else {
			_ = conn.Close()
		}
		events = append(events, event)
	}, CollectServerRU())
	var captured bytes.Buffer
	capture := NewRuntimeCapture(&captured)
	ctx := WithRuntimeCapture(context.Background(), capture, CollectServerRU())
	report, err := q.Compare(ctx, observed, AggregateCompareOptions{Case: "orders-fixed-v1"})
	if err != nil || !report.Complete || capture.Err() != nil {
		t.Fatalf("Compare: %v, report=%#v, capture=%v", err, report, capture.Err())
	}
	if len(state.queries) != 48 || len(events) != 24 || state.connections != 1 {
		t.Fatalf("queries=%d events=%d connections=%d", len(state.queries), len(events), state.connections)
	}
	if report.Options.Samples != 5 || report.Options.MaxRows != 100000 || report.Options.Case != "orders-fixed-v1" {
		t.Fatal("effective options missing", report.Options)
	}
	for i, event := range events {
		if event.Error != nil || event.Duration < 0 || event.Arguments != nil {
			t.Fatalf("event %d=%#v", i, event)
		}
		if i < 21 && (event.ServerRU == nil || !event.ServerRU.Known || event.ServerRU.AuxiliaryStatements != 1) {
			t.Fatalf("missing target RU: %#v", event)
		}
		if i >= 21 && event.ServerRU != nil {
			t.Fatal("plan acquired ordinary SELECT RU")
		}
	}
	for _, query := range state.queries {
		if strings.HasPrefix(query, "SET ") || strings.HasPrefix(query, "ALTER ") {
			t.Fatal("unexpected mutation", query)
		}
	}
	for round := range 7 {
		for step := range 3 {
			query := state.queries[(round*3+step)*2]
			variant := (round + step) % 3
			if variant == 0 && strings.Contains(query, "/*+") || variant == 1 && !strings.Contains(query, "TIKV[a]") || variant == 2 && !strings.Contains(query, "TIFLASH[a]") {
				t.Fatal("variant order or hints changed", query)
			}
		}
	}
	if strings.Contains(captured.String(), "987654321") {
		t.Fatal("bind leaked into capture")
	}
	for index, variant := range report.Variants {
		if len(variant.Samples) != 5 || variant.Rows != 2 || variant.ServerRU.Count != 5 || variant.ServerRU.Mean != float64(index+1) || variant.LatencyMS.Count != 5 {
			t.Fatalf("invalid samples=%#v", variant)
		}
		if index == 0 && variant.PlanStatus != "unrequested" || index > 0 && variant.PlanStatus != "matched" {
			t.Fatal(variant.PlanStatus)
		}
		if variant.Plan.Planned != nil || len(variant.Plan.Executed) != 1 || variant.Plan.Warnings == nil {
			t.Fatal("incorrect plan provenance")
		}
		var artifact bytes.Buffer
		if err := report.WriteCapture(&artifact, variant.Name); err != nil {
			t.Fatal(err)
		}
		analysis, err := runtimecapture.AnalyzeReader(&artifact, runtimecapture.WithWorkload(report.Options.Case))
		if err != nil {
			t.Fatal(err)
		}
		if analysis.Statistics.Statements != 5 || analysis.Workload == nil || analysis.Workload.Scopes != 5 {
			t.Fatal("export included warmup or plans")
		}
		baseline, err := runtimecapture.NewServerRUBaseline(analysis)
		if err != nil {
			t.Fatal(err)
		}
		comparison, err := runtimecapture.CompareServerRU(analysis, baseline)
		if err != nil || comparison.Summary.Passed != 1 {
			t.Fatalf("baseline integration: %#v %v", comparison, err)
		}
	}
	after, argsAfter, err := q.Build()
	if err != nil || before != after || !reflect.DeepEqual(argsBefore, argsAfter) {
		t.Fatal("Compare mutated source query")
	}
}

func TestAggregateCompareRejectsInvalidInputsBeforeIO(t *testing.T) {
	for _, options := range []AggregateCompareOptions{
		{}, {Case: "private/value"}, {Case: "test", Samples: 4}, {Case: "test", Samples: 1001}, {Case: "test", MaxRows: -1},
		{Case: "test", FloatRelativeTolerance: 2}, {Case: "test", FloatAbsoluteTolerance: math.NaN()}, {Case: "test", FloatRelativeTolerance: math.Inf(1)},
	} {
		q, state := aggregateCompareBenchmarkData(1)
		if _, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), options); err == nil || state.connections != 0 {
			t.Fatalf("options=%#v err=%v connections=%d", options, err, state.connections)
		}
	}
	q, state := aggregateCompareBenchmarkData(1)
	q.orderBy = nil
	if _, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "test"}); err == nil || state.connections != 0 {
		t.Fatal("unordered grouping accepted")
	}
	var nilQuery *AggregateQuery[aggregateBenchmarkSource]
	if _, err := nilQuery.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "test"}); err == nil {
		t.Fatal("nil query accepted")
	}
	q, _ = aggregateCompareBenchmarkData(1)
	if _, err := q.Compare(context.Background(), nilRowsExecutor{}, AggregateCompareOptions{Case: "test"}); err == nil {
		t.Fatal("executor without session affinity accepted")
	}
}

func TestAggregateCompareDetectsChangedValuesRowsAndOrdering(t *testing.T) {
	for _, kind := range []string{"value", "reused_bytes", "order", "count", "type"} {
		t.Run(kind, func(t *testing.T) {
			q, state := aggregateCompareBenchmarkData(2)
			state.values[0][2] = []byte("private-original-decimal")
			state.values[1][2] = []byte("another-value")
			state.filter = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
				if state.selects != 1 || query == lastServerRUQuery {
					return nil, nil, false
				}
				values := [][]driver.Value{append([]driver.Value(nil), state.values[0]...), append([]driver.Value(nil), state.values[1]...)}
				switch kind {
				case "value":
					values[0][2] = []byte("private-changed-decimal")
				case "reused_bytes":
					state.values[0][2].([]byte)[0] = 'x'
				case "order":
					values[0], values[1] = values[1], values[0]
				case "count":
					values = values[:1]
				case "type":
					values[0][2] = "private-original-decimal"
				}
				return &aggregateCompareTestRows{columns: state.columns, values: values, closed: func() { state.last = "select_closed" }}, nil, true
			}
			report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "mismatch"})
			if err == nil || report.Complete || !strings.Contains(err.Error(), "differs") || strings.Contains(err.Error(), "private-") {
				t.Fatalf("result equality failure=%v", err)
			}
			var artifact bytes.Buffer
			if report.WriteCapture(&artifact, "auto") == nil || artifact.Len() != 0 {
				t.Fatal("incomplete comparison exported")
			}
		})
	}
}

type aggregateCompareValuer struct{ calls *int }

func (v aggregateCompareValuer) Value() (driver.Value, error) {
	*v.calls++
	return int64(*v.calls), nil
}

func TestAggregateCompareFreezesArgumentsAndFloatTolerance(t *testing.T) {
	q, state := aggregateCompareBenchmarkData(1)
	calls := 0
	q.Having(GreaterThan("Count", aggregateCompareValuer{calls: &calls}))
	state.record = true
	state.values[0][2] = 1.0
	state.filter = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
		if state.selects > 0 && query != lastServerRUQuery && !strings.HasPrefix(query, "EXPLAIN") && query != "SHOW WARNINGS" {
			state.values[0][2] = 1.0000001
		}
		return nil, nil, false
	}
	report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "float", FloatRelativeTolerance: 0.000001})
	if err != nil || !report.Complete || calls != 1 {
		t.Fatalf("compare=%v, Valuer calls=%d", err, calls)
	}
	for i, args := range state.arguments {
		if len(args) != 0 && args[0].Value != int64(1) {
			t.Fatalf("input changed at %d: %#v", i, args)
		}
	}
	for _, pair := range [][2]any{{nil, 0.0}, {[]byte("1.00"), []byte("1.01")}, {math.NaN(), math.NaN()}, {math.Inf(1), math.MaxFloat64}, {math.MaxFloat64, -math.MaxFloat64}} {
		if equalAggregateComparisonValue(pair[0], pair[1], AggregateCompareOptions{FloatRelativeTolerance: 0.5}) {
			t.Fatal("unacceptable tolerance match", pair)
		}
	}
	input := []byte("frozen")
	unsigned := uint64(math.MaxUint64)
	args, err := snapshotAggregateCompareArguments([]any{input, &unsigned})
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 'x'
	unsigned = 0
	if string(args[0].([]byte)) != "frozen" || args[1] != uint64(math.MaxUint64) {
		t.Fatal("mutable argument was not frozen")
	}
}

func TestAggregateCompareFailuresRetainPartialCoverage(t *testing.T) {
	failure := errors.New("injected failure")
	for _, kind := range []string{"query", "iteration", "close", "ru", "ru_decode", "columns", "max_rows", "plan", "plan_columns", "warnings", "warning_scan", "warning_iteration", "warning_close", "unknown_task", "fallback", "cancel", "cancel_plan"} {
		t.Run(kind, func(t *testing.T) {
			q, state := aggregateCompareBenchmarkData(2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := AggregateCompareOptions{Case: "failure"}
			if kind == "max_rows" {
				options.MaxRows = 1
			}
			state.filter = func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error, bool) {
				plan := strings.HasPrefix(query, "EXPLAIN ANALYZE ")
				if query == lastServerRUQuery {
					if kind == "ru" {
						return nil, failure, true
					}
					if kind == "ru_decode" {
						return &aggregateCompareTestRows{columns: []string{"info"}, values: [][]driver.Value{{`{"missing":1}`}}}, nil, true
					}
				}
				if query == "SHOW WARNINGS" {
					if kind == "cancel_plan" {
						return &aggregateCompareTestRows{columns: []string{"Level", "Code", "Message"}, closed: cancel}, nil, true
					}
					if kind == "warnings" {
						return nil, failure, true
					}
					if strings.HasPrefix(kind, "warning_") {
						s := &allTestState{columns: []string{"Level", "Code", "Message"}, values: [][]driver.Value{{"Warning", int64(1), "private-warning"}}}
						if kind == "warning_scan" {
							s.values[0][1] = "invalid"
						}
						if kind == "warning_iteration" {
							s.nextErr = failure
						}
						if kind == "warning_close" {
							s.closeErr = failure
						}
						return &allTestRows{state: s}, nil, true
					}
				}
				if plan {
					if kind == "plan" {
						return nil, failure, true
					}
					if kind == "plan_columns" {
						return &aggregateCompareTestRows{columns: []string{"future_layout"}}, nil, true
					}
					if kind == "unknown_task" || kind == "fallback" {
						task := "future[tiflash]"
						if kind == "fallback" {
							task = "cop[tikv]"
						}
						return &aggregateCompareTestRows{columns: explainAnalyzeColumnNames[:], values: [][]driver.Value{{"TableFullScan_1", "2", int64(2), task, "table:a", "time:1ms", "", "N/A", "N/A"}}, closed: func() { state.last = "plan_closed" }}, nil, true
					}
				}
				if !plan && query != lastServerRUQuery && query != "SHOW WARNINGS" {
					if kind == "query" {
						return nil, failure, true
					}
					if kind == "cancel" {
						cancel()
						return nil, ctx.Err(), true
					}
					if kind == "columns" {
						return &aggregateCompareTestRows{columns: []string{"invalid"}}, nil, true
					}
					if kind == "iteration" || kind == "close" {
						s := &allTestState{columns: state.columns, values: state.values}
						if kind == "iteration" {
							s.nextErr = failure
						}
						if kind == "close" {
							s.closeErr = failure
						}
						return &allTestRows{state: s}, nil, true
					}
				}
				return nil, nil, false
			}
			db := openAggregateCompareTestDB(t, state)
			report, err := q.Compare(ctx, db, options)
			if err == nil || report.Complete || db.Stats().InUse != 0 {
				t.Fatalf("failure=%s error=%v complete=%t in_use=%d", kind, err, report.Complete, db.Stats().InUse)
			}
			if kind == "fallback" && report.Variants[2].PlanStatus != "mismatch" {
				t.Fatal(report.Variants[2])
			}
			if kind == "unknown_task" && report.Variants[2].PlanStatus != "unknown" {
				t.Fatal(report.Variants[2])
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if kind == "plan" || strings.HasPrefix(kind, "warning") || kind == "unknown_task" || kind == "fallback" || kind == "cancel_plan" {
				if len(report.Variants[0].Samples) != 5 {
					t.Fatal("completed measurements lost")
				}
			}
		})
	}
}

func TestAggregateCompareDriverNumericResults(t *testing.T) {
	for _, value := range []driver.Value{uint64(math.MaxUint64), float32(1.25), float64(1.25)} {
		q, state := aggregateCompareBenchmarkData(1)
		state.values[0][2] = value
		if report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "numeric"}); err != nil || !report.Complete {
			t.Fatalf("driver value %T: %v", value, err)
		}
	}
	if !equalAggregateComparisonValue(float32(1), float32(1.0000001), AggregateCompareOptions{FloatRelativeTolerance: 0.000001}) {
		t.Fatal("FLOAT tolerance was not applied")
	}
	q, state := aggregateCompareBenchmarkData(1)
	calls := 0
	q.Having(GreaterThan("Count", aggregateCompareValuer{calls: &calls}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Compare(ctx, openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "canceled"}); !errors.Is(err, context.Canceled) || calls != 0 || state.connections != 0 {
		t.Fatalf("canceled comparison performed work: %v, valuer=%d, connections=%d", err, calls, state.connections)
	}
}

func TestAggregateCompareBorrowedSessionsAndConcurrentReads(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		q, state := aggregateCompareBenchmarkData(0)
		db := openAggregateCompareTestDB(t, state)
		var session QueryExecutor
		var release func() error
		if transaction {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			session, release = tx, tx.Rollback
		} else {
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			session, release = conn, conn.Close
		}
		report, err := q.Compare(context.Background(), session, AggregateCompareOptions{Case: "empty"})
		if err != nil || !report.Complete || report.Variants[0].Rows != 0 || db.Stats().InUse != 1 {
			t.Fatalf("borrowed session: %v", err)
		}
		_ = release()
	}
	q, _ := aggregateCompareBenchmarkData(1)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			_, state := aggregateCompareBenchmarkData(1)
			db := sql.OpenDB(&aggregateCompareTestConnector{state: state})
			defer db.Close()
			if report, err := q.Compare(context.Background(), db, AggregateCompareOptions{Case: "parallel"}); err != nil || !report.Complete {
				t.Errorf("parallel Compare: %v", err)
			}
		})
	}
	wg.Wait()
}

type aggregateCompareShortWriter struct{}

func (aggregateCompareShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestAggregateCompareExportAndStatistics(t *testing.T) {
	q, state := aggregateCompareBenchmarkData(1)
	report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "export"})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(report.WriteCapture(aggregateCompareShortWriter{}, "auto"), io.ErrShortWrite) {
		t.Fatal("short write lost")
	}
	if report.WriteCapture(io.Discard, "missing") == nil || report.WriteCapture(nil, "auto") == nil {
		t.Fatal("invalid export accepted")
	}
	statistics := aggregateComparisonStatistics([]float64{1, 2, 5, 6})
	if statistics.Mean != 3.5 || statistics.Median != 3.5 || statistics.Minimum != 1 || statistics.Maximum != 6 {
		t.Fatal(statistics)
	}
	report.Variants[0].Samples[4].ServerRU = math.Inf(1)
	var output bytes.Buffer
	if report.WriteCapture(&output, "auto") == nil || output.Len() != 0 {
		t.Fatal("invalid final sample produced partial export")
	}
}
