package orm

import (
	"errors"
	"time"
)

// ServerRUObservation describes one opt-in same-session ServerRU collection
// attempt associated with a completed target statement.
//
// DiagnosticDuration covers connection pinning, the auxiliary ServerRU query,
// and release of an internally pinned connection. AuxiliaryStatements counts
// only attempted diagnostic SQL statements. Error never replaces the target
// statement result.
type ServerRUObservation struct {
	// Value is TiDB's reported ru_consumption when Known is true.
	Value float64
	// Known reports whether Value was decoded successfully.
	Known bool
	// DiagnosticDuration is time added by automatic ServerRU collection.
	DiagnosticDuration time.Duration
	// AuxiliaryStatements is the number of diagnostic SQL statements attempted.
	AuxiliaryStatements int
	// Error is a connection-pinning, query, decode, or connection-release error.
	Error error
}

func statementServerRUCollectionEnabled(value *statementObserverContextValue) bool {
	if value == nil {
		return false
	}
	observerEnabled := value.observer != nil && value.options&statementObserverCollectServerRU != 0
	captureEnabled := value.runtimeCapture != nil && value.options&statementRuntimeCollectServerRU != 0
	return observerEnabled || captureEnabled
}

func serverRUStatementOperation(operation StatementOperation) bool {
	switch operation {
	case StatementSelect, StatementInsert, StatementUpsert, StatementUpdate, StatementDelete:
		return true
	default:
		return false
	}
}

func (observation *statementObservation) addServerRUError(err error) {
	if observation == nil || observation.event.ServerRU == nil || err == nil {
		return
	}
	observation.event.ServerRU.Error = errors.Join(observation.event.ServerRU.Error, err)
}
