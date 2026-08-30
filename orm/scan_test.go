package orm

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	_ rowScanner = (*sql.Row)(nil)
	_ rowScanner = (*sql.Rows)(nil)
)

type scanDecimal struct {
	text string
}

func (value *scanDecimal) Scan(source any) error {
	text, ok := source.(string)
	if !ok {
		return errors.New("scanDecimal requires a string")
	}
	value.text = text
	return nil
}

// ScanAuditFields is test-only embedded metadata for row decoding.
type ScanAuditFields struct {
	CreatedAt time.Time
}

type scanModel struct {
	ID       uint64 `tidbgo:",pk"`
	Name     string
	Nickname *string
	Amount   scanDecimal
	*ScanAuditFields
	Ignored string `tidbgo:"-"`
}

type scanValuesRow struct {
	scan func([]any) error
}

func (row scanValuesRow) Scan(destinations ...any) error {
	return row.scan(destinations)
}

func TestRowDecoderScansMappedFieldsInDescriptorOrder(t *testing.T) {
	t.Parallel()

	plan, err := scanPlanFor(reflect.TypeFor[scanModel]())
	if err != nil {
		t.Fatalf("scanPlanFor() error = %v", err)
	}
	if got, want := plan.columns, []string{"id", "name", "nickname", "amount", "created_at"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %#v, want %#v", got, want)
	}

	createdAt := time.Date(2026, time.August, 29, 12, 34, 56, 0, time.UTC)
	row := scanValuesRow{scan: func(destinations []any) error {
		if got, want := len(destinations), 5; got != want {
			t.Fatalf("destination count = %d, want %d", got, want)
		}
		*destinations[0].(*uint64) = 7
		*destinations[1].(*string) = "Ada"
		nickname := "A"
		*destinations[2].(**string) = &nickname
		if err := destinations[3].(sql.Scanner).Scan("12.30"); err != nil {
			t.Fatalf("Amount.Scan() error = %v", err)
		}
		*destinations[4].(*time.Time) = createdAt
		return nil
	}}

	var target scanModel
	if err := plan.newDecoder().scan(row, &target); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if target.ID != 7 || target.Name != "Ada" || target.Nickname == nil || *target.Nickname != "A" {
		t.Fatalf("target scalar values = %#v", target)
	}
	if target.Amount.text != "12.30" {
		t.Fatalf("target.Amount = %#v", target.Amount)
	}
	if target.ScanAuditFields == nil || target.CreatedAt != createdAt {
		t.Fatalf("target.ScanAuditFields = %#v", target.ScanAuditFields)
	}
	if target.Ignored != "" {
		t.Fatalf("target.Ignored = %q", target.Ignored)
	}
}

func TestScanPlanIsCachedByNonPointerModelType(t *testing.T) {
	t.Parallel()

	valuePlan, err := scanPlanFor(reflect.TypeFor[scanModel]())
	if err != nil {
		t.Fatalf("scanPlanFor(value) error = %v", err)
	}
	pointerPlan, err := scanPlanFor(reflect.TypeFor[*scanModel]())
	if err != nil {
		t.Fatalf("scanPlanFor(pointer) error = %v", err)
	}
	if valuePlan != pointerPlan {
		t.Fatal("value and pointer model types returned different scan plans")
	}
}

type writeOnlyValue struct {
	text string
}

func (value writeOnlyValue) Value() (driver.Value, error) {
	return value.text, nil
}

type writeOnlyModel struct {
	Value writeOnlyValue
}

func TestScanPlanRejectsWriteOnlyCustomFields(t *testing.T) {
	t.Parallel()

	_, err := scanPlanFor(reflect.TypeFor[writeOnlyModel]())
	if err == nil || !strings.Contains(err.Error(), "writeOnlyModel.Value cannot be read") {
		t.Fatalf("scanPlanFor() error = %v", err)
	}
}

func TestRowDecoderRejectsInvalidInputsAndWrapsScanErrors(t *testing.T) {
	t.Parallel()

	plan, err := scanPlanFor(reflect.TypeFor[scanModel]())
	if err != nil {
		t.Fatalf("scanPlanFor() error = %v", err)
	}
	decoder := plan.newDecoder()
	validRow := scanValuesRow{scan: func([]any) error { return nil }}

	for _, target := range []any{nil, scanModel{}, (*scanModel)(nil), &struct{}{}, new(*scanModel)} {
		if err := decoder.scan(validRow, target); err == nil || !strings.Contains(err.Error(), "non-nil *orm.scanModel") {
			t.Fatalf("Scan(%T) error = %v", target, err)
		}
	}
	if err := decoder.scan(nil, &scanModel{}); err == nil || !strings.Contains(err.Error(), "nil source") {
		t.Fatalf("Scan(nil row) error = %v", err)
	}
	if err := (*rowDecoder)(nil).scan(validRow, &scanModel{}); err == nil || !strings.Contains(err.Error(), "uninitialized decoder") {
		t.Fatalf("nil decoder Scan() error = %v", err)
	}

	scanFailure := errors.New("row failure")
	failingRow := scanValuesRow{scan: func([]any) error { return scanFailure }}
	if err := decoder.scan(failingRow, &scanModel{}); !errors.Is(err, scanFailure) {
		t.Fatalf("Scan() error = %v, want wrapped row failure", err)
	}
	for index, destination := range decoder.destinations {
		if destination != nil {
			t.Fatalf("destination %d retained target = %#v", index, destination)
		}
	}
}
