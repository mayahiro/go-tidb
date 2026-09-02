package orm

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/mayahiro/go-tidb/model"
)

const (
	relationRootAlias                  = "tidbgo_r0"
	relationRootAliasSQLCapacity       = len(" AS ``") + len(relationRootAlias)
	relationTargetAlias1               = "tidbgo_r1"
	relationTargetAlias2               = "tidbgo_r2"
	relationJunctionAlias1             = "tidbgo_j1"
	relationJunctionAlias2             = "tidbgo_j2"
	relationPredicateBaseSQLCapacity   = 96
	relationManyToManyExtraSQLCapacity = 96
	relationSemiJoinRewriteHint        = "/*+ SEMI_JOIN_REWRITE() */ "
)

type relationPredicatePlan struct {
	sourceName       string
	relationName     string
	relationKind     model.RelationKind
	target           *model.Descriptor
	sourceColumns    []string
	targetColumns    []string
	softDeleteColumn string
	junction         *relationPredicateJunction
}

type relationPredicateJunction struct {
	tableName     string
	sourceColumns []string
	targetColumns []string
}

type relationPredicatePlanResult struct {
	plan *relationPredicatePlan
	err  error
}

type relationPredicatePlanKey struct {
	sourceType   reflect.Type
	relationName string
}

var relationPredicatePlanCache sync.Map

func relationPredicateExtraSQLCapacity(descriptor *model.Descriptor, predicates []predicate) int {
	capacity := 0
	for index := range predicates {
		current := predicates[index]
		childDescriptor := descriptor
		if current.operator == predicateHasRelation {
			if relation, ok := descriptor.RelationByName(current.field); ok {
				if relation.IsCollection() && len(current.children) != 0 {
					capacity += len(relationSemiJoinRewriteHint)
				}
				if relation.Kind() == model.RelationManyToMany {
					capacity += relationManyToManyExtraSQLCapacity
				}
				if predicatesHaveRelation(current.children) {
					if target, err := model.DescribeType(relation.TargetType()); err == nil {
						childDescriptor = target
					}
				}
			}
		}
		capacity += relationPredicateExtraSQLCapacity(childDescriptor, current.children)
	}
	return capacity
}

func relationPredicatePlanFor(source *model.Descriptor, relationName string) (*relationPredicatePlan, error) {
	key := relationPredicatePlanKey{sourceType: source.Type(), relationName: relationName}
	if cached, ok := relationPredicatePlanCache.Load(key); ok {
		result := cached.(relationPredicatePlanResult)
		return result.plan, result.err
	}

	relation, ok := source.RelationByName(relationName)
	if !ok {
		err := fmt.Errorf("orm: SELECT relation predicate %s.%s is not a mapped relation field", source.Name(), relationName)
		result, _ := relationPredicatePlanCache.LoadOrStore(key, relationPredicatePlanResult{err: err})
		return nil, result.(relationPredicatePlanResult).err
	}
	plan, err := compileRelationPredicatePlan(source, relation)
	result, _ := relationPredicatePlanCache.LoadOrStore(key, relationPredicatePlanResult{plan: plan, err: err})
	cached := result.(relationPredicatePlanResult)
	return cached.plan, cached.err
}

func compileRelationPredicatePlan(source *model.Descriptor, relation model.Relation) (*relationPredicatePlan, error) {
	sourceKey := relation.SourceKey()
	targetKey := relation.TargetKey()
	if len(sourceKey) == 0 || len(targetKey) == 0 {
		return nil, fmt.Errorf("orm: SELECT relation predicate %s.%s has invalid key metadata", source.Name(), relation.GoName())
	}
	if relation.Kind() != model.RelationManyToMany && len(sourceKey) != len(targetKey) {
		return nil, fmt.Errorf("orm: SELECT relation predicate %s.%s has invalid key metadata", source.Name(), relation.GoName())
	}

	target, err := model.DescribeType(relation.TargetType())
	if err != nil {
		return nil, fmt.Errorf("orm: describe SELECT relation predicate target %s.%s: %w", source.Name(), relation.GoName(), err)
	}
	plan := &relationPredicatePlan{
		sourceName:    source.Name(),
		relationName:  relation.GoName(),
		relationKind:  relation.Kind(),
		target:        target,
		sourceColumns: relationFieldColumns(sourceKey),
		targetColumns: relationFieldColumns(targetKey),
	}
	if softDeleteField, exists := target.SoftDeleteField(); exists {
		plan.softDeleteColumn = softDeleteField.ColumnName()
	}
	if relation.Kind() != model.RelationManyToMany {
		return plan, nil
	}

	junction, ok := relation.Junction()
	if !ok {
		return nil, fmt.Errorf("orm: SELECT relation predicate %s.%s has no junction metadata", source.Name(), relation.GoName())
	}
	sourceColumns := junction.SourceColumns()
	targetColumns := junction.TargetColumns()
	if len(sourceColumns) != len(sourceKey) || len(targetColumns) != len(targetKey) {
		return nil, fmt.Errorf("orm: SELECT relation predicate %s.%s has invalid junction metadata", source.Name(), relation.GoName())
	}
	plan.junction = &relationPredicateJunction{
		tableName:     junction.TableName(),
		sourceColumns: sourceColumns,
		targetColumns: targetColumns,
	}
	return plan, nil
}

