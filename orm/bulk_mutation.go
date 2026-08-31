package orm

import (
	"context"
	"fmt"
	"reflect"

	"github.com/mayahiro/go-tidb/model"
)

type bulkMutationPlan struct {
	operation       string
	descriptor      *model.Descriptor
	insertFields    []mutationFieldPlan
	updateFields    []mutationFieldPlan
	values          reflect.Value
	pointerElements bool
	noOp            bool
}

func prepareBulkMutation[T any](values []T, fieldNames []string, operation string, upsert bool) (bulkMutationPlan, error) {
	descriptor, pointerElements, err := insertManyDescriptor[T](operation)
	if err != nil {
		return bulkMutationPlan{}, err
	}
	result := bulkMutationPlan{
		operation:       operation,
		descriptor:      descriptor,
		values:          reflect.ValueOf(values),
		pointerElements: pointerElements,
		noOp:            len(values) == 0,
	}
	if result.noOp {
		return result, nil
	}
	plan := mutationPlanFor(descriptor)
	if plan.insertErr != nil {
		return bulkMutationPlan{}, plan.insertErr
	}
	result.insertFields = plan.insertFields
	if upsert {
		result.updateFields, err = mutationUpdateFields(descriptor, plan, fieldNames, "UPSERT")
		if err != nil {
			return bulkMutationPlan{}, err
		}
	}
	return result, nil
}

func (plan bulkMutationPlan) compileSingle() (compiledMutation, error) {
	if plan.noOp {
		return compiledMutation{modelName: plan.descriptor.Name(), noOp: true}, nil
	}
	rowsPerStatement, err := plan.rowsPerStatement()
	if err != nil {
		return compiledMutation{}, err
	}
	if plan.values.Len() > rowsPerStatement {
		return compiledMutation{}, bulkBuildStatementLimitError(plan.operation, plan.descriptor, plan.values.Len(), rowsPerStatement)
	}
	return plan.compileRange(0, plan.values.Len())
}

func (plan bulkMutationPlan) compileRange(start, end int) (compiledMutation, error) {
	rowCount := end - start
	arguments := make([]any, rowCount*len(plan.insertFields))
	if err := fillInsertManyArguments(arguments, plan.values, plan.descriptor, plan.insertFields, plan.pointerElements, start, end, plan.operation); err != nil {
		return compiledMutation{}, err
	}
	statement := renderInsert(plan.descriptor.TableName(), plan.insertFields, rowCount)
	if len(plan.updateFields) != 0 {
		statement = appendOnDuplicateKeyUpdate(statement, plan.updateFields)
	}
	return compiledMutation{
		modelName:  plan.descriptor.Name(),
		descriptor: plan.descriptor,
		sql:        statement,
		arguments:  arguments,
	}, nil
}

func (plan bulkMutationPlan) rowsPerStatement() (int, error) {
	fieldCount := len(plan.insertFields)
	if fieldCount == 0 {
		return min(plan.values.Len(), maxMutationParameters), nil
	}
	rows := maxMutationParameters / fieldCount
	if rows == 0 {
		return 0, fmt.Errorf("orm: %s for %s has %d insert fields, exceeding TiDB's %d-placeholder statement limit", plan.operation, plan.descriptor.Name(), fieldCount, maxMutationParameters)
	}
	return rows, nil
}

func (plan bulkMutationPlan) validatePointerElements() error {
	if !plan.pointerElements {
		return nil
	}
	for index := 0; index < plan.values.Len(); index++ {
		if plan.values.Index(index).IsNil() {
			return fmt.Errorf("orm: %s row %d: %s is nil", plan.operation, index, plan.descriptor.Name())
		}
	}
	return nil
}

func (plan bulkMutationPlan) statementCount() (int, error) {
	if plan.noOp {
		return 0, nil
	}
	rowsPerStatement, err := plan.rowsPerStatement()
	if err != nil {
		return 0, err
	}
	return bulkStatementCount(plan.values.Len(), rowsPerStatement), nil
}

