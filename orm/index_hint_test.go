package orm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
)

func TestForceIndexOrderedPages(t *testing.T) {
	for _, offset := range []int64{0, 10, 5400, 10800, 10850} {
		for _, index := range []string{"", "owner_order"} {
			t.Run(fmt.Sprintf("%d/%s", offset, index), func(t *testing.T) {
				q := indexHintListQuery().Offset(offset)
				if index != "" {
					q.ForceIndex(index)
				}
				sqlText, args, err := q.Build()
				if err != nil {
					t.Fatal(err)
				}
				want := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`owner_id`, `tidbgo_t0`.`target_id`, `tidbgo_t0`.`added_at`, `tidbgo_t1`.`id`, `tidbgo_t1`.`external_id`, `tidbgo_t1`.`title`, `tidbgo_t1`.`thumbnail`, `tidbgo_t1`.`short` FROM `index_hint_bookmarks` AS `tidbgo_t0`"
				if index != "" {
					want += " FORCE INDEX (`owner_order`)"
				}
				want += " LEFT JOIN `index_hint_targets` AS `tidbgo_t1` ON (`tidbgo_t0`.`target_id` = `tidbgo_t1`.`id`) WHERE `tidbgo_t0`.`owner_id` = ? ORDER BY `tidbgo_t0`.`added_at` DESC, `tidbgo_t0`.`id` DESC LIMIT ? OFFSET ?"
				if sqlText != want || !reflect.DeepEqual(args, []any{int64(7), int64(50), offset}) {
					t.Fatalf("Build() = %s, %#v", sqlText, args)
				}
				shape := queryShapeForTest(t, q)
				if shape.Compiler.Rewrite != queryshape.CompilerRewriteNone || shape.ForceIndex != index || shape.IndexAccesses[0].ForceIndex != index {
					t.Fatalf("shape = %#v", shape)
				}
			})
		}
	}
}

func TestForceIndexScalarScopesAndCursor(t *testing.T) {
	q := Query[indexHintTarget]().ForceIndex("first").ForceIndex("PRIMARY")
	sqlText, _, err := q.Build()
	if err != nil || !strings.HasSuffix(sqlText, "FROM `index_hint_targets` FORCE INDEX (`PRIMARY`) WHERE `deleted_at` IS NULL") {
		t.Fatalf("Build() = %s, %v", sqlText, err)
	}
	sqlText, _, err = q.WithDeleted().Build()
	if err != nil || !strings.HasSuffix(sqlText, "FROM `index_hint_targets` FORCE INDEX (`PRIMARY`)") {
		t.Fatalf("WithDeleted Build() = %s, %v", sqlText, err)
	}
	q2 := indexHintListQuery().ForceIndex("owner_order").SeekAfter(time.Unix(1000, 0), int64(8))
	sqlText, args, err := q2.Build()
	if err != nil || !strings.Contains(sqlText, "FORCE INDEX (`owner_order`) LEFT JOIN") || len(args) != 5 {
		t.Fatalf("SeekAfter Build() = %s, %#v, %v", sqlText, args, err)
	}
	q3 := Query[scanModel]().ForceIndex("name_idx")
	if _, _, err := q3.Build(); err != nil {
		t.Fatal(err)
	}
	plain, _, err := Query[scanModel]().Build()
	if err != nil || strings.Contains(plain, "FORCE INDEX") {
		t.Fatalf("cached unhinted Build() = %s, %v", plain, err)
	}
}

func TestForceIndexRejectsInvalidNamesBeforeExecution(t *testing.T) {
	for _, name := range []string{"", "has space", "idx`)", "idx,PRIMARY", "db.idx", "idx;SELECT", "日本語", "1idx", strings.Repeat("a", 65)} {
		t.Run(name, func(t *testing.T) {
			q := Query[scanModel]().ForceIndex(name)
			state := &allTestState{}
			db := openAllTestDB(t, state)
			_, _, buildErr := q.Build()
			_, countErr := q.Count(context.Background(), db)
			_, existsErr := q.Exists(context.Background(), db)
			_, allErr := q.All(context.Background(), db)
			for _, err := range []error{buildErr, countErr, existsErr, allErr} {
				if err == nil || !strings.Contains(err.Error(), "ForceIndex") {
					t.Fatalf("invalid name error = %v", err)
				}
			}
			if state.query != "" {
				t.Fatalf("invalid name executed SQL: %s", state.query)
			}
		})
	}
	if _, _, err := Query[scanModel]().ForceIndex(strings.Repeat("a", 64)).Build(); err != nil {
		t.Fatal(err)
	}
	var nilQuery *SelectQuery[scanModel]
	if nilQuery.ForceIndex("PRIMARY") != nil {
		t.Fatal("nil receiver must remain nil")
	}
}

