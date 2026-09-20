package orm

import (
	"fmt"
	"strings"
)

// StorageEngine names a TiDB table storage engine. The zero value represents
// an unspecified request or an unrecognized/non-storage plan task.
type StorageEngine string

const (
	// TiKV is TiDB's row-oriented storage engine.
	TiKV StorageEngine = "tikv"
	// TiFlash is TiDB's column-oriented storage engine.
	TiFlash StorageEngine = "tiflash"
)

// MPPMode controls statement-level MPP selection. The zero value leaves the
// caller's session settings unchanged. Disabling TiFlash MPP is not supported.
type MPPMode string

const (
	// MPPAuto enables MPP and lets TiDB compare its estimated cost.
	MPPAuto MPPMode = "auto"
	// MPPEnforce enables MPP and bypasses its cost comparison. Unsupported
	// operators, missing replicas, or other constraints can still prevent MPP.
	MPPEnforce MPPMode = "enforce"
)

// ReadPolicy records requested optimizer hints, independently of observed
// execution. Empty fields mean that no corresponding hint was requested.
type ReadPolicy struct {
	Engine    StorageEngine
	MPP       MPPMode
	engineSet bool
	mppSet    bool
}

func (p ReadPolicy) validate() error {
	if (p.engineSet || p.Engine != "") && p.Engine != TiKV && p.Engine != TiFlash {
		return fmt.Errorf("orm: ReadFrom requires TiKV or TiFlash")
	}
	if (p.mppSet || p.MPP != "") && p.MPP != MPPAuto && p.MPP != MPPEnforce {
		return fmt.Errorf("orm: MPP requires MPPAuto or MPPEnforce")
	}
	if p.Engine == TiKV && p.MPP == MPPEnforce {
		return fmt.Errorf("orm: MPPEnforce conflicts with TiKV")
	}
	return nil
}

func (p ReadPolicy) write(sql *strings.Builder) {
	p.writeTables(sql, []string{aggregateRootAlias})
}

func (p ReadPolicy) writeTables(sql *strings.Builder, tables []string) {
	if p.Engine == "" && p.MPP == "" {
		return
	}
	sql.WriteString("/*+ ")
	if p.Engine != "" && len(tables) != 0 {
		sql.WriteString("READ_FROM_STORAGE(")
		sql.WriteString(strings.ToUpper(string(p.Engine)))
		sql.WriteByte('[')
		for i, table := range tables {
			if i != 0 {
				sql.WriteByte(',')
			}
			sql.WriteString(table)
		}
		sql.WriteString("]) ")
	}
	if p.MPP != "" {
		sql.WriteString("SET_VAR(tidb_allow_mpp=1) SET_VAR(tidb_enforce_mpp=")
		if p.MPP == MPPEnforce {
			sql.WriteByte('1')
		} else {
			sql.WriteByte('0')
		}
		sql.WriteString(") ")
	}
	sql.WriteString("*/ ")
}

// PlanTask describes a recognized TiDB task. Kind is root, cop, batchCop, or
// mpp. Known is false for an unsupported task string; a known root task has no
// storage engine. TiFlash access alone does not imply the mpp kind.
type PlanTask struct {
	Engine StorageEngine
	Kind   string
	Known  bool
}

// TaskInfo classifies this planned operator without executing SQL.
func (row ExplainRow) TaskInfo() PlanTask { return parsePlanTask(row.Task) }

// TaskInfo classifies this operator from the explicit EXPLAIN ANALYZE execution.
// It does not describe a preceding or subsequent ordinary SELECT execution.
func (row ExplainAnalyzeRow) TaskInfo() PlanTask { return parsePlanTask(row.Task) }

func parsePlanTask(task string) PlanTask {
	switch task {
	case "root":
		return PlanTask{Kind: "root", Known: true}
	case "cop[tikv]":
		return PlanTask{Engine: TiKV, Kind: "cop", Known: true}
	case "cop[tiflash]":
		return PlanTask{Engine: TiFlash, Kind: "cop", Known: true}
	case "batchCop[tiflash]":
		return PlanTask{Engine: TiFlash, Kind: "batchCop", Known: true}
	case "mpp[tiflash]":
		return PlanTask{Engine: TiFlash, Kind: "mpp", Known: true}
	default:
		return PlanTask{}
	}
}