func (plan bulkMutationPlan) exec(ctx context.Context, executor ExecExecutor) (int64, error) {
	if plan.noOp {
		return 0, nil
	}
	if err := plan.validatePointerElements(); err != nil {
		return 0, err
	}
	rowsPerStatement, err := plan.rowsPerStatement()
	if err != nil {
		return 0, err
	}
	statementCount := bulkStatementCount(plan.values.Len(), rowsPerStatement)
	var statementGroup uint64
	var totalAffected int64
	statementOperation := StatementInsert
	if len(plan.updateFields) != 0 {
		statementOperation = StatementUpsert
	}
	for statementIndex, start := 0, 0; start < plan.values.Len(); statementIndex, start = statementIndex+1, start+rowsPerStatement {
		end := min(start+rowsPerStatement, plan.values.Len())
		compiled, compileErr := plan.compileRange(start, end)
		if compileErr != nil {
			return totalAffected, plan.batchError(statementIndex, statementCount, start, end, "compile", compileErr)
		}
		observation := beginStatementObservation(ctx, statementOperation, compiled.sql, compiled.arguments)
		plan.attachRuntimeCapture(observation, &statementGroup, statementOperation, statementIndex, statementCount, start, end)
		statementExecutor := executor
		if observation != nil && observation.event.ServerRU != nil {
			statementExecutor = observation.prepareServerRUExecExecutor(ctx, executor)
		}
		result, execErr := statementExecutor.ExecContext(ctx, compiled.sql, compiled.arguments...)
		if execErr != nil {
			observation.finish(0, false, execErr)
			return totalAffected, plan.batchError(statementIndex, statementCount, start, end, "execute", execErr)
		}
		if nilPredicateArgument(result) {
			execErr = fmt.Errorf("executor returned a nil result")
			observation.finish(0, false, execErr)
			return totalAffected, plan.batchError(statementIndex, statementCount, start, end, "execute", execErr)
		}
		affected, rowsErr := finishMutationStatementObservation(observation, result, plan.operation, plan.descriptor.Name())
		if rowsErr != nil {
			return totalAffected, plan.batchError(statementIndex, statementCount, start, end, "read result for", rowsErr)
		}
		totalAffected += affected
	}
	return totalAffected, nil
}

func (plan bulkMutationPlan) attachRuntimeCapture(observation *statementObservation, statementGroup *uint64, operation StatementOperation, statementIndex, statementCount, start, end int) {
	if observation == nil || observation.runtime == nil {
		return
	}
	if *statementGroup == 0 {
		*statementGroup = nextRuntimeStatementGroupWhen(true)
	}
	terminal := "insert_many"
	if operation == StatementUpsert {
		terminal = "upsert_many"
	}
	metadata := runtimeTypedMutationMetadata(plan.descriptor.Name(), terminal)
	metadata.batch = runtimeBatchMetadata(*statementGroup, statementIndex+1, statementCount, end-start, plan.values.Len())
	observation.runtime.metadata = metadata
}

func (plan bulkMutationPlan) batchError(statementIndex, statementCount, start, end int, action string, err error) error {
	if statementCount == 1 {
		return fmt.Errorf("orm: %s %s for %s: %w", action, plan.operation, plan.descriptor.Name(), err)
	}
	return fmt.Errorf("orm: %s %s batch %d/%d rows [%d:%d] for %s: %w", action, plan.operation, statementIndex+1, statementCount, start, end, plan.descriptor.Name(), err)
}

func bulkStatementCount(rowCount, rowsPerStatement int) int {
	return (rowCount-1)/rowsPerStatement + 1
}

func bulkBuildStatementLimitError(operation string, descriptor *model.Descriptor, rowCount, rowsPerStatement int) error {
	return fmt.Errorf("orm: build %s for %s requires %d statements for %d rows because TiDB limits one statement to %d placeholders; Build returns one statement, use Exec for automatic batching", operation, descriptor.Name(), bulkStatementCount(rowCount, rowsPerStatement), rowCount, maxMutationParameters)
}
