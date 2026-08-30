package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
)

const lastServerRUQuery = "SELECT @@tidb_last_query_info"

// ServerRUSession is a connection-pinned database/sql session that can read
// TiDB session state.
//
// Only *sql.Conn and an active *sql.Tx satisfy this constraint. A pooled
// *sql.DB is intentionally excluded because a follow-up query is not
// guaranteed to use the connection that executed the measured statement.
type ServerRUSession interface {
	*sql.Conn | *sql.Tx
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type lastQueryInfo struct {
	RUConsumption *float64 `json:"ru_consumption"`
}

// LastServerRU reads the ServerRU reported by TiDB for the last DML statement
// recorded on session.
//
// Call it immediately after the target DML statement has completed and all
// result rows have been closed. The read adds one database round trip. It
// reports TiDB's session-local ru_consumption value and must not be treated as
// billed RU. For an operation that executes multiple DML statements, it
// reports only the last one.
func LastServerRU[S ServerRUSession](ctx context.Context, session S) (float64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("orm: read last ServerRU with a nil context")
	}
	if nilPredicateArgument(session) {
		return 0, fmt.Errorf("orm: read last ServerRU with a nil session")
	}

	var raw string
	if err := session.QueryRowContext(ctx, lastServerRUQuery).Scan(&raw); err != nil {
		return 0, fmt.Errorf("orm: read last ServerRU: %w", err)
	}
	var info lastQueryInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return 0, fmt.Errorf("orm: decode last ServerRU: %w", err)
	}
	if info.RUConsumption == nil {
		return 0, fmt.Errorf("orm: decode last ServerRU: TiDB did not report ru_consumption")
	}
	if *info.RUConsumption < 0 || math.IsNaN(*info.RUConsumption) || math.IsInf(*info.RUConsumption, 0) {
		return 0, fmt.Errorf("orm: decode last ServerRU: invalid ru_consumption %v", *info.RUConsumption)
	}
	return *info.RUConsumption, nil
}
