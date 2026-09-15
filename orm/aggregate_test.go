package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

type aggregateOrder struct {
	model.Meta `tidbgo:"table=aggregate_orders"`
	ID         int64 `tidbgo:",pk"`
	ShopID     int64 `tidbgo:"shop_key"`
	Amount     scanDecimal
	Status     string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
	Computed   int64     `tidbgo:",computed"`
}

func aggregateStatsQuery() *AggregateQuery[aggregateOrder] {
	return Aggregate[aggregateOrder]().Select(Field("ShopID"), CountAll().As("OrderCount"), Sum("Amount").As("Total")).GroupBy("ShopID")
}

func TestAggregateBuildScopesExpressionsAndHints(t *testing.T) {
	q := aggregateStatsQuery().Where(Equal("Status", "paid")).
		Having(And(GreaterThan("OrderCount", int64(2)), IsNotNull("Total"))).
		OrderBy(Desc("Total"), Asc("ShopID")).Limit(5).Offset(1).
		ReadFrom(TiFlash).MPP(MPPEnforce)
	got, args, err := q.Build()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT /*+ READ_FROM_STORAGE(TIFLASH[a]) SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) */ `a`.`shop_key` AS `ShopID`, COUNT(*) AS `OrderCount`, SUM(`a`.`amount`) AS `Total` FROM `aggregate_orders` AS `a` WHERE `a`.`deleted_at` IS NULL AND `a`.`status` = ? GROUP BY `a`.`shop_key` HAVING (COUNT(*) > ? AND SUM(`a`.`amount`) IS NOT NULL) ORDER BY SUM(`a`.`amount`) DESC, `a`.`shop_key` ASC LIMIT ? OFFSET ?"
	if got != want || !reflect.DeepEqual(args, []any{"paid", int64(2), int64(5), int64(1)}) {
		t.Fatalf("SQL = %s; args = %#v", got, args)
	}
	args[0] = "changed"
	_, again, _ := q.Build()
	if again[0] != "paid" {
		t.Fatal("Build arguments are not detached")
	}
	plain, _, err := aggregateStatsQuery().WithDeleted().Build()
	if err != nil || strings.Contains(plain, "/*+") || strings.Contains(plain, "deleted_at") {
		t.Fatalf("default SQL = %s; error = %v", plain, err)
	}
	// The output name intentionally collides with a base column: HAVING and
	// ORDER BY must reference the aggregate expression, not the source column.
	alias, _, err := Aggregate[aggregateOrder]().Select(CountAll().As("ID")).Having(Equal("ID", 2)).OrderBy(Asc("ID")).Build()
	if err != nil || !strings.Contains(alias, "HAVING COUNT(*) = ? ORDER BY COUNT(*) ASC") {
		t.Fatalf("alias SQL = %s; error = %v", alias, err)
	}
}

func TestAggregateFunctionsAndHaving(t *testing.T) {
	q := Aggregate[aggregateOrder]().Select(Count("Amount").As("Count"), CountDistinct("ShopID").As("Shops"), Avg("Amount").As("Average"), Min("Amount").As("Minimum"), Max("Amount").As("Maximum"))
	query, args, err := q.Having(Or(In("Shops", []int{1, 2}), Not(Between("Count", 2, 4))), NotIn("Count", []int{}), IsNull("Minimum")).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"COUNT(`a`.`amount`)", "COUNT(DISTINCT `a`.`shop_key`)", "AVG(`a`.`amount`)", "MIN(`a`.`amount`)", "MAX(`a`.`amount`)", "HAVING (COUNT(DISTINCT `a`.`shop_key`) IN (?, ?) OR NOT (COUNT(`a`.`amount`) BETWEEN ? AND ?)) AND TRUE AND MIN(`a`.`amount`) IS NULL"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("missing %s in %s", fragment, query)
		}
	}
	if !reflect.DeepEqual(args, []any{1, 2, 2, 4}) {
		t.Fatal(args)
	}
}

func TestAggregateKeepsValuerOfflineAndHintFingerprintSeparate(t *testing.T) {
	calls := 0
	value := observedValuer{calls: &calls, text: "12.30"}
	q := Aggregate[valuerPredicateModel]().Select(CountAll().As("Count")).Where(Equal("Value", value)).Having(GreaterThan("Count", value))
	statement, args, err := q.Build()
	if err != nil || calls != 0 || len(args) != 2 {
		t.Fatalf("Build invoked Valuer or lost arguments: %v, calls=%d", err, calls)
	}
	plain := runtimecapture.StatementFingerprint("SELECT", statement)
	other, _, err := q.ReadFrom(TiFlash).Build()
	if err != nil || plain == runtimecapture.StatementFingerprint("SELECT", other) {
		t.Fatalf("hints must distinguish execution SQL: %v", err)
	}
}

