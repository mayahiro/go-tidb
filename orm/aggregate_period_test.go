package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

type aggregatePeriodSource struct {
	model.Meta `tidbgo:"table=period_orders"`
	CreatedAt  *time.Time `tidbgo:"created_at"`
	StoreID    int64
	Amount     int64
	Day        int64
	Computed   time.Time `tidbgo:",computed"`
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

func TestAggregatePeriodBuildAndOutputReferences(t *testing.T) {
	at := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	q := Aggregate[aggregatePeriodSource]().
		Select(Date("CreatedAt").As("Day"), YearMonth("CreatedAt").As("Month"), Field("StoreID").As("Store"), CountAll().As("Count")).
		GroupBy("Day", "Month", "Store").Where(GreaterThanOrEqual("CreatedAt", at)).
		Having(And(IsNotNull("Day"), GreaterThanOrEqual("Month", int64(202402)))).
		OrderBy(Asc("Day"), Asc("Store")).Limit(5).Offset(2)
	want := "SELECT DATE(`a`.`created_at`) AS `Day`, EXTRACT(YEAR_MONTH FROM `a`.`created_at`) AS `Month`, `a`.`store_id` AS `Store`, COUNT(*) AS `Count` FROM `period_orders` AS `a` WHERE `a`.`deleted_at` IS NULL AND `a`.`created_at` >= ? GROUP BY DATE(`a`.`created_at`), EXTRACT(YEAR_MONTH FROM `a`.`created_at`), `a`.`store_id` HAVING (MIN(DATE(`a`.`created_at`)) IS NOT NULL AND MIN(EXTRACT(YEAR_MONTH FROM `a`.`created_at`)) >= ?) ORDER BY DATE(`a`.`created_at`) ASC, `a`.`store_id` ASC LIMIT ? OFFSET ?"
	got, args, err := q.Build()
	if err != nil || got != want || !reflect.DeepEqual(args, []any{at, int64(202402), int64(5), int64(2)}) {
		t.Fatalf("SQL=%s args=%v err=%v", got, args, err)
	}
	// A source field named Day cannot shadow the selected DATE expression.
	if strings.Contains(got, "`a`.`day`") {
		t.Fatal("group/output alias resolved to an unrelated source column")
	}
	shared := Date("CreatedAt")
	duplicate := Aggregate[aggregatePeriodSource]().Select(shared.As("A"), shared.As("B"), CountAll().As("Count")).GroupBy("A")
	if _, _, err := duplicate.Build(); err != nil {
		t.Fatal("equivalent selected expressions cannot share a group key", err)
	}
	if _, _, err := duplicate.GroupBy("B").Build(); err == nil {
		t.Fatal("redundant group expressions accepted")
	}
	if shared.aliased {
		t.Fatal("As mutated shared expression")
	}
	// Builder call order is independent of reference resolution.
	if _, _, err := Aggregate[aggregatePeriodSource]().GroupBy("Period").Select(YearMonth("CreatedAt").As("Period")).Build(); err != nil {
		t.Fatal(err)
	}
}

func TestAggregatePeriodRejectsInvalidGroupsBeforeIO(t *testing.T) {
	for _, q := range []*AggregateQuery[aggregatePeriodSource]{
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt")),
		Aggregate[aggregatePeriodSource]().Select(YearMonth("CreatedAt")),
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day")),
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day")).GroupBy("CreatedAt"),
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day")).GroupBy("day"),
		Aggregate[aggregatePeriodSource]().Select(Date("Computed").As("Day")).GroupBy("Day"),
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt); DROP TABLE x").As("Day")).GroupBy("Day"),
		Aggregate[aggregatePeriodSource]().Select(YearMonth("unknown").As("Month")).GroupBy("Month"),
		Aggregate[aggregatePeriodSource]().Select(CountAll().As("Count")).GroupBy("Count"),
		Aggregate[aggregatePeriodSource]().Select(Field("CreatedAt"), Date("CreatedAt").As("Day")).GroupBy("Day"),
		Aggregate[aggregatePeriodSource]().Select(Field("StoreID").As("Store")).GroupBy("StoreID"),
		Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day"), YearMonth("CreatedAt").As("Month")).GroupBy("Day"),
	} {
		state := &allTestState{}
		var values []time.Time
		if err := q.ScanAll(context.Background(), openAllTestDB(t, state), &values); err == nil || state.query != "" {
			t.Fatalf("invalid group executed SQL=%s err=%v", state.query, err)
		}
	}
}

