package tidbcloud

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/redact"
	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
	"github.com/mayahiro/go-tidb/schema"
	"github.com/mayahiro/go-tidb/tiflash"
	"github.com/mayahiro/go-tidb/vector"
)

type starterTiFlashNode struct {
	model.Meta `tidbgo:"table=tidbgo_it_tiflash_extensions"`
	ID         int64 `tidbgo:",pk"`
	ParentID   *int64
	Category   int64
	Score      int64
	Embedding  vector.Vector
	DeletedAt  *time.Time           `tidbgo:",soft_delete"`
	Parent     *starterTiFlashNode  `tidbgo:"belongs_to,join=ParentID:ID"`
	Children   []starterTiFlashNode `tidbgo:"has_many,join=ID:ParentID"`
}

// TestTiDBCloudStarterTiFlashExtensions uses an owned fixture and explicit opt-in
// for replicas and vector-index DDL. It never removes pre-existing tables.
func TestTiDBCloudStarterTiFlashExtensions(t *testing.T) {
	dsn := os.Getenv(testDSNEnvironment)
	if dsn == "" || os.Getenv("TIDBGO_TEST_TIFLASH") != "1" {
		t.Skip("set TIDBGO_TEST_DSN and TIDBGO_TEST_TIFLASH=1")
	}
	config := parseTestDSN(t, dsn)
	config.ParseTime = true
	dsn = config.FormatDSN()
	db := openTestDatabase(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	verifyConnectedTarget(t, ctx, db, dsn)
	conn, err := db.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, dsn, "pin extension fixture", err)
	}
	defer conn.Close()
	capabilities, err := tiflash.ProbeCapabilities(ctx, conn)
	if err != nil {
		fatalDatabaseError(t, dsn, "capability probes", err)
	}
	if capabilities.WindowFunctions.State != tiflash.Supported || capabilities.VectorFunctions.State != tiflash.Supported || capabilities.MPPSettings.State != tiflash.Supported {
		t.Fatal("required capabilities unavailable", capabilities)
	}
	const table = "tidbgo_it_tiflash_extensions"
	const create = "CREATE TABLE " + table + " (id BIGINT PRIMARY KEY, parent_id BIGINT NULL, category BIGINT NOT NULL, score BIGINT NOT NULL, embedding VECTOR(3), deleted_at DATETIME(6) NULL, KEY parent_idx(parent_id))"
	if _, err := conn.ExecContext(ctx, create); err != nil {
		fatalDatabaseError(t, dsn, "create extension fixture", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
			t.Errorf("drop owned extension fixture: %s", redact.Error(err, dsn))
		}
	})
	initial, err := tiflash.InspectReplica(ctx, conn, config.DBName, table)
	if err != nil || initial.Configured {
		fatalDatabaseError(t, dsn, "initial replica inspection", err)
		t.Fatal(initial)
	}
	if _, err := tiflash.WaitReplicaReady(ctx, conn, config.DBName, table, time.Millisecond); !errors.Is(err, tiflash.ErrReplicaNotConfigured) {
		t.Fatal("unexpected unconfigured wait")
	}
	for start := 0; start < 5000; start += 500 {
		var statement strings.Builder
		statement.WriteString("INSERT INTO " + table + " (id,parent_id,category,score,embedding,deleted_at) VALUES ")
		args := make([]any, 0, 2500)
		for i := start; i < start+500; i++ {
			if i != start {
				statement.WriteByte(',')
			}
			statement.WriteString("(?,?,?,?,?,NULL)")
			var parent any
			if i >= 4 {
				parent = int64(i%5 + 1)
			}
			args = append(args, int64(i+1), parent, int64(i%4), int64(i%19), fmt.Sprintf("[%d,%d,1]", i%101, i%47))
		}
		if _, err := conn.ExecContext(ctx, statement.String(), args...); err != nil {
			fatalDatabaseError(t, dsn, "seed extension fixture", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "UPDATE "+table+" SET deleted_at=CURRENT_TIMESTAMP(6) WHERE id=4"); err != nil {
		fatalDatabaseError(t, dsn, "soft-delete extension fixture", err)
	}
	ddl, err := tiflash.BuildEnableReplica(config.DBName, table)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, ddl); err != nil {
		fatalDatabaseError(t, dsn, "enable fixture replicas", err)
	}
	if ready, err := tiflash.WaitReplicaReady(ctx, conn, config.DBName, table, 100*time.Millisecond); err != nil || !ready.Available {
		fatalDatabaseError(t, dsn, "wait initial replica", err)
		t.Fatal(ready)
	}
	if _, err := conn.ExecContext(ctx, "ANALYZE TABLE "+table); err != nil {
		fatalDatabaseError(t, dsn, "analyze extension fixture", err)
	}
	t.Run("grouped_windows_and_relations", func(t *testing.T) { testTiFlashExtensionWindows(t, ctx, conn, dsn) })
	t.Run("select_and_preload_policy", func(t *testing.T) {
		q := orm.Query[starterTiFlashNode]().Select("ID", "ParentID").Where(orm.LessThan("ID", int64(10))).Preload("Parent").Preload("Children").ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce).OrderBy(orm.Asc("ID"))
		got, err := q.All(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "select/preload policy", err)
		}
		if len(got) != 8 || len(got[0].Children) == 0 || got[4].Parent == nil {
			t.Fatal("select/preload scope mismatch")
		}
		count, err := orm.Query[starterTiFlashNode]().ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce).Count(ctx, conn)
		if err != nil || count != 4999 {
			fatalDatabaseError(t, dsn, "policy count", err)
			t.Fatal(count)
		}
	})
	indexDDL, err := orm.BuildVectorIndex[starterTiFlashNode]("Embedding", "embedding_l2", vector.L2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, indexDDL); err != nil {
		fatalDatabaseError(t, dsn, "create vector fixture index", err)
	}
	t.Run("vector_exact_and_approximate", func(t *testing.T) { testTiFlashExtensionVector(t, ctx, conn, dsn) })
	config.InterpolateParams = true
	interpolatedDSN := config.FormatDSN()
	interpolatedDB := openTestDatabase(t, interpolatedDSN)
	interpolated, err := interpolatedDB.Conn(ctx)
	if err != nil {
		fatalDatabaseError(t, interpolatedDSN, "pin interpolated fixture", err)
	}
	defer interpolated.Close()
	t.Run("interpolated_windows", func(t *testing.T) { testTiFlashExtensionWindows(t, ctx, interpolated, interpolatedDSN) })
	t.Run("interpolated_vectors", func(t *testing.T) { testTiFlashExtensionVector(t, ctx, interpolated, interpolatedDSN) })
	t.Run("vector_null_and_dimensions", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, "UPDATE tidbgo_it_tiflash_extensions SET embedding=NULL WHERE id=5000"); err != nil {
			fatalDatabaseError(t, dsn, "NULL vector fixture", err)
		}
		input, _ := vector.New([]float32{1, 2, 3})
		type nullableHit struct {
			ID       int64
			Distance sql.NullFloat64
		}
		var got []nullableHit
		if err := orm.Nearest[starterTiFlashNode]("Embedding", input, vector.Cosine, 1).Select("ID").ScanAll(ctx, conn, &got); err != nil {
			fatalDatabaseError(t, dsn, "NULL cosine distance", err)
		}
		if len(got) != 1 || got[0].ID != 5000 || got[0].Distance.Valid {
			t.Fatal(got)
		}
		wrong, _ := vector.New([]float32{1, 2})
		err := orm.Nearest[starterTiFlashNode]("Embedding", wrong, vector.L2, 1).Select("ID").ScanAll(ctx, conn, &got)
		if err == nil || len(got) != 1 || got[0].ID != 5000 {
			t.Fatal("dimension error did not preserve destination")
		}
	})

}

