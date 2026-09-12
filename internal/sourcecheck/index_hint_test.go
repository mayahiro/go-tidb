package sourcecheck

import (
	"fmt"
	"strings"
	"testing"
)

func TestSourceForceIndex(t *testing.T) {
	const modelSource = `package repository
import "github.com/mayahiro/go-tidb/orm"
type Item struct {
 ID int64 ` + "`tidbgo:\",pk\"`" + `
 OwnerID int64
 Title string
}
`
	catalog := parseSourceSchema(t, `CREATE TABLE item (
id BIGINT PRIMARY KEY, owner_id BIGINT NOT NULL, title VARCHAR(255),
KEY owner_order (owner_id, id), KEY wrong_index (title));`)
	for _, tc := range []struct {
		name, setup, call, code string
		uncertain               bool
	}{
		{name: "unhinted"},
		{name: "literal", call: `.ForceIndex("owner_order")`},
		{name: "constant", setup: `const name = "owner_order"`, call: `.ForceIndex(name)`},
		{name: "last wins", call: `.ForceIndex("wrong_index").ForceIndex("owner_order")`},
		{name: "wrong", call: `.ForceIndex("wrong_index")`, code: "QRY007"},
		{name: "missing", call: `.ForceIndex("absent")`, code: "QRY006"},
		{name: "dynamic", setup: `name := "owner_order"`, call: `.ForceIndex(name)`, uncertain: true},
		{name: "invalid", call: `.ForceIndex("")`, uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := modelSource + fmt.Sprintf(`func build() {
%s
_, _, _ = orm.Query[Item]()%s.Where(orm.Equal("OwnerID", 7)).OrderBy(orm.Desc("ID")).Limit(50).Build()
}`, tc.setup, tc.call)
			analysis := analyzeSourceWithOptions(t, source, WithSchema(catalog))
			if tc.uncertain {
				if analysis.Statistics.UncertainIndexPatterns != 1 || analysis.Statistics.AnalyzedIndexPatterns != 0 {
					t.Fatalf("statistics = %#v", analysis.Statistics)
				}
			} else if analysis.Statistics.AnalyzedIndexPatterns != 1 {
				t.Fatalf("statistics = %#v", analysis.Statistics)
			}
			if tc.code == "" && len(analysis.Diagnostics) != 0 || tc.code != "" && (len(analysis.Diagnostics) != 1 || analysis.Diagnostics[0].Code != tc.code) {
				t.Fatalf("diagnostics = %#v", analysis.Diagnostics)
			}
		})
	}
	for _, body := range []string{
		`q := orm.Query[Item]().Where(orm.Equal("OwnerID", 7)).OrderBy(orm.Desc("ID")).Limit(50)
q.ForceIndex("wrong_index")
_, _, _ = q.Build()`,
		`_, _, _ = branch(true).Build()
}
func branch(condition bool) *orm.SelectQuery[Item] {
if condition { return orm.Query[Item]().Where(orm.Equal("OwnerID", 7)).OrderBy(orm.Desc("ID")).Limit(50).ForceIndex("owner_order") }
return orm.Query[Item]().Where(orm.Equal("OwnerID", 7)).OrderBy(orm.Desc("ID")).Limit(50).ForceIndex("wrong_index")`,
	} {
		analysis := analyzeSourceWithOptions(t, modelSource+"func build() {"+body+"\n}", WithSchema(catalog))
		if analysis.Statistics.UncertainIndexPatterns != 1 || len(analysis.Diagnostics) != 0 {
			t.Fatalf("mutable or divergent index = %#v, %#v", analysis.Statistics, analysis.Diagnostics)
		}
	}
}

func TestSourceForceIndexDisablesRelationTopN(t *testing.T) {
	const source = `package repository
import "github.com/mayahiro/go-tidb/orm"
type Item struct {
 ID int64 TAG_PK
 Links []Link TAG_LINK
}
type Link struct {
 ItemID int64 TAG_PK
 OwnerID int64 TAG_PK
}
func build() {
 _, _, _ = orm.Query[Item]().Where(orm.Has("Links", orm.Equal("OwnerID", 7))).OrderBy(orm.Desc("ID")).Limit(50).ForceIndex("PRIMARY").Build()
}`
	text := strings.NewReplacer("TAG_PK", "`tidbgo:\",pk\"`", "TAG_LINK", "`tidbgo:\"has_many,join=ID:ItemID\"`").Replace(source)
	analysis := analyzeSourceWithOptions(t, text)
	if analysis.Statistics.AnalyzedRelationTopNPatterns != 0 || len(analysis.Diagnostics) != 0 {
		t.Fatalf("forced root relation decision = %#v, %#v", analysis.Statistics, analysis.Diagnostics)
	}
}
