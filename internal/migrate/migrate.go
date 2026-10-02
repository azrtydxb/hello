// Package migrate applies the embedded PostgreSQL migrations with goose,
// holding a Postgres advisory lock so concurrent hello-control replicas
// apply each migration exactly once.
package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/azrtydxb/hello/migrations"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

func provider(db *sql.DB) (*goose.Provider, error) {
	// Retry every second for up to five minutes: a waiting replica starts soon
	// after the one holding the lock finishes.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
}

// Up applies every pending migration and returns how many it applied.
func Up(ctx context.Context, db *sql.DB) (int, error) {
	p, err := provider(db)
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate up: %w", err)
	}
	return len(res), nil
}

// Status lists each migration and whether it is applied.
func Status(ctx context.Context, db *sql.DB) ([]*goose.MigrationStatus, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	return p.Status(ctx)
}
