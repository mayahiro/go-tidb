package orm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
)

var (
	runtimeCaptureFallbackID atomic.Uint64
	runtimeStatementGroupID  atomic.Uint64
)

// RuntimeCapture writes bind-free completed statement records as JSON Lines
// for later offline analysis.
//
// A RuntimeCapture is safe for concurrent use by multiple operation scopes.
// Writer errors never replace database results and are available through Err.
type RuntimeCapture struct {
	writer    io.Writer
	id        string
	nextScope atomic.Uint64
	mutex     sync.Mutex
	err       error
}

// NewRuntimeCapture creates a reusable structured statement capture.
//
// The caller owns writer and its lifecycle. A nil writer discards records.
func NewRuntimeCapture(writer io.Writer) *RuntimeCapture {
	if writer == nil {
		writer = io.Discard
	}
	return &RuntimeCapture{writer: writer, id: newRuntimeCaptureID()}
}

// WithRuntimeCapture returns a context that records completed go-tidb
// statements through capture.
//
// Call it once at a request, job, or test-operation boundary and pass the
// derived context through existing ORM terminals. It preserves an inherited
// StatementObserver and never enables bind argument capture. A later
// WithStatementObserver call also preserves the capture. Installing another
// RuntimeCapture on the derived context replaces the inherited capture.
func WithRuntimeCapture(ctx context.Context, capture *RuntimeCapture) context.Context {
	if capture == nil {
		return ctx
	}
	value := &statementObserverContextValue{}
	if parent := statementObserverContext(ctx); parent != nil {
		*value = *parent
	}
	value.runtimeCapture = capture
	value.runtimeScope = &statementRuntimeScope{id: capture.nextScope.Add(1)}
	return context.WithValue(ctx, statementObserverContextKey{}, value)
}

// Err returns the first artifact encoding or writer error observed by capture.
//
// It returns nil while all completed records have been written successfully.
func (capture *RuntimeCapture) Err() error {
	if capture == nil {
		return nil
	}
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return capture.err
}

func (capture *RuntimeCapture) observe(event StatementEvent, runtimeEvent *statementRuntimeEvent, rowsReturned int64, rowsReturnedKnown bool) {
	if runtimeEvent == nil || capture == nil {
		return
	}
	metadata := runtimeEvent.metadata
	source := metadata.source
	if source == "" {
		source = inferRuntimeCaptureSource(event.Operation)
	}
	var fingerprint string
	if metadata.query != nil {
		fingerprint = metadata.query.Fingerprint()
	} else {
		fingerprint = runtimecapture.StatementFingerprint(string(event.Operation), event.SQL)
	}
	record := runtimecapture.Record{
		Version:           runtimecapture.Version,
		CaptureID:         capture.id,
		ScopeID:           runtimeEvent.scopeID,
		Sequence:          runtimeEvent.sequence,
		Operation:         string(event.Operation),
		Source:            source,
		Terminal:          metadata.terminal,
		Model:             metadata.model,
		Relation:          metadata.relation,
		Fingerprint:       fingerprint,
		SQL:               event.SQL,
		ArgumentCount:     event.ArgumentCount,
		StartedAt:         event.StartedAt,
		DurationNS:        event.Duration.Nanoseconds(),
		RowsReturned:      rowsReturned,
		RowsReturnedKnown: rowsReturnedKnown,
		RowsAffected:      event.RowsAffected,
		RowsAffectedKnown: event.RowsAffectedKnown,
		MetadataError:     metadata.metadataError,
		Batch:             metadata.batch,
		Query:             metadata.query,
	}
	if event.Error != nil {
		record.Error = event.Error.Error()
	}
	encoded, err := json.Marshal(record)
	if err == nil {
		encoded = append(encoded, '\n')
	}

	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	if capture.err != nil {
		return
	}
	if err != nil {
		capture.err = fmt.Errorf("orm: encode runtime capture record: %w", err)
		return
	}
	written, writeErr := capture.writer.Write(encoded)
	if writeErr == nil && written != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		capture.err = fmt.Errorf("orm: write runtime capture record: %w", writeErr)
	}
}

func inferRuntimeCaptureSource(operation StatementOperation) runtimecapture.Source {
	switch operation {
	case StatementBegin, StatementCommit, StatementRollback:
		return runtimecapture.SourceTransaction
	case StatementExplain, StatementExplainAnalyze:
		return runtimecapture.SourcePlan
	default:
		return runtimecapture.SourceUnknown
	}
}

func newRuntimeCaptureID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	sequence := runtimeCaptureFallbackID.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(sequence, 36)
}

func nextRuntimeStatementGroupWhen(enabled bool) uint64 {
	if !enabled {
		return 0
	}
	return runtimeStatementGroupID.Add(1)
}

func runtimeBatchMetadata(group uint64, index, count, rows, totalRows int) *runtimecapture.Batch {
	if group == 0 {
		return nil
	}
	return &runtimecapture.Batch{
		Group:     group,
		Index:     index,
		Count:     count,
		Rows:      rows,
		TotalRows: totalRows,
	}
}
