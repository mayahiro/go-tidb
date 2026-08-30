package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type relationMutationValuerSource struct {
	model.Meta `tidbgo:"table=relation_mutation_sources"`
	ID         mutationValue                  `tidbgo:",pk"`
	Targets    []relationMutationValuerTarget `tidbgo:"many_to_many,through=relation_mutation_links,source=ID:source_id,target=target_id:ID"`
}

type relationMutationValuerTarget struct {
	model.Meta `tidbgo:"table=relation_mutation_targets"`
	ID         mutationValue `tidbgo:",pk"`
}

func TestAddRelationBuildsOneJunctionInsertOffline(t *testing.T) {
	t.Parallel()

	roleIDs := []uint64{11, 12}
	sqlText, arguments, err := AddRelation[preloadUser]("Roles", uint64(7), roleIDs...).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `preload_user_roles` (`user_id`, `role_id`) VALUES (?, ?), (?, ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	wantArguments := []any{uint64(7), uint64(11), uint64(7), uint64(12)}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", arguments, wantArguments)
	}
}

func TestAddRelationIgnoreExistingIsExplicit(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := AddRelation[preloadUser]("Roles", uint64(7), uint64(11)).
		IgnoreExisting().
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `preload_user_roles` (`user_id`, `role_id`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `user_id` = `user_id`"
	if sqlText != wantSQL || !reflect.DeepEqual(arguments, []any{uint64(7), uint64(11)}) {
		t.Fatalf("Build() = %q, %#v", sqlText, arguments)
	}
}

func TestAddRelationBuildDoesNotExecuteValuer(t *testing.T) {
	t.Parallel()

	calls := 0
	source := mutationValue{calls: &calls, text: "source"}
	target := mutationValue{calls: &calls, text: "target"}
	sqlText, arguments, err := AddRelation[relationMutationValuerSource]("Targets", source, target).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Value() calls = %d, want 0", calls)
	}
	if sqlText != "INSERT INTO `relation_mutation_links` (`source_id`, `target_id`) VALUES (?, ?)" {
		t.Fatalf("SQL = %q", sqlText)
	}
	if !reflect.DeepEqual(arguments, []any{source, target}) {
		t.Fatalf("arguments = %#v", arguments)
	}
}

func TestAddRelationExecutesThroughExplicitExecutor(t *testing.T) {
	t.Parallel()

	executor := &recordingExecExecutor{result: mutationResult{rowsAffected: 2}}
	affected, err := AddRelation[preloadUser]("Roles", uint64(7), uint64(11), uint64(12)).
		Exec(context.Background(), executor)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if affected != 2 || executor.calls != 1 {
		t.Fatalf("affected = %d, calls = %d", affected, executor.calls)
	}
	if executor.query != "INSERT INTO `preload_user_roles` (`user_id`, `role_id`) VALUES (?, ?), (?, ?)" {
		t.Fatalf("query = %q", executor.query)
	}
}

func TestRemoveRelationBuildsOneJunctionDelete(t *testing.T) {
	t.Parallel()

	roleIDs := []uint64{11, 12}
	sqlText, arguments, err := RemoveRelation[preloadUser]("Roles", uint64(7), roleIDs...).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "DELETE FROM `preload_user_roles` WHERE `user_id` = ? AND `role_id` IN (?, ?)"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	wantArguments := []any{uint64(7), uint64(11), uint64(12)}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("arguments = %#v, want %#v", arguments, wantArguments)
	}
}

func TestClearRelationBuildsOneSourceDelete(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := ClearRelation[preloadUser]("Roles", uint64(7)).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "DELETE FROM `preload_user_roles` WHERE `user_id` = ?"
	if sqlText != wantSQL || !reflect.DeepEqual(arguments, []any{uint64(7)}) {
		t.Fatalf("Build() = %q, %#v", sqlText, arguments)
	}
}

