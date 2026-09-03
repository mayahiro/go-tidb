package orm

import (
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type relationTopNVideo struct {
	model.Meta  `tidbgo:"table=relation_topn_videos"`
	ID          int64 `tidbgo:",pk"`
	MakerID     int64
	Title       string
	VideoGenres []relationTopNVideoGenre `tidbgo:"has_many,join=ID:VideoID"`
	Maker       *relationTopNMaker       `tidbgo:"belongs_to,join=MakerID:ID"`
}

type relationTopNVideoGenre struct {
	model.Meta `tidbgo:"table=relation_topn_video_genres"`
	VideoID    int64 `tidbgo:",pk"`
	GenreID    int64 `tidbgo:",pk"`
}

type relationTopNMaker struct {
	model.Meta `tidbgo:"table=relation_topn_makers"`
	ID         int64 `tidbgo:",pk"`
	Name       string
}

func relationTopNBenchmarkQuery() *SelectQuery[relationTopNVideo] {
	return Query[relationTopNVideo]().
		Select("ID", "Title").
		Where(Has("VideoGenres", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20).
		Preload("Maker")
}

func relationTopNManyToManyBenchmarkQuery() *SelectQuery[preloadUser] {
	return Query[preloadUser]().
		Select("ID", "Email").
		Where(Has("Roles", Equal("ID", uint64(7)))).
		OrderBy(Desc("ID")).
		Limit(20)
}

func BenchmarkSelectQueryBuildRelationTopN(b *testing.B) {
	query := relationTopNBenchmarkQuery()
	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err := query.Build()
		if err != nil {
			b.Fatal(err)
		}
		relationTopNSQLSink = sqlText
		relationTopNArgumentsSink = arguments
	}
}

func BenchmarkSelectQueryBuildManyToManyRelationTopN(b *testing.B) {
	query := relationTopNManyToManyBenchmarkQuery()
	b.ReportAllocs()
	for b.Loop() {
		sqlText, arguments, err := query.Build()
		if err != nil {
			b.Fatal(err)
		}
		relationTopNSQLSink = sqlText
		relationTopNArgumentsSink = arguments
	}
}

var (
	relationTopNSQLSink       string
	relationTopNArgumentsSink []any
)
