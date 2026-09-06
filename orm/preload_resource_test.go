package orm

import (
	"reflect"
	"sync"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

func TestPreloadReusesOnlyMatchingDefaultScanPlans(t *testing.T) {
	t.Parallel()
	descriptor, err := model.Describe[preloadGraph]()
	if err != nil {
		t.Fatal(err)
	}
	base, err := preloadPlanFor(descriptor, "NodeA")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		fields []string
		want   []string
		key    int
		reuse  bool
	}{
		{nil, []string{"id", "value"}, 0, true},
		{[]string{"ID", "Value"}, []string{"id", "value"}, 0, true},
		{[]string{"Value", "ID"}, []string{"value", "id"}, 1, false},
		{[]string{"Value"}, []string{"value", "id"}, 1, false},
		{[]string{"ID"}, []string{"id"}, 0, false},
	} {
		var options []PreloadOption
		if test.fields != nil {
			options = []PreloadOption{PreloadFields(test.fields...)}
		}
		plans, err := compilePreloadPlans(descriptor, []preloadRequest{{path: "NodeA", options: options}})
		if err != nil {
			t.Fatal(err)
		}
		plan := plans[0]
		if (plan.targetStatement == base.targetStatement) != test.reuse || !reflect.DeepEqual(plan.targetStatement.scanPlan.columns, test.want) || !reflect.DeepEqual(plan.targetKeyScan, []int{test.key}) {
			t.Fatalf("fields=%v columns=%v key=%v reused=%t", test.fields, plan.targetStatement.scanPlan.columns, plan.targetKeyScan, plan.targetStatement == base.targetStatement)
		}
		if plan == base {
			t.Fatal("query-local aliases and scopes must not share a mutable plan")
		}
	}
	if !reflect.DeepEqual(base.targetKeyScan, []int{0}) || base.sourceAlias != "" || base.targetAlias != "" || base.withDeleted || base.loadAllSources || len(base.children) != 0 || base.targetStatement.qualifier != "" {
		t.Fatalf("cached base was modified: %#v", base)
	}
}

func TestPreloadProjectionAddsKeysWithoutModifyingOptions(t *testing.T) {
	t.Parallel()
	// Spare capacity makes an accidental append to the caller's backing array
	// observable even when the caller's slice length would remain unchanged.
	backing := []string{"Value", "untouched", "untouched", "untouched"}
	option := PreloadOption{kind: preloadOptionFields, fields: backing[:1]}
	query := Query[preloadGraph]().Preload("NodeA", option).Preload("NodeB", option)
	first, args, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	second, nextArgs, err := query.Build()
	if err != nil || first != second || !reflect.DeepEqual(args, nextArgs) || !reflect.DeepEqual(backing, []string{"Value", "untouched", "untouched", "untouched"}) {
		t.Fatalf("SQL changed=%t fields=%v error=%v", first != second, backing, err)
	}

	descriptor, err := model.Describe[preloadOrder]()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := descriptor.FieldByGoName("ID")
	userID, _ := descriptor.FieldByGoName("UserID")
	plan := &preloadPlan{targetKey: []model.Field{userID}, children: []*preloadPlan{{sourceKey: []model.Field{id}}}}
	fields := []string{"Total", "untouched", "untouched", "untouched"}
	projection := preloadTargetProjection(fields[:1], plan)
	if !reflect.DeepEqual(projection, []string{"Total", "UserID", "ID"}) || !reflect.DeepEqual(fields, []string{"Total", "untouched", "untouched", "untouched"}) {
		t.Fatalf("projection=%v input=%v", projection, fields)
	}
}

func TestPreloadPlansStayIndependentAcrossConcurrentBuilds(t *testing.T) {
	t.Parallel()
	fields := PreloadFields("ID", "Value")
	queries := []*SelectQuery[preloadGraph]{
		Query[preloadGraph]().Select("ID").Preload("NodeA", fields).Preload("NodeB", PreloadFields("Value")),
		Query[preloadGraph]().Select("ID").Preload("NodeB", fields).Preload("NodeA", fields).Preload("Tags.Node", fields),
		Query[preloadGraph]().Preload("Children.Node", fields).Preload("Tags.Node", fields),
		Query[preloadGraph]().Preload("Children.Node", fields).Preload("Tags.Node", fields).Limit(2),
	}
	want := make([]compiledSelect, len(queries))
	for index, query := range queries {
		compiled, err := query.compile()
		if err != nil {
			t.Fatal(err)
		}
		want[index] = compiled
	}
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			for range 20 {
				for index, query := range queries {
					got, err := query.compile()
					if err != nil || !reflect.DeepEqual(got, want[index]) {
						t.Errorf("query=%d plan or SQL changed: %v", index, err)
						return
					}
				}
			}
		})
	}
	workers.Wait()
}

type preloadResourceTarget struct {
	ID int
	V0 string
}

func TestPreloadAppendPreservesOwnershipAndOrder(t *testing.T) {
	for _, capacity := range []int{0, 1, 8} {
		values := make([]preloadResourceTarget, 0, capacity)
		pointers := make([]*preloadResourceTarget, 0, capacity)
		copies := make([]*preloadResourceTarget, 0, capacity)
		valueField := reflect.ValueOf(&values).Elem()
		pointerField := reflect.ValueOf(&pointers).Elem()
		copyField := reflect.ValueOf(&copies).Elem()
		reused := &preloadResourceTarget{V0: "value"}
		for index := range 10 {
			reused.ID = index
			assignPreloadedTarget(valueField, reflect.ValueOf(reused), false)
			target := &preloadResourceTarget{ID: index, V0: "pointer"}
			assignPreloadedTarget(pointerField, reflect.ValueOf(target), false)
			assignPreloadedTarget(copyField, reflect.ValueOf(target), true)
			if pointers[index] != target || copies[index] == target {
				t.Fatal("pointer ownership changed")
			}
		}
		reused.ID = -1
		for index := range 10 {
			if values[index].ID != index || values[index].V0 != "value" || pointers[index].ID != index || copies[index].ID != index {
				t.Fatalf("initial capacity=%d result changed at index=%d", capacity, index)
			}
			pointers[index].ID = -1
			if copies[index].ID != index {
				t.Fatal("duplicate parents share a mutable target pointer")
			}
		}
	}
}

func TestPreloadAppendDoesNotAllocateSliceHeaders(t *testing.T) {
	values := make([]preloadResourceTarget, 0, 10)
	pointers := make([]*preloadResourceTarget, 0, 10)
	target := reflect.ValueOf(&preloadResourceTarget{ID: 1})
	for _, field := range []reflect.Value{reflect.ValueOf(&values).Elem(), reflect.ValueOf(&pointers).Elem()} {
		allocations := testing.AllocsPerRun(100, func() {
			field.SetLen(0)
			for range 10 {
				assignPreloadedTarget(field, target, false)
			}
		})
		if allocations != 0 {
			t.Fatalf("%s allocated %g times with sufficient capacity", field.Type(), allocations)
		}
	}
}
