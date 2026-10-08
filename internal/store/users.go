package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// ErrLastAdmin rejects a role change that would leave no admin.
var ErrLastAdmin = errors.New("store: last admin")

// User is a management user without its password hash.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      auth.Role `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateUser inserts a management user with role, except that the first
// user is always admin (spec S-23).
func (s *Store) CreateUser(ctx context.Context, actor, username, passwordHash string, role auth.Role) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash, role)
			VALUES ($1, $2, CASE WHEN EXISTS (SELECT 1 FROM users) THEN $3 ELSE 'admin' END)
			RETURNING id`,
			username, passwordHash, string(role)).Scan(&id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "user", strconv.FormatInt(id, 10))
	})
	return id, err
}

// ListUsers returns every management user with its role, by id.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, role, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserRole changes user id's role and records the audit event. Demoting
// the last admin returns ErrLastAdmin; concurrent demotions serialise on
// the admin rows, so two of them cannot both pass the check.
func (s *Store) SetUserRole(ctx context.Context, actor string, id int64, role auth.Role) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		admins, err := lockAdmins(ctx, tx)
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT id, username, role, created_at FROM users WHERE id = $1 FOR UPDATE`, id).
			Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		if u.Role == role {
			return nil
		}
		if u.Role == auth.RoleAdmin && admins <= 1 {
			return ErrLastAdmin
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, string(role)); err != nil {
			return err
		}
		u.Role = role
		return insertAudit(ctx, tx, actor, "set-role:"+string(role), "user", strconv.FormatInt(id, 10))
	})
	return u, err
}

// lockAdmins locks every admin row and counts them.
func lockAdmins(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE role = 'admin' FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}
