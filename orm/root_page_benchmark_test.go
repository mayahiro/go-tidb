package orm

import (
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type rootPageRecord struct {
	model.Meta `tidbgo:"table=root_page_records"`
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64
	TargetID   int64
	AddedAt    time.Time
	Target     *rootPageTarget `tidbgo:"belongs_to"`
}

type rootPageTarget struct {
	model.Meta `tidbgo:"table=root_page_targets"`
	ID         int64 `tidbgo:",pk"`
	Label      string
	Payload    string
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func rootPageQuery() *SelectQuery[rootPageRecord] {
	return Query[rootPageRecord]().
		Where(Equal("OwnerID", int64(7))).
		OrderBy(Desc("AddedAt"), Desc("ID")).
		Limit(50).
		Preload("Target")
}

// BenchmarkRootPageCompiler measures warmed offline compilation of the
// candidate page and shapes where an additional root lookup is undesirable.
func BenchmarkRootPageCompiler(b *testing.B) {
	for _, name := range []string{"page", "offset", "large_limit", "primary_order", "range_filter", "scalar"} {
		b.Run(name, func(b *testing.B) {
			query := rootPageQuery()
			switch name {
			case "offset":
				query.Offset(9000)
			case "large_limit":
				query.Limit(5000)
			case "primary_order":
				query.selection.orderBy = nil
				query.OrderBy(Desc("ID"))
			case "range_filter":
				query.Where(LessThan("TargetID", int64(5)))
			case "scalar":
				query.selection.preloads = nil
			}
			if _, _, err := query.Build(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				statement, args, err := query.Build()
				if err != nil {
					b.Fatal(err)
				}
				queryBenchmarkSQLSink, queryBenchmarkArgumentsSink = statement, args
			}
		})
	}
}
