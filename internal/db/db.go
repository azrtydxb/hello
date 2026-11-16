// Package db is Hello's momo (github.com/azrtydxb/momo) handle over the
// *sql.DB every binary already opens.
//
// momo is the query layer only, adopted one package at a time; goose stays
// the migration tool. The handle shares the pool of the *sql.DB it wraps (no
// second pool, no new connection settings), and momo code joins a
// transaction raw database/sql code opened with AdoptTx. The generated
// models of the tables momo reads and writes live in package models; see
// docs/database.md.
package db

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/driver/pgxdriver"
	"github.com/azrtydxb/momo/momolog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SlowQuery is how long a statement may take before its log line is raised
// from debug to warn.
const SlowQuery = 500 * time.Millisecond

// New returns the momo handle over db. Every statement is logged through log
// (the binary's telemetry logger) at debug level with the component "db":
// the SQL, its duration, the row count and any error; a statement slower
// than SlowQuery is logged at warn. Argument values are never logged,
// because sealed tokens and secrets are statement arguments.
func New(db *sql.DB, log *slog.Logger) *momo.DB {
	return momo.New(pgxdriver.FromSQLDB(db), logHook(log))
}

// NewPool returns the momo handle over a pgx pool (hello-sip's CDR writer),
// logged as New logs. Statements run in the pool's own query execution mode
// (pgxpool's default caches prepared statements), not momo's default for the
// native driver, so they reach PostgreSQL as the pool sent them before.
func NewPool(pool *pgxpool.Pool, log *slog.Logger) *momo.DB {
	return momo.New(pgxdriver.New(pool, pgxdriver.WithExecMode(pool.Config().ConnConfig.DefaultQueryExecMode)), logHook(log))
}

func logHook(log *slog.Logger) momo.Option {
	return momo.WithHook(momolog.New(log.With("component", "db"), momolog.Level(slog.LevelDebug), momolog.SlowOver(SlowQuery)))
}

// AdoptTx returns m's view of tx, a transaction raw database/sql code began:
// statements run through it are part of tx, and the caller still commits or
// rolls tx back (momo never does, it returns momo.ErrAdoptedTx). A nil tx
// returns m itself, so a function that takes an optional transaction can
// pass the result straight to momo.
func AdoptTx(m *momo.DB, tx *sql.Tx) momo.Querier {
	if tx == nil {
		return m
	}
	return m.AdoptTx(pgxdriver.SQLTx(tx))
}
