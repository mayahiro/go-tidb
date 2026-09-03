package modelmeta

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRelation(t *testing.T) {
	t.Parallel()

	direct, err := ParseRelation("has_many,join=ID:VideoID", true)
	if err != nil {
		t.Fatalf("ParseRelation(direct) error = %v", err)
	}
	if direct.Kind != RelationHasMany || !reflect.DeepEqual(direct.Joins, []RelationPair{{Left: "ID", Right: "VideoID"}}) {
		t.Fatalf("ParseRelation(direct) = %#v", direct)
	}

	junction, err := ParseRelation("many_to_many,through=videos_genres,source=ID:video_id,target=genre_id:ID", true)
	if err != nil {
		t.Fatalf("ParseRelation(junction) error = %v", err)
	}
	if junction.Kind != RelationManyToMany || junction.Through != "videos_genres" ||
		!reflect.DeepEqual(junction.SourcePairs, []RelationPair{{Left: "ID", Right: "video_id"}}) ||
		!reflect.DeepEqual(junction.TargetPairs, []RelationPair{{Left: "genre_id", Right: "ID"}}) {
		t.Fatalf("ParseRelation(junction) = %#v", junction)
	}
}

func TestParseRelationRejectsInvalidDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value      string
		collection bool
		contains   string
	}{
		{value: "unknown", contains: "kind must be the first"},
		{value: "has_many", contains: "does not match"},
		{value: "belongs_to", collection: true, contains: "does not match"},
		{value: "has_many,join=ID", collection: true, contains: "exactly one"},
		{value: "has_many,through=links", collection: true, contains: "direct relations"},
		{value: "many_to_many,through=links", collection: true, contains: "requires through"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRelation(test.value, test.collection)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("ParseRelation() error = %v, want %q", err, test.contains)
			}
		})
	}
}
