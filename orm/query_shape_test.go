package orm

import (
	"reflect"
	"testing"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/model"
)

func TestSelectQueryShapeDescribesRelationTopNWithoutBindValues(t *testing.T) {
	t.Parallel()

	shape := queryShapeForTest(t, relationTopNBenchmarkQuery())
	if shape.Model != "relationTopNVideo" || shape.Table != "relation_topn_videos" {
		t.Fatalf("model shape = %#v", shape)
	}
	if got, want := shape.Projection, []string{"id", "title"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projection = %#v, want %#v", got, want)
	}
	if len(shape.Predicates) != 1 {
		t.Fatalf("predicates = %#v, want one", shape.Predicates)
	}
	relation := shape.Predicates[0]
	if relation.Operator != queryshape.PredicateHasRelation || relation.Relation != "VideoGenres" ||
		relation.RelationKind != string(model.RelationHasMany) || relation.Table != "relation_topn_video_genres" {
		t.Fatalf("relation predicate = %#v", relation)
	}
	if got, want := relation.RelationSourceColumns, []string{"id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source columns = %#v, want %#v", got, want)
	}
	if got, want := relation.RelationTargetColumns, []string{"video_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target columns = %#v, want %#v", got, want)
	}
	if len(relation.Children) != 1 || relation.Children[0].Operator != queryshape.PredicateEqual ||
		relation.Children[0].Column != "genre_id" || relation.Children[0].ValueCount != 1 {
		t.Fatalf("relation children = %#v", relation.Children)
	}
	if shape.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopN || shape.Compiler.Relation != "VideoGenres" {
		t.Fatalf("compiler = %#v", shape.Compiler)
	}
	if len(shape.IndexAccesses) != 1 {
		t.Fatalf("index accesses = %#v, want one", shape.IndexAccesses)
	}
	access := shape.IndexAccesses[0]
	if access.Kind != queryshape.IndexAccessRelationTopN || access.Table != "relation_topn_video_genres" ||
		!reflect.DeepEqual(access.EqualityColumns, []string{"genre_id"}) || !reflect.DeepEqual(access.OrderColumns, []string{"video_id"}) {
		t.Fatalf("index access = %#v", access)
	}
	if len(shape.Preloads) != 1 || shape.Preloads[0].Path != "Maker" || !shape.Preloads[0].Inline ||
		shape.Preloads[0].Table != "relation_topn_makers" {
		t.Fatalf("preloads = %#v", shape.Preloads)
	}
	if got, want := shape.Preloads[0].SourceColumns, []string{"maker_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload source columns = %#v, want %#v", got, want)
	}
	if got, want := shape.Preloads[0].TargetColumns, []string{"id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload target columns = %#v, want %#v", got, want)
	}
}

func TestSelectQueryShapeRecordsSoftDeleteScopesAndFallback(t *testing.T) {
	t.Parallel()

	active := queryShapeForTest(t, Query[relationTopNSoftVideo]().
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20))
	if active.SoftDeleteColumn != "deleted_at" || active.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopNFallback {
		t.Fatalf("active shape = %#v", active)
	}
	if len(active.Predicates) != 1 || active.Predicates[0].SoftDeleteColumn != "deleted_at" {
		t.Fatalf("active relation predicate = %#v", active.Predicates)
	}

	withDeleted := queryShapeForTest(t, Query[relationTopNSoftVideo]().
		WithDeleted().
		Where(Has("Links", Equal("GenreID", int64(7)))).
		OrderBy(Desc("ID")).
		Limit(20))
	if withDeleted.SoftDeleteColumn != "" || withDeleted.Compiler.Rewrite != queryshape.CompilerRewriteRelationTopN {
		t.Fatalf("with-deleted shape = %#v", withDeleted)
	}
	if withDeleted.Predicates[0].SoftDeleteColumn != "deleted_at" {
		t.Fatalf("with-deleted relation predicate = %#v", withDeleted.Predicates[0])
	}
	if len(withDeleted.IndexAccesses) != 1 ||
		!reflect.DeepEqual(withDeleted.IndexAccesses[0].EqualityColumns, []string{"genre_id", "deleted_at"}) {
		t.Fatalf("with-deleted relation index access = %#v", withDeleted.IndexAccesses)
	}
	if active.Fingerprint() == withDeleted.Fingerprint() {
		t.Fatal("soft-delete scope did not change the query fingerprint")
	}
}

func TestSelectQueryShapeIncludesRootSoftDeleteInIndexAccess(t *testing.T) {
	t.Parallel()

	shape := queryShapeForTest(t, Query[relationTopNSoftVideo]().OrderBy(Desc("ID")).Limit(20))
	if len(shape.IndexAccesses) != 1 ||
		!reflect.DeepEqual(shape.IndexAccesses[0].EqualityColumns, []string{"deleted_at"}) ||
		!reflect.DeepEqual(shape.IndexAccesses[0].OrderColumns, []string{"id"}) {
		t.Fatalf("index accesses = %#v", shape.IndexAccesses)
	}
}

func queryShapeForTest[T any](t testing.TB, query *SelectQuery[T]) queryshape.Query {
	t.Helper()
	compiled, err := query.compile()
	if err != nil {
		t.Fatalf("compile() error = %v", err)
	}
	descriptor, err := model.DescribeType(compiled.statement.scanPlan.modelType)
	if err != nil {
		t.Fatalf("model.DescribeType() error = %v", err)
	}
	relationTopN, err := analyzeRelationTopN(descriptor, &query.selection)
	if err != nil {
		t.Fatalf("analyzeRelationTopN() error = %v", err)
	}
	shape, err := buildSelectQueryShape(descriptor, &query.selection, compiled, relationTopN)
	if err != nil {
		t.Fatalf("buildSelectQueryShape() error = %v", err)
	}
	return shape
}
