package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRawBuildReturnsUnchangedSQLWithoutExecutingValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	argument := mutationValue{calls: &calls, text: "12.30"}
	query := Raw[mutationModel]("WITH values AS (SELECT ? AS amount) SELECT amount FROM values", argument)
	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "WITH values AS (SELECT ? AS amount) SELECT amount FROM values"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if calls != 0 || len(arguments) != 1 || !reflect.DeepEqual(arguments[0], argument) {
		t.Fatalf("calls = %d, arguments = %#v", calls, arguments)
	}
}

func TestRawAllMapsReturnedColumnsIncludingComputedFields(t *testing.T) {
	state := &allTestState{
		columns: []string{"id", "name", "count"},
		values: [][]driver.Value{
			{int64(1), "Ada", int64(3)},
			{int64(2), "Grace", int64(5)},
		},
	}
	database := openAllTestDB(t, state)

	values, err := Raw[mutationModel]("SELECT id, name, COUNT(*) AS count FROM mutation_models GROUP BY id, name HAVING COUNT(*) > ?", 1).All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	want := []mutationModel{{ID: 1, Name: "Ada", Count: 3}, {ID: 2, Name: "Grace", Count: 5}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
	if state.query != "SELECT id, name, COUNT(*) AS count FROM mutation_models GROUP BY id, name HAVING COUNT(*) > ?" {
		t.Fatalf("query = %q", state.query)
	}
	if len(state.arguments) != 1 || state.arguments[0].Value != int64(1) {
		t.Fatalf("arguments = %#v", state.arguments)
	}
}

func TestRawSingleRowTerminalsDoNotRewriteSQL(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*sql.DB) (mutationModel, error)
	}{
		{name: "first", run: func(database *sql.DB) (mutationModel, error) {
			return Raw[mutationModel]("SELECT id FROM mutation_models").First(context.Background(), database)
		}},
		{name: "only", run: func(database *sql.DB) (mutationModel, error) {
			return Raw[mutationModel]("SELECT id FROM mutation_models").Only(context.Background(), database)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &allTestState{columns: []string{"id"}, values: [][]driver.Value{{int64(7)}}}
			database := openAllTestDB(t, state)
			value, err := test.run(database)
			if err != nil {
				t.Fatalf("terminal error = %v", err)
			}
			if value.ID != 7 || state.query != "SELECT id FROM mutation_models" {
				t.Fatalf("value = %#v, query = %q", value, state.query)
			}
		})
	}
}

func TestRawOnlyReportsMultipleRows(t *testing.T) {
	state := &allTestState{
		columns: []string{"id"},
		values:  [][]driver.Value{{int64(1)}, {int64(2)}},
	}
	database := openAllTestDB(t, state)
	_, err := Raw[mutationModel]("SELECT id FROM mutation_models").Only(context.Background(), database)
	if !errors.Is(err, ErrMultipleRows) {
		t.Fatalf("Only() error = %v, want ErrMultipleRows", err)
	}
}

func TestRawRejectsInvalidSQLModelsAndColumns(t *testing.T) {
	t.Parallel()

	if _, _, err := Raw[mutationModel](" ").Build(); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty SQL error = %v", err)
	}
	if _, _, err := Raw[*mutationModel]("SELECT id").Build(); err == nil || !strings.Contains(err.Error(), "non-pointer struct") {
		t.Fatalf("pointer model error = %v", err)
	}

	tests := []struct {
		name    string
		columns []string
		want    string
	}{
		{name: "unknown", columns: []string{"missing"}, want: "not mapped"},
		{name: "duplicate", columns: []string{"id", "id"}, want: "repeats result column"},
		{name: "none", columns: []string{}, want: "returned no columns"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			descriptor, err := mutationDescriptor[mutationModel]("test")
			if err != nil {
				t.Fatalf("mutationDescriptor() error = %v", err)
			}
			if _, err := compileRawScanPlan(descriptor, test.columns); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("compileRawScanPlan() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRawExecExecutesExplicitSQL(t *testing.T) {
	t.Parallel()

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 3}}
	affected, err := RawExec(context.Background(), executor, "DELETE FROM logs WHERE requested_at < ?", "2026-01-01")
	if err != nil {
		t.Fatalf("RawExec() error = %v", err)
	}
	if affected != 3 || executor.query != "DELETE FROM logs WHERE requested_at < ?" {
		t.Fatalf("affected = %d, executor = %#v", affected, executor)
	}
	if got, want := executor.arguments, []any{"2026-01-01"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}
