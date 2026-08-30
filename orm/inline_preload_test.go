package orm

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type inlineCompositeParent struct {
	model.Meta `tidbgo:"table=inline_composite_parents"`
	TenantID   uint64
	TargetID   uint64
	Target     *inlineCompositeTarget `tidbgo:"belongs_to,join=TenantID:TenantID,join=TargetID:ID"`
}

type inlineCompositeTarget struct {
	model.Meta `tidbgo:"table=inline_composite_targets"`
	TenantID   uint64 `tidbgo:",pk"`
	ID         uint64 `tidbgo:",pk"`
	Value      string
}

type inlineScannerParent struct {
	model.Meta `tidbgo:"table=inline_scanner_parents"`
	ID         uint64 `tidbgo:",pk"`
	TargetID   uint64
	Target     *inlineScannerTarget `tidbgo:"belongs_to,join=TargetID:ID"`
}

type inlineScannerTarget struct {
	model.Meta `tidbgo:"table=inline_scanner_targets"`
	ID         uint64 `tidbgo:",pk"`
	Amount     scanDecimal
	Label      *string
}

func TestSelectQueryInlinePreloadRejectsPartiallyNullCompositeKey(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{{
			columns: []string{"tenant_id", "target_id", "joined_tenant_id", "joined_id", "joined_value"},
			values:  [][]driver.Value{{int64(1), int64(2), nil, int64(2), "value"}},
		}},
	}
	database := openPreloadTestDB(t, state)

	values, err := Query[inlineCompositeParent]().Preload("Target").All(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), "partially NULL relation key") {
		t.Fatalf("All() values = %#v, error = %v, want partially NULL relation key error", values, err)
	}
	if values != nil {
		t.Fatalf("All() values = %#v, want nil on scan failure", values)
	}
}

func TestSelectQueryInlinePreloadUsesScannerAndNullablePointer(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{{
			columns: []string{"id", "target_id", "joined_id", "joined_amount", "joined_label"},
			values:  [][]driver.Value{{int64(1), int64(2), int64(2), "12.30", []byte("target")}},
		}},
	}
	database := openPreloadTestDB(t, state)

	values, err := Query[inlineScannerParent]().Preload("Target").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(values) != 1 || values[0].Target == nil || values[0].Target.Amount.text != "12.30" || values[0].Target.Label == nil || *values[0].Target.Label != "target" {
		t.Fatalf("values = %#v, want scanner and pointer fields hydrated", values)
	}
}

func TestScanInlineValueSupportsNativeDriverRepresentations(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, time.August, 30, 12, 34, 56, 789, time.UTC)
	var (
		boolean  bool
		integer  int32
		unsigned uint64
		decimal  float32
		text     string
		bytes    []byte
		when     time.Time
	)
	tests := []struct {
		name   string
		target any
		source any
	}{
		{name: "bool", target: &boolean, source: []byte("1")},
		{name: "int", target: &integer, source: []byte("-2")},
		{name: "uint", target: &unsigned, source: int64(3)},
		{name: "float", target: &decimal, source: []byte("4.5")},
		{name: "string", target: &text, source: []byte("value")},
		{name: "bytes", target: &bytes, source: []byte("payload")},
		{name: "time", target: &when, source: timestamp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := scanInlineValue(reflect.ValueOf(tt.target).Elem(), tt.source); err != nil {
				t.Fatalf("scanInlineValue() error = %v", err)
			}
		})
	}
	if !boolean || integer != -2 || unsigned != 3 || decimal != 4.5 || text != "value" || string(bytes) != "payload" || when != timestamp {
		t.Fatalf("decoded values = %t, %d, %d, %f, %q, %q, %v", boolean, integer, unsigned, decimal, text, bytes, when)
	}
	if err := scanInlineValue(reflect.ValueOf(&integer).Elem(), nil); err == nil || !strings.Contains(err.Error(), "converting NULL") {
		t.Fatalf("scanInlineValue(NULL int) error = %v", err)
	}
}
