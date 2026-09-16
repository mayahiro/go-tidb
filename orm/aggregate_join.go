package orm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

type aggregateJoin struct {
	path, alias, sourceAlias string
	plan                     *relationPredicatePlan
}

type aggregateJoins []aggregateJoin

func (joins *aggregateJoins) resolve(source *model.Descriptor, fieldPath string) (model.Field, string, error) {
	alias, path := aggregateRootAlias, ""
	for {
		name, rest, dotted := strings.Cut(fieldPath, ".")
		if !dotted {
			field, err := aggregateSourceField(source, name)
			return field, alias, err
		}
		path = joinRelationPath(path, name)
		found := false
		for _, join := range *joins {
			if join.path == path {
				source, alias, found = join.plan.target, join.alias, true
				break
			}
		}
		if !found {
			relation, ok := source.RelationByName(name)
			if !ok || relation.IsCollection() {
				return model.Field{}, "", fmt.Errorf("orm: aggregate path %s requires a mapped to-one relation", path)
			}
			plan, err := relationPredicatePlanFor(source, name)
			if err != nil {
				return model.Field{}, "", err
			}
			if !relationTopNMatchesUniqueKey(relationFieldGoNames(relation.TargetKey()), relationTopNUniqueGoNames(plan.target)) {
				return model.Field{}, "", fmt.Errorf("orm: aggregate path %s requires a declared primary or unique target key", path)
			}
			join := aggregateJoin{path: path, alias: "tidbgo_a" + strconv.Itoa(len(*joins)+1), sourceAlias: alias, plan: plan}
			*joins = append(*joins, join)
			source, alias = plan.target, join.alias
		}
		fieldPath = rest
	}
}

func (joins aggregateJoins) write(sql *strings.Builder) {
	for _, join := range joins {
		sql.WriteString(" LEFT JOIN ")
		writeAliasedRelationTable(sql, join.plan.target.TableName(), join.alias)
		sql.WriteString(" ON (")
		writeRelationColumnEqualities(sql, join.alias, join.plan.targetColumns, join.sourceAlias, join.plan.sourceColumns)
		if join.plan.softDeleteColumn != "" {
			sql.WriteString(" AND ")
			writePreloadSoftDeletePredicate(sql, join.alias, join.plan.softDeleteColumn)
		}
		sql.WriteByte(')')
	}
}

func (joins aggregateJoins) writePolicy(sql *strings.Builder, policy ReadPolicy) {
	if len(joins) == 0 || policy.Engine == "" {
		policy.write(sql)
		return
	}
	aliases := make([]string, 1, len(joins)+1)
	aliases[0] = aggregateRootAlias
	for _, join := range joins {
		aliases = append(aliases, join.alias)
	}
	policy.writeTables(sql, aliases)
}