func TestRelationMutationsSupportCompositeKeys(t *testing.T) {
	t.Parallel()

	source := CompositeKey(uint64(10), uint64(7))
	targets := []RelationKey{
		CompositeKey(uint64(10), uint64(21)),
		CompositeKey(uint64(10), uint64(22)),
	}

	addSQL, addArguments, err := AddRelation[preloadMember]("Groups", source, targets...).Build()
	if err != nil {
		t.Fatalf("AddRelation().Build() error = %v", err)
	}
	wantAddSQL := "INSERT INTO `preload_member_groups` (`tenant_id`, `member_id`, `group_tenant_id`, `group_id`) VALUES (?, ?, ?, ?), (?, ?, ?, ?)"
	if addSQL != wantAddSQL {
		t.Fatalf("add SQL = %q, want %q", addSQL, wantAddSQL)
	}
	wantAddArguments := []any{uint64(10), uint64(7), uint64(10), uint64(21), uint64(10), uint64(7), uint64(10), uint64(22)}
	if !reflect.DeepEqual(addArguments, wantAddArguments) {
		t.Fatalf("add arguments = %#v, want %#v", addArguments, wantAddArguments)
	}

	removeSQL, removeArguments, err := RemoveRelation[preloadMember]("Groups", source, targets...).Build()
	if err != nil {
		t.Fatalf("RemoveRelation().Build() error = %v", err)
	}
	wantRemoveSQL := "DELETE FROM `preload_member_groups` WHERE `tenant_id` = ? AND `member_id` = ? AND ((`group_tenant_id` = ? AND `group_id` = ?) OR (`group_tenant_id` = ? AND `group_id` = ?))"
	if removeSQL != wantRemoveSQL {
		t.Fatalf("remove SQL = %q, want %q", removeSQL, wantRemoveSQL)
	}
	wantRemoveArguments := []any{uint64(10), uint64(7), uint64(10), uint64(21), uint64(10), uint64(22)}
	if !reflect.DeepEqual(removeArguments, wantRemoveArguments) {
		t.Fatalf("remove arguments = %#v, want %#v", removeArguments, wantRemoveArguments)
	}

	clearSQL, clearArguments, err := ClearRelation[preloadMember]("Groups", source).Build()
	if err != nil {
		t.Fatalf("ClearRelation().Build() error = %v", err)
	}
	wantClearSQL := "DELETE FROM `preload_member_groups` WHERE `tenant_id` = ? AND `member_id` = ?"
	if clearSQL != wantClearSQL || !reflect.DeepEqual(clearArguments, []any{uint64(10), uint64(7)}) {
		t.Fatalf("ClearRelation().Build() = %q, %#v", clearSQL, clearArguments)
	}
}

func TestEmptyRelationTargetListIsNoOp(t *testing.T) {
	t.Parallel()

	targets := []uint64(nil)
	tests := []struct {
		name  string
		build func() (string, []any, error)
		exec  func(ExecExecutor) (int64, error)
	}{
		{
			name:  "add",
			build: AddRelation[preloadUser]("Roles", uint64(7), targets...).Build,
			exec: func(executor ExecExecutor) (int64, error) {
				return AddRelation[preloadUser]("Roles", uint64(7), targets...).Exec(context.Background(), executor)
			},
		},
		{
			name:  "remove",
			build: RemoveRelation[preloadUser]("Roles", uint64(7), targets...).Build,
			exec: func(executor ExecExecutor) (int64, error) {
				return RemoveRelation[preloadUser]("Roles", uint64(7), targets...).Exec(context.Background(), executor)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sqlText, arguments, err := test.build()
			if err != nil || sqlText != "" || arguments != nil {
				t.Fatalf("Build() = %q, %#v, %v", sqlText, arguments, err)
			}
			executor := &recordingExecExecutor{}
			affected, err := test.exec(executor)
			if err != nil || affected != 0 || executor.calls != 0 {
				t.Fatalf("Exec() = %d, %v; calls = %d", affected, err, executor.calls)
			}
		})
	}
}

func TestRelationMutationValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func() error
		want  string
	}{
		{
			name: "unknown relation",
			build: func() error {
				_, _, err := AddRelation[preloadUser]("Missing", uint64(7), uint64(11)).Build()
				return err
			},
			want: "not a mapped relation",
		},
		{
			name: "direct relation",
			build: func() error {
				_, _, err := AddRelation[preloadUser]("Orders", uint64(7), uint64(11)).Build()
				return err
			},
			want: "pure many-to-many",
		},
		{
			name: "pointer model",
			build: func() error {
				_, _, err := AddRelation[*preloadUser]("Roles", uint64(7), uint64(11)).Build()
				return err
			},
			want: "non-pointer struct",
		},
		{
			name: "nil source",
			build: func() error {
				var source *uint64
				_, _, err := AddRelation[preloadUser]("Roles", source, uint64(11)).Build()
				return err
			},
			want: "source must not be nil",
		},
		{
			name: "composite source requires CompositeKey",
			build: func() error {
				_, _, err := AddRelation[preloadMember]("Groups", uint64(7), CompositeKey(uint64(10), uint64(21))).Build()
				return err
			},
			want: "source has 2 columns and requires CompositeKey",
		},
		{
			name: "composite target width",
			build: func() error {
				_, _, err := AddRelation[preloadMember]("Groups", CompositeKey(uint64(10), uint64(7)), CompositeKey(uint64(21))).Build()
				return err
			},
			want: "target 1 requires 2 components, got 1",
		},
		{
			name: "single key rejects CompositeKey",
			build: func() error {
				_, _, err := AddRelation[preloadUser]("Roles", CompositeKey(uint64(7)), uint64(11)).Build()
				return err
			},
			want: "source has one column and requires a scalar",
		},
		{
			name: "nil composite component",
			build: func() error {
				_, _, err := ClearRelation[preloadMember]("Groups", CompositeKey(uint64(10), nil)).Build()
				return err
			},
			want: "component 2 must not be nil",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.build(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRelationMutationEnforcesTiDBParameterLimit(t *testing.T) {
	t.Parallel()

	targets := make([]uint64, maxMutationParameters/2+1)
	_, _, err := AddRelation[preloadUser]("Roles", uint64(7), targets...).Build()
	if err == nil || !strings.Contains(err.Error(), "65536 parameters") || !strings.Contains(err.Error(), "65535-placeholder") {
		t.Fatalf("Build() error = %v", err)
	}
}

func TestRelationMutationExecutionReportsErrors(t *testing.T) {
	t.Parallel()

	var nilAdd *RelationAddQuery[preloadUser]
	if _, _, err := nilAdd.Build(); err == nil || !strings.Contains(err.Error(), "nil relation INSERT") {
		t.Fatalf("nil Add Build() error = %v", err)
	}
	var nilDelete *RelationDeleteQuery[preloadUser]
	if _, _, err := nilDelete.Build(); err == nil || !strings.Contains(err.Error(), "nil relation DELETE") {
		t.Fatalf("nil Delete Build() error = %v", err)
	}
	if _, err := AddRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec(nil, &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("nil context Exec() error = %v", err)
	}
	executionFailure := errors.New("relation execution failed")
	if _, err := RemoveRelation[preloadUser]("Roles", uint64(7), uint64(11)).Exec(context.Background(), &recordingExecExecutor{err: executionFailure}); !errors.Is(err, executionFailure) {
		t.Fatalf("executor failure = %v", err)
	}
	if _, err := ClearRelation[preloadUser]("Roles", uint64(7)).Exec(context.Background(), &recordingExecExecutor{}); err == nil || !strings.Contains(err.Error(), "nil result") {
		t.Fatalf("nil result Exec() error = %v", err)
	}
	rowsFailure := errors.New("rows affected failed")
	if _, err := ClearRelation[preloadUser]("Roles", uint64(7)).Exec(context.Background(), &recordingExecExecutor{result: mutationResult{rowsErr: rowsFailure}}); !errors.Is(err, rowsFailure) {
		t.Fatalf("rows affected failure = %v", err)
	}
}
