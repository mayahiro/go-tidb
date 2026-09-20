package sourcecheck

import "testing"

func TestSourceIndexCoverageRespectsReadPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy                string
		candidates, uncertain int
	}{
		{"orm.TiFlash", 0, 0}, {"orm.TiKV", 1, 0}, {"engine", 1, 1},
	} {
		analysis := analyzeSourceWithOptions(t, `package repository
import "github.com/mayahiro/go-tidb/orm"
type User struct { ID int64; Name string }
func query(){ orm.Query[User]().ReadFrom(`+tc.policy+`).OrderBy(orm.Asc("ID")).Limit(10).Build() }
`, WithSchema(parseSourceSchema(t, "CREATE TABLE user (id BIGINT, name TEXT)")))
		if analysis.Statistics.IndexPatterns != tc.candidates || analysis.Statistics.UncertainIndexPatterns != tc.uncertain {
			t.Fatal(analysis.Statistics)
		}
		if tc.policy == "orm.TiFlash" && len(analysis.Diagnostics) != 0 {
			t.Fatal(analysis.Diagnostics)
		}
	}
}
