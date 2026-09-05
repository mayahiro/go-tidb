package orm

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

type mutationFieldPlan struct {
	field         model.Field
	index         []int
	addressValuer bool
}

type mutationModelPlan struct {
	descriptor    *model.Descriptor
	insertFields  []mutationFieldPlan
	insertSQL     string
	upsertSQL     string
	insertErr     error
	autoRandom    *mutationFieldPlan
	updateFields  []mutationFieldPlan
	updateSQL     string
	updateErr     error
	primaryKey    []mutationFieldPlan
	primaryKeyErr error
	deleteSQL     string
	softDelete    *mutationFieldPlan
}

var mutationModelPlanCache sync.Map

func mutationPlanFor(descriptor *model.Descriptor) *mutationModelPlan {
	modelType := descriptor.Type()
	if cached, ok := mutationModelPlanCache.Load(modelType); ok {
		return cached.(*mutationModelPlan)
	}
	plan := compileMutationModelPlan(descriptor)
	result, _ := mutationModelPlanCache.LoadOrStore(modelType, plan)
	return result.(*mutationModelPlan)
}

func compileMutationModelPlan(descriptor *model.Descriptor) *mutationModelPlan {
	plan := &mutationModelPlan{descriptor: descriptor}
	for _, field := range descriptor.Fields() {
		current := compileMutationFieldPlan(field)
		if field.IsPrimaryKey() {
			plan.primaryKey = append(plan.primaryKey, current)
		}
		if field.IsAutoRandom() {
			autoRandom := current
			plan.autoRandom = &autoRandom
		} else if !field.IsComputed() {
			if !field.CanValue() && plan.insertErr == nil {
				plan.insertErr = fmt.Errorf("orm: INSERT field %s.%s cannot be used as a database argument", descriptor.Name(), field.GoName())
			}
			plan.insertFields = append(plan.insertFields, current)
		}
		if field.IsSoftDelete() {
			softDelete := current
			plan.softDelete = &softDelete
		}
		if !field.IsPrimaryKey() && !field.IsAutoRandom() && !field.IsComputed() {
			if !field.CanValue() && plan.updateErr == nil {
				plan.updateErr = fmt.Errorf("orm: writable field %s.%s cannot be used as a database argument", descriptor.Name(), field.GoName())
			}
			plan.updateFields = append(plan.updateFields, current)
		}
	}

	if plan.insertErr == nil {
		plan.insertSQL = renderInsert(descriptor.TableName(), plan.insertFields, 1, nil)
	}
	if len(plan.updateFields) == 0 && plan.updateErr == nil {
		plan.updateErr = fmt.Errorf("orm: model %s has no writable non-primary-key fields", descriptor.Name())
	}
	if plan.insertErr == nil && plan.updateErr == nil {
		plan.upsertSQL = appendOnDuplicateKeyUpdate(plan.insertSQL, plan.updateFields)
	}
	if len(plan.primaryKey) == 0 {
		plan.primaryKeyErr = fmt.Errorf("orm: model %s requires a declared primary key", descriptor.Name())
	} else {
		for _, field := range plan.primaryKey {
			if !field.field.CanValue() {
				plan.primaryKeyErr = fmt.Errorf("orm: primary-key field %s.%s cannot be used as a database argument", descriptor.Name(), field.field.GoName())
				break
			}
		}
	}
	if plan.updateErr == nil && plan.primaryKeyErr == nil {
		plan.updateSQL = renderPrimaryKeyUpdate(descriptor.TableName(), plan.updateFields, plan.primaryKey, plan.softDelete)
	}
	if plan.primaryKeyErr == nil {
		plan.deleteSQL = renderPrimaryKeyDelete(descriptor.TableName(), plan.primaryKey, plan.softDelete)
	}
	return plan
}

func compileMutationFieldPlan(field model.Field) mutationFieldPlan {
	plan := mutationFieldPlan{field: field, index: field.Index()}
	if field.UsesValuer() {
		fieldType := field.GoType()
		plan.addressValuer = !fieldType.Implements(driverValuerType) && reflect.PointerTo(fieldType).Implements(driverValuerType)
	}
	return plan
}

func mutationUpdateFields(descriptor *model.Descriptor, plan *mutationModelPlan, names []string, operation string) ([]mutationFieldPlan, error) {
	if len(names) == 0 {
		if plan.updateErr != nil {
			return nil, plan.updateErr
		}
		return plan.updateFields, nil
	}
	return selectedUpdateFields(descriptor, names, operation)
}

func selectedUpdateFields(descriptor *model.Descriptor, names []string, operation string) ([]mutationFieldPlan, error) {
	fields := make([]mutationFieldPlan, len(names))
	seen := make(map[string]bool, len(names))
	for index, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("orm: %s for %s repeats field %q", operation, descriptor.Name(), name)
		}
		field, exists := descriptor.FieldByGoName(name)
		if !exists {
			return nil, fmt.Errorf("orm: %s field %s.%s is not a mapped scalar field", operation, descriptor.Name(), name)
		}
		if field.IsPrimaryKey() || field.IsAutoRandom() {
			return nil, fmt.Errorf("orm: %s field %s.%s is a primary-key field", operation, descriptor.Name(), name)
		}
		if field.IsComputed() {
			return nil, fmt.Errorf("orm: %s field %s.%s is computed", operation, descriptor.Name(), name)
		}
		if !field.CanValue() {
			return nil, fmt.Errorf("orm: %s field %s.%s cannot be used as a database argument", operation, descriptor.Name(), name)
		}
		seen[name] = true
		fields[index] = compileMutationFieldPlan(field)
	}
	return fields, nil
}

func mutationArguments(root reflect.Value, descriptor *model.Descriptor, fields []mutationFieldPlan) ([]any, error) {
	arguments := make([]any, len(fields))
	if err := fillMutationArguments(arguments, root, descriptor, fields); err != nil {
		return nil, err
	}
	return arguments, nil
}

func fillMutationArguments(arguments []any, root reflect.Value, descriptor *model.Descriptor, fields []mutationFieldPlan) error {
	for index, field := range fields {
		argument, err := mutationArgument(root, descriptor, field)
		if err != nil {
			return err
		}
		arguments[index] = argument
	}
	return nil
}

func fillPrimaryKeyArguments(arguments []any, root reflect.Value, descriptor *model.Descriptor, primaryKey []mutationFieldPlan, operation string) error {
	if err := fillMutationArguments(arguments, root, descriptor, primaryKey); err != nil {
		return err
	}
	for index, argument := range arguments {
		if argument == nil {
			return fmt.Errorf("orm: %s primary-key field %s.%s must not be nil", operation, descriptor.Name(), primaryKey[index].field.GoName())
		}
	}
	return nil
}
