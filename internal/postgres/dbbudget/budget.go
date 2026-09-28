package dbbudget

import (
	"context"
	"fmt"
	"time"

	"weave-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const operationTimeout = 1500 * time.Millisecond

// DBTX applies one operation deadline to pool acquisition, SQL execution, and row consumption.
type DBTX struct {
	db               sqlc.DBTX
	operationTimeout time.Duration
}

// NewDBTX wraps a pool or transaction with a bounded operation lifetime.
func NewDBTX(db sqlc.DBTX) DBTX {
	if wrapped, ok := db.(DBTX); ok {
		return wrapped
	}
	return NewDBTXWithTimeout(db, operationTimeout)
}

// NewDBTXWithTimeout wraps a pool or transaction with the supplied operation lifetime.
func NewDBTXWithTimeout(db sqlc.DBTX, timeout time.Duration) DBTX {
	if wrapped, ok := db.(DBTX); ok {
		return DBTX{db: wrapped.db, operationTimeout: timeout}
	}
	return DBTX{db: db, operationTimeout: timeout}
}

// Queries constructs SQLC queries that cannot wait indefinitely for a pool connection or lock.
func Queries(db sqlc.DBTX) *sqlc.Queries {
	return sqlc.New(NewDBTX(db))
}

// QueriesWithTimeout constructs SQLC queries with a custom per-operation lifetime.
func QueriesWithTimeout(db sqlc.DBTX, timeout time.Duration) *sqlc.Queries {
	return sqlc.New(NewDBTXWithTimeout(db, timeout))
}

func (db DBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	dbCtx, cancel := context.WithTimeout(ctx, db.operationTimeout)
	defer cancel()
	return db.db.Exec(dbCtx, query, args...)
}

func (db DBTX) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	dbCtx, cancel := context.WithTimeout(ctx, db.operationTimeout)
	rows, err := db.db.Query(dbCtx, query, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	return budgetedRows{Rows: rows, cancel: cancel}, nil
}

func (db DBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	dbCtx, cancel := context.WithTimeout(ctx, db.operationTimeout)
	return budgetedRow{Row: db.db.QueryRow(dbCtx, query, args...), cancel: cancel}
}

func (db DBTX) Begin(ctx context.Context) (pgx.Tx, error) {
	dbCtx, cancel := context.WithTimeout(ctx, db.operationTimeout)
	defer cancel()
	beginner, ok := db.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("database handle %T does not support transactions", db.db)
	}
	tx, err := beginner.Begin(dbCtx)
	if err != nil {
		return nil, err
	}
	return budgetedTx{Tx: tx, operationTimeout: db.operationTimeout}, nil
}

func (db DBTX) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	dbCtx, cancel := context.WithTimeout(ctx, db.operationTimeout)
	defer cancel()
	beginner, ok := db.db.(interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("database handle %T does not support transactions", db.db)
	}
	tx, err := beginner.BeginTx(dbCtx, options)
	if err != nil {
		return nil, err
	}
	return budgetedTx{Tx: tx, operationTimeout: db.operationTimeout}, nil
}

type budgetedRow struct {
	pgx.Row
	cancel context.CancelFunc
}

func (row budgetedRow) Scan(dest ...any) error {
	defer row.cancel()
	return row.Row.Scan(dest...)
}

type budgetedRows struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (rows budgetedRows) Close() {
	rows.Rows.Close()
	rows.cancel()
}

func (rows budgetedRows) Next() bool {
	next := rows.Rows.Next()
	if !next {
		rows.cancel()
	}
	return next
}

type budgetedTx struct {
	pgx.Tx
	operationTimeout time.Duration
}

func (tx budgetedTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	dbCtx, cancel := context.WithTimeout(ctx, tx.operationTimeout)
	defer cancel()
	return tx.Tx.Exec(dbCtx, query, args...)
}

func (tx budgetedTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	dbCtx, cancel := context.WithTimeout(ctx, tx.operationTimeout)
	rows, err := tx.Tx.Query(dbCtx, query, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	return budgetedRows{Rows: rows, cancel: cancel}, nil
}

func (tx budgetedTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	dbCtx, cancel := context.WithTimeout(ctx, tx.operationTimeout)
	return budgetedRow{Row: tx.Tx.QueryRow(dbCtx, query, args...), cancel: cancel}
}

func (tx budgetedTx) Commit(ctx context.Context) error {
	dbCtx, cancel := context.WithTimeout(ctx, tx.operationTimeout)
	defer cancel()
	return tx.Tx.Commit(dbCtx)
}

func (tx budgetedTx) Rollback(ctx context.Context) error {
	dbCtx, cancel := context.WithTimeout(ctx, tx.operationTimeout)
	defer cancel()
	return tx.Tx.Rollback(dbCtx)
}
