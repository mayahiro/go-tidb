package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

type aggregateConditionalSource struct {
	model.Meta `tidbgo:"table=conditional_orders"`
	ShopID     int64
	Amount     int64
	Status     string
	CreatedAt  time.Time
	DeletedAt  time.Time `tidbgo:",soft_delete"`
	Computed   string    `tidbgo:",computed"`
}

func TestAggregateConditionalBuildAndArgumentOrder(t *testing.T) {
	paid := CountIf(And(Equal("Status", "paid"), Not(In("ShopID", []int64{8, 9}))))
	revenue := SumIf("Amount", GreaterThan("Amount", int64(5)))
	q := Aggregate[aggregateConditionalSource]().
		Select(Field("ShopID").As("Shop"), CountAll().As("All"), paid.As("Paid"), revenue.As("Revenue")).
		GroupBy("Shop").Where(Between("ShopID", int64(1), int64(10))).
		Having(And(GreaterThan("Paid", int64(2)), LessThan("Revenue", int64(100)))).
		OrderBy(Desc("Revenue"), Asc("Shop")).Limit(4).Offset(1)
	countSQL := "COUNT(CASE WHEN (`a`.`status` = ? AND NOT (`a`.`shop_id` IN (?, ?))) THEN 1 END)"
	sumSQL := "SUM(CASE WHEN `a`.`amount` > ? THEN `a`.`amount` END)"
	want := "SELECT `a`.`shop_id` AS `Shop`, COUNT(*) AS `All`, " + countSQL + " AS `Paid`, " + sumSQL + " AS `Revenue` FROM `conditional_orders` AS `a` WHERE `a`.`deleted_at` IS NULL AND `a`.`shop_id` BETWEEN ? AND ? GROUP BY `a`.`shop_id` HAVING (" + countSQL + " > ? AND " + sumSQL + " < ?) ORDER BY " + sumSQL + " DESC, `a`.`shop_id` ASC LIMIT ? OFFSET ?"
	wantArgs := []any{"paid", int64(8), int64(9), int64(5), int64(1), int64(10), "paid", int64(8), int64(9), int64(2), int64(5), int64(100), int64(5), int64(4), int64(1)}
	statement, args, err := q.Build()
	if err != nil || statement != want || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("SQL=%s args=%v err=%v", statement, args, err)
	}
	if paid.aliased || revenue.aliased {
		t.Fatal("As changed the shared conditional expression")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 10 {
				got, arguments, err := q.Build()
				if err != nil || got != want || !reflect.DeepEqual(arguments, wantArgs) {
					t.Error("concurrent Build changed SQL or arguments", err)
					return
				}
				arguments[0] = "caller-owned"
			}
		})
	}
	workers.Wait()
}

func TestAggregateConditionalCalendarPredicates(t *testing.T) {
	for _, key := range []AggregateExpression{Date("CreatedAt"), YearMonth("CreatedAt")} {
		q := Aggregate[aggregateConditionalSource]().Select(key.As("Period"),
			CountIf(Or(Contains("Status", "%_!"), IsNull("Status"))).As("Count"),
			SumIf("Amount", NotIn("ShopID", []int64{})).As("Total"),
			CountIf(In("ShopID", []int64{})).As("None")).GroupBy("Period").
			Having(IsNotNull("Period"), GreaterThan("Count", int64(0))).OrderBy(Asc("Period"))
		statement, args, err := q.Build()
		if err != nil || !reflect.DeepEqual(args, []any{"%!%!_!!%", "%!%!_!!%", int64(0)}) {
			t.Fatalf("SQL=%s args=%v err=%v", statement, args, err)
		}
		for _, fragment := range []string{"COUNT(CASE WHEN (", " LIKE ? ESCAPE '!' OR `a`.`status` IS NULL)", "SUM(CASE WHEN TRUE THEN `a`.`amount` END)", "COUNT(CASE WHEN FALSE THEN 1 END)", " HAVING MIN("} {
			if !strings.Contains(statement, fragment) {
				t.Errorf("missing %s in %s", fragment, statement)
			}
		}
	}
}

