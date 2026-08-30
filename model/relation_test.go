package model

import (
	"reflect"
	"strings"
	"testing"
)

type Account struct {
	Meta     `tidbgo:"table=accounts"`
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `tidbgo:"has_many"`
	Profile  *Profile  `tidbgo:"has_one"`
	Groups   []*Group  `tidbgo:"many_to_many,through=account_groups,source=ID:account_id,target=group_id:ID"`
}

type Invoice struct {
	Meta      `tidbgo:"table=invoices"`
	ID        uint64 `tidbgo:",pk"`
	AccountID uint64
	Account   *Account `tidbgo:"belongs_to"`
}

type Profile struct {
	Meta      `tidbgo:"table=profiles"`
	ID        uint64 `tidbgo:",pk"`
	AccountID uint64
}

type Group struct {
	Meta `tidbgo:"table=groups"`
	ID   uint64 `tidbgo:",pk"`
}

func TestDescribeMapsDirectAndManyToManyRelations(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[Account]()
	if err != nil {
		t.Fatalf("Describe[Account]() error = %v", err)
	}
	if got, want := relationNames(descriptor.Relations()), []string{"Invoices", "Profile", "Groups"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("relations = %#v, want %#v", got, want)
	}

	invoices, ok := descriptor.RelationByName("Invoices")
	if !ok || invoices.Kind() != RelationHasMany || !invoices.IsCollection() || invoices.TargetType() != reflect.TypeFor[Invoice]() {
		t.Fatalf("Invoices relation = %#v, exists = %t", invoices, ok)
	}
	if got, want := fieldNames(invoices.SourceKey()), []string{"ID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Invoices source key = %#v, want %#v", got, want)
	}
	if got, want := fieldNames(invoices.TargetKey()), []string{"AccountID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Invoices target key = %#v, want %#v", got, want)
	}
	if got, want := invoices.Index(), []int{2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Invoices index = %#v, want %#v", got, want)
	}
	if _, exists := invoices.Junction(); exists {
		t.Fatal("Invoices unexpectedly has junction metadata")
	}

	profile, ok := descriptor.RelationByName("Profile")
	if !ok || profile.Kind() != RelationHasOne || profile.IsCollection() || profile.TargetType() != reflect.TypeFor[Profile]() {
		t.Fatalf("Profile relation = %#v, exists = %t", profile, ok)
	}

	groups, ok := descriptor.RelationByName("Groups")
	if !ok || groups.Kind() != RelationManyToMany || !groups.IsCollection() || groups.TargetType() != reflect.TypeFor[Group]() {
		t.Fatalf("Groups relation = %#v, exists = %t", groups, ok)
	}
	junction, exists := groups.Junction()
	if !exists || junction.TableName() != "account_groups" {
		t.Fatalf("Groups junction = %#v, exists = %t", junction, exists)
	}
	if got, want := junction.SourceColumns(), []string{"account_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("junction source columns = %#v, want %#v", got, want)
	}
	if got, want := junction.TargetColumns(), []string{"group_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("junction target columns = %#v, want %#v", got, want)
	}
	index := groups.Index()
	index[0] = 99
	sourceKey := groups.SourceKey()
	sourceKey[0].index[0] = 99
	sourceColumns := junction.SourceColumns()
	sourceColumns[0] = "changed"
	again, _ := descriptor.RelationByName("Groups")
	againJunction, _ := again.Junction()
	if again.Index()[0] != 4 || again.SourceKey()[0].Index()[0] != 1 || againJunction.SourceColumns()[0] != "account_id" {
		t.Fatalf("cached relation metadata was mutated = %#v %#v", again, againJunction)
	}

	invoice, err := Describe[Invoice]()
	if err != nil {
		t.Fatalf("Describe[Invoice]() error = %v", err)
	}
	account, ok := invoice.RelationByName("Account")
	if !ok || account.Kind() != RelationBelongsTo {
		t.Fatalf("Account relation = %#v, exists = %t", account, ok)
	}
	if got, want := fieldNames(account.SourceKey()), []string{"AccountID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Account source key = %#v, want %#v", got, want)
	}
	if got, want := fieldNames(account.TargetKey()), []string{"ID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Account target key = %#v, want %#v", got, want)
	}
	if _, exists := invoice.FieldByGoName("Account"); exists {
		t.Fatal("relation field was mapped as a scalar field")
	}
}

type Tenant struct {
	TenantID uint64         `tidbgo:",pk"`
	ID       uint64         `tidbgo:",pk"`
	Records  []TenantRecord `tidbgo:"has_many,join=TenantID:TenantID,join=ID:ParentID"`
}

type TenantRecord struct {
	TenantID uint64
	ParentID uint64
	Value    string
}

