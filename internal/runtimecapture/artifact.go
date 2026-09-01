// Package runtimecapture defines the private structured artifacts and offline
// analysis shared by the ORM runtime writer and tidbgo commands.
package runtimecapture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/mayahiro/go-tidb/internal/queryshape"
)

// Version identifies the JSON Lines runtime artifact format.
const Version = 1

const statementFingerprintPrefix = "s1:"

// Source identifies which ORM execution path produced a statement.
type Source string

const (
	SourceTypedSelect   Source = "typed_select"
	SourcePreload       Source = "preload"
	SourceTypedMutation Source = "typed_mutation"
	SourceRaw           Source = "raw"
	SourceTransaction   Source = "transaction"
	SourcePlan          Source = "plan"
	SourceUnknown       Source = "unknown"
)

// Batch identifies one statement within an automatically split ORM operation.
type Batch struct {
	Group      uint64 `json:"group"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	Rows       int    `json:"rows"`
	TotalRows  int    `json:"total_rows"`
	Relation   string `json:"relation,omitempty"`
	LoadAll    bool   `json:"load_all,omitempty"`
	KeyColumns int    `json:"key_columns,omitempty"`
}

// ServerRU records one high-cost automatic same-session diagnostic result.
type ServerRU struct {
	Value                float64 `json:"value,omitempty"`
	Known                bool    `json:"known"`
	DiagnosticDurationNS int64   `json:"diagnostic_duration_ns"`
	AuxiliaryStatements  int     `json:"auxiliary_statements"`
	Error                string  `json:"error,omitempty"`
}

// Record is one completed ORM statement in a runtime capture JSON Lines file.
type Record struct {
	Version           int               `json:"version"`
	CaptureID         string            `json:"capture_id"`
	ScopeID           uint64            `json:"scope_id"`
	Sequence          uint64            `json:"sequence"`
	Operation         string            `json:"operation"`
	Source            Source            `json:"source"`
	Terminal          string            `json:"terminal,omitempty"`
	Model             string            `json:"model,omitempty"`
	Relation          string            `json:"relation,omitempty"`
	Fingerprint       string            `json:"fingerprint"`
	SQL               string            `json:"sql"`
	ArgumentCount     int               `json:"argument_count"`
	StartedAt         time.Time         `json:"started_at"`
	DurationNS        int64             `json:"duration_ns"`
	RowsReturned      int64             `json:"rows_returned,omitempty"`
	RowsReturnedKnown bool              `json:"rows_returned_known"`
	RowsAffected      int64             `json:"rows_affected,omitempty"`
	RowsAffectedKnown bool              `json:"rows_affected_known"`
	Error             string            `json:"error,omitempty"`
	MetadataError     string            `json:"metadata_error,omitempty"`
	Batch             *Batch            `json:"batch,omitempty"`
	Query             *queryshape.Query `json:"query,omitempty"`
	ServerRU          *ServerRU         `json:"server_ru,omitempty"`
}

// StatementFingerprint returns a stable bind-free identity for one SQL
// template when a typed logical query shape is unavailable.
func StatementFingerprint(operation, statement string) string {
	digest := sha256.Sum256([]byte("go-tidb-runtime-statement\x00" + operation + "\x00" + statement))
	var result [len(statementFingerprintPrefix) + sha256.Size*2]byte
	copy(result[:], statementFingerprintPrefix)
	hex.Encode(result[len(statementFingerprintPrefix):], digest[:])
	return string(result[:])
}

// Validate checks the versioned fields required by offline analysis.
func (record Record) Validate() error {
	if record.Version != Version {
		return fmt.Errorf("runtime capture version is %d, want %d", record.Version, Version)
	}
	if record.CaptureID == "" {
		return fmt.Errorf("runtime capture record requires capture_id")
	}
	if record.ScopeID == 0 {
		return fmt.Errorf("runtime capture record requires a positive scope_id")
	}
	if record.Sequence == 0 {
		return fmt.Errorf("runtime capture record requires a positive sequence")
	}
	if record.Operation == "" {
		return fmt.Errorf("runtime capture record requires operation")
	}
	if record.Fingerprint == "" {
		return fmt.Errorf("runtime capture record requires fingerprint")
	}
	if record.DurationNS < 0 {
		return fmt.Errorf("runtime capture record has negative duration_ns")
	}
	if record.ArgumentCount < 0 {
		return fmt.Errorf("runtime capture record has negative argument_count")
	}
	if record.ServerRU != nil {
		if record.ServerRU.DiagnosticDurationNS < 0 {
			return fmt.Errorf("runtime capture record has negative ServerRU diagnostic_duration_ns")
		}
		if record.ServerRU.AuxiliaryStatements < 0 || record.ServerRU.AuxiliaryStatements > 1 {
			return fmt.Errorf("runtime capture record has invalid ServerRU auxiliary statement count")
		}
		if record.ServerRU.Known {
			if record.ServerRU.AuxiliaryStatements != 1 || record.ServerRU.Value < 0 || math.IsNaN(record.ServerRU.Value) || math.IsInf(record.ServerRU.Value, 0) {
				return fmt.Errorf("runtime capture record has invalid known ServerRU value")
			}
		} else {
			if record.ServerRU.Value != 0 {
				return fmt.Errorf("runtime capture record has an unknown nonzero ServerRU value")
			}
			if record.ServerRU.Error == "" {
				return fmt.Errorf("runtime capture record has no ServerRU result or error")
			}
		}
	}
	if record.Batch != nil {
		if record.Batch.Group == 0 || record.Batch.Index < 1 || record.Batch.Count < 1 || record.Batch.Index > record.Batch.Count {
			return fmt.Errorf("runtime capture record has invalid batch position")
		}
		if record.Batch.Rows < 0 || record.Batch.TotalRows < record.Batch.Rows {
			return fmt.Errorf("runtime capture record has invalid batch row counts")
		}
	}
	return nil
}
