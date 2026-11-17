package store

// The store's statements run on momo (github.com/azrtydxb/momo), Hello's
// query layer (docs/database.md). This file is the seam: dbq and the types
// behind it offer the method set the store's code was written against
// (ExecContext, QueryContext, QueryRowContext, BeginTx, Commit, Rollback),
// and every statement made through them is a momo raw statement on the
// store's *momo.DB or on momo's view of an open *sql.Tx (internal/db.AdoptTx).
// A statement binds by name (@name with momo.Named), or, in a file not yet
// converted, with native $n placeholders, which momo binds on PostgreSQL as
// positional arguments; files move to @name one domain at a time
// (docs/database.md lists the recipe). A converted file is listed in
// namedFiles (dbq_offline_test.go), which then refuses a $n in it.
//
// Transactions are still begun by database/sql and adopted, so a *sql.Tx
// stays available to code outside the store that takes one (the directory
// Provisioner), and momo never commits or rolls back: the store does, as
// before. Only three things stay raw database/sql: BEGIN itself, the pinned
// connection that holds a session advisory lock (TryLock), and the read of
// the migration histories (SchemaVersion), which the engines own.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/azrtydxb/momo"

	hellodb "github.com/azrtydxb/hello/internal/db"
)

// dbq is a handle a store statement runs on: the pool (*dbPool) or an open
// transaction (*dbTx). It is also a momo.Querier, so momo.Exec, momo.Query
// and the builders take it as it is.
type dbq interface {
	momo.Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*dbRows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *dbRow
}

// dbConn runs raw statements on a momo handle with database/sql's calling
// conventions.
type dbConn struct{ momo.Querier }

// ExecContext runs a statement that returns no rows. Its result reports the
// rows affected; LastInsertId is unsupported, as it is on PostgreSQL.
func (c dbConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	n, err := momo.Exec(ctx, c.Querier, query, args...)
	if err != nil {
		return nil, err
	}
	return execResult(n), nil
}

// QueryContext runs a statement and returns its open rows; the caller closes
// them.
func (c dbConn) QueryContext(ctx context.Context, query string, args ...any) (*dbRows, error) {
	r, err := momo.RawRows(ctx, c.Querier, query, args...)
	if err != nil {
		return nil, err
	}
	return &dbRows{r: r}, nil
}

// QueryRowContext runs a statement for its first row. As with *sql.Row, an
// error is deferred to Scan, and a statement with no row scans as
// sql.ErrNoRows.
func (c dbConn) QueryRowContext(ctx context.Context, query string, args ...any) *dbRow {
	r, err := momo.RawRows(ctx, c.Querier, query, args...)
	return &dbRow{r: r, err: err}
}

// dbPool is the store's database: momo's handle over the *sql.DB, plus the
// *sql.DB itself, which begins the transactions momo then adopts.
type dbPool struct {
	dbConn
	m   *momo.DB
	raw *sql.DB
}

func newPool(db *sql.DB, m *momo.DB) *dbPool {
	return &dbPool{dbConn: dbConn{m}, m: m, raw: db}
}

// BeginTx begins a database/sql transaction and adopts it.
func (p *dbPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*dbTx, error) {
	tx, err := p.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return p.adopt(tx), nil
}

// adopt returns the store's view of tx, a transaction its caller began and
// still commits or rolls back.
func (p *dbPool) adopt(tx *sql.Tx) *dbTx {
	return &dbTx{dbConn: dbConn{hellodb.AdoptTx(p.m, tx)}, tx: tx}
}

// dbTx is an open transaction: statements run on momo's adopted view of tx.
type dbTx struct {
	dbConn
	tx *sql.Tx
}

// Commit commits the transaction.
func (t *dbTx) Commit() error { return t.tx.Commit() }

// Rollback rolls the transaction back; after Commit it is a no-op returning
// sql.ErrTxDone.
func (t *dbTx) Rollback() error { return t.tx.Rollback() }

// execResult is the rows-affected count of an ExecContext.
type execResult int64

func (r execResult) LastInsertId() (int64, error) {
	return 0, errors.New("store: LastInsertId is not supported by PostgreSQL")
}

func (r execResult) RowsAffected() (int64, error) { return int64(r), nil }

// dbRows is the open result of QueryContext.
type dbRows struct{ r momo.ColumnRows }

// Next prepares the next row for Scan; false at the end or on an error,
// which Err then reports.
func (r *dbRows) Next() bool { return r.r.Next() }

// Scan copies the current row's columns into dest.
func (r *dbRows) Scan(dest ...any) error { return r.r.Scan(dest...) }

// Err is the error that ended the iteration, if any.
func (r *dbRows) Err() error { return r.r.Err() }

// Close releases the rows; safe to call more than once.
func (r *dbRows) Close() error { return r.r.Close() }

// dbRow is the result of QueryRowContext.
type dbRow struct {
	r   momo.ColumnRows
	err error
}

// Scan copies the first row's columns into dest and closes the rows. It
// returns sql.ErrNoRows when the statement returned no row.
func (r *dbRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer func() { _ = r.r.Close() }()
	if !r.r.Next() {
		if err := r.r.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := r.r.Scan(dest...); err != nil {
		return err
	}
	return r.r.Close()
}

// Err reports the error of running the statement, without scanning.
func (r *dbRow) Err() error { return r.err }

// one is the first row of a momo.Query or momo.Scalars read, or
// sql.ErrNoRows when the read returned none, so that mapErr (ErrNotFound) and
// the store's errors.Is(err, sql.ErrNoRows) checks treat it as they treat
// QueryRowContext's Scan. A read error is returned as is.
//
//	rev, err := one(momo.Scalars[int64](ctx, s.q(ctx), `SELECT config_revision FROM schema_info`))
func one[T any](rows []T, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, sql.ErrNoRows
	}
	return rows[0], nil
}
