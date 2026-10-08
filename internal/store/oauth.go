package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
)

// The OAuth 2.1 authorization server's persistence (spec ai-external-access
// S-6 to S-12): *Store implements oauth.Store. Only SHA-256 hashes of codes,
// tokens and secrets are stored, and every credential, client, grant and
// consent change writes its audit row in the same transaction.

var _ oauth.Store = (*Store)(nil)

// oauthErr maps the package sentinels to the oauth ones.
func oauthErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrNotFound):
		return oauth.ErrNotFound
	case errors.Is(err, ErrConflict):
		return oauth.ErrConflict
	}
	return err
}

const clientCols = `client_id, kind, name, coalesce(client_uri, ''), to_json(redirect_uris)::text,
	coalesce(metadata::text, ''), cache_until, created_at, last_used_at, coalesce(description, ''),
	coalesce(role, ''), to_json(scopes)::text, enabled, created_by`

type rowScanner interface{ Scan(...any) error }

func scanClient(r rowScanner) (oauth.Client, error) {
	var (
		c                oauth.Client
		redirects        string
		meta             string
		cache, last      sql.NullTime
		scopes           sql.NullString
		createdBy        sql.NullInt64
		role, clientKind string
	)
	if err := r.Scan(&c.ID, &clientKind, &c.Name, &c.ClientURI, &redirects, &meta, &cache, &c.CreatedAt, &last,
		&c.Description, &role, &scopes, &c.Enabled, &createdBy); err != nil {
		return c, err
	}
	c.Kind, c.Role = clientKind, auth.Role(role)
	if err := json.Unmarshal([]byte(redirects), &c.RedirectURIs); err != nil {
		return c, err
	}
	if meta != "" {
		c.Metadata = []byte(meta)
	}
	if cache.Valid {
		c.CacheUntil = cache.Time
	}
	c.LastUsedAt = nullTime(last)
	if createdBy.Valid {
		c.CreatedBy = &createdBy.Int64
	}
	var err error
	if scopes.Valid {
		c.Scopes, err = scanScopes(scopes)
	}
	return c, err
}

// Client implements oauth.Store.
func (s *Store) Client(ctx context.Context, id string) (oauth.Client, error) {
	c, err := scanClient(s.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM oauth_clients WHERE client_id = $1`, id))
	return c, oauthErr(err)
}

func jsonOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// SaveCIMDClient implements oauth.Store.
func (s *Store) SaveCIMDClient(ctx context.Context, c oauth.Client) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_clients (client_id, kind, name, client_uri, redirect_uris, metadata, fetched_at, cache_until)
		VALUES ($1, 'cimd', $2, NULLIF($3, ''), $4::text[], $5::jsonb, now(), $6)
		ON CONFLICT (client_id) DO UPDATE SET name = EXCLUDED.name, client_uri = EXCLUDED.client_uri,
			redirect_uris = EXCLUDED.redirect_uris, metadata = EXCLUDED.metadata,
			fetched_at = now(), cache_until = EXCLUDED.cache_until
		WHERE oauth_clients.kind = 'cimd'`,
		c.ID, c.Name, c.ClientURI, nonNil(c.RedirectURIs), jsonOrNil(c.Metadata), c.CacheUntil)
	return err
}

// RegisterClient implements oauth.Store.
func (s *Store) RegisterClient(ctx context.Context, actor string, c oauth.Client) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_clients (client_id, kind, name, client_uri, redirect_uris, metadata)
			VALUES ($1, 'dcr', $2, NULLIF($3, ''), $4::text[], $5::jsonb)`,
			c.ID, c.Name, c.ClientURI, nonNil(c.RedirectURIs), jsonOrNil(c.Metadata)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "register", "oauth_client", c.ID)
	}))
}

// CreateRequest implements oauth.Store.
func (s *Store) CreateRequest(ctx context.Context, r oauth.AuthRequest) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_requests (id, client_id, redirect_uri, state, code_challenge, scopes, resources, expires_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6::text[], $7::text[], $8)`,
		r.ID, r.ClientID, r.RedirectURI, r.State, r.CodeChallenge, scopeArg(r.Scopes), nonNil(r.Resources), r.ExpiresAt)
	return oauthErr(mapErr(err))
}

const requestCols = `id, client_id, redirect_uri, coalesce(state, ''), code_challenge,
	to_json(scopes)::text, to_json(resources)::text, created_at, expires_at, coalesce(user_id, 0)`

