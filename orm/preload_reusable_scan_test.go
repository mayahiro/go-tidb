package orm

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type reusableScanParent struct {
	ID       int64                    `tidbgo:",pk"`
	Values   []reusableScanTarget     `tidbgo:"many_to_many,through=reusable_edges,source=ID:parent_id,target=target_id:ID"`
	Pointers []*reusableScanTarget    `tidbgo:"many_to_many,through=reusable_edges,source=ID:parent_id,target=target_id:ID"`
	Embedded []reusableEmbeddedTarget `tidbgo:"many_to_many,through=reusable_edges,source=ID:parent_id,target=target_id:ID"`
}

type reusableScanTarget struct {
	ID int64 `tidbgo:",pk"`
	V0 *string
	V1 []byte
	V2 reusableScanValue
	V3 time.Time `tidbgo:",soft_delete"`
}

type reusableScanValue struct{ Text, Previous string }

func (v *reusableScanValue) Scan(value any) error {
	v.Previous = v.Text
	switch value := value.(type) {
	case nil:
		v.Text = ""
	case string:
		v.Text = value
	case []byte:
		v.Text = string(value)
	default:
		return fmt.Errorf("unsupported reusable scan value %T", value)
	}
	return nil
}

// ReusableEmbeddedFields exercises an address that changes across row resets.
type ReusableEmbeddedFields struct{ V0 string }
type reusableEmbeddedTarget struct {
	ID int64 `tidbgo:",pk"`
	*ReusableEmbeddedFields
}

func TestManyToManyReusableScanResetsValuesAndRetainsOwnership(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, path := range []string{"Values", "Pointers"} {
		t.Run(path, func(t *testing.T) {
			descriptor, err := model.Describe[reusableScanParent]()
			if err != nil {
				t.Fatal(err)
			}
			plans, err := compilePreloadPlans(descriptor, []preloadRequest{{path: path}})
			if err != nil {
				t.Fatal(err)
			}
			decoder := newManyToManyPreloadDecoder(plans[0])
			if decoder.reusable != (path == "Values") {
				t.Fatal("unexpected reusable decoder selection")
			}
			state := &preloadTestState{responses: []*preloadTestResponse{
				{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}, {int64(1)}, {int64(2)}}},
				{columns: []string{"parent_id", "id", "v0", "v1", "v2", "v3"}, values: [][]driver.Value{
					{int64(1), int64(11), "first", []byte("bytes"), "first", stamp},
					{int64(1), int64(12), nil, nil, nil, nil},
					{int64(2), int64(13), "third", []byte("other"), "third", nil},
				}},
			}}
			values, err := Query[reusableScanParent]().Limit(3).Preload(path).All(context.Background(), openPreloadTestDB(t, state))
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 3 {
				t.Fatal("incorrect parent count")
			}
			at := func(parent, child int) *reusableScanTarget {
				if path == "Values" {
					return &values[parent].Values[child]
				}
				return values[parent].Pointers[child]
			}
			first, zero, last := at(0, 0), at(0, 1), at(2, 0)
			if first.ID != 11 || first.V0 == nil || *first.V0 != "first" || string(first.V1) != "bytes" || first.V2.Text != "first" || first.V3 != stamp {
				t.Fatalf("first row changed: %#v", first)
			}
			if zero.ID != 12 || zero.V0 != nil || zero.V1 != nil || zero.V2.Text != "" || zero.V2.Previous != "" || !zero.V3.IsZero() {
				t.Fatalf("previous row leaked: %#v", zero)
			}
			if last.ID != 13 || *last.V0 != "third" || last.V2.Text != "third" || last.V2.Previous != "" {
				t.Fatalf("last row changed: %#v", last)
			}
			if first == at(1, 0) {
				t.Fatal("duplicate parents share the target struct")
			}
			first.ID = -1
			if at(1, 0).ID != 11 {
				t.Fatal("duplicate parent scalar changed")
			}
			first.V1[0] = 'x'
			if string(last.V1) != "other" {
				t.Fatal("different rows share scan bytes")
			}
		})
	}
}

func TestManyToManyReusableScanFallsBackForEmbeddedPointer(t *testing.T) {
	descriptor, err := model.Describe[reusableScanParent]()
	if err != nil {
		t.Fatal(err)
	}
	plans, err := compilePreloadPlans(descriptor, []preloadRequest{{path: "Embedded"}})
	if err != nil {
		t.Fatal(err)
	}
	if newManyToManyPreloadDecoder(plans[0]).reusable {
		t.Fatal("embedded pointer must bind each row")
	}
	state := &preloadTestState{responses: []*preloadTestResponse{
		{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
		{columns: []string{"parent_id", "id", "v0"}, values: [][]driver.Value{{int64(1), int64(11), "first"}, {int64(1), int64(12), "second"}}},
	}}
	values, err := Query[reusableScanParent]().Preload("Embedded").All(context.Background(), openPreloadTestDB(t, state))
	if err != nil {
		t.Fatal(err)
	}
	rows := values[0].Embedded
	if len(rows) != 2 || rows[0].V0 != "first" || rows[1].V0 != "second" || rows[0].ReusableEmbeddedFields == rows[1].ReusableEmbeddedFields {
		t.Fatalf("embedded results changed: %#v", rows)
	}
}

func TestManyToManyReusableScanReportsLaterRowError(t *testing.T) {
	state := &preloadTestState{responses: []*preloadTestResponse{
		{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
		{columns: []string{"parent_id", "id", "v0", "v1", "v2", "v3"}, values: [][]driver.Value{
			{int64(1), int64(11), nil, nil, "first", nil},
			{int64(1), int64(12), nil, nil, int64(42), nil},
		}},
	}}
	_, err := Query[reusableScanParent]().Preload("Values").All(context.Background(), openPreloadTestDB(t, state))
	if err == nil || !strings.Contains(err.Error(), "unsupported reusable scan value") {
		t.Fatalf("later row error = %v", err)
	}
}