type starterWindowResult struct {
	ParentCategory sql.NullInt64
	Total          int64
	Matched        int64
	MatchedTotal   sql.NullString
	Position       int64
	Previous       sql.NullInt64
	Running        string
}

func testTiFlashExtensionWindows(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string) {
	spec := orm.WindowSpec{OrderBy: []orm.OrderTerm{orm.Asc("ParentCategory")}}
	for _, engine := range []orm.StorageEngine{orm.TiKV, orm.TiFlash} {
		q := orm.Aggregate[starterTiFlashNode]().Select(orm.Field("Parent.Category").As("ParentCategory"), orm.CountAll().As("Total"), orm.CountIf(orm.Has("Parent", orm.Equal("Category", int64(1)))).As("Matched"), orm.SumIf("Score", orm.Has("Parent", orm.Equal("Category", int64(1)))).As("MatchedTotal")).GroupBy("ParentCategory").Having(orm.GreaterThan("Matched", int64(-1))).Window(orm.RowNumber().Over(spec).As("Position"), orm.Lag("Total", 1).Over(spec).As("Previous"), orm.Sum("Total").Over(orm.WindowSpec{OrderBy: spec.OrderBy, Rows: orm.RowsBetween(orm.UnboundedPreceding, orm.CurrentRow)}).As("Running")).OrderBy(orm.Asc("ParentCategory")).Limit(3).ReadFrom(engine)
		if engine == orm.TiFlash {
			q.MPP(orm.MPPEnforce)
		}
		var got []starterWindowResult
		started := time.Now()
		if err := q.ScanAll(ctx, conn, &got); err != nil {
			fatalDatabaseError(t, dsn, "grouped window scan", err)
		}
		elapsed := time.Since(started)
		ru, err := orm.LastServerRU(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "window RU", err)
		}
		raw := "SELECT ParentCategory,Total,Matched,MatchedTotal,ROW_NUMBER() OVER(ORDER BY ParentCategory),LAG(Total,1) OVER(ORDER BY ParentCategory),SUM(Total) OVER(ORDER BY ParentCategory ROWS UNBOUNDED PRECEDING) FROM (SELECT p.category AS ParentCategory,COUNT(*) AS Total,COUNT(CASE WHEN p.category=1 THEN 1 END) AS Matched,SUM(CASE WHEN p.category=1 THEN a.score END) AS MatchedTotal FROM tidbgo_it_tiflash_extensions a LEFT JOIN tidbgo_it_tiflash_extensions p ON p.id=a.parent_id AND p.deleted_at IS NULL WHERE a.deleted_at IS NULL GROUP BY p.category) grouped ORDER BY ParentCategory LIMIT 3"
		referenceStarted := time.Now()
		rows, err := conn.QueryContext(ctx, raw)
		if err != nil {
			fatalDatabaseError(t, dsn, "reference window", err)
		}
		var want []starterWindowResult
		for rows.Next() {
			var value starterWindowResult
			if err := rows.Scan(&value.ParentCategory, &value.Total, &value.Matched, &value.MatchedTotal, &value.Position, &value.Previous, &value.Running); err != nil {
				t.Fatal(err)
			}
			want = append(want, value)
		}
		if err := rows.Err(); err != nil {
			fatalDatabaseError(t, dsn, "reference rows", err)
		}
		rows.Close()
		referenceElapsed := time.Since(referenceStarted)
		referenceRU, err := orm.LastServerRU(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "reference window RU", err)
		}
		t.Logf("window reference=manual_join engine=auto latency=%s ServerRU=%.6f", referenceElapsed, referenceRU)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("window values mismatch: got=%v want=%v", got, want)
		}
		plan, err := q.ExplainAnalyze(ctx, conn)
		if err != nil {
			fatalDatabaseError(t, dsn, "window plan", err)
		}
		if plan.WarningsError != nil {
			fatalDatabaseError(t, dsn, "window warnings", plan.WarningsError)
		}
		for _, warning := range plan.Warnings {
			t.Logf("window warning code=%d message=%s", warning.Code, redact.Error(errors.New(warning.Message), dsn))
		}
		summary := plan.Summary()
		if !summary.Executed || !summary.Result.ActualKnown || summary.Result.Actual != 3 {
			t.Fatalf("window summary=%#v", summary)
		}
		t.Logf("window engine=%s rows=%d latency=%s ServerRU=%.6f warnings=%d operators=%d", engine, len(got), elapsed, ru, len(plan.Warnings), len(summary.Operators))
	}
}