func scanRequest(r rowScanner) (oauth.AuthRequest, error) {
	var (
		q                 oauth.AuthRequest
		scopes, resources sql.NullString
	)
	if err := r.Scan(&q.ID, &q.ClientID, &q.RedirectURI, &q.State, &q.CodeChallenge, &scopes, &resources,
		&q.CreatedAt, &q.ExpiresAt, &q.UserID); err != nil {
		return q, err
	}
	var err error
	if q.Scopes, err = scanScopes(scopes); err != nil {
		return q, err
	}
	return q, json.Unmarshal([]byte(resources.String), &q.Resources)
}

// Request implements oauth.Store.
func (s *Store) Request(ctx context.Context, id string) (oauth.AuthRequest, error) {
	q, err := scanRequest(s.db.QueryRowContext(ctx, `SELECT `+requestCols+` FROM oauth_requests
		WHERE id = $1 AND expires_at > now() AND user_id IS NULL`, id))
	return q, oauthErr(err)
}

// ApproveRequest implements oauth.Store.
func (s *Store) ApproveRequest(ctx context.Context, actor, id string, userID int64, scopes auth.Scopes, codeHash []byte, codeExpires time.Time) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE oauth_requests SET user_id = $2, scopes = $3::text[], code_hash = $4, code_expires_at = $5
			WHERE id = $1 AND expires_at > now() AND user_id IS NULL`,
			id, userID, scopeArg(scopes), codeHash, codeExpires)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "approve", "oauth_request", id)
	}))
}

// DenyRequest implements oauth.Store.
func (s *Store) DenyRequest(ctx context.Context, actor, id string) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM oauth_requests WHERE id = $1 AND user_id IS NULL`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "deny", "oauth_request", id)
	}))
}

