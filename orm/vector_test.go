package orm

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/schema"
	"github.com/mayahiro/go-tidb/vector"
)

type vectorDocument struct {
	model.Meta `tidbgo:"table=vector_documents"`
	ID         int64 `tidbgo:",pk"`
	TenantID   int64
	Embedding  vector.Vector
	DeletedAt  *time.Time `tidbgo:",soft_delete"`
}

func vectorTestInput(t testing.TB) vector.Vector {
	t.Helper()
	v, err := vector.New([]float32{1, 0, 2})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVectorSearchSQLAndIsolation(t *testing.T) {
	input := vectorTestInput(t)
	q := Nearest[vectorDocument]("Embedding", input, vector.Cosine, 5).Select("ID").Where(Equal("TenantID", int64(7)))
	statement, args, err := q.Build()
	want := "SELECT `v`.`id` AS `ID`, VEC_COSINE_DISTANCE(`v`.`embedding`, ?) AS `Distance` FROM `vector_documents` AS `v` WHERE `v`.`deleted_at` IS NULL AND `v`.`tenant_id` = ? ORDER BY `Distance` ASC, `v`.`id` ASC LIMIT ?"
	if err != nil || statement != want || !reflect.DeepEqual(args, []any{input, int64(7), int64(5)}) {
		t.Fatalf("%s %v %v", statement, args, err)
	}
	args[0] = "modified"
	_, again, _ := q.Build()
	if !reflect.DeepEqual(again[0], input) {
		t.Fatal("argument aliasing")
	}
	approx, _, err := q.Mode(VectorApproximate).ReadFrom(TiFlash).MPP(MPPEnforce).Build()
	if err != nil || strings.Contains(approx, "`Distance` ASC,") || !strings.Contains(approx, "WHERE `v`.`deleted_at` IS NULL AND `v`.`tenant_id` = ? ORDER BY") || !strings.Contains(approx, "READ_FROM_STORAGE(TIFLASH[v])") {
		t.Fatal(approx, err)
	}
	ddl, err := BuildVectorIndex[vectorDocument]("Embedding", "embedding_cosine", vector.Cosine)
	if err != nil || ddl != "CREATE VECTOR INDEX `embedding_cosine` ON `vector_documents` ((VEC_COSINE_DISTANCE(`embedding`))) USING HNSW" {
		t.Fatal(ddl, err)
	}
}

func TestVectorSearchRejectsInvalidBeforeExecution(t *testing.T) {
	v := vectorTestInput(t)
	queries := []*VectorQuery[vectorDocument]{
		nil, Nearest[vectorDocument]("Embedding", vector.Vector{}, vector.L2, 1),
		Nearest[vectorDocument]("ID", v, vector.L2, 1),
		Nearest[vectorDocument]("Embedding", v, "unsafe()", 1),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 0),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1<<32),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1).Mode(99),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1).DistanceAs("ID"),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1).DistanceAs("x);--"),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1).ReadFrom(TiKV).MPP(MPPEnforce),
		Nearest[vectorDocument]("Embedding", v, vector.L2, 1).Where(Equal("Missing", 1)),
	}
	for _, q := range queries {
		if _, _, err := q.Build(); err == nil {
			t.Fatalf("accepted %#v", q)
		}
	}
	if _, _, err := Nearest[*vectorDocument]("Embedding", v, vector.L2, 1).Build(); err == nil {
		t.Fatal("pointer source")
	}
	if _, err := BuildVectorIndex[vectorDocument]("Embedding", "bad`;--", vector.L2); err == nil {
		t.Fatal("index injection")
	}
}

