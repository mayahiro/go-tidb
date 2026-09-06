package modelmeta

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRelation(t *testing.T) {
	t.Parallel()

	via, err := ParseRelation("many_to_many,via=Edges.Target", true)
	if err != nil || via.Via != "Edges.Target" || via.Through != "" || len(via.Joins) != 0 {
		t.Fatalf("ParseRelation(via) = %#v, %v", via, err)
	}

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
		{value: "many_to_many,via=Edges.Target,via=Edges.Target", collection: true, contains: "must not be repeated"},
		{value: "many_to_many,via=Edges.Target,through=links", collection: true, contains: "must not be combined"},
		{value: "many_to_many,via=Edges.Target,source=ID:id", collection: true, contains: "must not be combined"},
		{value: "many_to_many,via=Edges.Target,target=id:ID", collection: true, contains: "must not be combined"},
		{value: "many_to_many,via=Edges.Target,join=ID:ID", collection: true, contains: "does not support join"},
		{value: "has_many,via=Edges.Target", collection: true, contains: "direct relations"},
		{value: "many_to_many,via=Edges", collection: true, contains: "two exported"},
		{value: "many_to_many,via=edges.Target", collection: true, contains: "two exported"},
		{value: "many_to_many,via=Edges.target", collection: true, contains: "two exported"},
		{value: "many_to_many,via=Edges.Target.More", collection: true, contains: "two exported"},
		{value: "many_to_many,via=Edges.Target--", collection: true, contains: "two exported"},
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
