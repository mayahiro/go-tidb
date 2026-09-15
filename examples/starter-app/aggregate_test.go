package starterapp

import (
	"fmt"

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
