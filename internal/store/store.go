// Package store is hello-control's PostgreSQL access: management users,
// sessions, API tokens, extensions, devices, audit events, CDRs and the
// configuration revision. Every extension or device mutation runs in one
// transaction with its audit row, the revision bump and the NOTIFY that
// hello-sip listens for.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound is returned when the row, or a row it references, does not
	// exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a unique value is already taken.
	ErrConflict = errors.New("store: conflict")
)

// NotifyChannel is the PostgreSQL channel a configuration change notifies,
// with the new revision as payload.
const NotifyChannel = "hello_config"

// Store wraps the hello-control database.
type Store struct {
	db *sql.DB
}

// New wraps db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// mapErr turns driver errors into the package sentinels.
func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrNotFound, pg.ConstraintName)
		}
	}
	return err
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return mapErr(err)
	}
	return tx.Commit()
}

func insertAudit(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, actor, action, resource, resourceID string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, resource_id) VALUES ($1, $2, $3, $4)`,
		actor, action, resource, resourceID)
	return err
}

// configChange runs fn and, in the same transaction, records the audit
// event, bumps the configuration revision and notifies hello-sip. fn returns
// the affected resource's id.
func (s *Store) configChange(ctx context.Context, actor, action, resource string, fn func(*sql.Tx) (int64, error)) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		id, err := fn(tx)
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, actor, action, resource, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		var rev int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE schema_info SET config_revision = config_revision + 1 RETURNING config_revision`).Scan(&rev); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, NotifyChannel, strconv.FormatInt(rev, 10))
		return err
	})
}

// Audit records a non-configuration event such as a login.
func (s *Store) Audit(ctx context.Context, actor, action, resource, resourceID string) error {
	return insertAudit(ctx, s.db, actor, action, resource, resourceID)
}

// ConfigRevision returns the current configuration revision.
func (s *Store) ConfigRevision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT config_revision FROM schema_info`).Scan(&rev)
	return rev, err
}

// Users, sessions and tokens.

// CountUsers reports how many management users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstUser inserts a user only while the users table is empty, so
// concurrently starting replicas create at most one bootstrap user.
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string) (bool, error) {
	created := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash)
			SELECT $1, $2 WHERE NOT EXISTS (SELECT 1 FROM users)
			ON CONFLICT (username) DO NOTHING
			RETURNING id`, username, passwordHash).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		created = true
		return insertAudit(ctx, tx, "system", "create", "user", strconv.FormatInt(id, 10))
	})
	return created, err
}

// CreateUser inserts a management user.
func (s *Store) CreateUser(ctx context.Context, actor, username, passwordHash string) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
			username, passwordHash).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "user", strconv.FormatInt(id, 10))
	})
	return id, err
}

// UserByName returns a user's id and bcrypt hash.
func (s *Store) UserByName(ctx context.Context, username string) (int64, string, error) {
	var (
		id   int64
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash FROM users WHERE username = $1`, username).Scan(&id, &hash)
	return id, hash, mapErr(err)
}

// CreateSession stores a session by the hash of its cookie value.
func (s *Store) CreateSession(ctx context.Context, userID int64, hash []byte, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash, userID, expires)
	return err
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hash)
	return err
}

// PruneSessions deletes expired sessions and reports how many.
func (s *Store) PruneSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SessionActor implements auth.Lookup.
func (s *Store) SessionActor(ctx context.Context, hash []byte) (auth.Actor, error) {
	var a auth.Actor
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hash).Scan(&a.UserID, &a.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return a, auth.ErrNoCredentials
	}
	return a, err
}

// TokenActor implements auth.Lookup and records the token's last use.
func (s *Store) TokenActor(ctx context.Context, hash []byte) (auth.Actor, error) {
	var a auth.Actor
	err := s.db.QueryRowContext(ctx, `
		UPDATE api_tokens t SET last_used_at = now() FROM users u
		WHERE t.token_hash = $1 AND u.id = t.user_id
		RETURNING t.id, u.id, u.username`, hash).Scan(&a.TokenID, &a.UserID, &a.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return a, auth.ErrNoCredentials
	}
	return a, err
}

// Token is an API token's metadata; the token itself is never stored.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// ListTokens returns a user's API tokens, oldest first.
func (s *Store) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, created_at, last_used_at FROM api_tokens WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Token{}
	for rows.Next() {
		var t Token
		var last sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &last); err != nil {
			return nil, err
		}
		t.LastUsedAt = nullTime(last)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateToken stores a new API token for userID by its hash.
func (s *Store) CreateToken(ctx context.Context, actor string, userID int64, name string, hash []byte) (Token, error) {
	t := Token{Name: name}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO api_tokens (user_id, name, token_hash) VALUES ($1, $2, $3) RETURNING id, created_at`,
			userID, name, hash).Scan(&t.ID, &t.CreatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "api_token", strconv.FormatInt(t.ID, 10))
	})
	return t, err
}

// DeleteToken revokes one of userID's API tokens.
func (s *Store) DeleteToken(ctx context.Context, actor string, userID, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, id, userID)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "api_token", strconv.FormatInt(id, 10))
	})
}

// requireRow returns ErrNotFound when res affected no row.
func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
