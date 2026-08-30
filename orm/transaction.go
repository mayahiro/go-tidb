package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TransactionBeginner starts a database/sql transaction and is implemented by
// *sql.DB and *sql.Conn.
type TransactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

var (
	_ TransactionBeginner = (*sql.DB)(nil)
	_ TransactionBeginner = (*sql.Conn)(nil)
)

// Transaction begins a transaction with default database/sql options, runs
// callback, and commits only when callback returns nil.
//
// Transaction rolls back when callback returns an error or panics. A callback
// error is returned unchanged when rollback succeeds; a rollback failure is
// joined to it. A panic is propagated after rollback. Transaction never
// retries callback, and callback must not commit or roll back the supplied
// *sql.Tx itself. Use BeginTx directly when custom sql.TxOptions are required.
func Transaction(ctx context.Context, beginner TransactionBeginner, callback func(*sql.Tx) error) error {
	if ctx == nil {
		return fmt.Errorf("orm: begin transaction with a nil context")
	}
	if nilPredicateArgument(beginner) {
		return fmt.Errorf("orm: begin transaction with a nil beginner")
	}
	if callback == nil {
		return fmt.Errorf("orm: begin transaction with a nil callback")
	}

	beginObservation := beginStatementObservation(ctx, StatementBegin, "BEGIN", nil)
	transaction, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		err = fmt.Errorf("orm: begin transaction: %w", err)
		beginObservation.finish(0, false, err)
		return err
	}
	if transaction == nil {
		err = fmt.Errorf("orm: begin transaction: beginner returned a nil transaction")
		beginObservation.finish(0, false, err)
		return err
	}
	completed := false
	defer func() {
		if completed {
			_ = transaction.Rollback()
			return
		}
		rollbackObservation := beginStatementObservation(ctx, StatementRollback, "ROLLBACK", nil)
		rollbackErr := transaction.Rollback()
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("orm: roll back transaction: %w", rollbackErr)
		}
		rollbackObservation.finish(0, false, rollbackErr)
	}()
	beginObservation.finish(0, false, nil)

	if err := callback(transaction); err != nil {
		rollbackObservation := beginStatementObservation(ctx, StatementRollback, "ROLLBACK", nil)
		rollbackErr := transaction.Rollback()
		completed = true
		if rollbackErr == nil {
			rollbackObservation.finish(0, false, nil)
			return err
		}
		rollbackErr = fmt.Errorf("orm: roll back transaction: %w", rollbackErr)
		rollbackObservation.finish(0, false, rollbackErr)
		return errors.Join(err, rollbackErr)
	}
	commitObservation := beginStatementObservation(ctx, StatementCommit, "COMMIT", nil)
	commitErr := transaction.Commit()
	completed = true
	if commitErr != nil {
		err = fmt.Errorf("orm: commit transaction: %w", commitErr)
		commitObservation.finish(0, false, err)
		return err
	}
	commitObservation.finish(0, false, nil)
	return nil
}