func TestVectorScanAllAtomicAndCapture(t *testing.T) {
	type hit struct {
		ID       int64
		Distance *float64
	}
	for _, invalid := range []bool{false, true} {
		state := &allTestState{columns: []string{"ID", "Distance"}, values: [][]driver.Value{{int64(1), nil}, {int64(2), 0.5}}}
		if invalid {
			state.values[1][1] = "invalid distance"
		}
		db := sql.OpenDB(&allTestConnector{state: state})
		var capture bytes.Buffer
		ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&capture))
		got := []hit{{ID: 99}}
		err := Nearest[vectorDocument]("Embedding", vectorTestInput(t), vector.L2, 2).Select("ID").ScanAll(ctx, db, &got)
		db.Close()
		if invalid {
			if err == nil || !reflect.DeepEqual(got, []hit{{ID: 99}}) {
				t.Fatal(got, err)
			}
		} else if err != nil || len(got) != 2 || got[0].Distance != nil || got[1].Distance == nil || *got[1].Distance != 0.5 {
			t.Fatal(got, err)
		}
		if state.closeCalls != 1 || !strings.Contains(capture.String(), `"source":"typed_vector"`) || strings.Contains(capture.String(), "[1,0,2]") {
			t.Fatal("rows/capture", state.closeCalls, capture.String())
		}
	}
}

func TestVectorSchemaAndPlanEvidence(t *testing.T) {
	catalog, err := schema.Parse("CREATE TABLE vector_documents (id BIGINT PRIMARY KEY, embedding VECTOR(3), VECTOR INDEX emb ((VEC_L2_DISTANCE(embedding))) USING HNSW)")
	if err != nil {
		t.Fatal(err)
	}
	q := Nearest[vectorDocument]("Embedding", vectorTestInput(t), vector.L2, 2).Mode(VectorApproximate)
	diagnostics, err := q.SchemaDiagnostics(catalog)
	if err != nil || len(diagnostics) != 1 || diagnostics[0].Code != "VEC002" {
		t.Fatal(diagnostics, err)
	}
	diagnostics, err = q.WithDeleted().SchemaDiagnostics(catalog)
	if err != nil || len(diagnostics) != 0 {
		t.Fatal(diagnostics, err)
	}
	wrong, _ := schema.Parse("CREATE TABLE vector_documents (embedding VECTOR(4))")
	diagnostics, err = q.SchemaDiagnostics(wrong)
	if err != nil || len(diagnostics) != 1 || diagnostics[0].Code != "VEC001" {
		t.Fatal(diagnostics, err)
	}
	for _, tc := range []struct {
		task, info string
		want       VectorIndexUsage
	}{
		{"mpp[tiflash]", "annIndex:embedding_l2", VectorIndexUsed},
		{"mpp[tiflash]", "keep order:false", VectorIndexNotUsed},
		{"future[engine]", "annIndex:embedding_l2", VectorIndexUnknown},
	} {
		p := VectorPlan{Mode: VectorApproximate, Planned: []ExplainRow{{ID: "TableFullScan_1", Task: tc.task, AccessObject: "table:v", OperatorInfo: tc.info}}}
		if p.IndexUsage() != tc.want {
			t.Fatal(p.IndexUsage(), tc.want)
		}
	}
	state := aggregatePlanState(false)
	state.target.values[0] = []driver.Value{"TableFullScan_1", "3", "mpp[tiflash]", "table:v", "annIndex:embedding_l2"}
	db := sql.OpenDB(&aggregatePlanConnector{state: state})
	defer db.Close()
	plan, err := q.Explain(context.Background(), db)
	if err != nil || plan.IndexUsage() != VectorIndexUsed || plan.Executed != nil || state.targetConnection != state.warningConnection {
		t.Fatal(plan, err)
	}
}

func BenchmarkVectorSearchBuild(b *testing.B) {
	for _, approximate := range []bool{false, true} {
		name := "exact"
		if approximate {
			name = "approximate"
		}
		b.Run(name, func(b *testing.B) {
			q := Nearest[vectorDocument]("Embedding", vectorTestInput(b), vector.L2, 20).Select("ID")
			if approximate {
				q.Mode(VectorApproximate)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := q.Build(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
