package starterapp

import (
	"context"
	"database/sql"

	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
	"github.com/mayahiro/go-tidb/vector"
)

// RankedOrderTotal contains a user group and its rank by total order amount.
// Exact decimal text and nullable relation fields preserve SQL values.
type RankedOrderTotal struct {
	UserID   int64
	Email    sql.NullString
	Total    sql.NullString
	Position int64
}

// RankedOrderTotals groups orders, joins a unique user, and ranks groups before
// limiting the final result. It does not request storage hints or probe TiDB.
func RankedOrderTotals(ctx context.Context, executor orm.QueryExecutor) ([]RankedOrderTotal, error) {
	var result []RankedOrderTotal
	err := orm.Aggregate[Order]().
		Select(orm.Field("UserID"), orm.Field("User.Email").As("Email"), orm.Sum("Total").As("Total")).
		GroupBy("UserID", "Email").
		Window(orm.RowNumber().Over(orm.WindowSpec{
			OrderBy: []orm.OrderTerm{orm.Desc("Total"), orm.Asc("UserID")},
		}).As("Position")).
		OrderBy(orm.Asc("Position")).Limit(20).ScanAll(ctx, executor, &result)
	return result, err
}

// SearchDocument maps a fixed-dimensional embedding used by SearchDocuments.
// Provision embedding VECTOR(3); creating a vector index is an explicit choice.
type SearchDocument struct {
	model.Meta `tidbgo:"table=search_documents"`
	ID         int64 `tidbgo:",pk"`
	TenantID   int64
	Title      string
	Embedding  vector.Vector
}

// DocumentHit is a narrow nearest-neighbor result without the stored embedding.
type DocumentHit struct {
	ID       int64
	Title    string
	Distance float64
}

// SearchDocuments searches exactly within one tenant. The tenant restriction
// stays before Top-K even though it prevents TiDB's current ANN index path.
func SearchDocuments(ctx context.Context, executor orm.QueryExecutor, tenantID int64, input vector.Vector) ([]DocumentHit, error) {
	if err := input.ValidateDimensions(3); err != nil {
		return nil, err
	}
	var result []DocumentHit
	err := orm.Nearest[SearchDocument]("Embedding", input, vector.L2, 10).
		Select("ID", "Title").Where(orm.Equal("TenantID", tenantID)).
		ScanAll(ctx, executor, &result)
	return result, err
}
