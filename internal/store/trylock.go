package store

// TryLock's session advisory lock lives on a connection pinned with raw
// database/sql (dbq.go), where momo's binds do not apply, so its statements
// keep their native $1 and this file is not in namedFiles. It moved here
// from ai_core.go when that file was converted to @name; its SQL and its
// keys are unchanged.

import (
	"context"
	"database/sql/driver"
	"time"
)

// TryLock takes pg_try_advisory_lock(hashtext(key)) on a connection of
// its own, held until unlock (or until the process dies and PostgreSQL
// drops the connection). Any "one replica does this" task uses it with a
// key of its own: the AI scheduler and, with the integrations spec, the
// directory sync, webhook sweeper and CRM refresher. Keys are namespaced
// hello:<area>:<task> and must not collide with the configuration lock.
// The lock is the pinned connection's session, so it stays on raw
// database/sql (see dbq.go).
func (s *Store) TryLock(ctx context.Context, key string) (func(), bool, error) {
	conn, err := s.db.raw.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1::text))`, key).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !ok {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1::text))`, key); err != nil {
			// The lock is the session's: discard the connection so it goes.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, true, nil
}
