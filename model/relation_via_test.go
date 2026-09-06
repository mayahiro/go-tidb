package model

import (
	"reflect"
	"strings"
	"testing"
)

type viaOwner struct {
	ID      int64        `tidbgo:"owner_key,pk"`
	Targets []*viaTarget `tidbgo:"many_to_many,via=Edges.Target"`
	Edges   []viaEdge    `tidbgo:"has_many,join=ID:OwnerID"`
}

type viaEdge struct {
	Meta     `tidbgo:"table=owner_edges"`
	ID       int64  `tidbgo:",pk,auto_random"`
	OwnerID  int64  `tidbgo:"owner_ref"`
	TargetID *int64 `tidbgo:"target_ref"`
	Priority int
	Target   *viaTarget `tidbgo:"belongs_to"`
}

type viaTarget struct {
	ID int64 `tidbgo:"target_key,pk"`
}

func TestDescribeViaRelationReusesEdgeMappings(t *testing.T) {
	t.Parallel()
	descriptor, err := Describe[viaOwner]()
	if err != nil {
		t.Fatal(err)
	}
	if got := relationNames(descriptor.Relations()); !reflect.DeepEqual(got, []string{"Targets", "Edges"}) {
		t.Fatalf("relation order = %v", got)
	}
	relation, _ := descriptor.RelationByName("Targets")
	if relation.Via() != "Edges.Target" || relation.Kind() != RelationManyToMany || relation.TargetType() != reflect.TypeFor[viaTarget]() {
		t.Fatalf("relation = %#v", relation)
	}
	junction, ok := relation.Junction()
	if !ok || junction.TableName() != "owner_edges" || !reflect.DeepEqual(junction.SourceColumns(), []string{"owner_ref"}) || !reflect.DeepEqual(junction.TargetColumns(), []string{"target_ref"}) {
		t.Fatalf("junction = %#v", junction)
	}
	if relation.SourceKey()[0].ColumnName() != "owner_key" || relation.TargetKey()[0].ColumnName() != "target_key" {
		t.Fatalf("keys = %v / %v", relation.SourceKey(), relation.TargetKey())
	}
	columns := junction.SourceColumns()
	columns[0] = "modified"
	again, _ := relation.Junction()
	if again.SourceColumns()[0] != "owner_ref" {
		t.Fatal("mutable junction")
	}
	if _, exists := descriptor.FieldByGoName("Targets"); exists {
		t.Fatal("relation mapped as scalar")
	}
}

type viaCompositeOwner struct {
	Tenant  int64                `tidbgo:",pk"`
	ID      int64                `tidbgo:",pk"`
	Edges   []viaCompositeEdge   `tidbgo:"has_many,join=Tenant:Tenant,join=ID:OwnerID"`
	Targets []viaCompositeTarget `tidbgo:"many_to_many,via=Edges.Target"`
}
type viaCompositeEdge struct {
	Tenant   int64
	OwnerID  int64
	TargetID int64
	Target   *viaCompositeTarget `tidbgo:"belongs_to,join=Tenant:Tenant,join=TargetID:ID"`
}
type viaCompositeTarget struct {
	Tenant int64 `tidbgo:",pk"`
	ID     int64 `tidbgo:",pk"`
}

func TestDescribeViaCompositeSharedTenantKey(t *testing.T) {
	t.Parallel()
	descriptor, err := Describe[viaCompositeOwner]()
	if err != nil {
		t.Fatal(err)
	}
	relation, _ := descriptor.RelationByName("Targets")
	junction, _ := relation.Junction()
	if !reflect.DeepEqual(junction.SourceColumns(), []string{"tenant", "owner_id"}) || !reflect.DeepEqual(junction.TargetColumns(), []string{"tenant", "target_id"}) {
		t.Fatalf("junction = %#v", junction)
	}
}

func TestDescribeViaRejectsInvalidPaths(t *testing.T) {
	t.Parallel()
	type missing struct {
		ID      int64       `tidbgo:",pk"`
		Targets []viaTarget `tidbgo:"many_to_many,via=Missing.Target"`
	}
	type recursive struct {
		ID      int64       `tidbgo:",pk"`
		Targets []viaTarget `tidbgo:"many_to_many,via=Targets.Target"`
	}
	type wrongTarget struct {
		ID      int64     `tidbgo:",pk"`
		Edges   []viaEdge `tidbgo:"has_many,join=ID:OwnerID"`
		Targets []Group   `tidbgo:"many_to_many,via=Edges.Target"`
	}
	type unmappedTarget struct {
		ID      int64       `tidbgo:",pk"`
		Edges   []viaEdge   `tidbgo:"has_many,join=ID:OwnerID"`
		Targets []viaTarget `tidbgo:"many_to_many,via=Edges.Priority"`
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[missing](), reflect.TypeFor[recursive](), reflect.TypeFor[wrongTarget](), reflect.TypeFor[unmappedTarget]()} {
		if _, err := DescribeType(typ); err == nil || !strings.Contains(err.Error(), "via field") {
			t.Fatalf("%s error = %v", typ, err)
		}
	}
}
