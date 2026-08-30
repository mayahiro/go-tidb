package orm

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StatementOperation identifies the logical kind of an executed statement.
type StatementOperation string

const (
	// StatementSelect identifies a model or raw SELECT.
	StatementSelect StatementOperation = "SELECT"
	// StatementExplain identifies a SELECT execution-plan inspection.
	StatementExplain StatementOperation = "EXPLAIN"
	// StatementInsert identifies a single or bulk INSERT.
	StatementInsert StatementOperation = "INSERT"
	// StatementUpsert identifies INSERT ON DUPLICATE KEY UPDATE.
	StatementUpsert StatementOperation = "UPSERT"
	// StatementUpdate identifies an UPDATE.
	StatementUpdate StatementOperation = "UPDATE"
	// StatementDelete identifies a DELETE.
	StatementDelete StatementOperation = "DELETE"
	// StatementExec identifies raw SQL that cannot be classified more narrowly.
	StatementExec StatementOperation = "EXEC"
	// StatementBegin identifies the start of a transaction.
	StatementBegin StatementOperation = "BEGIN"
	// StatementCommit identifies a transaction commit.
	StatementCommit StatementOperation = "COMMIT"
	// StatementRollback identifies a transaction rollback.
	StatementRollback StatementOperation = "ROLLBACK"
)

// StatementEvent describes one attempted database statement after it finishes.
//
// SQL contains the statement template and ArgumentCount contains only the bind
// count. Arguments is nil unless IncludeStatementArguments was explicitly
// enabled. Duration includes scanning and closing rows for SELECT and EXPLAIN
// statements, but excludes observer work.
type StatementEvent struct {
	// Operation is the logical statement kind.
	Operation StatementOperation
	// SQL is the statement template, or the lifecycle name for a transaction.
	SQL string
	// ArgumentCount is the number of bind arguments without their values.
	ArgumentCount int
	// Arguments contains original Go bind values only when explicitly enabled.
	// Values are not interpolated into SQL or converted by the database driver.
	Arguments []any
	// StartedAt is the local time immediately before the database call.
	StartedAt time.Time
	// Duration is the elapsed time through statement completion.
	Duration time.Duration
	// RowsAffected is the database-reported mutation count when known.
	RowsAffected int64
	// RowsAffectedKnown reports whether RowsAffected is available.
	RowsAffectedKnown bool
	// Error is the execution or result-processing error, if any.
	Error error
}

// StatementObserver receives completed statement events synchronously.
// Implementations should return quickly and must provide their own concurrency
// safety when needed.
type StatementObserver func(StatementEvent)

type statementObserverContextKey struct{}

type statementObserverContextValue struct {
	observer         StatementObserver
	includeArguments bool
}

// StatementObserverOption configures statement observation for one context.
type StatementObserverOption interface {
	applyStatementObserver(*statementObserverContextValue)
}

type includeStatementArgumentsOption struct{}

func (includeStatementArgumentsOption) applyStatementObserver(value *statementObserverContextValue) {
	value.includeArguments = true
}

// IncludeStatementArguments includes a snapshot of original Go bind values in
// each StatementEvent and in NewStatementLogger output.
//
// Argument values can contain secrets or personal data. They remain separate
// from SQL and are not driver-converted or interpolated.
func IncludeStatementArguments() StatementObserverOption {
	return includeStatementArgumentsOption{}
}

// WithStatementObserver returns a context that observes ORM statements.
//
// The observer is called once after each attempted SELECT, EXPLAIN, mutation,
// begin, commit, or rollback. Argument values are omitted unless
// IncludeStatementArguments is passed. Passing nil disables an observer
// inherited from ctx.
func WithStatementObserver(ctx context.Context, observer StatementObserver, options ...StatementObserverOption) context.Context {
	value := statementObserverContextValue{observer: observer}
	for _, option := range options {
		if option != nil {
			option.applyStatementObserver(&value)
		}
	}
	return context.WithValue(ctx, statementObserverContextKey{}, value)
}

type statementObservation struct {
	observer StatementObserver
	event    StatementEvent
}

func beginStatementObservation(ctx context.Context, operation StatementOperation, statement string, arguments []any) *statementObservation {
	value, _ := ctx.Value(statementObserverContextKey{}).(statementObserverContextValue)
	if value.observer == nil {
		return nil
	}
	result := &statementObservation{
		observer: value.observer,
		event: StatementEvent{
			Operation:     operation,
			SQL:           statement,
			ArgumentCount: len(arguments),
			StartedAt:     time.Now(),
		},
	}
	if value.includeArguments {
		result.event.Arguments = append([]any(nil), arguments...)
	}
	return result
}

func (observation *statementObservation) finish(affected int64, affectedKnown bool, err error) {
	if observation == nil || observation.observer == nil {
		return
	}
	observation.event.Duration = time.Since(observation.event.StartedAt)
	observation.event.RowsAffected = affected
	observation.event.RowsAffectedKnown = affectedKnown
	observation.event.Error = err
	observer := observation.observer
	observation.observer = nil
	observer(observation.event)
}

func finishMutationStatementObservation(observation *statementObservation, result sql.Result, operation, modelName string) (int64, error) {
	affected, err := mutationRowsAffected(result, operation, modelName)
	observation.finish(affected, err == nil, err)
	return affected, err
}

type observedQueryRows struct {
	*sql.Rows
	observation *statementObservation
}