func relationFieldColumns(fields []model.Field) []string {
	columns := make([]string, len(fields))
	for index := range fields {
		columns[index] = fields[index].ColumnName()
	}
	return columns
}

func (c *predicateCompiler) writeRelation(current predicate) error {
	if len(current.values) != 0 {
		return fmt.Errorf("orm: relation SELECT predicate for %s.%s must not contain values", c.descriptor.Name(), current.field)
	}
	if current.operator != predicateHasRelation {
		return fmt.Errorf("orm: SELECT relation predicate has unknown operator %d", current.operator)
	}

	plan, err := relationPredicatePlanFor(c.descriptor, current.field)
	if err != nil {
		return err
	}
	c.relationAlias++
	aliasIndex := c.relationAlias
	targetAlias := relationTargetAlias(aliasIndex)
	sourceAlias := c.qualifier
	if sourceAlias == "" {
		sourceAlias = c.descriptor.TableName()
	}

	c.query.WriteString("EXISTS (SELECT ")
	if relationPredicateUsesSemiJoinRewrite(plan, current, c.negationDepth, c.disjunctionDepth) {
		c.query.WriteString(relationSemiJoinRewriteHint)
	}
	c.query.WriteString("1 FROM ")
	if plan.junction == nil {
		writeAliasedRelationTable(c.query, plan.target.TableName(), targetAlias)
		c.query.WriteString(" WHERE (")
		writeRelationColumnEqualities(c.query, targetAlias, plan.targetColumns, sourceAlias, plan.sourceColumns)
	} else {
		junctionAlias := relationJunctionAlias(aliasIndex)
		writeAliasedRelationTable(c.query, plan.junction.tableName, junctionAlias)
		c.query.WriteString(" JOIN ")
		writeAliasedRelationTable(c.query, plan.target.TableName(), targetAlias)
		c.query.WriteString(" ON (")
		writeRelationColumnEqualities(c.query, targetAlias, plan.targetColumns, junctionAlias, plan.junction.targetColumns)
		c.query.WriteString(") WHERE (")
		writeRelationColumnEqualities(c.query, junctionAlias, plan.junction.sourceColumns, sourceAlias, plan.sourceColumns)
	}
	c.query.WriteByte(')')
	if plan.softDeleteColumn != "" {
		c.query.WriteString(" AND ")
		writePreloadSoftDeletePredicate(c.query, targetAlias, plan.softDeleteColumn)
	}

	if len(current.children) != 0 {
		sourceDescriptor := c.descriptor
		sourceQualifier := c.qualifier
		c.descriptor = plan.target
		c.qualifier = targetAlias
		for index := range current.children {
			c.query.WriteString(" AND ")
			if err := c.write(current.children[index]); err != nil {
				c.descriptor = sourceDescriptor
				c.qualifier = sourceQualifier
				return fmt.Errorf("orm: SELECT relation predicate %s.%s: %w", plan.sourceName, plan.relationName, err)
			}
		}
		c.descriptor = sourceDescriptor
		c.qualifier = sourceQualifier
	}
	c.query.WriteByte(')')
	return nil
}

func relationPredicateUsesSemiJoinRewrite(plan *relationPredicatePlan, current predicate, negationDepth, disjunctionDepth int) bool {
	return negationDepth == 0 && disjunctionDepth == 0 &&
		len(current.children) != 0 &&
		(plan.relationKind == model.RelationHasMany || plan.relationKind == model.RelationManyToMany)
}

func writeRelationRootAlias(query *strings.Builder) {
	query.WriteString(" AS ")
	writeQuotedIdentifier(query, relationRootAlias)
}

func relationTargetAlias(index int) string {
	switch index {
	case 1:
		return relationTargetAlias1
	case 2:
		return relationTargetAlias2
	default:
		return "tidbgo_r" + strconv.Itoa(index)
	}
}

func relationJunctionAlias(index int) string {
	switch index {
	case 1:
		return relationJunctionAlias1
	case 2:
		return relationJunctionAlias2
	default:
		return "tidbgo_j" + strconv.Itoa(index)
	}
}

func writeAliasedRelationTable(query *strings.Builder, table, alias string) {
	writeQuotedIdentifier(query, table)
	query.WriteString(" AS ")
	writeQuotedIdentifier(query, alias)
}

func writeRelationColumnEqualities(query *strings.Builder, leftQualifier string, leftColumns []string, rightQualifier string, rightColumns []string) {
	for index := range leftColumns {
		if index != 0 {
			query.WriteString(" AND ")
		}
		writeQualifiedIdentifier(query, leftQualifier, leftColumns[index])
		query.WriteString(" = ")
		writeQualifiedIdentifier(query, rightQualifier, rightColumns[index])
	}
}