// insertTokens stores issued tokens (their hashes) in tx.
func insertTokens(ctx context.Context, tx *sql.Tx, ts []oauth.Token) error {
	for _, t := range ts {
		var grant, user any
		if t.GrantID != 0 {
			grant = t.GrantID
		}
		if t.UserID != 0 {
			user = t.UserID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_tokens (hash, kind, grant_id, client_id, user_id, scopes, resources, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6::text[], $7::text[], $8)`,
			t.Hash, t.Kind, grant, t.ClientID, user, scopeArg(t.Scopes), nonNil(t.Resources), t.ExpiresAt); err != nil {
			return err
		}
	}
	return nil
}

// revokeGrants revokes the grants matching where (with args) and every
// token issued under them.
func revokeGrants(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]int64, error) {
	//nolint:gosec // G202: where is a caller's constant condition; values are parameters.
	rows, err := tx.QueryContext(ctx, `UPDATE oauth_grants SET revoked_at = now()
		WHERE revoked_at IS NULL AND `+where+` RETURNING id`, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = now()
			WHERE grant_id = ANY($1::bigint[]) AND revoked_at IS NULL`, ids); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// RedeemCode implements oauth.Store.
func (s *Store) RedeemCode(ctx context.Context, actor string, codeHash []byte,
	issue func(oauth.AuthRequest) (oauth.Grant, []oauth.Token, error)) error {
	reused := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		q, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestCols+` FROM oauth_requests
			WHERE code_hash = $1 FOR UPDATE`, codeHash))
		if errors.Is(err, sql.ErrNoRows) {
			return oauth.ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		var used sql.NullTime
		var expires time.Time
		if err := tx.QueryRowContext(ctx, `SELECT code_used_at, code_expires_at FROM oauth_requests WHERE id = $1`,
			q.ID).Scan(&used, &expires); err != nil {
			return err
		}
		if used.Valid {
			// A second use: revoke what the first use issued (every grant
			// the user gave the client since the request).
			ids, err := revokeGrants(ctx, tx, `user_id = $1 AND client_id = $2 AND created_at >= $3`,
				q.UserID, q.ClientID, q.CreatedAt)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := insertAudit(ctx, tx, "system", "revoke_reused_code", "oauth_grant", strconv.FormatInt(id, 10)); err != nil {
					return err
				}
			}
			reused = true
			return nil
		}
		if !expires.After(time.Now()) {
			return oauth.ErrInvalidGrant
		}
		g, toks, err := issue(q)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_requests SET code_used_at = now() WHERE id = $1`, q.ID); err != nil {
			return err
		}
		var gid int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO oauth_grants (user_id, client_id, scopes, resources, last_used_at)
			VALUES ($1, $2, $3::text[], $4::text[], now()) RETURNING id`,
			g.UserID, g.ClientID, scopeArg(g.Scopes), nonNil(g.Resources)).Scan(&gid); err != nil {
			return err
		}
		for i := range toks {
			toks[i].GrantID = gid
		}
		if err := insertTokens(ctx, tx, toks); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at = now() WHERE client_id = $1`, q.ClientID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "issue", "oauth_grant", strconv.FormatInt(gid, 10))
	})
	if reused {
		return oauth.ErrReused
	}
	return oauthErr(err)
}

// tokenCols are oauth_tokens' columns (hashes, never credentials).
//
//nolint:gosec // G101: column names, not a credential.
const tokenCols = `hash, kind, coalesce(grant_id, 0), client_id, coalesce(user_id, 0), to_json(scopes)::text,
	to_json(resources)::text, created_at, expires_at`

func scanToken(r rowScanner, spent, revoked *sql.NullTime) (oauth.Token, error) {
	var (
		t                 oauth.Token
		scopes, resources sql.NullString
	)
	if err := r.Scan(&t.Hash, &t.Kind, &t.GrantID, &t.ClientID, &t.UserID, &scopes, &resources, &t.CreatedAt,
		&t.ExpiresAt, spent, revoked); err != nil {
		return t, err
	}
	var err error
	if t.Scopes, err = scanScopes(scopes); err != nil {
		return t, err
	}
	return t, json.Unmarshal([]byte(resources.String), &t.Resources)
}

// Refresh implements oauth.Store.
func (s *Store) Refresh(ctx context.Context, actor string, hash []byte, maxAge time.Duration,
	issue func(old oauth.Token, g oauth.Grant) ([]oauth.Token, error)) error {
	reused := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var spent, revoked sql.NullTime
		old, err := scanToken(tx.QueryRowContext(ctx, `SELECT `+tokenCols+`, spent_at, revoked_at FROM oauth_tokens
			WHERE hash = $1 AND kind = 'refresh' FOR UPDATE`, hash), &spent, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return oauth.ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		if spent.Valid {
			ids, err := revokeGrants(ctx, tx, `id = $1`, old.GrantID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := insertAudit(ctx, tx, "system", "revoke_reused_refresh", "oauth_grant", strconv.FormatInt(id, 10)); err != nil {
					return err
				}
			}
			reused = true
			return nil
		}
		if revoked.Valid || !old.ExpiresAt.After(time.Now()) {
			return oauth.ErrInvalidGrant
		}
		g, err := grantByID(ctx, tx, old.GrantID)
		if errors.Is(err, sql.ErrNoRows) {
			return oauth.ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		if g.RevokedAt != nil || time.Since(g.CreatedAt) > maxAge {
			return oauth.ErrInvalidGrant
		}
		toks, err := issue(old, g)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET spent_at = now() WHERE hash = $1`, hash); err != nil {
			return err
		}
		if err := insertTokens(ctx, tx, toks); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "refresh", "oauth_grant", strconv.FormatInt(g.ID, 10))
	})
	if reused {
		return oauth.ErrReused
	}
	return oauthErr(err)
}

const grantCols = `g.id, g.user_id, u.username, g.client_id, c.name, c.kind, to_json(g.scopes)::text,
	to_json(g.resources)::text, g.created_at, g.last_used_at, g.revoked_at`

func scanGrant(r rowScanner) (oauth.Grant, error) {
	var (
		g                 oauth.Grant
		scopes, resources sql.NullString
		last, revoked     sql.NullTime
	)
	if err := r.Scan(&g.ID, &g.UserID, &g.Username, &g.ClientID, &g.ClientName, &g.ClientKind, &scopes, &resources,
		&g.CreatedAt, &last, &revoked); err != nil {
		return g, err
	}
	g.LastUsedAt, g.RevokedAt = nullTime(last), nullTime(revoked)
	var err error
	if g.Scopes, err = scanScopes(scopes); err != nil {
		return g, err
	}
	return g, json.Unmarshal([]byte(resources.String), &g.Resources)
}

func grantByID(ctx context.Context, tx *sql.Tx, id int64) (oauth.Grant, error) {
	return scanGrant(tx.QueryRowContext(ctx, `SELECT `+grantCols+` FROM oauth_grants g
		JOIN users u ON u.id = g.user_id JOIN oauth_clients c ON c.client_id = g.client_id
		WHERE g.id = $1`, id))
}