func (rows *observedQueryRows) finishStatementObservation(err error) {
	rows.observation.finish(0, false, err)
	rows.observation = nil
}

func finishRowsStatementObservation(rows resultRows, err error) {
	observed, ok := rows.(interface{ finishStatementObservation(error) })
	if ok {
		observed.finishStatementObservation(err)
	}
}

func inferStatementOperation(statement string) StatementOperation {
	trimmed := strings.TrimLeft(statement, " \t\r\n")
	end := 0
	for end < len(trimmed) {
		character := trimmed[end]
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			break
		}
		end++
	}
	switch strings.ToUpper(trimmed[:end]) {
	case "INSERT":
		return StatementInsert
	case "UPDATE":
		return StatementUpdate
	case "DELETE":
		return StatementDelete
	default:
		return StatementExec
	}
}

type statementLogger struct {
	writer io.Writer
	color  bool
	mutex  sync.Mutex
}

// NewStatementLogger returns a concurrency-safe observer that writes one line
// per statement.
//
// It logs the SQL template and bind count. Bind values are logged only when
// IncludeStatementArguments configures the context. ANSI colors are enabled
// automatically for a character-device *os.File and disabled for other writers
// such as files and buffers. Write failures do not change query results.
func NewStatementLogger(writer io.Writer) StatementObserver {
	if writer == nil {
		return func(StatementEvent) {}
	}
	logger := &statementLogger{writer: writer, color: statementLogColorEnabled(writer)}
	return logger.observe
}

func statementLogColorEnabled(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (logger *statementLogger) observe(event StatementEvent) {
	var formattedArguments []byte
	if event.Arguments != nil {
		formattedArguments = formatStatementArguments(event.Arguments)
	}
	var line strings.Builder
	line.Grow(len(event.SQL) + len(formattedArguments) + 96)
	line.WriteString("[tidbgo] ")
	line.WriteString(event.StartedAt.Format("15:04:05.000"))
	line.WriteByte(' ')
	logger.writeOperation(&line, event.Operation)
	for padding := len(event.Operation); padding < len(StatementRollback); padding++ {
		line.WriteByte(' ')
	}
	line.WriteByte(' ')
	line.WriteString(event.Duration.String())
	line.WriteString(" args=")
	line.WriteString(strconv.Itoa(event.ArgumentCount))
	if formattedArguments != nil {
		line.WriteString(" values=")
		writeStatementLogBytes(&line, formattedArguments)
	}
	if event.RowsAffectedKnown {
		line.WriteString(" affected=")
		line.WriteString(strconv.FormatInt(event.RowsAffected, 10))
	}
	if event.SQL != "" {
		line.WriteByte(' ')
		writeStatementLogText(&line, event.SQL)
	}
	if event.Error != nil {
		line.WriteString(" error=")
		if logger.color {
			line.WriteString("\x1b[31m")
		}
		writeStatementLogText(&line, event.Error.Error())
		if logger.color {
			line.WriteString("\x1b[0m")
		}
	}
	line.WriteByte('\n')

	logger.mutex.Lock()
	_, _ = io.WriteString(logger.writer, line.String())
	logger.mutex.Unlock()
}

func formatStatementArguments(arguments []any) []byte {
	result := make([]byte, 0, len(arguments)*16+2)
	result = append(result, '[')
	for index, argument := range arguments {
		if index != 0 {
			result = append(result, ',', ' ')
		}
		result = fmt.Appendf(result, "%#v", argument)
	}
	return append(result, ']')
}

func writeStatementLogText(line *strings.Builder, value string) {
	const hexadecimal = "0123456789abcdef"
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch character {
		case '\n':
			line.WriteString("\\n")
		case '\r':
			line.WriteString("\\r")
		case '\t':
			line.WriteString("\\t")
		default:
			if character < 0x20 || character == 0x7f {
				line.WriteString("\\x")
				line.WriteByte(hexadecimal[character>>4])
				line.WriteByte(hexadecimal[character&0x0f])
				continue
			}
			line.WriteByte(character)
		}
	}
}

func writeStatementLogBytes(line *strings.Builder, value []byte) {
	const hexadecimal = "0123456789abcdef"
	for _, character := range value {
		switch character {
		case '\n':
			line.WriteString("\\n")
		case '\r':
			line.WriteString("\\r")
		case '\t':
			line.WriteString("\\t")
		default:
			if character < 0x20 || character == 0x7f {
				line.WriteString("\\x")
				line.WriteByte(hexadecimal[character>>4])
				line.WriteByte(hexadecimal[character&0x0f])
				continue
			}
			line.WriteByte(character)
		}
	}
}

func (logger *statementLogger) writeOperation(line *strings.Builder, operation StatementOperation) {
	if !logger.color {
		line.WriteString(string(operation))
		return
	}
	line.WriteString(statementOperationColor(operation))
	line.WriteString(string(operation))
	line.WriteString("\x1b[0m")
}

func statementOperationColor(operation StatementOperation) string {
	switch operation {
	case StatementSelect:
		return "\x1b[32m"
	case StatementExplain:
		return "\x1b[96m"
	case StatementInsert:
		return "\x1b[34m"
	case StatementUpsert:
		return "\x1b[36m"
	case StatementUpdate:
		return "\x1b[33m"
	case StatementDelete:
		return "\x1b[35m"
	default:
		return "\x1b[90m"
	}
}