func TestAggregateConditionalRejectsBeforeIO(t *testing.T) {
	for name, condition := range map[string]Predicate{
		"zero": {}, "unknown": Equal("Missing", 1), "column": Equal("shop_id", 1),
		"computed": Equal("Computed", "x"), "injection": Equal("Status) END); DROP TABLE x", "x"),
		"relation": Has("Orders"), "nested relation": Or(Equal("Status", "paid"), Not(Has("Orders"))),
		"nil": Equal("Status", nil), "nil list": In("Status", []any{nil}),
		"empty logical": And(), "short logical": Or(Equal("Status", "paid")),
		"numeric pattern": Contains("Amount", "x"), "invalid pattern value": HasPrefix("Status", 1),
	} {
		t.Run(name, func(t *testing.T) {
			for _, expr := range []AggregateExpression{CountIf(condition), SumIf("Amount", condition)} {
				state := &allTestState{}
				var values []int64
				err := Aggregate[aggregateConditionalSource]().Select(expr.As("Value")).ScanAll(context.Background(), openAllTestDB(t, state), &values)
				if err == nil || state.query != "" {
					t.Fatal("invalid condition reached I/O", err)
				}
			}
		})
	}
	for _, q := range []*AggregateQuery[aggregateConditionalSource]{
		Aggregate[aggregateConditionalSource]().Select(CountIf(Equal("Status", "paid"))),
		Aggregate[aggregateConditionalSource]().Select(SumIf("Missing", Equal("Status", "paid")).As("Total")),
		Aggregate[aggregateConditionalSource]().Select(CountIf(Equal("Status", "paid")).As("Count")).GroupBy("Count"),
		Aggregate[aggregateConditionalSource]().Select(CountIf(Equal("Paid", 1)).As("Paid")),
	} {
		if _, _, err := q.Build(); err == nil {
			t.Fatal("invalid conditional expression was accepted")
		}
	}
}

func TestAggregateConditionalScanNullAndDestinationOwnership(t *testing.T) {
	q := Aggregate[aggregateConditionalSource]().Select(CountIf(Equal("Status", "paid")).As("Count"), SumIf("Amount", Equal("Status", "paid")).As("Total"))
	state := &allTestState{columns: []string{"Count", "Total"}, values: [][]driver.Value{{int64(0), nil}}}
	db := openAllTestDB(t, state)
	type result struct {
		Count int64
		Total sql.NullString
	}
	want := []result{{}}
	var got []result
	if err := q.ScanAll(context.Background(), db, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	state.values = [][]driver.Value{{int64(3), []byte("12345678901234567890.25")}}
	if err := q.ScanAll(context.Background(), db, &got); err != nil || got[0].Total.String != "12345678901234567890.25" {
		t.Fatal(got, err)
	}
	old := got
	state.values[0][0] = "9223372036854775808"
	if err := q.ScanAll(context.Background(), db, &got); err == nil || !reflect.DeepEqual(got, old) {
		t.Fatal("overflow replaced destination")
	}
	state.columns, state.values = []string{"Total"}, [][]driver.Value{{nil}, {int64(10)}}
	var sums []*int64
	if err := Aggregate[aggregateConditionalSource]().Select(SumIf("Amount", IsNotNull("Status")).As("Total")).ScanAll(context.Background(), db, &sums); err != nil || len(sums) != 2 || sums[0] != nil || *sums[1] != 10 {
		t.Fatal(sums, err)
	}
}

type aggregateConditionalValuer struct{ calls *int }

func (v aggregateConditionalValuer) Value() (driver.Value, error) { *v.calls++; return int64(7), nil }

func TestAggregateConditionalCompareFreezesParametersAndExports(t *testing.T) {
	_, state := aggregateCompareBenchmarkData(1)
	state.columns, state.values, state.record = []string{"Count", "Total"}, [][]driver.Value{{int64(0), nil}}, true
	calls := 0
	status := []byte("paid")
	q := Aggregate[aggregateConditionalSource]().Select(CountIf(Equal("ShopID", aggregateConditionalValuer{&calls})).As("Count"), SumIf("Amount", Equal("Status", status)).As("Total")).Having(GreaterThanOrEqual("Count", int64(0))).OrderBy(Asc("Count"))
	if _, args, err := q.Build(); err != nil || calls != 0 || len(args) != 5 {
		t.Fatal("Build evaluated a Valuer or lost parameters", calls, args, err)
	}
	state.filter = func(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error, bool) {
		status[0] = 'x'
		return nil, nil, false
	}
	report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "conditional-v1"})
	if err != nil || !report.Complete || calls != 3 {
		t.Fatal("comparison did not freeze each SQL parameter once", calls, err)
	}
	for _, args := range state.arguments {
		if len(args) == 0 {
			continue
		}
		want := []any{int64(7), []byte("paid"), int64(7), int64(0), int64(7)}
		if len(args) != len(want) {
			t.Fatal("wrong parameter count", args)
		}
		for i := range args {
			if !reflect.DeepEqual(args[i].Value, want[i]) {
				t.Fatal("parameter changed", args)
			}
		}
	}
	for _, variant := range report.Variants {
		var output bytes.Buffer
		if err := report.WriteCapture(&output, variant.Name); err != nil {
			t.Fatal(err)
		}
		analysis, err := runtimecapture.AnalyzeReader(&output, runtimecapture.WithWorkload(report.Options.Case))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtimecapture.NewServerRUBaseline(analysis); err != nil {
			t.Fatal(err)
		}
	}
}