func TestAggregateParallelScanAndServerRU(t *testing.T) {
	q := aggregateStatsQuery()
	var workers sync.WaitGroup
	for range 10 {
		workers.Go(func() {
			state := &allTestState{columns: []string{"ShopID", "OrderCount", "Total"}, values: [][]driver.Value{{int64(1), int64(2), "3.25"}}}
			db := sql.OpenDB(&allTestConnector{state: state})
			defer db.Close()
			for range 10 {
				var results []*struct {
					ShopID, OrderCount int64
					Total              sql.NullString
				}
				if err := q.ScanAll(context.Background(), db, &results); err != nil || len(results) != 1 || results[0].Total.String != "3.25" {
					t.Errorf("parallel result: %v", err)
				}
			}
		})
	}
	workers.Wait()
	state := &serverRUObserverState{serverRU: `{"ru_consumption":2.5}`, requireTargetRowsClose: true, targetColumns: []string{"Count"}, targetValues: [][]driver.Value{{int64(7)}}}
	db := openServerRUObserverDB(t, state)
	var event StatementEvent
	ctx := WithStatementObserver(context.Background(), func(value StatementEvent) { event = value }, CollectServerRU())
	var counts []int64
	if err := Aggregate[aggregateOrder]().Select(CountAll().As("Count")).ScanAll(ctx, db, &counts); err != nil {
		t.Fatal(err)
	}
	assertCollectedServerRU(t, event.ServerRU, 2.5)
	target, probe, _, queries, closed := state.snapshot()
	if target != probe || queries != 1 || !closed || !reflect.DeepEqual(counts, []int64{7}) {
		t.Fatal("aggregate ServerRU did not use the completed target session")
	}
}

func TestAggregateRejectsInvalidStructureOffline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		q       *AggregateQuery[aggregateOrder]
		message string
	}{
		{"nil", nil, "nil aggregate"},
		{"empty", Aggregate[aggregateOrder](), "at least one"},
		{"no alias", Aggregate[aggregateOrder]().Select(CountAll()), "use As"},
		{"empty alias", Aggregate[aggregateOrder]().Select(Field("ShopID").As("")), "use As"},
		{"unsafe alias", Aggregate[aggregateOrder]().Select(CountAll().As("A*/ SELECT")), "exported Go name"},
		{"unexported alias", Aggregate[aggregateOrder]().Select(CountAll().As("total")), "exported Go name"},
		{"duplicate alias", Aggregate[aggregateOrder]().Select(CountAll().As("Total"), Sum("Amount").As("TOTAL")), "collides"},
		{"group missing", Aggregate[aggregateOrder]().Select(Field("ShopID"), CountAll().As("Count")), "GroupBy"},
		{"group repeated", aggregateStatsQuery().GroupBy("ShopID"), "repeats"},
		{"SQL field", aggregateStatsQuery().GroupBy("shop_key"), "unknown output"},
		{"computed", Aggregate[aggregateOrder]().Select(Sum("Computed").As("Total")), "base-table field"},
		{"unknown where", aggregateStatsQuery().Where(Equal("unknown", 1)), "mapped scalar"},
		{"relation", aggregateStatsQuery().Where(Not(Has("Orders"))), "relations"},
		{"having source", aggregateStatsQuery().Having(Equal("Amount", 1)), "unknown output"},
		{"having nil", aggregateStatsQuery().Having(Equal("Total", nil)), "NULL argument"},
		{"having LIKE", aggregateStatsQuery().Having(Contains("Total", "1")), "unsupported"},
		{"having relation", aggregateStatsQuery().Having(Has("Orders")), "relations"},
		{"having logical", aggregateStatsQuery().Having(And(Equal("OrderCount", 2))), "invalid logical"},
		{"order unknown", aggregateStatsQuery().OrderBy(Asc("Amount")), "unknown output"},
		{"limit negative", aggregateStatsQuery().Limit(-1), "must not be negative"},
		{"offset without limit", aggregateStatsQuery().Offset(2), "requires LIMIT"},
		{"engine", aggregateStatsQuery().ReadFrom("anything"), "TiKV or TiFlash"},
		{"zero engine", aggregateStatsQuery().ReadFrom(""), "TiKV or TiFlash"},
		{"MPP", aggregateStatsQuery().MPP("disabled"), "MPPAuto or MPPEnforce"},
		{"conflict", aggregateStatsQuery().ReadFrom(TiKV).MPP(MPPEnforce), "conflicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.q.Build()
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v; want %s", err, tc.message)
			}
		})
	}
	if _, _, err := Aggregate[int]().Select(CountAll().As("Count")).Build(); err == nil {
		t.Fatal("scalar source accepted")
	}
	if _, _, err := Aggregate[*aggregateOrder]().Select(CountAll().As("Count")).Build(); err == nil {
		t.Fatal("pointer source accepted")
	}
}

