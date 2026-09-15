package starterapp

import (
	"fmt"
	"time"

	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

// This example builds an aggregate offline. Execute the same query with
// ScanAll(ctx, db, &stats), where stats is a slice with UserID, OrderCount,
// and Total fields; use a nullable decimal Scanner for Total when needed.
func Example_aggregate() {
	q := orm.Aggregate[Order]().
		Select(orm.Field("UserID"), orm.CountAll().As("OrderCount"), orm.Sum("Total").As("Total")).
		GroupBy("UserID").
		Having(orm.GreaterThan("OrderCount", int64(1))).
		OrderBy(orm.Desc("Total"), orm.Asc("UserID")).Limit(10)
	statement, arguments, err := q.Build()
	if err != nil {
		panic(err)
	}
	fmt.Println(statement)
	fmt.Println(arguments)
	// Output:
	// SELECT `a`.`user_id` AS `UserID`, COUNT(*) AS `OrderCount`, SUM(`a`.`total`) AS `Total` FROM `orders` AS `a` GROUP BY `a`.`user_id` HAVING COUNT(*) > ? ORDER BY SUM(`a`.`total`) DESC, `a`.`user_id` ASC LIMIT ?
	// [1 10]
}

// A reporting source can map the database-managed registration timestamp
// without adding it to the ordinary User model or its write operations.
func Example_calendarAggregation() {
	type Registration struct {
		model.Meta `tidbgo:"table=users"`
		CreatedAt  time.Time
	}
	for _, key := range []orm.AggregateExpression{orm.Date("CreatedAt"), orm.YearMonth("CreatedAt")} {
		q := orm.Aggregate[Registration]().Select(key.As("Period"), orm.CountAll().As("Count")).
			GroupBy("Period").OrderBy(orm.Asc("Period"))
		statement, _, err := q.Build()
		if err != nil {
			panic(err)
		}
		fmt.Println(statement)
	}
	// Output:
	// SELECT DATE(`a`.`created_at`) AS `Period`, COUNT(*) AS `Count` FROM `users` AS `a` GROUP BY DATE(`a`.`created_at`) ORDER BY DATE(`a`.`created_at`) ASC
	// SELECT EXTRACT(YEAR_MONTH FROM `a`.`created_at`) AS `Period`, COUNT(*) AS `Count` FROM `users` AS `a` GROUP BY EXTRACT(YEAR_MONTH FROM `a`.`created_at`) ORDER BY EXTRACT(YEAR_MONTH FROM `a`.`created_at`) ASC
}
