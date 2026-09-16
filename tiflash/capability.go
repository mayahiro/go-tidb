package tiflash

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CapabilityState distinguishes demonstrated support, demonstrated absence, and
// an unchecked or failed check. Query failures remain Unknown, including denials.
type CapabilityState string

const (
	// Unknown means the capability was not established; it is the zero value.
	Unknown CapabilityState = ""
	// Supported means the documented probe succeeded for this executor.
	Supported CapabilityState = "supported"
	// Unsupported means a successful metadata query demonstrated absence.
	Unsupported CapabilityState = "unsupported"
)

// Capability contains one explicit probe outcome. Err can contain unredacted
// server text; it is returned only to the caller and is never logged here.
type Capability struct {
	State CapabilityState
	Err   error
}

// Capabilities records narrow probes, not a promise that every table/operator
// can use TiFlash, MPP, or a vector index. No probe enables a feature or creates
// replicas. Zero values are unchecked. MPPSettings only tests setting presence;
// the two Metadata fields only test read access to their metadata tables.
type Capabilities struct {
	ReplicaMetadata     Capability
	MPPSettings         Capability
	WindowFunctions     Capability
	VectorFunctions     Capability
	VectorIndexMetadata Capability
}

// ProbeCapabilities runs five small read-only checks explicitly. It accepts an
// existing executor and changes no session settings. Results retain successful
// probes when another fails; the returned error joins probe errors. Unsupported
// is used only when either MPP setting name is absent from a successful SHOW.
// Pin a connection when checking a particular session or heterogeneous cluster.
func ProbeCapabilities(ctx context.Context, executor QueryExecutor) (result Capabilities, err error) {
	if err := validateExecution(ctx, executor); err != nil {
		return result, err
	}
	probes := []struct {
		target *Capability
		sql    string
	}{
		{&result.ReplicaMetadata, "SELECT REPLICA_COUNT, AVAILABLE, PROGRESS FROM information_schema.TIFLASH_REPLICA LIMIT 0"},
		{&result.MPPSettings, "SHOW VARIABLES WHERE Variable_name IN ('tidb_allow_mpp', 'tidb_enforce_mpp')"},
		{&result.WindowFunctions, "EXPLAIN SELECT ROW_NUMBER() OVER ()"},
		{&result.VectorFunctions, "SELECT VEC_DIMS('[0]')"},
		{&result.VectorIndexMetadata, "SELECT INDEX_NAME FROM information_schema.TIFLASH_INDEXES LIMIT 0"},
	}
	for _, probe := range probes {
		if ctx.Err() != nil {
			return result, errors.Join(err, ctx.Err())
		}
		*probe.target = probeCapability(ctx, executor, probe.sql, probe.target == &result.MPPSettings)
		err = errors.Join(err, probe.target.Err)
	}
	return result, err
}

func probeCapability(ctx context.Context, executor QueryExecutor, statement string, settings bool) (result Capability) {
	rows, err := executor.QueryContext(ctx, statement)
	if err != nil {
		result.Err = fmt.Errorf("tiflash: capability probe: %w", err)
		return result
	}
	if rows == nil {
		result.Err = fmt.Errorf("tiflash: capability probe returned nil rows")
		return result
	}
	defer func() {
		result.Err = errors.Join(result.Err, rows.Close())
		if result.Err != nil {
			result.State = Unknown
		}
	}()
	allow, enforce := false, false
	for rows.Next() {
		if settings {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				result.Err = err
				return result
			}
			allow = allow || strings.EqualFold(name, "tidb_allow_mpp")
			enforce = enforce || strings.EqualFold(name, "tidb_enforce_mpp")
		}
	}
	result.Err = rows.Err()
	if result.Err == nil {
		result.State = Supported
		if settings && (!allow || !enforce) {
			result.State = Unsupported
		}
	}
	return result
}
