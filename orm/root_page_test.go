package orm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
	"github.com/mayahiro/go-tidb/model"
)

func TestRootPageBuildLimitsKeysBeforeInlinePreload(t *testing.T) {
	query := rootPageQuery()
	statement, args, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`owner_id`, `tidbgo_t0`.`target_id`, `tidbgo_t0`.`added_at`, `tidbgo_t1`.`id`, `tidbgo_t1`.`label`, `tidbgo_t1`.`payload`, `tidbgo_t1`.`deleted_at` FROM (SELECT `tidbgo_p0`.`id` FROM `root_page_records` AS `tidbgo_p0` WHERE `tidbgo_p0`.`owner_id` = ? ORDER BY `tidbgo_p0`.`added_at` DESC, `tidbgo_p0`.`id` DESC LIMIT ?) AS `tidbgo_k0` STRAIGHT_JOIN `root_page_records` AS `tidbgo_t0` ON (`tidbgo_k0`.`id` = `tidbgo_t0`.`id`) LEFT JOIN `root_page_targets` AS `tidbgo_t1` ON (`tidbgo_t0`.`target_id` = `tidbgo_t1`.`id` AND `tidbgo_t1`.`deleted_at` IS NULL) ORDER BY `tidbgo_t0`.`added_at` DESC, `tidbgo_t0`.`id` DESC"
	if statement != want || !reflect.DeepEqual(args, []any{int64(7), int64(50)}) {
		t.Fatalf("Build() = %s, %#v", statement, args)
	}
	count, err := query.compileCount()
	if err != nil || strings.Contains(count.sql, "JOIN") || strings.Contains(count.sql, rootPageSourceAlias) {
		t.Fatalf("Count must keep its independent scalar compilation: %#v, %v", count, err)
	}
	for _, limit := range []int64{1, 2} {
		compiled, err := query.compileWithLimit(limit)
		if err != nil || !compiled.rootPage || compiled.arguments[1] != limit {
			t.Fatalf("terminal limit=%d: %#v, %v", limit, compiled, err)
		}
	}
	if query.selection.pagination.limit != 50 {
		t.Fatal("terminal compilation changed the original builder")
	}
}

func TestRootPageFallbacksKeepOrdinarySelect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SelectQuery[rootPageRecord])
	}{
		{"offset", func(q *SelectQuery[rootPageRecord]) { q.Offset(1) }},
		{"large page", func(q *SelectQuery[rootPageRecord]) { q.Limit(101) }},
		{"zero page", func(q *SelectQuery[rootPageRecord]) { q.Limit(0) }},
		{"no limit", func(q *SelectQuery[rootPageRecord]) { q.selection.pagination = pagination{} }},
		{"no order", func(q *SelectQuery[rootPageRecord]) { q.selection.orderBy = nil }},
		{"primary order", func(q *SelectQuery[rootPageRecord]) { q.selection.orderBy = nil; q.OrderBy(Desc("ID")) }},
		{"mixed order", func(q *SelectQuery[rootPageRecord]) { q.selection.orderBy[1].direction = orderAscending }},
		{"no equality", func(q *SelectQuery[rootPageRecord]) { q.selection.predicates = nil }},
		{"range", func(q *SelectQuery[rootPageRecord]) { q.Where(LessThan("TargetID", int64(5))) }},
		{"or", func(q *SelectQuery[rootPageRecord]) {
			q.Where(Or(Equal("TargetID", int64(2)), Equal("TargetID", int64(3))))
		}},
		{"point lookup", func(q *SelectQuery[rootPageRecord]) { q.Where(Equal("ID", int64(1))) }},
		{"covered root", func(q *SelectQuery[rootPageRecord]) { q.Where(Equal("TargetID", int64(2))) }},
		{"keyset", func(q *SelectQuery[rootPageRecord]) { q.SeekAfter(time.Now(), int64(100)) }},
		{"no inline preload", func(q *SelectQuery[rootPageRecord]) { q.selection.preloads = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := rootPageQuery()
			tc.change(query)
			compiled, err := query.compile()
			if err != nil || compiled.rootPage || strings.Contains(compiled.statement.sql, rootPageSourceAlias) {
				t.Fatalf("fallback = %#v, %v", compiled, err)
			}
		})
	}
	for _, change := range []func(*SelectQuery[rootPageRecord]){
		func(q *SelectQuery[rootPageRecord]) { q.Offset(-1) },
		func(q *SelectQuery[rootPageRecord]) { q.Limit(-1) },
		func(q *SelectQuery[rootPageRecord]) { q.OrderBy(Desc("AddedAt")) },
		func(q *SelectQuery[rootPageRecord]) { q.Where(Equal("OwnerID", nil)) },
	} {
		query := rootPageQuery()
		change(query)
		if _, _, err := query.Build(); err == nil {
			t.Fatal("invalid query passed validation")
		}
	}
}

