package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

type transactionTestState struct {
	beginErr       error
	commitErr      error
	rollbackErr    error
	execErr        error
	beginCalls     int
	commitCalls    int
	rollbackCalls  int
	execCalls      int
	beginOptions   driver.TxOptions
	callbackTxSeen *sql.Tx
}

type transactionTestConnector struct {
	state *transactionTestState
}

func (connector *transactionTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &transactionTestConn{state: connector.state}, nil
}

func (*transactionTestConnector) Driver() driver.Driver {
	return transactionTestDriver{}
}

type transactionTestDriver struct{}

func (transactionTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("transaction test driver requires OpenDB")
}

type transactionTestConn struct {
	state *transactionTestState
}

func (*transactionTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*transactionTestConn) Close() error {
	return nil
}

func (connection *transactionTestConn) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}

func (connection *transactionTestConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.state.beginCalls++
	connection.state.beginOptions = options
	if connection.state.beginErr != nil {
		return nil, connection.state.beginErr
	}
	return &transactionTestTx{state: connection.state}, nil
}

func (connection *transactionTestConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	connection.state.execCalls++
	if connection.state.execErr != nil {
		return nil, connection.state.execErr
	}
	return driver.RowsAffected(1), nil
}

type transactionTestTx struct {
	state *transactionTestState
}

func (transaction *transactionTestTx) Commit() error {
	transaction.state.commitCalls++
	return transaction.state.commitErr
}

func (transaction *transactionTestTx) Rollback() error {
	transaction.state.rollbackCalls++
	return transaction.state.rollbackErr
}

type nilTransactionBeginner struct{}

func (nilTransactionBeginner) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	return nil, nil
}

func TestTransactionCommitsSuccessfulCallback(t *testing.T) {
	t.Parallel()

	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)
	ctx := context.Background()

	err := Transaction(ctx, database, func(transaction *sql.Tx) error {
		state.callbackTxSeen = transaction
		result, err := transaction.ExecContext(ctx, "UPDATE counters SET value = value + 1")
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			t.Fatalf("RowsAffected() = %d, want 1", affected)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction() error = %v", err)
	}
	if state.callbackTxSeen == nil {
		t.Fatal("Transaction() callback received a nil transaction")
	}
	if state.beginCalls != 1 || state.execCalls != 1 || state.commitCalls != 1 || state.rollbackCalls != 0 {
		t.Fatalf(
			"lifecycle calls = begin:%d exec:%d commit:%d rollback:%d, want 1, 1, 1, 0",
			state.beginCalls,
			state.execCalls,
			state.commitCalls,
			state.rollbackCalls,
		)
	}
	if state.beginOptions != (driver.TxOptions{}) {
		t.Fatalf("BeginTx() options = %#v, want default options", state.beginOptions)
	}
}

func TestTransactionAcceptsSQLConn(t *testing.T) {
	t.Parallel()

	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("DB.Conn() error = %v", err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Errorf("Conn.Close() error = %v", err)
		}
	})

	err = Transaction(context.Background(), connection, func(*sql.Tx) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction() error = %v", err)
	}
	if state.beginCalls != 1 || state.commitCalls != 1 || state.rollbackCalls != 0 {
		t.Fatalf(
			"lifecycle calls = begin:%d commit:%d rollback:%d, want 1, 1, 0",
			state.beginCalls,
			state.commitCalls,
			state.rollbackCalls,
		)
	}
}

func TestTransactionRollsBackCallbackError(t *testing.T) {
	t.Parallel()

	callbackErr := errors.New("callback failed")
	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)

	err := Transaction(context.Background(), database, func(*sql.Tx) error {
		return callbackErr
	})
	if err != callbackErr {
		t.Fatalf("Transaction() error = %v, want unchanged callback error", err)
	}
	if state.beginCalls != 1 || state.commitCalls != 0 || state.rollbackCalls != 1 {
		t.Fatalf(
			"lifecycle calls = begin:%d commit:%d rollback:%d, want 1, 0, 1",
			state.beginCalls,
			state.commitCalls,
			state.rollbackCalls,
		)
	}
}

