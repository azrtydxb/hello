package sso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/azrtydxb/hello/internal/secret"
)

// PG is the Store on PostgreSQL, on momo (internal/db). Client secrets are
// sealed with Box under AAD sso:<provider>:client_secret.
//
// The statements are the hand-written SQL they were on database/sql, with
// @name binds instead of $n, and rows are scanned positionally as before
// (queryRow and queryEach over momo.RawRows). Errors reach callers as
// before: ErrNotFound for a missing row, ErrLinked for an identity or a
// username already taken, ErrNoBreakGlass, and the driver's error (a
// *pgconn.PgError is still reachable with errors.As) for anything else.
type PG struct {
	DB  *momo.DB
	Box *secret.Box
}

func secretAAD(p string) string { return "sso:" + p + ":client_secret" }

func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// isUnique reports a unique violation: momo.ErrUniqueViolation, or the
// *pgconn.PgError 23505 check this store always made (it still holds
// through momo's error).
func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.Is(err, momo.ErrUniqueViolation) || errors.As(err, &pg) && pg.Code == "23505"
}

// row is one row of a query, run when it is scanned: QueryRow(...).Scan's
// contract on momo, sql.ErrNoRows when there is no row, so mapErr maps it
// as before (G7: momo has no single-row raw helper).
type row struct {
	ctx  context.Context
	q    momo.Querier
	sql  string
	args []any
}

func queryRow(ctx context.Context, q momo.Querier, sql string, args ...any) row {
	return row{ctx: ctx, q: q, sql: sql, args: args}
}