func TestAggregateScanAllResultOwnershipNullAndOverflow(t *testing.T) {
	type stats struct {
		Total      sql.NullString
		OrderCount int64
		ShopID     int64 `tidbgo:"ignored"`
		Extra      string
	}
	state := &allTestState{columns: []string{"ShopID", "OrderCount", "Total"}, values: [][]driver.Value{{int64(3), int64(2), []byte("123.45")}, {int64(4), int64(1), nil}}}
	db := openAllTestDB(t, state)
	values := []stats{{ShopID: 99, Extra: "owned"}}
	original := values
	if err := aggregateStatsQuery().ScanAll(context.Background(), db, &values); err != nil {
		t.Fatal(err)
	}
	want := []stats{{ShopID: 3, OrderCount: 2, Total: sql.NullString{String: "123.45", Valid: true}}, {ShopID: 4, OrderCount: 1}}
	if !reflect.DeepEqual(values, want) || original[0].Extra != "owned" {
		t.Fatalf("values = %#v; original = %#v", values, original)
	}
	for _, tc := range []struct {
		name        string
		input       driver.Value
		destination any
	}{
		{"overflow", int64(256), &[]uint8{7}},
		{"NULL integer", nil, &[]int64{7}},
		{"NULL time aggregate", nil, &[]time.Time{time.Unix(1, 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &allTestState{columns: []string{"Result"}, values: [][]driver.Value{{tc.input}}}
			db := openAllTestDB(t, state)
			before := reflect.ValueOf(tc.destination).Elem().Interface()
			err := Aggregate[aggregateOrder]().Select(Max("DeletedAt").As("Result")).ScanAll(context.Background(), db, tc.destination)
			if err == nil || !reflect.DeepEqual(reflect.ValueOf(tc.destination).Elem().Interface(), before) {
				t.Fatalf("error = %v; result = %v", err, tc.destination)
			}
		})
	}
	var sums []scanDecimal
	state.columns, state.values = []string{"Total"}, [][]driver.Value{{"12345678901234567890.12"}}
	if err := Aggregate[aggregateOrder]().Select(Sum("Amount").As("Total")).ScanAll(context.Background(), db, &sums); err != nil || len(sums) != 1 || sums[0].text != "12345678901234567890.12" {
		t.Fatalf("Scanner = %v; error = %v", sums, err)
	}
	state.values = nil
	if err := aggregateStatsQuery().ScanAll(context.Background(), db, &values); err != nil || values == nil || len(values) != 0 {
		t.Fatalf("empty = %#v; error = %v", values, err)
	}
}

func TestAggregateScanAllValidationAndErrors(t *testing.T) {
	for _, target := range []any{nil, (*[]int64)(nil), []int64{}, new([]int64), new([]struct{ OrderCount int64 }), new([]sql.RawBytes)} {
		state := &allTestState{}
		if err := aggregateStatsQuery().ScanAll(context.Background(), openAllTestDB(t, state), target); err == nil || state.query != "" {
			t.Fatalf("target=%T; error=%v; query=%s", target, err, state.query)
		}
	}
	failure := errors.New("driver failure")
	for _, state := range []*allTestState{
		{queryErr: failure},
		{columns: []string{"Count"}, values: [][]driver.Value{{int64(1)}}, nextErr: failure},
		{columns: []string{"Count"}, values: [][]driver.Value{{int64(1)}}, closeErr: failure},
	} {
		values := []int64{99}
		err := Aggregate[aggregateOrder]().Select(CountAll().As("Count")).ScanAll(context.Background(), openAllTestDB(t, state), &values)
		if !errors.Is(err, failure) || !reflect.DeepEqual(values, []int64{99}) {
			t.Fatalf("error = %v; result = %v", err, values)
		}
	}
}

func TestAggregateCaptureAndConcurrentBuild(t *testing.T) {
	q := aggregateStatsQuery().Where(Equal("Status", "private-bind"))
	plain, _, _ := q.Build()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 20 {
				sql, _, err := q.Build()
				if err != nil || sql != plain {
					t.Errorf("parallel Build changed: %v", err)
				}
				_, _, _ = aggregateStatsQuery().ReadFrom(TiFlash).Build()
			}
		})
	}
	wg.Wait()
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	state := &allTestState{columns: []string{"ShopID", "OrderCount", "Total"}}
	var values []struct {
		ShopID, OrderCount int64
		Total              sql.NullString
	}
	if err := q.ScanAll(ctx, openAllTestDB(t, state), &values); err != nil {
		t.Fatal(err)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if len(records) != 1 || records[0].Source != runtimecapture.SourceTypedAggregate || records[0].Query != nil || !strings.HasPrefix(records[0].Fingerprint, "s1:") {
		t.Fatalf("capture=%v", records)
	}
	if strings.Contains(records[0].SQL, "private-bind") {
		t.Fatal("capture leaked bind")
	}
}
