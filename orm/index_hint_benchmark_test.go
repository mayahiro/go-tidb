package orm

import (
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type indexHintBookmark struct {
	model.Meta `tidbgo:"table=index_hint_bookmarks"`
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64
	TargetID   int64
	AddedAt    time.Time
	Target     *indexHintTarget `tidbgo:"belongs_to"`
}

type indexHintTarget struct {
	model.Meta `tidbgo:"table=index_hint_targets"`
	ID         int64 `tidbgo:",pk"`
	ExternalID string
	Title      string
	Thumbnail  string
	Short      bool
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func indexHintListQuery() *SelectQuery[indexHintBookmark] {
	return Query[indexHintBookmark]().Where(Equal("OwnerID", int64(7))).
		OrderBy(Desc("AddedAt"), Desc("ID")).Limit(50).
		Preload("Target", PreloadFields("ID", "ExternalID", "Title", "Thumbnail", "Short"), PreloadWithDeleted())
}

// BenchmarkOrderedListCompiler measures warmed compilation of paginated lists.
func BenchmarkOrderedListCompiler(b *testing.B) {
	for _, name := range []string{"first_50", "second_10", "middle", "last", "large_limit", "scalar"} {
		b.Run(name, func(b *testing.B) {
			query := indexHintListQuery().ForceIndex("owner_order")
			switch name {
			case "second_10":
				query.Limit(10).Offset(10)
			case "middle":
				query.Offset(5400)
			case "last":
				query.Offset(10800)
			case "large_limit":
				query.Limit(5000)
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