func testTiFlashExtensionVector(t *testing.T, ctx context.Context, conn *sql.Conn, dsn string) {
	input, err := vector.New([]float32{7, 11, 1})
	if err != nil {
		t.Fatal(err)
	}
	type hit struct {
		ID       int64
		Distance float64
	}
	for _, filtered := range []bool{false, true} {
		for _, mode := range []orm.VectorSearchMode{orm.VectorExact, orm.VectorApproximate} {
			q := orm.Nearest[starterTiFlashNode]("Embedding", input, vector.L2, 10).Select("ID").WithDeleted().Mode(mode).ReadFrom(orm.TiFlash).MPP(orm.MPPEnforce)
			if filtered {
				q.Where(orm.Equal("Category", int64(1)))
			}
			var got []hit
			started := time.Now()
			if err := q.ScanAll(ctx, conn, &got); err != nil {
				fatalDatabaseError(t, dsn, "vector search", err)
			}
			elapsed := time.Since(started)
			ru, err := orm.LastServerRU(ctx, conn)
			if err != nil {
				fatalDatabaseError(t, dsn, "vector RU", err)
			}
			if len(got) != 10 {
				t.Fatalf("vector returned %d", len(got))
			}
			for i, row := range got {
				if math.IsNaN(row.Distance) || row.Distance < 0 || (i > 0 && row.Distance < got[i-1].Distance) {
					t.Fatal("vector distance order", got)
				}
			}
			if mode == orm.VectorExact {
				where := ""
				if filtered {
					where = " WHERE category=1"
				}
				rows, err := conn.QueryContext(ctx, "SELECT /*+ READ_FROM_STORAGE(TIKV[a]) */ id,VEC_L2_DISTANCE(embedding,?) FROM tidbgo_it_tiflash_extensions a"+where+" ORDER BY 2,id LIMIT 10", input)
				if err != nil {
					fatalDatabaseError(t, dsn, "exact vector reference", err)
				}
				var want []hit
				for rows.Next() {
					var value hit
					if err := rows.Scan(&value.ID, &value.Distance); err != nil {
						t.Fatal(err)
					}
					want = append(want, value)
				}
				if err := rows.Err(); err != nil {
					fatalDatabaseError(t, dsn, "exact vector rows", err)
				}
				rows.Close()
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("exact vector differs: got=%v want=%v", got, want)
				}
			}
			plan, err := q.ExplainAnalyze(ctx, conn)
			if err != nil {
				fatalDatabaseError(t, dsn, "vector plan", err)
			}
			if mode == orm.VectorExact && plan.IndexUsage() == orm.VectorIndexUsed {
				t.Fatal("exact search used ANN")
			}
			if !filtered && mode == orm.VectorApproximate && plan.IndexUsage() != orm.VectorIndexUsed {
				t.Fatalf("ANN candidate did not use index: usage=%s", plan.IndexUsage())
			}
			t.Logf("vector mode=%d prefilter=%t rows=%d latency=%s ServerRU=%.6f index=%s warnings=%d", mode, filtered, len(got), elapsed, ru, plan.IndexUsage(), len(plan.Warnings))
		}
	}
	var tableName, ddl string
	if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE tidbgo_it_tiflash_extensions").Scan(&tableName, &ddl); err != nil {
		fatalDatabaseError(t, dsn, "vector schema snapshot", err)
	}
	catalog, err := schema.Parse(ddl)
	if err != nil {
		t.Fatalf("parse actual vector schema: %v", err)
	}
	diagnostics, err := orm.Nearest[starterTiFlashNode]("Embedding", input, vector.L2, 10).WithDeleted().Mode(orm.VectorApproximate).SchemaDiagnostics(catalog)
	if err != nil || len(diagnostics) != 0 {
		t.Fatal(diagnostics, err)
	}
}