func TestDescribePreservesExplicitCompositeRelationJoinOrder(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[Tenant]()
	if err != nil {
		t.Fatalf("Describe[Tenant]() error = %v", err)
	}
	relation, ok := descriptor.RelationByName("Records")
	if !ok {
		t.Fatal("Records relation does not exist")
	}
	if got, want := fieldNames(relation.SourceKey()), []string{"TenantID", "ID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source key = %#v, want %#v", got, want)
	}
	if got, want := fieldNames(relation.TargetKey()), []string{"TenantID", "ParentID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target key = %#v, want %#v", got, want)
	}
}

type RelationWithoutTag struct {
	ID       uint64 `tidbgo:",pk"`
	Invoices []Invoice
}

type IgnoredRelationShapedField struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `tidbgo:"-"`
}

func TestDescribeAllowsIgnoringRelationShapedFields(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[IgnoredRelationShapedField]()
	if err != nil {
		t.Fatalf("Describe[IgnoredRelationShapedField]() error = %v", err)
	}
	if got := relationNames(descriptor.Relations()); len(got) != 0 {
		t.Fatalf("relations = %#v, want none", got)
	}
	if got, want := fieldNames(descriptor.Fields()), []string{"ID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %#v, want %#v", got, want)
	}
}

type WrongRelationCardinality struct {
	ID      uint64   `tidbgo:",pk"`
	Invoice *Invoice `tidbgo:"has_many"`
}

type RelationWithDBTag struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `db:"-" tidbgo:"has_many,join=ID:AccountID"`
}

type RelationWithOnlyDBIgnore struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `db:"-"`
}

func TestDescribeIgnoresDBTagOnRelation(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[RelationWithDBTag]()
	if err != nil {
		t.Fatalf("Describe[RelationWithDBTag]() error = %v", err)
	}
	if got, want := relationNames(descriptor.Relations()), []string{"Invoices"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("relations = %#v, want %#v", got, want)
	}
}

type RelationKindAfterFirstValue struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `tidbgo:",has_many"`
}

type IgnoredRelationWithMetadata struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `tidbgo:"-,has_many"`
}

type ValueToOneRelation struct {
	ID      uint64  `tidbgo:",pk"`
	Invoice Invoice `tidbgo:"has_one"`
}

type InvalidRelationTarget struct {
	ID     uint64   `tidbgo:",pk"`
	Values []string `tidbgo:"has_many"`
}

type MissingInferredForeignKey struct {
	ID       uint64    `tidbgo:",pk"`
	Invoices []Invoice `tidbgo:"has_many"`
}

type IncompatibleBelongsTo struct {
	ID        uint64 `tidbgo:",pk"`
	AccountID string
	Account   *Account `tidbgo:"belongs_to"`
}

type IncompleteManyToMany struct {
	ID     uint64  `tidbgo:",pk"`
	Groups []Group `tidbgo:"many_to_many,through=account_groups,source=ID:account_id"`
}

type MissingManyToManyKey struct {
	ID     uint64  `tidbgo:",pk"`
	Groups []Group `tidbgo:"many_to_many,through=account_groups,source=Missing:account_id,target=group_id:ID"`
}

func TestDescribeRejectsInvalidRelations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		describe func() error
		contains string
	}{
		{name: "missing tag", describe: describeError[RelationWithoutTag], contains: "requires a tidbgo relation tag"},
		{name: "db ignore without tidbgo relation tag", describe: describeError[RelationWithOnlyDBIgnore], contains: "requires a tidbgo relation tag"},
		{name: "relation kind order", describe: describeError[RelationKindAfterFirstValue], contains: "not supported after the column position"},
		{name: "ignore with relation metadata", describe: describeError[IgnoredRelationWithMetadata], contains: "must not be combined"},
		{name: "wrong cardinality", describe: describeError[WrongRelationCardinality], contains: "does not match"},
		{name: "value to-one", describe: describeError[ValueToOneRelation], contains: "must be a pointer"},
		{name: "invalid target", describe: describeError[InvalidRelationTarget], contains: "must target a named struct"},
		{name: "missing inferred foreign key", describe: describeError[MissingInferredForeignKey], contains: "MissingInferredForeignKeyID"},
		{name: "incompatible direct key", describe: describeError[IncompatibleBelongsTo], contains: "incompatible Go representations"},
		{name: "incomplete many-to-many", describe: describeError[IncompleteManyToMany], contains: "requires through, source, and target"},
		{name: "missing many-to-many key", describe: describeError[MissingManyToManyKey], contains: "source key field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.describe()
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("Describe() error = %v, want substring %q", err, tt.contains)
			}
		})
	}
}

func relationNames(relations []Relation) []string {
	names := make([]string, len(relations))
	for index, relation := range relations {
		names[index] = relation.GoName()
	}
	return names
}

func fieldNames(fields []Field) []string {
	names := make([]string, len(fields))
	for index, field := range fields {
		names[index] = field.GoName()
	}
	return names
}