type rootPageComposite struct {
	model.Meta `tidbgo:"table=root_page_composite"`
	TenantID   int64 `tidbgo:",pk"`
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64
	TargetID   int64
	AddedAt    time.Time
	DeletedAt  *time.Time      `tidbgo:",soft_delete"`
	Target     *rootPageTarget `tidbgo:"belongs_to"`
}

func TestRootPageCompositeKeyScopesAndProjection(t *testing.T) {
	query := Query[rootPageComposite]().Select("OwnerID", "AddedAt").
		Where(And(Equal("TenantID", int64(3)), Equal("OwnerID", int64(7)))).
		OrderBy(Asc("AddedAt")).Limit(100).Offset(0).
		Preload("Target", PreloadWithDeleted())
	statement, args, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{
		"FROM (SELECT `tidbgo_p0`.`tenant_id`, `tidbgo_p0`.`id`",
		"WHERE `tidbgo_p0`.`deleted_at` IS NULL AND",
		"LIMIT ? OFFSET ?",
		"ON (`tidbgo_k0`.`tenant_id` = `tidbgo_t0`.`tenant_id` AND `tidbgo_k0`.`id` = `tidbgo_t0`.`id`)",
		"ORDER BY `tidbgo_t0`.`added_at` ASC",
	} {
		if !strings.Contains(statement, part) {
			t.Fatalf("SQL lacks %q: %s", part, statement)
		}
	}
	if strings.Contains(statement, "`tidbgo_t1`.`deleted_at` IS NULL") || !reflect.DeepEqual(args, []any{int64(3), int64(7), int64(100), int64(0)}) {
		t.Fatalf("scopes or arguments differ: %s, %#v", statement, args)
	}
	projection, _, _ := strings.Cut(statement, " FROM ")
	if strings.Contains(projection, "`tidbgo_t0`.`id`") || strings.Contains(projection, "`tidbgo_t0`.`tenant_id`") {
		t.Fatal("internal page keys leaked into the requested projection")
	}
	statement, _, err = query.WithDeleted().Build()
	if err != nil || strings.Contains(statement, " IS NULL") {
		t.Fatalf("WithDeleted scope = %s, %v", statement, err)
	}
}

type rootPageUnprovenTarget struct {
	model.Meta `tidbgo:"table=root_page_unproven"`
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64
	TargetID   int64
	AddedAt    time.Time
	Target     *rootPageUnprovenChild `tidbgo:"has_one,join=TargetID:RootID"`
}

type rootPageUnprovenChild struct {
	ID     int64 `tidbgo:",pk"`
	RootID int64
}

func TestRootPageRequiresProvenInlineCardinality(t *testing.T) {
	query := Query[rootPageUnprovenTarget]().Where(Equal("OwnerID", int64(7))).OrderBy(Desc("AddedAt")).Limit(50).Preload("Target")
	compiled, err := query.compile()
	if err != nil || compiled.rootPage {
		t.Fatalf("unproven target key = %#v, %v", compiled, err)
	}
}

type rootPageUniqueRecord struct {
	ID         int64 `tidbgo:",pk"`
	OwnerID    int64 `tidbgo:",unique=owner_target"`
	TargetCode int64 `tidbgo:",unique=owner_target"`
	AddedAt    time.Time
	Label      string
	Target     *rootPageUniqueTarget `tidbgo:"belongs_to,join=TargetCode:Code"`
}

type rootPageUniqueTarget struct {
	ID     int64 `tidbgo:",pk"`
	Code   int64 `tidbgo:",unique=code"`
	LeafID int64
	Leaf   *rootPageTarget        `tidbgo:"belongs_to,join=LeafID:ID"`
	Loose  *rootPageUnprovenChild `tidbgo:"has_one,join=LeafID:RootID"`
}

func TestRootPageCandidateKeysAndNestedCardinality(t *testing.T) {
	for _, tc := range []struct {
		path  string
		point bool
		want  bool
	}{
		{path: "Target", want: true},
		{path: "Target.Leaf", want: true},
		{path: "Target.Loose", want: false},
		{path: "Target", point: true, want: false},
	} {
		t.Run(fmt.Sprintf("%s/point=%t", tc.path, tc.point), func(t *testing.T) {
			query := Query[rootPageUniqueRecord]().Where(Equal("OwnerID", int64(7))).
				OrderBy(Desc("AddedAt")).Limit(50).Preload(tc.path)
			if tc.point {
				query.Where(Equal("TargetCode", int64(3)))
			}
			compiled, err := query.compile()
			if err != nil || compiled.rootPage != tc.want {
				t.Fatalf("candidate/nested key proof = %#v, %v", compiled, err)
			}
		})
	}
}

