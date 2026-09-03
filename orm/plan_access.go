package orm

import (
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

const fixedPlanAccessBindingCount = 4

type planAccessBinding struct {
	alias         string
	physicalTable string
	model         string
	relationPath  string
}

type planAccessResolver struct {
	root       planAccessBinding
	hasRoot    bool
	fixed      [fixedPlanAccessBindingCount]planAccessBinding
	fixedCount int
	additional []planAccessBinding
}

type resolvedPlanAccess struct {
	physicalTable string
	model         string
	relationPath  string
}

func compilePlanAccessResolver(descriptor *model.Descriptor, selection *selectQuery, compiled compiledSelect, resolver *planAccessResolver) error {
	*resolver = planAccessResolver{
		root: planAccessBinding{
			physicalTable: descriptor.TableName(),
			model:         descriptor.Name(),
		},
		hasRoot: true,
	}
	if compiled.statement.qualifier != "" {
		resolver.root.alias = compiled.statement.qualifier
	}
	resolver.appendInlinePreloads(compiled.statement.inlinePreloads, "")

	relationTopN, err := analyzeRelationTopN(descriptor, selection)
	if err != nil {
		return err
	}
	if relationTopN.optimized {
		metadata := relationTopN.plan.metadata
		if metadata.junction == nil {
			resolver.add(planAccessBinding{
				alias:         relationTopNAssociationAlias,
				physicalTable: metadata.target.TableName(),
				model:         metadata.target.Name(),
				relationPath:  metadata.relationName,
			})
		} else {
			resolver.add(planAccessBinding{
				alias:         relationTopNAssociationAlias,
				physicalTable: metadata.junction.tableName,
				relationPath:  metadata.relationName,
			})
			if !relationTopN.plan.junctionOnly {
				resolver.add(planAccessBinding{
					alias:         relationTopNManyTargetAlias,
					physicalTable: metadata.target.TableName(),
					model:         metadata.target.Name(),
					relationPath:  metadata.relationName,
				})
			}
		}
		nextAlias := 0
		if err := resolver.appendRelationPredicates(metadata.target, relationTopN.plan.predicate.children, metadata.relationName, &nextAlias); err != nil {
			return err
		}
		return nil
	}

	nextAlias := 0
	if err := resolver.appendRelationPredicates(descriptor, selection.predicates, "", &nextAlias); err != nil {
		return err
	}
	return nil
}

func (r *planAccessResolver) appendInlinePreloads(plans []*preloadPlan, parentPath string) {
	for _, plan := range plans {
		path := joinRelationPath(parentPath, plan.relationName)
		r.add(planAccessBinding{
			alias:         plan.targetAlias,
			physicalTable: plan.targetTable,
			model:         plan.targetType.Name(),
			relationPath:  path,
		})
		r.appendInlinePreloads(plan.inlineChildren, path)
	}
}

func (r *planAccessResolver) appendRelationPredicates(
	descriptor *model.Descriptor,
	predicates []predicate,
	parentPath string,
	nextAlias *int,
) error {
	for index := range predicates {
		current := predicates[index]
		if current.operator != predicateHasRelation {
			if err := r.appendRelationPredicates(descriptor, current.children, parentPath, nextAlias); err != nil {
				return err
			}
			continue
		}

		plan, err := relationPredicatePlanFor(descriptor, current.field)
		if err != nil {
			return err
		}
		(*nextAlias)++
		aliasIndex := *nextAlias
		path := joinRelationPath(parentPath, plan.relationName)
		r.add(planAccessBinding{
			alias:         relationTargetAlias(aliasIndex),
			physicalTable: plan.target.TableName(),
			model:         plan.target.Name(),
			relationPath:  path,
		})
		if plan.junction != nil {
			r.add(planAccessBinding{
				alias:         relationJunctionAlias(aliasIndex),
				physicalTable: plan.junction.tableName,
				relationPath:  path,
			})
		}
		if err := r.appendRelationPredicates(plan.target, current.children, path, nextAlias); err != nil {
			return err
		}
	}
	return nil
}

func (r *planAccessResolver) add(binding planAccessBinding) {
	if r.fixedCount < len(r.fixed) {
		r.fixed[r.fixedCount] = binding
		r.fixedCount++
		return
	}
	r.additional = append(r.additional, binding)
}

func joinRelationPath(parent, relation string) string {
	if parent == "" {
		return relation
	}
	return parent + "." + relation
}

func (r *planAccessResolver) resolve(accessObject string) resolvedPlanAccess {
	table := planAccessObjectTable(accessObject)
	if table == "" {
		return resolvedPlanAccess{}
	}
	if resolved, ok := r.resolveName(table, true); ok {
		return resolved
	}
	resolved, _ := r.resolveName(table, false)
	return resolved
}

func (r *planAccessResolver) resolveName(name string, alias bool) (resolvedPlanAccess, bool) {
	var result resolvedPlanAccess
	matched := false
	if r.hasRoot && planAccessBindingMatches(r.root, name, alias) {
		result = mergeResolvedPlanAccess(result, r.root, matched)
		matched = true
	}
	for index := 0; index < r.fixedCount; index++ {
		binding := r.fixed[index]
		if !planAccessBindingMatches(binding, name, alias) {
			continue
		}
		result = mergeResolvedPlanAccess(result, binding, matched)
		matched = true
	}
	for index := range r.additional {
		binding := r.additional[index]
		if !planAccessBindingMatches(binding, name, alias) {
			continue
		}
		result = mergeResolvedPlanAccess(result, binding, matched)
		matched = true
	}
	return result, matched
}

func planAccessBindingMatches(binding planAccessBinding, name string, alias bool) bool {
	if alias {
		return binding.alias != "" && binding.alias == name
	}
	return binding.physicalTable == name
}

func mergeResolvedPlanAccess(current resolvedPlanAccess, binding planAccessBinding, exists bool) resolvedPlanAccess {
	if !exists {
		return resolvedPlanAccess{
			physicalTable: binding.physicalTable,
			model:         binding.model,
			relationPath:  binding.relationPath,
		}
	}
	if current.physicalTable != binding.physicalTable {
		current.physicalTable = ""
	}
	if current.model != binding.model {
		current.model = ""
	}
	if current.relationPath != binding.relationPath {
		current.relationPath = ""
	}
	return current
}

func planAccessObjectTable(accessObject string) string {
	remaining := accessObject
	tableName := ""
	foundTable := false
	for {
		part, rest, found := strings.Cut(remaining, ",")
		part = strings.TrimSpace(part)
		if table, ok := strings.CutPrefix(part, "table:"); ok {
			if foundTable {
				return ""
			}
			tableName = strings.TrimSpace(table)
			foundTable = true
		}
		if !found {
			return tableName
		}
		remaining = rest
	}
}
