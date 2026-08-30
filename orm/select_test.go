package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type reservedSelectModel struct {
	model.Meta `tidbgo:"table=order"`
	ID         uint64 `tidbgo:",pk"`
	Value      string `tidbgo:"select"`
}

func TestCompileSelectUsesQuotedDefaultProjection(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{modelType: reflect.TypeFor[reservedSelectModel]()})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id`, `select` FROM `order`"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if got, want := statement.statement.scanPlan.columns, []string{"id", "select"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scan columns = %#v, want %#v", got, want)
	}
	if statement.arguments != nil {
		t.Fatalf("arguments = %#v, want nil", statement.arguments)
	}
}

func TestCompileSelectCachesDefaultStatementByNonPointerType(t *testing.T) {
	t.Parallel()

	valueDescriptor, err := model.DescribeType(reflect.TypeFor[reservedSelectModel]())
	if err != nil {
		t.Fatalf("DescribeType(value) error = %v", err)
	}
	pointerDescriptor, err := model.DescribeType(reflect.TypeFor[*reservedSelectModel]())
	if err != nil {
		t.Fatalf("DescribeType(pointer) error = %v", err)
	}
	valueStatement, err := compileDefaultSelect(valueDescriptor)
	if err != nil {
		t.Fatalf("compileDefaultSelect(value) error = %v", err)
	}
	pointerStatement, err := compileDefaultSelect(pointerDescriptor)
	if err != nil {
		t.Fatalf("compileDefaultSelect(pointer) error = %v", err)
	}
	if valueStatement != pointerStatement {
		t.Fatal("value and pointer model types returned different default SELECT statements")
	}
}

func TestCompileSelectPreservesExplicitProjectionAndScanOrder(t *testing.T) {
	t.Parallel()

	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[scanModel](),
		projection: []string{"Name", "ID"},
	})
	if err != nil {
		t.Fatalf("compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `name`, `id` FROM `scan_model`"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if got, want := statement.statement.scanPlan.columns, []string{"name", "id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scan columns = %#v, want %#v", got, want)
	}

	row := scanValuesRow{scan: func(destinations []any) error {
		if got, want := len(destinations), 2; got != want {
			t.Fatalf("destination count = %d, want %d", got, want)
		}
		*destinations[0].(*string) = "Grace"
		*destinations[1].(*uint64) = 9
		return nil
	}}
	nickname := "unchanged"
	target := scanModel{Nickname: &nickname, Amount: scanDecimal{text: "unchanged"}}
	if err := statement.statement.scanPlan.newDecoder().scan(row, &target); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if target.ID != 9 || target.Name != "Grace" {
		t.Fatalf("selected target values = %#v", target)
	}
	if target.Nickname != &nickname || target.Amount.text != "unchanged" || target.ScanAuditFields != nil {
		t.Fatalf("unselected target values changed = %#v", target)
	}
}

type mixedReadModel struct {
	ID    uint64
	Value writeOnlyValue
}

func TestCompileSelectChecksOnlySelectedFieldReadCapabilities(t *testing.T) {
	t.Parallel()

	if _, err := compileSelect(&selectQuery{modelType: reflect.TypeFor[mixedReadModel]()}); err == nil || !strings.Contains(err.Error(), "mixedReadModel.Value cannot be read") {
		t.Fatalf("default compileSelect() error = %v", err)
	}
	statement, err := compileSelect(&selectQuery{
		modelType:  reflect.TypeFor[mixedReadModel](),
		projection: []string{"ID"},
	})
	if err != nil {
		t.Fatalf("projected compileSelect() error = %v", err)
	}
	if got, want := statement.statement.sql, "SELECT `id` FROM `mixed_read_model`"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

func TestCompileSelectRejectsInvalidProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		projection []string
		want       string
	}{
		{name: "empty", projection: []string{}, want: "at least one"},
		{name: "duplicate", projection: []string{"ID", "ID"}, want: "repeats field"},
		{name: "unknown", projection: []string{"Missing"}, want: "not a mapped scalar field"},
		{name: "ignored", projection: []string{"Ignored"}, want: "not a mapped scalar field"},
		{name: "embedded container", projection: []string{"ScanAuditFields"}, want: "not a mapped scalar field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileSelect(&selectQuery{
				modelType:  reflect.TypeFor[scanModel](),
				projection: tt.projection,
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("compileSelect() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
