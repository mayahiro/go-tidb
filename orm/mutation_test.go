package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type mutationValue struct {
	calls *int
	text  string
}

func (*mutationValue) Scan(any) error {
	return nil
}

func (value mutationValue) Value() (driver.Value, error) {
	if value.calls != nil {
		(*value.calls)++
	}
	return value.text, nil
}

type mutationModel struct {
	model.Meta `tidbgo:"table=mutation_models"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Name       string
	Nickname   *string
	Amount     mutationValue
	Count      int `tidbgo:"count,computed"`
}

type compositeMutationModel struct {
	model.Meta `tidbgo:"table=memberships"`
	TenantID   int64 `tidbgo:",pk"`
	UserID     int64 `tidbgo:",pk"`
	Role       string
}

type mutationWithoutPrimaryKey struct {
	Name string
}

type mutationOnlyAutoRandom struct {
	model.Meta `tidbgo:"table=mutation_only_ids"`
	ID         int64 `tidbgo:",pk,auto_random"`
}

type bulkMutationModel struct {
	model.Meta `tidbgo:"table=bulk_mutation_models"`
	ID         int64 `tidbgo:",pk"`
	Value      int64
}

type recordingExecExecutor struct {
	query     string
	arguments []any
	result    sql.Result
	err       error
	calls     int
}

type bulkExecution struct {
	query         string
	argumentCount int
	firstArgument any
	lastArgument  any
}

type bulkRecordingExecExecutor struct {
	executions []bulkExecution
	failCall   int
	err        error
}

func (executor *bulkRecordingExecExecutor) ExecContext(_ context.Context, query string, arguments ...any) (sql.Result, error) {
	execution := bulkExecution{query: query, argumentCount: len(arguments)}
	if len(arguments) != 0 {
		execution.firstArgument = arguments[0]
		execution.lastArgument = arguments[len(arguments)-1]
	}
	executor.executions = append(executor.executions, execution)
	if executor.failCall == len(executor.executions) {
		return nil, executor.err
	}
	return mutationResult{rowsAffected: int64(len(arguments) / 2)}, nil
}

func (executor *recordingExecExecutor) ExecContext(_ context.Context, query string, arguments ...any) (sql.Result, error) {
	executor.calls++
	executor.query = query
	executor.arguments = append([]any(nil), arguments...)
	return executor.result, executor.err
}

type mutationResult struct {
	lastInsertID int64
	rowsAffected int64
	lastIDErr    error
	rowsErr      error
}

func (result mutationResult) LastInsertId() (int64, error) {
	return result.lastInsertID, result.lastIDErr
}

func (result mutationResult) RowsAffected() (int64, error) {
	return result.rowsAffected, result.rowsErr
}

func TestInsertBuildsOfflineWithoutExecutingValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	nickname := "Ada"
	value := mutationModel{
		ID:       99,
		Name:     "Ada",
		Nickname: &nickname,
		Amount:   mutationValue{calls: &calls, text: "12.30"},
		Count:    7,
	}
	sqlText, arguments, err := Insert(&value).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?)"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if len(arguments) != 3 || arguments[0] != "Ada" || arguments[1] != &nickname || !reflect.DeepEqual(arguments[2], value.Amount) {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestInsertExecutesAndPopulatesAutoRandomPrimaryKey(t *testing.T) {
	t.Parallel()

	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
	executor := &recordingExecExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	affected, err := Insert(&value).Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 1 || value.ID != 8143 {
		t.Fatalf("affected = %d, ID = %d", affected, value.ID)
	}
	if executor.calls != 1 || executor.query != "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?)" {
		t.Fatalf("executor = %#v", executor)
	}
}

func TestInsertManyBuildsOneStatementAndLeavesGeneratedIDsUnset(t *testing.T) {
	t.Parallel()

	values := []mutationModel{
		{Name: "Ada", Amount: mutationValue{text: "1.00"}},
		{Name: "Grace", Amount: mutationValue{text: "2.00"}},
	}
	sqlText, arguments, err := InsertMany(values).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?), (?, ?, ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if len(arguments) != 6 || arguments[0] != "Ada" || arguments[3] != "Grace" {
		t.Fatalf("arguments = %#v", arguments)
	}
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
	affected, err := InsertMany(values).Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || values[0].ID != 0 || values[1].ID != 0 {
		t.Fatalf("affected = %d, values = %#v", affected, values)
	}
}

func TestInsertManyAcceptsPointerSlice(t *testing.T) {
	t.Parallel()

	ada := &mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
	grace := &mutationModel{Name: "Grace", Amount: mutationValue{text: "2.00"}}
	values := []*mutationModel{ada, grace}
	query := InsertMany(values)
	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?), (?, ?, ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if len(arguments) != 6 || arguments[0] != "Ada" || arguments[3] != "Grace" {
		t.Fatalf("arguments = %#v", arguments)
	}

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
	affected, err := query.Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || ada.ID != 0 || grace.ID != 0 {
		t.Fatalf("affected = %d, values = %#v", affected, values)
	}
}

func TestInsertManyExecAutomaticallySplitsAtTiDBPlaceholderLimit(t *testing.T) {
	rowCount := maxMutationParameters/2 + 1
	values := make([]bulkMutationModel, rowCount)
	for index := range values {
		values[index] = bulkMutationModel{ID: int64(index + 1), Value: int64(index + 101)}
	}
	query := InsertMany(values)
	if _, _, err := query.Build(); err == nil || !strings.Contains(err.Error(), "requires 2 statements") || !strings.Contains(err.Error(), "use Exec") {
		t.Fatalf("Build() error = %v, want automatic-batching guidance", err)
	}

	executor := &bulkRecordingExecExecutor{}
	affected, err := query.Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != int64(rowCount) {
		t.Fatalf("affected = %d, want %d", affected, rowCount)
	}
	if len(executor.executions) != 2 {
		t.Fatalf("execution count = %d, want 2", len(executor.executions))
	}
	first, second := executor.executions[0], executor.executions[1]
	if first.argumentCount != maxMutationParameters-1 || strings.Count(first.query, "?") != maxMutationParameters-1 {
		t.Fatalf("first batch arguments = %d, placeholders = %d", first.argumentCount, strings.Count(first.query, "?"))
	}
	if second.argumentCount != 2 || strings.Count(second.query, "?") != 2 {
		t.Fatalf("second batch arguments = %d, placeholders = %d", second.argumentCount, strings.Count(second.query, "?"))
	}
	if first.firstArgument != int64(1) || first.lastArgument != int64(maxMutationParameters/2+100) {
		t.Fatalf("first batch boundary arguments = %#v, %#v", first.firstArgument, first.lastArgument)
	}
	if second.firstArgument != int64(rowCount) || second.lastArgument != int64(rowCount+100) {
		t.Fatalf("second batch boundary arguments = %#v, %#v", second.firstArgument, second.lastArgument)
	}
}

func TestInsertManyExecBoundsGeneratedOnlyRowsWithoutPlaceholders(t *testing.T) {
	values := make([]mutationOnlyAutoRandom, maxMutationParameters+1)
	query := InsertMany(values)
	if _, _, err := query.Build(); err == nil || !strings.Contains(err.Error(), "requires 2 statements") {
		t.Fatalf("Build() error = %v, want multi-statement error", err)
	}
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	var affected int64
	report, err := Debug(context.Background(), func(ctx context.Context) error {
		var execErr error
		affected, execErr = query.Exec(ctx, executor)
		return execErr
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || executor.calls != 2 {
		t.Fatalf("affected = %d, execution count = %d, want 2 statements", affected, executor.calls)
	}
	if len(report.Statements) != 2 {
		t.Fatalf("Debug() statements = %#v, want 2", report.Statements)
	}
	for index, event := range report.Statements {
		if event.Operation != StatementInsert || event.ArgumentCount != 0 || !event.RowsAffectedKnown || event.RowsAffected != 1 || event.Error != nil {
			t.Fatalf("Debug() statement %d = %#v", index, event)
		}
	}
}

func TestInsertManyRejectsNilPointerElement(t *testing.T) {
	t.Parallel()

	value := &mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
	_, _, err := InsertMany([]*mutationModel{value, nil}).Build()
	if err == nil || !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Build() error = %v, want row-indexed nil error", err)
	}
}

func TestInsertManyEmptySliceIsNoOp(t *testing.T) {
	t.Parallel()

	query := InsertMany([]mutationModel(nil))
	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if sqlText != "" || arguments != nil {
		t.Fatalf("Build() = %q %#v, want empty", sqlText, arguments)
	}
	executor := &recordingExecExecutor{}
	affected, err := query.Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 0 || executor.calls != 0 {
		t.Fatalf("affected = %d, calls = %d", affected, executor.calls)
	}

	pointerQuery := InsertMany([]*mutationModel(nil))
	sqlText, arguments, err = pointerQuery.Build()
	if err != nil {
		t.Fatalf("pointer Build() error = %v", err)
	}
	if sqlText != "" || arguments != nil {
		t.Fatalf("pointer Build() = %q %#v, want empty", sqlText, arguments)
	}

	upsertQuery := UpsertMany([]bulkMutationModel(nil), "Value")
	sqlText, arguments, err = upsertQuery.Build()
	if err != nil {
		t.Fatalf("empty UpsertMany().Build() error = %v", err)
	}
	if sqlText != "" || arguments != nil {
		t.Fatalf("empty UpsertMany().Build() = %q %#v, want empty", sqlText, arguments)
	}
	affected, err = upsertQuery.Exec(context.Background(), executor)
	if err != nil || affected != 0 || executor.calls != 0 {
		t.Fatalf("empty UpsertMany().Exec() affected = %d, calls = %d, error = %v", affected, executor.calls, err)
	}
}

func TestInsertSupportsModelWithOnlyAutoRandomField(t *testing.T) {
	t.Parallel()

	value := mutationOnlyAutoRandom{}
	sqlText, arguments, err := Insert(&value).Build()
	if err != nil {
		t.Fatalf("Insert().Build() error = %v", err)
	}
	if sqlText != "INSERT INTO `mutation_only_ids` () VALUES ()" || len(arguments) != 0 {
		t.Fatalf("Insert().Build() = %q, %#v", sqlText, arguments)
	}

	first := &mutationOnlyAutoRandom{}
	second := &mutationOnlyAutoRandom{}
	sqlText, arguments, err = InsertMany([]*mutationOnlyAutoRandom{first, second}).Build()
	if err != nil {
		t.Fatalf("InsertMany().Build() error = %v", err)
	}
	if sqlText != "INSERT INTO `mutation_only_ids` () VALUES (), ()" || len(arguments) != 0 {
		t.Fatalf("InsertMany().Build() = %q, %#v", sqlText, arguments)
	}

	if _, _, err := Update(&value).Build(); err == nil || !strings.Contains(err.Error(), "no writable") {
		t.Fatalf("Update().Build() error = %v, want no writable fields", err)
	}
}

func TestUpsertBuildsDefaultAndSelectedUpdateFields(t *testing.T) {
	t.Parallel()

	calls := 0
	nickname := "Ada"
	value := mutationModel{
		ID:       99,
		Name:     "Ada",
		Nickname: &nickname,
		Amount:   mutationValue{calls: &calls, text: "12.30"},
		Count:    7,
	}
	sqlText, arguments, err := Upsert(&value).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `nickname` = VALUES(`nickname`), `amount` = VALUES(`amount`)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if calls != 0 || len(arguments) != 3 || arguments[0] != "Ada" || arguments[1] != &nickname || !reflect.DeepEqual(arguments[2], value.Amount) {
		t.Fatalf("Value() calls = %d, arguments = %#v", calls, arguments)
	}

	sqlText, arguments, err = Upsert(&value, "Name").Build()
	if err != nil {
		t.Fatalf("selected Build() error = %v", err)
	}
	wantSQL = "INSERT INTO `mutation_models` (`name`, `nickname`, `amount`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)"
	if sqlText != wantSQL || len(arguments) != 3 {
		t.Fatalf("selected Build() = %q, %#v, want %q", sqlText, arguments, wantSQL)
	}
}

func TestUpsertExecNeverChangesAutoRandomID(t *testing.T) {
	t.Parallel()

	inserted := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}
	insertExecutor := &recordingExecExecutor{result: mutationResult{lastInsertID: 8143, rowsAffected: 1}}
	affected, err := Upsert(&inserted).Exec(context.Background(), insertExecutor)
	if err != nil {
		t.Fatalf("insert Exec() error = %v", err)
	}
	if affected != 1 || inserted.ID != 0 {
		t.Fatalf("insert affected = %d, ID = %d", affected, inserted.ID)
	}

	updated := mutationModel{ID: 42, Name: "Grace", Amount: mutationValue{text: "2.00"}}
	updateExecutor := &recordingExecExecutor{result: mutationResult{lastInsertID: 9917, rowsAffected: 2}}
	affected, err = Upsert(&updated).Exec(context.Background(), updateExecutor)
	if err != nil {
		t.Fatalf("update Exec() error = %v", err)
	}
	if affected != 2 || updated.ID != 42 {
		t.Fatalf("update affected = %d, ID = %d", affected, updated.ID)
	}
}

func TestUpsertManyBuildsAndExecutesPointerSlice(t *testing.T) {
	t.Parallel()

	first := &bulkMutationModel{ID: 1, Value: 10}
	second := &bulkMutationModel{ID: 2, Value: 20}
	query := UpsertMany([]*bulkMutationModel{first, second})
	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `bulk_mutation_models` (`id`, `value`) VALUES (?, ?), (?, ?) ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)"
	if sqlText != wantSQL || !reflect.DeepEqual(arguments, []any{int64(1), int64(10), int64(2), int64(20)}) {
		t.Fatalf("Build() = %q, %#v, want %q", sqlText, arguments, wantSQL)
	}
	executor := &bulkRecordingExecExecutor{}
	affected, err := query.Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || len(executor.executions) != 1 {
		t.Fatalf("affected = %d, executions = %d", affected, len(executor.executions))
	}
}

func TestUpsertManyExecAutomaticallySplitsAtTiDBPlaceholderLimit(t *testing.T) {
	rowCount := maxMutationParameters/2 + 1
	values := make([]bulkMutationModel, rowCount)
	for index := range values {
		values[index] = bulkMutationModel{ID: int64(index + 1), Value: int64(index + 101)}
	}
	query := UpsertMany(values, "Value")
	if _, _, err := query.Build(); err == nil || !strings.Contains(err.Error(), "requires 2 statements") {
		t.Fatalf("Build() error = %v, want multi-statement error", err)
	}

	executor := &bulkRecordingExecExecutor{}
	affected, err := query.Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != int64(rowCount) || len(executor.executions) != 2 {
		t.Fatalf("affected = %d, executions = %d", affected, len(executor.executions))
	}
	for index, execution := range executor.executions {
		if !strings.HasSuffix(execution.query, " ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)") {
			t.Fatalf("batch %d SQL suffix = %q", index+1, execution.query[max(0, len(execution.query)-100):])
		}
	}
}

func TestBulkMutationReportsPartialProgressWhenLaterBatchFails(t *testing.T) {
	rowCount := maxMutationParameters/2 + 1
	values := make([]bulkMutationModel, rowCount)
	failure := errors.New("second batch failure")
	executor := &bulkRecordingExecExecutor{failCall: 2, err: failure}
	affected, err := InsertMany(values).Exec(context.Background(), executor)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "batch 2/2") || !strings.Contains(err.Error(), "rows [32767:32768]") {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != int64(maxMutationParameters/2) {
		t.Fatalf("affected = %d, want %d", affected, maxMutationParameters/2)
	}
}

func TestUpdateUsesSelectedFieldsAndEveryPrimaryKeyComponent(t *testing.T) {
	t.Parallel()

	value := compositeMutationModel{TenantID: 7, UserID: 9, Role: "admin"}
	sqlText, arguments, err := Update(&value, "Role").Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "UPDATE `memberships` SET `role` = ? WHERE `tenant_id` = ? AND `user_id` = ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"admin", int64(7), int64(9)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestUpdateWithoutFieldsWritesEveryWritableNonPrimaryKeyField(t *testing.T) {
	t.Parallel()

	nickname := "Grace"
	value := mutationModel{
		ID:       11,
		Name:     "Ada",
		Nickname: &nickname,
		Amount:   mutationValue{text: "12.30"},
		Count:    99,
	}
	sqlText, arguments, err := Update(&value).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "UPDATE `mutation_models` SET `name` = ?, `nickname` = ?, `amount` = ? WHERE `id` = ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if len(arguments) != 4 || arguments[0] != "Ada" || arguments[1] != &nickname || !reflect.DeepEqual(arguments[2], value.Amount) || arguments[3] != int64(11) {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestUpdateExecutesByPrimaryKey(t *testing.T) {
	t.Parallel()

	value := mutationModel{ID: 11, Name: "Grace", Amount: mutationValue{text: "1.00"}}
	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 1}}
	affected, err := Update(&value, "Name").Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 1 || executor.query != "UPDATE `mutation_models` SET `name` = ? WHERE `id` = ?" {
		t.Fatalf("affected = %d, executor = %#v", affected, executor)
	}
	if got, want := executor.arguments, []any{"Grace", int64(11)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestDeleteBuildsPrimaryKeyAndPredicateForms(t *testing.T) {
	t.Parallel()

	value := compositeMutationModel{TenantID: 7, UserID: 9}
	sqlText, arguments, err := Delete(&value).Build()
	if err != nil {
		t.Fatalf("Delete().Build() error = %v", err)
	}
	if got, want := sqlText, "DELETE FROM `memberships` WHERE `tenant_id` = ? AND `user_id` = ?"; got != want {
		t.Fatalf("primary-key SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{int64(7), int64(9)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary-key arguments = %#v, want %#v", got, want)
	}

	sqlText, arguments, err = DeleteWhere[compositeMutationModel](
		Equal("TenantID", int64(7)),
		NotEqual("Role", "owner"),
	).Build()
	if err != nil {
		t.Fatalf("DeleteWhere().Build() error = %v", err)
	}
	if got, want := sqlText, "DELETE FROM `memberships` WHERE `tenant_id` = ? AND `role` <> ?"; got != want {
		t.Fatalf("predicate SQL = %q, want %q", got, want)
	}
	if got, want := arguments, []any{int64(7), "owner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("predicate arguments = %#v, want %#v", got, want)
	}
}

func TestMutationsRejectUnsafeOrInvalidInput(t *testing.T) {
	t.Parallel()

	value := mutationModel{ID: 1}
	tests := []struct {
		name  string
		build func() error
		want  string
	}{
		{name: "nil insert query", build: func() error { var query *InsertQuery[mutationModel]; _, _, err := query.Build(); return err }, want: "nil INSERT"},
		{name: "nil insert model", build: func() error { _, _, err := Insert((*mutationModel)(nil)).Build(); return err }, want: "nil model"},
		{name: "invalid bulk model pointer depth", build: func() error { _, _, err := InsertMany([]**mutationModel(nil)).Build(); return err }, want: "struct or pointer to struct"},
		{name: "nil upsert query", build: func() error { var query *UpsertQuery[mutationModel]; _, _, err := query.Build(); return err }, want: "nil UPSERT"},
		{name: "nil upsert model", build: func() error { _, _, err := Upsert((*mutationModel)(nil)).Build(); return err }, want: "nil model"},
		{name: "invalid bulk upsert model pointer depth", build: func() error { _, _, err := UpsertMany([]**mutationModel(nil)).Build(); return err }, want: "struct or pointer to struct"},
		{name: "duplicate upsert field", build: func() error { _, _, err := Upsert(&value, "Name", "Name").Build(); return err }, want: "repeats field"},
		{name: "upsert primary key", build: func() error { _, _, err := Upsert(&value, "ID").Build(); return err }, want: "primary-key"},
		{name: "upsert computed", build: func() error { _, _, err := Upsert(&value, "Count").Build(); return err }, want: "computed"},
		{name: "upsert unknown", build: func() error { _, _, err := Upsert(&value, "Missing").Build(); return err }, want: "not a mapped scalar"},
		{name: "upsert without writable fields", build: func() error {
			current := mutationOnlyAutoRandom{}
			_, _, err := Upsert(&current).Build()
			return err
		}, want: "no writable"},
		{name: "duplicate update field", build: func() error { _, _, err := Update(&value, "Name", "Name").Build(); return err }, want: "repeats field"},
		{name: "update primary key", build: func() error { _, _, err := Update(&value, "ID").Build(); return err }, want: "primary-key"},
		{name: "update computed", build: func() error { _, _, err := Update(&value, "Count").Build(); return err }, want: "computed"},
		{name: "update unknown", build: func() error { _, _, err := Update(&value, "Missing").Build(); return err }, want: "not a mapped scalar"},
		{name: "update without primary key", build: func() error {
			current := mutationWithoutPrimaryKey{Name: "Ada"}
			_, _, err := Update(&current, "Name").Build()
			return err
		}, want: "declared primary key"},
		{name: "delete without primary key", build: func() error {
			current := mutationWithoutPrimaryKey{Name: "Ada"}
			_, _, err := Delete(&current).Build()
			return err
		}, want: "declared primary key"},
		{name: "delete without predicate", build: func() error { _, _, err := DeleteWhere[mutationModel]().Build(); return err }, want: "at least one predicate"},
		{name: "delete relation predicate", build: func() error { _, _, err := DeleteWhere[mutationModel](Has("Missing")).Build(); return err }, want: "does not support relation predicates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.build(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMutationExecutionReportsExecutorAndResultErrors(t *testing.T) {
	t.Parallel()

	executionFailure := errors.New("execution failure")
	lastIDFailure := errors.New("last ID failure")
	rowsFailure := errors.New("rows failure")
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}

	if _, err := Insert(&value).Exec(nil, &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("nil context error = %v", err)
	}
	var typedNil *recordingExecExecutor
	if _, err := Insert(&value).Exec(context.Background(), typedNil); err == nil || !strings.Contains(err.Error(), "nil executor") {
		t.Fatalf("nil executor error = %v", err)
	}
	if _, err := Insert(&value).Exec(context.Background(), &recordingExecExecutor{err: executionFailure}); !errors.Is(err, executionFailure) {
		t.Fatalf("execution error = %v", err)
	}
	if _, err := Insert(&value).Exec(context.Background(), &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil result error = %v", err)
	}
	if _, err := Insert(&value).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{lastIDErr: lastIDFailure}}); !errors.Is(err, lastIDFailure) {
		t.Fatalf("LastInsertId error = %v", err)
	}
	if _, err := Insert(&value).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{lastInsertID: 1, rowsErr: rowsFailure}}); !errors.Is(err, rowsFailure) {
		t.Fatalf("RowsAffected error = %v", err)
	}
}

func TestUpsertExecutionReportsExecutorAndResultErrors(t *testing.T) {
	t.Parallel()

	executionFailure := errors.New("upsert execution failure")
	rowsFailure := errors.New("upsert rows failure")
	value := mutationModel{Name: "Ada", Amount: mutationValue{text: "1.00"}}

	if _, err := Upsert(&value).Exec(context.Background(), &recordingExecExecutor{err: executionFailure}); !errors.Is(err, executionFailure) {
		t.Fatalf("execution error = %v", err)
	}
	if _, err := Upsert(&value).Exec(context.Background(), &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil result error = %v", err)
	}
	if _, err := Upsert(&value).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{rowsErr: rowsFailure}}); !errors.Is(err, rowsFailure) {
		t.Fatalf("RowsAffected error = %v", err)
	}
	if _, err := Upsert(&value).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{rowsAffected: 1, lastIDErr: errors.New("must not be read")}}); err != nil {
		t.Fatalf("LastInsertId must not be read: %v", err)
	}
}

func TestBulkMutationExecutionReportsResultErrors(t *testing.T) {
	t.Parallel()

	rowsFailure := errors.New("bulk rows failure")
	values := []bulkMutationModel{{ID: 1, Value: 10}}
	if _, err := InsertMany(values).Exec(context.Background(), &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil result error = %v", err)
	}
	if _, err := UpsertMany(values).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{rowsErr: rowsFailure}}); !errors.Is(err, rowsFailure) {
		t.Fatalf("RowsAffected error = %v", err)
	}
}

func TestComputedFieldsAreExcludedFromBaseTableQueries(t *testing.T) {
	t.Parallel()

	sqlText, _, err := Query[mutationModel]().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if strings.Contains(sqlText, "`count`") {
		t.Fatalf("SQL = %q, computed field must be omitted", sqlText)
	}
	for _, query := range []interface{ Build() (string, []any, error) }{
		Query[mutationModel]().Select("Count"),
		Query[mutationModel]().Where(Equal("Count", 1)),
		Query[mutationModel]().OrderBy(Asc("Count")),
	} {
		if _, _, err := query.Build(); err == nil || !strings.Contains(err.Error(), "computed") {
			t.Fatalf("computed query error = %v", err)
		}
	}
}