// ClientCredentials implements oauth.Store.
func (s *Store) ClientCredentials(ctx context.Context, clientID string, secretHash []byte,
	issue func(oauth.Client) (oauth.Token, error)) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		var secretID int64
		err := tx.QueryRowContext(ctx, `
			SELECT s.id FROM oauth_client_secrets s JOIN oauth_clients c ON c.client_id = s.client_id
			WHERE s.secret_hash = $1 AND s.client_id = $2 AND c.kind = 'service' AND c.enabled
			  AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at > now())`,
			secretHash, clientID).Scan(&secretID)
		if errors.Is(err, sql.ErrNoRows) {
			return oauth.ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		c, err := scanClient(tx.QueryRowContext(ctx, `SELECT `+clientCols+` FROM oauth_clients WHERE client_id = $1`, clientID))
		if err != nil {
			return err
		}
		t, err := issue(c)
		if err != nil {
			return err
		}
		if err := insertTokens(ctx, tx, []oauth.Token{t}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_client_secrets SET last_used_at = now() WHERE id = $1`, secretID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at = now() WHERE client_id = $1`, clientID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, "service:"+c.Name, "issue", "oauth_token", clientID)
	}))
}

// RevokeToken implements oauth.Store.
func (s *Store) RevokeToken(ctx context.Context, actor string, hash []byte) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		var (
			kind  string
			grant sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `UPDATE oauth_tokens SET revoked_at = coalesce(revoked_at, now())
			WHERE hash = $1 RETURNING kind, grant_id`, hash).Scan(&kind, &grant)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if kind == oauth.TokenRefresh && grant.Valid {
			if _, err := revokeGrants(ctx, tx, `id = $1`, grant.Int64); err != nil {
				return err
			}
			return insertAudit(ctx, tx, actor, "revoke", "oauth_grant", strconv.FormatInt(grant.Int64, 10))
		}
		return insertAudit(ctx, tx, actor, "revoke", "oauth_token", kind)
	}))
}

// Grants implements oauth.Store.
func (s *Store) Grants(ctx context.Context, userID int64) ([]oauth.Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+grantCols+` FROM oauth_grants g
		JOIN users u ON u.id = g.user_id JOIN oauth_clients c ON c.client_id = g.client_id
		WHERE g.revoked_at IS NULL AND ($1::bigint = 0 OR g.user_id = $1::bigint) ORDER BY g.id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []oauth.Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeGrant implements oauth.Store.
func (s *Store) RevokeGrant(ctx context.Context, actor string, id, userID int64) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		ids, err := revokeGrants(ctx, tx, `id = $1 AND ($2::bigint = 0 OR user_id = $2::bigint)`, id, userID)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return oauth.ErrNotFound
		}
		return insertAudit(ctx, tx, actor, "revoke", "oauth_grant", strconv.FormatInt(id, 10))
	}))
}

// ServiceAccounts implements oauth.Store.
func (s *Store) ServiceAccounts(ctx context.Context) ([]oauth.Client, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+clientCols+` FROM oauth_clients WHERE kind = 'service' ORDER BY created_at, client_id`)
	if err != nil {
		return nil, err
	}
	var out []oauth.Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Secrets, err = s.secrets(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []oauth.Client{}
	}
	return out, nil
}

func (s *Store) secrets(ctx context.Context, clientID string) ([]oauth.Secret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, expires_at, revoked_at, last_used_at
		FROM oauth_client_secrets WHERE client_id = $1 AND revoked_at IS NULL ORDER BY id`, clientID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []oauth.Secret{}
	for rows.Next() {
		var (
			sc                 oauth.Secret
			exp, revoked, last sql.NullTime
		)
		if err := rows.Scan(&sc.ID, &sc.CreatedAt, &exp, &revoked, &last); err != nil {
			return nil, err
		}
		sc.ExpiresAt, sc.RevokedAt, sc.LastUsedAt = nullTime(exp), nullTime(revoked), nullTime(last)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ServiceAccount implements oauth.Store.
func (s *Store) ServiceAccount(ctx context.Context, id string) (oauth.Client, error) {
	c, err := scanClient(s.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM oauth_clients
		WHERE client_id = $1 AND kind = 'service'`, id))
	if err != nil {
		return c, oauthErr(err)
	}
	c.Secrets, err = s.secrets(ctx, id)
	return c, err
}

// CreateServiceAccount implements oauth.Store.
func (s *Store) CreateServiceAccount(ctx context.Context, actor string, c oauth.Client) (oauth.Client, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_clients WHERE kind = 'service' AND name = $1)`,
			c.Name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return oauth.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_clients (client_id, kind, name, description, role, scopes, enabled, created_by)
			VALUES ($1, 'service', $2, $3, $4, $5::text[], $6, $7)`,
			c.ID, c.Name, c.Description, string(c.Role), scopeArg(c.Scopes), c.Enabled, c.CreatedBy); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "service_account", c.ID)
	})
	if err != nil {
		return oauth.Client{}, oauthErr(err)
	}
	return s.ServiceAccount(ctx, c.ID)
}

// UpdateServiceAccount implements oauth.Store.
func (s *Store) UpdateServiceAccount(ctx context.Context, actor, id string, ch oauth.ServiceAccountChange) (oauth.Client, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var role, scopes, enabled any
		if ch.Role != nil {
			role = string(*ch.Role)
		}
		if ch.Scopes != nil {
			scopes = scopeArg(*ch.Scopes)
		}
		if ch.Enabled != nil {
			enabled = *ch.Enabled
		}
		res, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET description = coalesce($2, description),
			role = coalesce($3, role), scopes = coalesce($4::text[], scopes), enabled = coalesce($5, enabled)
			WHERE client_id = $1 AND kind = 'service'`, id, ch.Description, role, scopes, enabled)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		// Narrowing or disabling stops the account's live tokens now.
		if ch.Enabled != nil || ch.Scopes != nil || ch.Role != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = now()
				WHERE client_id = $1 AND revoked_at IS NULL`, id); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, actor, "update", "service_account", id)
	})
	if err != nil {
		return oauth.Client{}, oauthErr(err)
	}
	return s.ServiceAccount(ctx, id)
}

// DeleteServiceAccount implements oauth.Store.
func (s *Store) DeleteServiceAccount(ctx context.Context, actor, id string) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients WHERE client_id = $1 AND kind = 'service'`, id)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "service_account", id)
	}))
}

