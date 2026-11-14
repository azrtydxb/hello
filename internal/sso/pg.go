package sso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/secret"
)

// PG is the Store on PostgreSQL. Client secrets are sealed with Box under
// AAD sso:<provider>:client_secret.
type PG struct {
	DB  *sql.DB
	Box *secret.Box
}

func secretAAD(p string) string { return "sso:" + p + ":client_secret" }

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func audit(ctx context.Context, q execer, actor, action, resource, id string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES ($1, $2, $3, $4, NULL)`,
		actor, action, resource, id)
	return err
}

// Audit implements Store.
func (p *PG) Audit(ctx context.Context, actor, action, resource, id string) error {
	return audit(ctx, p.DB, actor, action, resource, id)
}

func (p *PG) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

const providerCols = `provider, enabled, client_id, client_secret_enc IS NOT NULL, tenant, to_json(domains)::text, jit, updated_at`

func scanProvider(r interface{ Scan(...any) error }) (Provider, error) {
	var (
		pr      Provider
		domains string
	)
	if err := r.Scan(&pr.Provider, &pr.Enabled, &pr.ClientID, &pr.HasClientSecret, &pr.Tenant, &domains, &pr.JIT, &pr.UpdatedAt); err != nil {
		return pr, err
	}
	if err := json.Unmarshal([]byte(domains), &pr.Domains); err != nil || pr.Domains == nil {
		pr.Domains = []string{}
	}
	return pr, nil
}

// ListProviders implements Store.
func (p *PG) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT `+providerCols+` FROM sso_providers ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Provider{}
	for rows.Next() {
		pr, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// GetProvider implements Store.
func (p *PG) GetProvider(ctx context.Context, name string) (Provider, error) {
	pr, err := scanProvider(p.DB.QueryRowContext(ctx, `SELECT `+providerCols+` FROM sso_providers WHERE provider = $1`, name))
	return pr, mapErr(err)
}

// SaveProvider implements Store.
func (p *PG) SaveProvider(ctx context.Context, actor string, pr Provider, secretValue *string) (Provider, error) {
	var sealed []byte
	if secretValue != nil {
		if p.Box == nil {
			return pr, errors.New("sso: no secret box configured")
		}
		var err error
		if sealed, err = p.Box.Seal(*secretValue, secretAAD(pr.Provider)); err != nil {
			return pr, err
		}
	}
	domains, _ := json.Marshal(pr.Domains)
	err := p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (provider, enabled, client_id, client_secret_enc, tenant, domains, jit, updated_at)
			VALUES ($1, $2, $3, $4, $5, ARRAY(SELECT jsonb_array_elements_text($6::jsonb)), $7, now())
			ON CONFLICT (provider) DO UPDATE SET
				enabled = EXCLUDED.enabled, client_id = EXCLUDED.client_id,
				client_secret_enc = COALESCE(EXCLUDED.client_secret_enc, sso_providers.client_secret_enc),
				tenant = EXCLUDED.tenant, domains = EXCLUDED.domains, jit = EXCLUDED.jit, updated_at = now()`,
			pr.Provider, pr.Enabled, pr.ClientID, sealed, pr.Tenant, string(domains), pr.JIT); err != nil {
			return err
		}
		action := "update"
		if secretValue != nil {
			action = "update-secret"
		}
		return audit(ctx, tx, actor, action, "sso_provider", pr.Provider)
	})
	if err != nil {
		return pr, err
	}
	return p.GetProvider(ctx, pr.Provider)
}

// ProviderSecret implements Store.
func (p *PG) ProviderSecret(ctx context.Context, name string) (string, error) {
	var enc []byte
	if err := p.DB.QueryRowContext(ctx, `SELECT client_secret_enc FROM sso_providers WHERE provider = $1`, name).Scan(&enc); err != nil {
		return "", mapErr(err)
	}
	if enc == nil || p.Box == nil {
		return "", ErrNotFound
	}
	return p.Box.Open(enc, secretAAD(name))
}

// SSORequired implements Store.
func (p *PG) SSORequired(ctx context.Context) (bool, error) {
	var on bool
	err := p.DB.QueryRowContext(ctx, `SELECT sso_required FROM auth_settings`).Scan(&on)
	return on, err
}

// SetSSORequired implements Store. Turning it on locks the admin rows and
// needs an enabled break-glass admin among them.
func (p *PG) SetSSORequired(ctx context.Context, actor string, on bool) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		if on {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT id FROM users WHERE role = 'admin' AND enabled AND break_glass FOR UPDATE) b`).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNoBreakGlass
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_settings SET sso_required = $1`, on); err != nil {
			return err
		}
		action := "sso-optional"
		if on {
			action = "sso-required"
		}
		return audit(ctx, tx, actor, action, "auth_settings", "sso")
	})
}