func TestForceIndexTerminalsAndCapture(t *testing.T) {
	for _, terminal := range []string{"all", "first", "only", "count", "exists", "explain", "explain_analyze"} {
		t.Run(terminal, func(t *testing.T) {
			state := &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}}
			if terminal == "explain" {
				state = explainTestState()
			} else if terminal == "explain_analyze" {
				state = explainAnalyzeTestState()
			}
			db := openAllTestDB(t, state)
			q := Query[scanModel]().Select("ID").ForceIndex("PRIMARY").Limit(50).Offset(10)
			var output bytes.Buffer
			ctx := WithRuntimeCapture(context.Background(), NewRuntimeCapture(&output))
			var err error
			switch terminal {
			case "all":
				_, err = q.All(ctx, db)
			case "first":
				_, err = q.First(ctx, db)
			case "only":
				_, err = q.Only(ctx, db)
			case "count":
				_, err = q.Count(ctx, db)
			case "exists":
				_, err = q.Exists(ctx, db)
			case "explain":
				_, err = q.Explain(ctx, db)
			case "explain_analyze":
				_, err = q.ExplainAnalyze(ctx, db)
			}
			if err != nil || !strings.Contains(state.query, "FROM `scan_model` FORCE INDEX (`PRIMARY`)") {
				t.Fatalf("SQL = %s, error = %v", state.query, err)
			}
			if q.selection.pagination.limit != 50 || q.selection.pagination.offset != 10 {
				t.Fatal("terminal mutated pagination")
			}
			if terminal == "all" || terminal == "first" || terminal == "only" {
				records := decodeRuntimeCaptureForTest(t, &output)
				if len(records) != 1 || records[0].Query.ForceIndex != "PRIMARY" {
					t.Fatalf("capture = %#v", records)
				}
			}
		})
	}
}

func TestForceIndexPreservesRootForRelationRewrites(t *testing.T) {
	q := relationTopNBenchmarkQuery().ForceIndex("PRIMARY")
	sqlText, _, err := q.Build()
	if err != nil || strings.Contains(sqlText, "FROM (SELECT") || strings.Count(sqlText, "FORCE INDEX") != 1 || !strings.Contains(sqlText, "WHERE EXISTS") {
		t.Fatalf("relation SELECT = %s, %v", sqlText, err)
	}
	count, err := Query[relationTopNVideo]().Where(Has("VideoGenres", Equal("GenreID", int64(7)))).ForceIndex("PRIMARY").compileCount()
	if err != nil || !strings.Contains(count.sql, "FROM `relation_topn_videos` AS `tidbgo_r0` FORCE INDEX (`PRIMARY`) WHERE EXISTS") {
		t.Fatalf("relation Count = %s, %v", count.sql, err)
	}
	compiled, err := Query[preloadUser]().ForceIndex("PRIMARY").Preload("Roles").compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.preloads[0].targetStatement.sql, "FORCE INDEX") {
		t.Fatal("root hint propagated to a collection")
	}
}

func TestForceIndexDiagnosticsAndFingerprint(t *testing.T) {
	catalog := parseQueryIndexCatalog(t, queryIndexSchema("KEY owner_order (tenant_id, id), KEY wrong_index (title)"))
	seen := make(map[string]bool)
	for _, tc := range []struct{ name, code string }{{"", ""}, {"owner_order", ""}, {"wrong_index", "QRY007"}, {"absent", "QRY006"}} {
		q := Query[queryIndexModel]().Where(Equal("TenantID", int64(7))).OrderBy(Desc("ID")).Limit(50)
		if tc.name != "" {
			q.ForceIndex(tc.name)
		}
		diagnostics := queryIndexDiagnosticsForTest(t, q, catalog)
		if tc.code == "" && len(diagnostics) != 0 || tc.code != "" && (len(diagnostics) != 1 || diagnostics[0].Code != tc.code || !strings.Contains(diagnostics[0].Message, tc.name)) {
			t.Fatalf("%s diagnostics = %#v", tc.name, diagnostics)
		}
		fingerprint := queryShapeForTest(t, q).Fingerprint()
		if seen[fingerprint] {
			t.Fatal("index choice must distinguish fingerprints")
		}
		seen[fingerprint] = true
		q.Limit(10)
		if queryShapeForTest(t, q).Fingerprint() != fingerprint {
			t.Fatal("page size must not change the fingerprint")
		}
	}
	q := Query[queryIndexModel]().ForceIndex("OWNER_ORDER").Where(Equal("TenantID", int64(7))).OrderBy(Desc("ID")).Limit(10)
	if diagnostics := queryIndexDiagnosticsForTest(t, q, catalog); len(diagnostics) != 0 {
		t.Fatalf("case-insensitive index name = %#v", diagnostics)
	}
}