// maxLiveSecrets is how many unrevoked secrets a service account may hold.
const maxLiveSecrets = 2

// AddClientSecret implements oauth.Store.
func (s *Store) AddClientSecret(ctx context.Context, actor, clientID string, hash []byte, expires *time.Time) (oauth.Secret, error) {
	sc := oauth.Secret{ExpiresAt: expires}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Lock the account so two concurrent adds cannot both see one
		// live secret.
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM oauth_clients WHERE client_id = $1 FOR UPDATE`,
			clientID).Scan(&kind); err != nil {
			return err
		}
		if kind != oauth.ClientService {
			return oauth.ErrNotFound
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM oauth_client_secrets
			WHERE client_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`,
			clientID).Scan(&n); err != nil {
			return err
		}
		if n >= maxLiveSecrets {
			return oauth.ErrTooManySecrets
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO oauth_client_secrets (client_id, secret_hash, expires_at)
			VALUES ($1, $2, $3) RETURNING id, created_at`, clientID, hash, expires).Scan(&sc.ID, &sc.CreatedAt); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "client_secret", strconv.FormatInt(sc.ID, 10))
	})
	return sc, oauthErr(err)
}

// RevokeClientSecret implements oauth.Store. The account's access tokens
// are revoked with it (a token does not record which secret issued it), so
// a stolen secret's tokens stop on the next request.
func (s *Store) RevokeClientSecret(ctx context.Context, actor, clientID string, secretID int64) error {
	return oauthErr(s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE oauth_client_secrets SET revoked_at = now()
			WHERE id = $1 AND client_id = $2 AND revoked_at IS NULL`, secretID, clientID)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = now()
			WHERE client_id = $1 AND revoked_at IS NULL`, clientID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "revoke", "client_secret", strconv.FormatInt(secretID, 10))
	}))
}

// PruneOAuth implements oauth.Store.
func (s *Store) PruneOAuth(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM oauth_tokens WHERE expires_at < $1 OR revoked_at < $1`,
		`DELETE FROM oauth_requests WHERE expires_at < $1 AND (code_expires_at IS NULL OR code_expires_at < $1)`,
		`DELETE FROM oauth_grants WHERE revoked_at < $1`,
		// Dynamically registered clients that never completed a grant
		// within a day, or were unused for 30 days (spec S-10).
		`DELETE FROM oauth_clients WHERE kind = 'dcr' AND $1::timestamptz IS NOT NULL AND (
			(last_used_at IS NULL AND created_at < now() - interval '1 day')
			OR last_used_at < now() - interval '30 days')`,
		`DELETE FROM oauth_client_secrets WHERE revoked_at < $1 OR expires_at < $1`,
	} {
		res, err := s.db.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, fmt.Errorf("prune oauth: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
