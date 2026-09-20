package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

func TestAggregateWindowsAfterGroupingBeforePaging(t *testing.T) {
	spec := WindowSpec{PartitionBy: []string{"Shop"}, OrderBy: []OrderTerm{Asc("Month")}, Rows: RowsBetween(UnboundedPreceding, CurrentRow)}
	running := Sum("Total").Over(spec).As("Running")
	spec.PartitionBy[0], spec.OrderBy[0], spec.Rows.end = "caller_change", Desc("caller_change"), 10
	q := Aggregate[aggregateRelationOrder]().Select(Field("ShopID").As("Shop"), YearMonth("CreatedAt").As("Month"), SumIf("Amount", Has("User", Equal("Active", true))).As("Total")).
		GroupBy("Shop", "Month").Having(GreaterThan("Total", 5)).
		Window(running, Lag("Total", 1).Over(WindowSpec{PartitionBy: []string{"Shop"}, OrderBy: []OrderTerm{Asc("Month")}}).As("Previous"), Rank().Over(WindowSpec{OrderBy: []OrderTerm{Desc("Total")}}).As("Position")).
		OrderBy(Asc("Position"), Asc("Shop"), Asc("Month")).Limit(3).Offset(2).ReadFrom(TiFlash).MPP(MPPEnforce)
	statement, args, err := q.Build()
	if err != nil || !reflect.DeepEqual(args, []any{int64(1), true, true, 5, int64(3), int64(2)}) {
		t.Fatal(statement, args, err)
	}
	for _, want := range []string{
		"SELECT /*+ SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=1) */",
		"SUM(`w`.`Total`) OVER (PARTITION BY `w`.`Shop` ORDER BY `w`.`Month` ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS `Running`",
		"LAG(`w`.`Total`, ?) OVER (PARTITION BY `w`.`Shop` ORDER BY `w`.`Month` ASC) AS `Previous`",
		"RANK() OVER (ORDER BY `w`.`Total` DESC) AS `Position`",
		"FROM (SELECT /*+ READ_FROM_STORAGE(TIFLASH[a]) */",
		") AS `w` ORDER BY `Position` ASC, `w`.`Shop` ASC, `w`.`Month` ASC LIMIT ? OFFSET ?",
	} {
		if !strings.Contains(statement, want) {
			t.Fatal("missing", want, statement)
		}
	}
	if strings.Count(statement, "LIMIT") != 1 || strings.Count(statement, "tidb_allow_mpp") != 1 || strings.Contains(statement, "caller_change") {
		t.Fatal("paging/policy/spec leaked into the grouped input", statement)
	}
	state := &allTestState{columns: []string{"Shop", "Month", "Total", "Running", "Previous", "Position"}, values: [][]driver.Value{{int64(1), int64(202609), "10.25", "10.25", nil, int64(1)}}}
	var values []struct {
		Shop, Month, Position int64
		Total, Running        string
		Previous              sql.NullString
	}
	if err := q.ScanAll(context.Background(), openAllTestDB(t, state), &values); err != nil || len(values) != 1 || values[0].Previous.Valid || values[0].Running != "10.25" {
		t.Fatal(values, err)
	}
}

func TestAggregateWindowComparisonPreservesBindingOrder(t *testing.T) {
	q, state := aggregateCompareBenchmarkData(2)
	q.Where(Equal("Amount", int64(7))).Window(Lag("Total", 1).Over(WindowSpec{OrderBy: []OrderTerm{Asc("ShopID")}}).As("Previous")).Limit(2)
	state.columns = append(state.columns, "Previous")
	for i := range state.values {
		var previous driver.Value
		if i != 0 {
			previous = int64(42)
		}
		state.values[i] = append(state.values[i], previous)
	}
	state.record = true
	before, args, err := q.Build()
	if err != nil {
		t.Fatal(err)
	}
	report, err := q.Compare(context.Background(), openAggregateCompareTestDB(t, state), AggregateCompareOptions{Case: "grouped-window"})
	if err != nil || !report.Complete {
		t.Fatal(report, err)
	}
	for i, statement := range state.queries {
		if statement == lastServerRUQuery {
			continue
		}
		if strings.HasPrefix(statement, "SELECT ") || strings.HasPrefix(statement, "EXPLAIN ANALYZE ") {
			got := state.arguments[i]
			if len(got) != 3 || got[0].Value != int64(1) || got[1].Value != int64(7) || got[2].Value != int64(2) {
				t.Fatal(statement, got)
			}
		}
	}
	after, afterArgs, err := q.Build()
	if err != nil || before != after || !reflect.DeepEqual(args, afterArgs) {
		t.Fatal("comparison mutated window query", err)
	}
}

func TestAggregateWindowValidationAndFrames(t *testing.T) {
	order := WindowSpec{OrderBy: []OrderTerm{Asc("ShopID")}}
	valid := []WindowExpression{RowNumber().Over(order), DenseRank().Over(order), Lead("Total", 0).Over(order), FirstValue("Total").Over(order), LastValue("Total").Over(WindowSpec{OrderBy: order.OrderBy, Rows: RowsBetween(-2, 1)}), Avg("Total").Over(WindowSpec{}), CountAll().Over(WindowSpec{}), Count("Total").Over(order), Min("Total").Over(order), Max("Total").Over(order)}
	base := func() *AggregateQuery[aggregateRelationOrder] {
		return Aggregate[aggregateRelationOrder]().Select(Field("ShopID"), Sum("Amount").As("Total")).GroupBy("ShopID")
	}
	for _, expr := range valid {
		if _, _, err := base().Window(expr.As("Value")).Build(); err != nil {
			t.Fatal(err)
		}
	}
	for _, expr := range []WindowExpression{
		{}, RowNumber(), RowNumber().Over(WindowSpec{}), Lag("Total", -1).Over(order), Lag("Missing", 1).Over(order),
		Sum("Missing").Over(order), SumIf("Total", Equal("ShopID", 1)).Over(order), CountDistinct("Total").Over(order),
		Sum("Total").Over(WindowSpec{PartitionBy: []string{"Missing"}}),
		Sum("Total").Over(WindowSpec{PartitionBy: []string{"ShopID", "ShopID"}}),
		Sum("Total").Over(WindowSpec{OrderBy: []OrderTerm{Asc("ShopID"), Desc("ShopID")}}),
		Sum("Total").Over(WindowSpec{OrderBy: order.OrderBy, Rows: RowsBetween(1, -1)}),
		Sum("Total").Over(WindowSpec{OrderBy: order.OrderBy, Rows: RowsBetween(UnboundedFollowing, UnboundedFollowing)}),
		Sum("Total").Over(WindowSpec{Rows: RowsBetween(-2, 0)}),
		Rank().Over(WindowSpec{OrderBy: order.OrderBy, Rows: RowsBetween(-2, 0)}),
	} {
		if _, _, err := base().Window(expr.As("Value")).Build(); err == nil {
			t.Fatal("invalid window accepted", expr)
		}
	}
	for _, q := range []*AggregateQuery[aggregateRelationOrder]{
		base().Window(RowNumber().Over(order)),
		base().Window(RowNumber().Over(order).As("Total")),
		base().Window(RowNumber().Over(order).As("Position"), Sum("Position").Over(order).As("SumRank")),
		base().Window(RowNumber().Over(order).As("Position")).Having(GreaterThan("Position", 1)),
		base().Window(RowNumber().Over(order).As("Position")).OrderBy(Asc("Missing")),
	} {
		if _, _, err := q.Build(); err == nil {
			t.Fatal("invalid window reference accepted")
		}
	}
}
