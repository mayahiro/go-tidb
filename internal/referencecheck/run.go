package referencecheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Queryer supplies the caller-owned read connection or transaction.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Result reports one probe. Checked is false after an error or when execution
// stopped before this reference. Orphan means at least one orphan, not a count.
type Result struct {
	Reference string `json:"reference"`
	Checked   bool   `json:"checked"`
	Orphan    bool   `json:"orphan"`
}

// OperationError preserves a database cause for callers without including
// server messages, credentials, SQL, or row values in Error.
type OperationError struct {
	Reference string
	cause     error
}

// Error returns a value-free operation summary.
func (e *OperationError) Error() string {
	return fmt.Sprintf("reference audit could not complete %q", e.Reference)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *OperationError) Unwrap() error { return e.cause }

// Run sequentially executes read-only probes under a required deadline. Each
// SELECT observes its own snapshot unless the caller supplies a transaction.
// The function never changes transaction, session, schema, or application data.
func Run(ctx context.Context, executor Queryer, refs []Reference) ([]Result, error) {
	if ctx == nil || executor == nil {
		return nil, errors.New("reference audit requires a context and query executor")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("reference audit requires a context deadline")
	}
	queries := make([]string, len(refs))
	results := make([]Result, len(refs))
	for i, ref := range refs {
		query, err := SQL(ref)
		if err != nil {
			return nil, err
		}
		queries[i] = query
		results[i].Reference = ref.Name
	}
	for i, query := range queries {
		if err := ctx.Err(); err != nil {
			return results, &OperationError{Reference: refs[i].Name, cause: err}
		}
		orphan, err := probe(ctx, executor, query)
		if err != nil {
			return results, &OperationError{Reference: refs[i].Name, cause: err}
		}
		results[i].Checked = true
		results[i].Orphan = orphan
	}
	return results, nil
}

func probe(ctx context.Context, executor Queryer, query string) (bool, error) {
	rows, err := executor.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, errors.New("orphan probe returned no result")
	}
	var orphan bool
	if err := rows.Scan(&orphan); err != nil {
		return false, err
	}
	if rows.Next() {
		return false, errors.New("orphan probe returned multiple results")
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	return orphan, nil
}