type rootPageWithoutPrimary struct {
	RecordID int64
	OwnerID  int64
	TargetID int64
	AddedAt  time.Time
	Target   *rootPageTarget `tidbgo:"belongs_to"`
}

func TestRootPageRequiresRootPrimaryKey(t *testing.T) {
	query := Query[rootPageWithoutPrimary]().Where(Equal("OwnerID", int64(7))).
		OrderBy(Desc("AddedAt")).Limit(50).Preload("Target")
	compiled, err := query.compile()
	if err != nil || compiled.rootPage {
		t.Fatalf("root without primary key = %#v, %v", compiled, err)
	}
}

func TestRootPageAllHydratesOnceAndPreservesMissingTargets(t *testing.T) {
	stamp := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	state := &preloadTestState{record: true, responses: []*preloadTestResponse{{
		columns: []string{"id", "owner_id", "target_id", "added_at", "joined_id", "label", "payload", "deleted_at"},
		values: [][]driver.Value{
			{int64(5), int64(7), int64(9), stamp, int64(9), "target", "body", nil},
			{int64(4), int64(7), int64(99), stamp, nil, nil, nil, nil},
		},
	}}}
	var output bytes.Buffer
	capture := NewRuntimeCapture(&output)
	ctx := WithRuntimeCapture(context.Background(), capture)
	rows, err := rootPageQuery().All(ctx, openPreloadTestDB(t, state))
	if err != nil || len(rows) != 2 || rows[0].ID != 5 || rows[0].Target == nil || rows[0].Target.Label != "target" || rows[1].ID != 4 || rows[1].Target != nil {
		t.Fatalf("All() = %#v, %v", rows, err)
	}
	calls := preloadCalls(state)
	if len(calls) != 1 || !strings.Contains(calls[0].query, rootPageSourceAlias) {
		t.Fatalf("All issued unexpected statements: %#v", calls)
	}
	records := decodeRuntimeCaptureForTest(t, &output)
	if capture.Err() != nil || len(records) != 1 || records[0].Query == nil ||
		records[0].Query.Compiler.Rewrite != queryshape.CompilerRewriteRootPage ||
		records[0].Query.Limit.Value != 0 || records[0].RowsReturned != 2 {
		t.Fatalf("captured page decision = %#v, %v", records, capture.Err())
	}
}

func TestRootPageFingerprintAndPlanAccessTrackRewrite(t *testing.T) {
	query := rootPageQuery()
	shape := queryShapeForTest(t, query)
	if shape.Compiler.Rewrite != queryshape.CompilerRewriteRootPage || len(shape.IndexAccesses) != 1 || shape.IndexAccesses[0].Table != "root_page_records" {
		t.Fatalf("shape = %#v", shape)
	}
	if shape.Fingerprint() != queryShapeForTest(t, rootPageQuery().Limit(100)).Fingerprint() || shape.Fingerprint() == queryShapeForTest(t, rootPageQuery().Limit(101)).Fingerprint() {
		t.Fatal("fingerprint did not distinguish a compiler decision from bind values")
	}
	compiled, err := query.compile()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := model.Describe[rootPageRecord]()
	if err != nil {
		t.Fatal(err)
	}
	var resolver planAccessResolver
	if err := compilePlanAccessResolver(descriptor, &query.selection, compiled, &resolver); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{rootPageSourceAlias, inlinePreloadRootAlias} {
		access := resolver.resolve("table:" + alias)
		if access.physicalTable != "root_page_records" || access.model != "rootPageRecord" || access.relationPath != "" {
			t.Fatalf("root access %s = %#v", alias, access)
		}
	}
	if access := resolver.resolve("table:" + inlinePreloadAlias1); access.relationPath != "Target" || access.physicalTable != "root_page_targets" {
		t.Fatalf("preload access = %#v", access)
	}
}

func TestRootPageConcurrentBuildKeepsMetadataImmutable(t *testing.T) {
	query := rootPageQuery()
	want, args, err := query.Build()
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			got, gotArgs, err := query.Build()
			if err != nil || got != want || !reflect.DeepEqual(gotArgs, args) {
				t.Errorf("concurrent Build = %s, %#v, %v", got, gotArgs, err)
			}
		})
	}
	group.Wait()
}