func TestTransactionJoinsCallbackAndRollbackErrors(t *testing.T) {
	t.Parallel()

	callbackErr := errors.New("callback failed")
	rollbackErr := errors.New("rollback failed")
	state := &transactionTestState{rollbackErr: rollbackErr}
	database := openTransactionTestDB(t, state)

	err := Transaction(context.Background(), database, func(*sql.Tx) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("Transaction() error = %v, want callback and rollback errors", err)
	}
	if !strings.Contains(err.Error(), "orm: roll back transaction") {
		t.Fatalf("Transaction() error = %q, want rollback context", err)
	}
	if state.commitCalls != 0 || state.rollbackCalls != 1 {
		t.Fatalf("lifecycle calls = commit:%d rollback:%d, want 0, 1", state.commitCalls, state.rollbackCalls)
	}
}

func TestTransactionReportsBeginAndCommitErrors(t *testing.T) {
	t.Parallel()

	t.Run("begin", func(t *testing.T) {
		beginErr := errors.New("begin failed")
		state := &transactionTestState{beginErr: beginErr}
		database := openTransactionTestDB(t, state)
		callbackCalls := 0

		err := Transaction(context.Background(), database, func(*sql.Tx) error {
			callbackCalls++
			return nil
		})
		if !errors.Is(err, beginErr) || !strings.Contains(err.Error(), "orm: begin transaction") {
			t.Fatalf("Transaction() error = %v, want wrapped begin error", err)
		}
		if callbackCalls != 0 || state.commitCalls != 0 || state.rollbackCalls != 0 {
			t.Fatalf(
				"calls = callback:%d commit:%d rollback:%d, want 0, 0, 0",
				callbackCalls,
				state.commitCalls,
				state.rollbackCalls,
			)
		}
	})

	t.Run("commit", func(t *testing.T) {
		commitErr := errors.New("commit failed")
		state := &transactionTestState{commitErr: commitErr}
		database := openTransactionTestDB(t, state)

		err := Transaction(context.Background(), database, func(*sql.Tx) error {
			return nil
		})
		if !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "orm: commit transaction") {
			t.Fatalf("Transaction() error = %v, want wrapped commit error", err)
		}
		if state.commitCalls != 1 || state.rollbackCalls != 0 {
			t.Fatalf("lifecycle calls = commit:%d rollback:%d, want 1, 0", state.commitCalls, state.rollbackCalls)
		}
	})
}

func TestTransactionRollsBackAndPropagatesPanic(t *testing.T) {
	t.Parallel()

	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)
	panicValue := &struct{ message string }{message: "callback panic"}
	var recovered any
	returned := false

	func() {
		defer func() {
			recovered = recover()
		}()
		_ = Transaction(context.Background(), database, func(*sql.Tx) error {
			panic(panicValue)
		})
		returned = true
	}()

	if returned {
		t.Fatal("Transaction() returned after callback panic")
	}
	if recovered != panicValue {
		t.Fatalf("recovered panic = %#v, want %#v", recovered, panicValue)
	}
	if state.commitCalls != 0 || state.rollbackCalls != 1 {
		t.Fatalf("lifecycle calls = commit:%d rollback:%d, want 0, 1", state.commitCalls, state.rollbackCalls)
	}
}

func TestTransactionValidatesInputsBeforeBeginning(t *testing.T) {
	t.Parallel()

	state := &transactionTestState{}
	database := openTransactionTestDB(t, state)
	var typedNilDatabase *sql.DB
	validCallback := func(*sql.Tx) error { return nil }
	tests := []struct {
		name     string
		ctx      context.Context
		beginner TransactionBeginner
		callback func(*sql.Tx) error
		want     string
	}{
		{name: "nil context", beginner: database, callback: validCallback, want: "nil context"},
		{name: "nil beginner", ctx: context.Background(), callback: validCallback, want: "nil beginner"},
		{name: "typed nil beginner", ctx: context.Background(), beginner: typedNilDatabase, callback: validCallback, want: "nil beginner"},
		{name: "nil callback", ctx: context.Background(), beginner: database, want: "nil callback"},
		{name: "nil transaction", ctx: context.Background(), beginner: nilTransactionBeginner{}, callback: validCallback, want: "beginner returned a nil transaction"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Transaction(test.ctx, test.beginner, test.callback)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Transaction() error = %v, want %q", err, test.want)
			}
		})
	}
	if state.beginCalls != 0 {
		t.Fatalf("BeginTx() calls = %d, want 0 for invalid inputs", state.beginCalls)
	}
}

func openTransactionTestDB(t testing.TB, state *transactionTestState) *sql.DB {
	t.Helper()

	database := sql.OpenDB(&transactionTestConnector{state: state})
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("DB.Close() error = %v", err)
		}
	})
	return database
}
