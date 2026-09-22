package tidbcloud

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/internal/referencecheck"
)

func TestTiDBCloudStarterReferenceAudit(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" {
		t.Skip("TIDBGO_TEST_DSN is not set; skipping connected reference audit")
	}
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	parent, child, edge := "tidbgo_ref_parent_"+suffix, "tidbgo_ref_child_"+suffix, "tidbgo_ref_edge_"+suffix
	var created []string
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.ExecContext(cleanup, "DROP TABLE `"+created[i]+"`"); err != nil {
				t.Errorf("remove reference fixture: %s", redact.Error(err, dsn))
			}
		}
	})
	exec := func(statement string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement); err != nil {
			fatalDatabaseError(t, dsn, "prepare reference audit fixture", err)
		}
	}
	for _, table := range []struct{ name, ddl string }{
		{parent, "tenant BIGINT NOT NULL,id BIGINT NOT NULL,parent_id BIGINT,deleted_at DATETIME,PRIMARY KEY(tenant,id)"},
		{child, "id BIGINT PRIMARY KEY,tenant BIGINT,parent_id BIGINT,KEY parent_lookup(tenant,parent_id)"},
		{edge, "left_tenant BIGINT,left_id BIGINT,right_tenant BIGINT,right_id BIGINT"},
	} {
		exec("CREATE TABLE `" + table.name + "` (" + table.ddl + ")")
		created = append(created, table.name)
	}
	exec("INSERT INTO `" + parent + "` VALUES (1,10,NULL,'2020-01-01'),(2,10,NULL,NULL)")
	exec("INSERT INTO `" + child + "` VALUES (1,NULL,999),(2,1,NULL),(3,1,10),(4,2,10)")
	exec("INSERT INTO `" + edge + "` VALUES (1,10,2,10),(NULL,999,1,10)")
	refs := []referencecheck.Reference{
		{Name: "child.parent", ChildTable: child, ChildColumns: []string{"tenant", "parent_id"}, ParentTable: parent, ParentColumns: []string{"tenant", "id"}},
		{Name: "edge.left", ChildTable: edge, ChildColumns: []string{"left_tenant", "left_id"}, ParentTable: parent, ParentColumns: []string{"tenant", "id"}},
		{Name: "edge.right", ChildTable: edge, ChildColumns: []string{"right_tenant", "right_id"}, ParentTable: parent, ParentColumns: []string{"tenant", "id"}},
		{Name: "parent.parent", ChildTable: parent, ChildColumns: []string{"tenant", "parent_id"}, ParentTable: parent, ParentColumns: []string{"tenant", "id"}},
	}
	verify := func(want []bool) {
		t.Helper()
		results, err := referencecheck.Run(ctx, db, refs)
		if err != nil {
			fatalDatabaseError(t, dsn, "run reference probes", err)
		}
		for i, result := range results {
			if !result.Checked || result.Orphan != want[i] {
				t.Fatalf("reference %s: checked=%t orphan=%t want=%t", result.Reference, result.Checked, result.Orphan, want[i])
			}
		}
	}
	verify([]bool{false, false, false, false})
	exec("INSERT INTO `" + child + "` VALUES (5,3,10)")
	exec("INSERT INTO `" + edge + "` VALUES (3,10,2,10)")
	verify([]bool{true, true, false, false})
	exec("INSERT INTO `" + edge + "` VALUES (1,10,3,10)")
	exec("UPDATE `" + parent + "` SET parent_id=77 WHERE tenant=1")
	verify([]bool{true, true, true, true})
	// A successful audit must be repeated after writes; it is not a constraint.
	exec("DELETE FROM `" + child + "` WHERE id=5")
	exec("DELETE FROM `" + edge + "` WHERE left_tenant=3 OR right_tenant=3")
	exec("UPDATE `" + parent + "` SET parent_id=NULL")
	verify([]bool{false, false, false, false})
}
