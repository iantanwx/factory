package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Tx is one write transaction on the writer connection. It is valid only
// inside the function given to Store.Write.
type Tx struct {
	ctx context.Context
	tx  *sql.Tx
	now time.Time
}

// Write runs fn in one BEGIN IMMEDIATE transaction on the writer connection.
// It commits when fn returns nil, and it rolls back when fn returns an
// error or panics. Writes from many goroutines queue for the one writer
// connection. fn must not call Write or Read on the same Store: Write would
// wait for itself.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) (err error) {
	sqlTx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin write: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	if err := fn(&Tx{ctx: ctx, tx: sqlTx, now: s.now()}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// Read runs fn in one read-only transaction on the reader pool. The
// transaction sees one consistent snapshot, and a write inside it fails.
func (s *Store) Read(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("store: begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// Context is the context the transaction runs under.
func (t *Tx) Context() context.Context { return t.ctx }

// Now is the store clock at the start of the transaction. Every time the
// store writes inside one transaction uses it.
func (t *Tx) Now() time.Time { return t.now }

// Exec runs a statement inside the transaction.
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

// Query runs a query inside the transaction.
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.ctx, query, args...)
}

// QueryRow runs a query that returns at most one row inside the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

// millis converts a time to the unix milliseconds the schema stores. The
// zero time is stored as NULL.
func millis(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

// fromMillis converts a stored unix millisecond value back. NULL is the
// zero time.
func fromMillis(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}