// Scan scans the first row into dest.
func (r row) Scan(dest ...any) error {
	rows, err := momo.RawRows(r.ctx, r.q, r.sql, r.args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Close()
}

// scanner is a row being read: a momo.Rows or a row.
type scanner interface{ Scan(...any) error }

// queryEach runs the query and calls fn for every row, in order.
func queryEach(ctx context.Context, q momo.Querier, fn func(scanner) error, sql string, args ...any) error {
	rows, err := momo.RawRows(ctx, q, sql, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

func audit(ctx context.Context, q momo.Querier, actor, action, resource, id string) error {
	_, err := momo.Exec(ctx, q,
		`INSERT INTO audit_events (actor, action, resource, resource_id, via) VALUES (@actor, @action, @resource, @resource_id, NULL)`,
		momo.Named("actor", actor), momo.Named("action", action), momo.Named("resource", resource), momo.Named("resource_id", id))
	return err
}

// Audit implements Store.
func (p *PG) Audit(ctx context.Context, actor, action, resource, id string) error {
	return audit(ctx, p.DB, actor, action, resource, id)
}

// tx runs f in a transaction: committed when f returns nil, rolled back
// when it returns an error, which is returned as is.
func (p *PG) tx(ctx context.Context, f func(*momo.Tx) error) error {
	return p.DB.Tx(ctx, momo.TxOptions{}, f)
}

const providerCols = `provider, enabled, client_id, client_secret_enc IS NOT NULL, tenant, to_json(domains)::text, jit, updated_at`

func scanProvider(r scanner) (Provider, error) {
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
	out := []Provider{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		pr, err := scanProvider(r)
		if err != nil {
			return err
		}
		out = append(out, pr)
		return nil
	}, `SELECT `+providerCols+` FROM sso_providers ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetProvider implements Store.
func (p *PG) GetProvider(ctx context.Context, name string) (Provider, error) {
	pr, err := scanProvider(queryRow(ctx, p.DB, `SELECT `+providerCols+` FROM sso_providers WHERE provider = @provider`, momo.Named("provider", name)))
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
	err := p.tx(ctx, func(tx *momo.Tx) error {
		if _, err := momo.Exec(ctx, tx, `
			INSERT INTO sso_providers (provider, enabled, client_id, client_secret_enc, tenant, domains, jit, updated_at)
			VALUES (@provider, @enabled, @client_id, @client_secret_enc, @tenant, ARRAY(SELECT jsonb_array_elements_text(@domains::jsonb)), @jit, now())
			ON CONFLICT (provider) DO UPDATE SET
				enabled = EXCLUDED.enabled, client_id = EXCLUDED.client_id,
				client_secret_enc = COALESCE(EXCLUDED.client_secret_enc, sso_providers.client_secret_enc),
				tenant = EXCLUDED.tenant, domains = EXCLUDED.domains, jit = EXCLUDED.jit, updated_at = now()`,
			momo.Named("provider", pr.Provider), momo.Named("enabled", pr.Enabled), momo.Named("client_id", pr.ClientID),
			momo.Named("client_secret_enc", sealed), momo.Named("tenant", pr.Tenant), momo.Named("domains", string(domains)),
			momo.Named("jit", pr.JIT)); err != nil {
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
	if err := queryRow(ctx, p.DB, `SELECT client_secret_enc FROM sso_providers WHERE provider = @provider`, momo.Named("provider", name)).Scan(&enc); err != nil {
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
	err := queryRow(ctx, p.DB, `SELECT sso_required FROM auth_settings`).Scan(&on)
	return on, err
}

// SetSSORequired implements Store. Turning it on locks the admin rows and
// needs an enabled break-glass admin among them.
func (p *PG) SetSSORequired(ctx context.Context, actor string, on bool) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		if on {
			var n int
			if err := queryRow(ctx, tx, `SELECT count(*) FROM (SELECT id FROM users WHERE role = 'admin' AND enabled AND break_glass FOR UPDATE) b`).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNoBreakGlass
			}
		}
		if _, err := momo.Exec(ctx, tx, `UPDATE auth_settings SET sso_required = @on`, momo.Named("on", on)); err != nil {
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
	out := []BreakGlassUser{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var b BreakGlassUser
		if err := r.Scan(&b.ID, &b.Username, &b.Enabled); err != nil {
			return err
		}
		out = append(out, b)
		return nil
	}, `SELECT id, username, enabled FROM users WHERE break_glass ORDER BY username`)
	if err != nil {
		return nil, err
	}
	return out, nil
}

const userSelect = `SELECT u.id, u.username, u.role, u.enabled, coalesce(u.source_connector_id, 0), coalesce(c.kind, ''),
	coalesce(c.config->>'tenantId', ''), coalesce(u.external_id, ''), u.break_glass
	FROM users u LEFT JOIN directory_connectors c ON c.id = u.source_connector_id`

func scanUser(r scanner) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.ConnectorID, &u.ConnectorKind, &u.ConnectorTenant, &u.ExternalID, &u.BreakGlass)
	return u, err
}

// GetUser implements Store.
func (p *PG) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(queryRow(ctx, p.DB, userSelect+` WHERE u.id = @id`, momo.Named("id", id)))
	return u, mapErr(err)
}

// IdentityUser implements Store.
func (p *PG) IdentityUser(ctx context.Context, provider, subject string) (User, error) {
	u, err := scanUser(queryRow(ctx, p.DB, userSelect+` JOIN user_identities i ON i.user_id = u.id WHERE i.provider = @provider AND i.subject = @subject`,
		momo.Named("provider", provider), momo.Named("subject", subject)))
	return u, mapErr(err)
}

// SyncedUser implements Store.
func (p *PG) SyncedUser(ctx context.Context, kind, externalID string) (User, error) {
	u, err := scanUser(queryRow(ctx, p.DB, userSelect+` WHERE c.kind = @kind AND lower(u.external_id) = lower(@external_id)`,
		momo.Named("kind", kind), momo.Named("external_id", externalID)))
	return u, mapErr(err)
}

// SyncedUsersByEmail implements Store.
func (p *PG) SyncedUsersByEmail(ctx context.Context, email string) ([]User, error) {
	var out []User
	err := queryEach(ctx, p.DB, func(r scanner) error {
		u, err := scanUser(r)
		if err != nil {
			return err
		}
		out = append(out, u)
		return nil
	}, userSelect+` WHERE u.source_connector_id IS NOT NULL AND lower(u.email) = lower(@email) ORDER BY u.id`, momo.Named("email", email))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// insertIdentity links an identity to a user; a taken (provider, subject)
// or (user, provider) is ErrLinked.
func insertIdentity(ctx context.Context, tx *momo.Tx, userID int64, id Identity) error {
	_, err := momo.Exec(ctx, tx, `
		INSERT INTO user_identities (user_id, provider, subject, tenant, email, last_login_at)
		VALUES (@user_id, @provider, @subject, @tenant, @email, now())`,
		momo.Named("user_id", userID), momo.Named("provider", id.Provider), momo.Named("subject", id.Subject),
		momo.Named("tenant", id.Tenant), momo.Named("email", id.Email))
	if isUnique(err) {
		return ErrLinked
	}
	return err
}

// Link implements Store.
func (p *PG) Link(ctx context.Context, actor string, id Identity) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		if err := insertIdentity(ctx, tx, id.UserID, id); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "link-identity:"+id.Provider, "user", strconv.FormatInt(id.UserID, 10))
	})
}

// TouchIdentity implements Store.
func (p *PG) TouchIdentity(ctx context.Context, provider, subject string, at time.Time) error {
	_, err := momo.Exec(ctx, p.DB, `UPDATE user_identities SET last_login_at = @at WHERE provider = @provider AND subject = @subject`,
		momo.Named("provider", provider), momo.Named("subject", subject), momo.Named("at", at))
	return err
}

// ListIdentities implements Store.
func (p *PG) ListIdentities(ctx context.Context, userID int64) ([]Identity, error) {
	out := []Identity{}
	err := queryEach(ctx, p.DB, func(r scanner) error {
		var (
			i    Identity
			last sql.NullTime
		)
		if err := r.Scan(&i.ID, &i.UserID, &i.Provider, &i.Subject, &i.Tenant, &i.Email, &i.CreatedAt, &last); err != nil {
			return err
		}
		if last.Valid {
			i.LastLoginAt = &last.Time
		}
		out = append(out, i)
		return nil
	}, `SELECT id, user_id, provider, subject, tenant, email, created_at, last_login_at
		FROM user_identities WHERE user_id = @user_id ORDER BY provider`, momo.Named("user_id", userID))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Unlink implements Store.
func (p *PG) Unlink(ctx context.Context, actor string, userID int64, provider string) error {
	return p.tx(ctx, func(tx *momo.Tx) error {
		n, err := momo.Exec(ctx, tx, `DELETE FROM user_identities WHERE user_id = @user_id AND provider = @provider`,
			momo.Named("user_id", userID), momo.Named("provider", provider))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, actor, "unlink-identity:"+provider, "user", strconv.FormatInt(userID, 10))
	})
}

// CreateUser implements Store: a user role account with the no-match
// password marker, linked to id.
func (p *PG) CreateUser(ctx context.Context, actor, username, email, display string, id Identity) (User, error) {
	var u User
	err := p.tx(ctx, func(tx *momo.Tx) error {
		var uid int64
		if err := queryRow(ctx, tx, `
			INSERT INTO users (username, password_hash, role, display_name, email, enabled, must_change_password)
			VALUES (@username, '!', 'user', @display_name, @email, TRUE, FALSE) RETURNING id`,
			momo.Named("username", username), momo.Named("display_name", display), momo.Named("email", email)).Scan(&uid); err != nil {
			if isUnique(err) {
				return ErrLinked
			}
			return err
		}
		if err := insertIdentity(ctx, tx, uid, id); err != nil {
			return err
		}
		if err := audit(ctx, tx, actor, "create-jit", "user", strconv.FormatInt(uid, 10)); err != nil {
			return err
		}
		var err error
		u, err = scanUser(queryRow(ctx, tx, userSelect+` WHERE u.id = @id`, momo.Named("id", uid)))
		return err
	})
	return u, err
}
