package starterapp

import (
	"fmt"
	"os"
	"testing"

	"github.com/mayahiro/go-tidb/check"
	"github.com/mayahiro/go-tidb/orm"
	"github.com/mayahiro/go-tidb/schema"
	"github.com/mayahiro/go-tidb/tiflash"
	"github.com/mayahiro/go-tidb/vector"
)

func TestSearchDocumentMapping(t *testing.T) {
	text, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := schema.Parse(string(text))
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := check.Schema[SearchDocument](catalog); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	input, err := vector.New([]float32{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics, err := orm.Nearest[SearchDocument]("Embedding", input, vector.L2, 10).SchemaDiagnostics(catalog); err != nil || len(diagnostics) != 0 {
		t.Fatal(diagnostics, err)
	}
}

func Example_vectorSearch() {
	input, err := vector.New([]float32{1, 2, 3})
	if err != nil {
		panic(err)
	}
	statement, _, err := orm.Nearest[SearchDocument]("Embedding", input, vector.L2, 10).Select("ID").Where(orm.Equal("TenantID", int64(7))).Build()
	if err != nil {
		panic(err)
	}
	fmt.Println(statement)
	// Output:
	// SELECT `v`.`id` AS `ID`, VEC_L2_DISTANCE(`v`.`embedding`, ?) AS `Distance` FROM `search_documents` AS `v` WHERE `v`.`tenant_id` = ? ORDER BY `Distance` ASC, `v`.`id` ASC LIMIT ?
}

func Example_tiFlashPreparation() {
	ddl, err := tiflash.BuildEnableReplica("app", "search_documents")
	if err != nil {
		panic(err)
	}
	fmt.Println(ddl)
	index, err := orm.BuildVectorIndex[SearchDocument]("Embedding", "embedding_l2", vector.L2)
	if err != nil {
		panic(err)
	}
	fmt.Println(index)
	// Output:
	// ALTER TABLE `app`.`search_documents` SET TIFLASH REPLICA 2
	// CREATE VECTOR INDEX `embedding_l2` ON `search_documents` ((VEC_L2_DISTANCE(`embedding`))) USING HNSW
}
