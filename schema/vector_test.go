package schema

import "testing"

func TestVectorMetadata(t *testing.T) {
	c, err := Parse("CREATE TABLE documents (id BIGINT PRIMARY KEY, embedding VECTOR(768), other VECTOR, VECTOR INDEX emb ((VEC_COSINE_DISTANCE(embedding))) USING HNSW)")
	if err != nil {
		t.Fatal(err)
	}
	table, _ := c.Table("documents")
	column, _ := table.Column("embedding")
	if column.TypeName() != "VECTOR" || column.VectorDimensions() != 768 {
		t.Fatal(column)
	}
	indexes := table.Indexes()
	field, metric, ok := indexes[1].Vector()
	if !ok || field != "embedding" || metric != "VEC_COSINE_DISTANCE" || indexes[1].SupportsDefaultColumnLookup() || indexes[1].ProvidesUnconditionalUniqueness() {
		t.Fatal(indexes[1])
	}
	for _, dimensions := range []string{"0", "-1", "16384", "3,4", "bad"} {
		if _, err := Parse("CREATE TABLE docs (v VECTOR(" + dimensions + "))"); err == nil {
			t.Fatal("accepted", dimensions)
		}
	}
	c, err = Parse("CREATE TABLE docs (v VECTOR(3), VECTOR INDEX unsupported ((OTHER_DISTANCE(v))) USING HNSW)")
	if err == nil {
		t.Fatal("unrecognized vector distance expression accepted")
	}
}