// BreakGlassUsers implements Store.
func (p *PG) BreakGlassUsers(ctx context.Context) ([]BreakGlassUser, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT id, username, enabled FROM users WHERE break_glass ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []BreakGlassUser{}
	for rows.Next() {
		var b BreakGlassUser
		if err := rows.Scan(&b.ID, &b.Username, &b.Enabled); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const userSelect = `SELECT u.id, u.username, u.role, u.enabled, coalesce(u.source_connector_id, 0), coalesce(c.kind, ''),
	coalesce(c.config->>'tenantId', ''), coalesce(u.external_id, ''), u.break_glass
	FROM users u LEFT JOIN directory_connectors c ON c.id = u.source_connector_id`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.ConnectorID, &u.ConnectorKind, &u.ConnectorTenant, &u.ExternalID, &u.BreakGlass)
	return u, err
}

// GetUser implements Store.
func (p *PG) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(p.DB.QueryRowContext(ctx, userSelect+` WHERE u.id = $1`, id))
	return u, mapErr(err)
}

// IdentityUser implements Store.
func (p *PG) IdentityUser(ctx context.Context, provider, subject string) (User, error) {
	u, err := scanUser(p.DB.QueryRowContext(ctx, userSelect+` JOIN user_identities i ON i.user_id = u.id WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
	return u, mapErr(err)
}

// SyncedUser implements Store.
func (p *PG) SyncedUser(ctx context.Context, kind, externalID string) (User, error) {
	u, err := scanUser(p.DB.QueryRowContext(ctx, userSelect+` WHERE c.kind = $1 AND lower(u.external_id) = lower($2)`, kind, externalID))
	return u, mapErr(err)
}

// SyncedUsersByEmail implements Store.
func (p *PG) SyncedUsersByEmail(ctx context.Context, email string) ([]User, error) {
	rows, err := p.DB.QueryContext(ctx, userSelect+` WHERE u.source_connector_id IS NOT NULL AND lower(u.email) = lower($1) ORDER BY u.id`, email)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Link implements Store.
func (p *PG) Link(ctx context.Context, actor string, id Identity) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_identities (user_id, provider, subject, tenant, email, last_login_at)
			VALUES ($1, $2, $3, $4, $5, now())`, id.UserID, id.Provider, id.Subject, id.Tenant, id.Email); err != nil {
			if isUnique(err) {
				return ErrLinked
			}
			return err
		}
		return audit(ctx, tx, actor, "link-identity:"+id.Provider, "user", strconv.FormatInt(id.UserID, 10))
	})
}

// TouchIdentity implements Store.
func (p *PG) TouchIdentity(ctx context.Context, provider, subject string, at time.Time) error {
	_, err := p.DB.ExecContext(ctx, `UPDATE user_identities SET last_login_at = $3 WHERE provider = $1 AND subject = $2`, provider, subject, at)
	return err
}

// ListIdentities implements Store.
func (p *PG) ListIdentities(ctx context.Context, userID int64) ([]Identity, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT id, user_id, provider, subject, tenant, email, created_at, last_login_at
		FROM user_identities WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Identity{}
	for rows.Next() {
		var (
			i    Identity
			last sql.NullTime
		)
		if err := rows.Scan(&i.ID, &i.UserID, &i.Provider, &i.Subject, &i.Tenant, &i.Email, &i.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			i.LastLoginAt = &last.Time
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Unlink implements Store.
func (p *PG) Unlink(ctx context.Context, actor string, userID int64, provider string) error {
	return p.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id = $1 AND provider = $2`, userID, provider)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, actor, "unlink-identity:"+provider, "user", strconv.FormatInt(userID, 10))
	})
}

// CreateUser implements Store: a user role account with the no-match
// password marker, linked to id.
func (p *PG) CreateUser(ctx context.Context, actor, username, email, display string, id Identity) (User, error) {
	var u User
	err := p.tx(ctx, func(tx *sql.Tx) error {
		var uid int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash, role, display_name, email, enabled, must_change_password)
			VALUES ($1, '!', 'user', $2, $3, TRUE, FALSE) RETURNING id`, username, display, email).Scan(&uid); err != nil {
			if isUnique(err) {
				return ErrLinked
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_identities (user_id, provider, subject, tenant, email, last_login_at)
			VALUES ($1, $2, $3, $4, $5, now())`, uid, id.Provider, id.Subject, id.Tenant, id.Email); err != nil {
			if isUnique(err) {
				return ErrLinked
			}
			return err
		}
		if err := audit(ctx, tx, actor, "create-jit", "user", strconv.FormatInt(uid, 10)); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRowContext(ctx, userSelect+` WHERE u.id = $1`, uid))
		return err
	})
	return u, err
}
