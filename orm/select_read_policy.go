package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

func (q *selectQuery) policy() ReadPolicy {
	if q.readPolicy == nil {
		return ReadPolicy{}
	}
	return *q.readPolicy
}

func (q *selectQuery) validatePolicy() error {
	if q.readPolicy == nil {
		return nil
	}
	if err := q.readPolicy.validate(); err != nil {
		return err
	}
	if q.forceIndexSet && q.readPolicy.Engine == TiFlash {
		return fmt.Errorf("orm: ReadFrom(TiFlash) conflicts with ForceIndex")
	}
	return nil
}

// This receives completed compiler-owned SQL and explicit physical aliases,
// never guesses table names from SQL text, and never changes a cached statement.
func prependReadPolicy(statement string, policy ReadPolicy, tables []string) string {
	if policy.Engine == "" && policy.MPP == "" {
		return statement
	}
	var hint strings.Builder
	policy.writeTables(&hint, tables)
	text := hint.String()
	const selectPrefix = "SELECT "
	rest := strings.TrimPrefix(statement, selectPrefix)
	if strings.HasPrefix(rest, "/*+") {
		// All existing hints here are emitted by this compiler.
		end := strings.Index(rest, "*/")
		return selectPrefix + strings.TrimSuffix(text, "*/ ") + strings.TrimSpace(rest[3:end]) + " */" + rest[end+2:]
	}
	return selectPrefix + text + rest
}

func inlinePolicyTables(tables []string, plans []*preloadPlan) []string {
	for _, plan := range plans {
		tables = append(tables, plan.targetAlias)
		tables = inlinePolicyTables(tables, plan.inlineChildren)
	}
	return tables
}

func applySelectReadPolicy(source *model.Descriptor, query *selectQuery, compiled *compiledSelect) {
	root := compiled.statement.qualifier
	if root == "" {
		root = source.TableName()
	}
	tables := inlinePolicyTables([]string{root}, compiled.statement.inlinePreloads)
	statement := *compiled.statement
	statement.sql = prependReadPolicy(statement.sql, query.policy(), tables)
	compiled.statement = &statement
	applyPreloadReadPolicy(compiled.preloads, query.readPolicy)
}

func applyPreloadReadPolicy(plans []*preloadPlan, policy *ReadPolicy) {
	for _, plan := range plans {
		// compilePreloadNode detaches each plan from the metadata cache.
		plan.readPolicy = policy
		applyPreloadReadPolicy(plan.children, policy)
	}
}

func preloadReadPolicy(statement string, plan *preloadPlan) string {
	if plan.readPolicy == nil {
		return statement
	}
	var tables []string
	if plan.junction != nil {
		tables = []string{"j", "t"}
	} else {
		root := plan.targetStatement.qualifier
		if root == "" {
			root = plan.targetTable
		}
		tables = []string{root}
	}
	tables = inlinePolicyTables(tables, plan.targetStatement.inlinePreloads)
	return prependReadPolicy(statement, *plan.readPolicy, tables)
}