func TestAggregatePeriodNullableScanOwnershipAndCapture(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2024, 2, 29, 0, 0, 0, 0, time.FixedZone("fixture", 9*60*60))
	q := Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day"), YearMonth("CreatedAt").As("Month"), CountAll().As("Count")).GroupBy("Day", "Month").OrderBy(Asc("Day"))
	state := &allTestState{columns: []string{"Day", "Month", "Count"}, values: [][]driver.Value{{nil, nil, int64(1)}, {day, int64(202402), int64(2)}}}
	db := openAllTestDB(t, state)
	type result struct {
		Day   sql.NullTime
		Month sql.NullInt64
		Count int64
	}
	var values []result
	var artifact bytes.Buffer
	capture := NewRuntimeCapture(&artifact)
	if err := q.ScanAll(WithRuntimeCapture(ctx, capture), db, &values); err != nil {
		t.Fatal(err)
	}
	want := []result{{Count: 1}, {Day: sql.NullTime{Time: day, Valid: true}, Month: sql.NullInt64{Int64: 202402, Valid: true}, Count: 2}}
	if !reflect.DeepEqual(values, want) || capture.Err() != nil {
		t.Fatalf("result=%#v capture=%v", values, capture.Err())
	}
	analysis, err := runtimecapture.AnalyzeReader(&artifact)
	if err != nil || analysis.Statistics.Statements != 1 {
		t.Fatalf("capture=%#v err=%v", analysis, err)
	}
	state.columns, state.values = []string{"Day"}, [][]driver.Value{{nil}, {day}}
	dateQuery := Aggregate[aggregatePeriodSource]().Select(Date("CreatedAt").As("Day")).GroupBy("Day")
	var dates []*time.Time
	if err := dateQuery.ScanAll(ctx, db, &dates); err != nil || len(dates) != 2 || dates[0] != nil || !dates[1].Equal(day) {
		t.Fatalf("date pointers=%v err=%v", dates, err)
	}
	original := []time.Time{day}
	unchanged := original
	if err := dateQuery.ScanAll(ctx, db, &original); err == nil || !reflect.DeepEqual(original, unchanged) {
		t.Fatal("NULL date overwrote non-nullable destination")
	}
	state.values = nil
	if err := dateQuery.ScanAll(ctx, db, &dates); err != nil || dates == nil || len(dates) != 0 {
		t.Fatal("empty grouped date result", dates, err)
	}
}

func TestAggregatePeriodComparePlanAndBaseline(t *testing.T) {
	for _, expr := range []AggregateExpression{Date("CreatedAt"), YearMonth("CreatedAt")} {
		_, state := aggregateCompareBenchmarkData(1)
		state.columns = []string{"Period", "Count", "Total"}
		key := driver.Value(int64(202402))
		if expr.function == "DATE" {
			key = time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
		}
		state.values = [][]driver.Value{{nil, int64(1), nil}, {key, int64(2), int64(42)}}
		q := Aggregate[aggregatePeriodSource]().Select(expr.As("Period"), CountAll().As("Count"), Sum("Amount").As("Total")).GroupBy("Period").OrderBy(Asc("Period"))
		report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "calendar-v1"})
		if err != nil || !report.Complete {
			t.Fatal("calendar comparison", err)
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
}
