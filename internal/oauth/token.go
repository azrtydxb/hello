package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// maxForm bounds a token, revoke or register request body.
const maxForm = 64 << 10

// tokenError is an RFC 6749 error answer.
type tokenError struct {
	Status int
	Code   string
	Desc   string
}

func (e *tokenError) Error() string { return e.Code + ": " + e.Desc }

func terr(status int, code, desc string) *tokenError {
	return &tokenError{Status: status, Code: code, Desc: desc}
}

var errBadGrant = terr(http.StatusBadRequest, "invalid_grant", "the grant is invalid, expired or revoked")

// tokenResponse is the RFC 6749 section 5.1 answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func writeTokenError(w http.ResponseWriter, e *tokenError) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="hello"`)
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Code, "error_description": e.Desc})
}

// clientAuth is the client identification of a token or revoke request:
// HTTP Basic or the form's client_id and client_secret.
type clientAuth struct {
	ID, Secret string
}

func readClientAuth(r *http.Request) (clientAuth, *tokenError) {
	formID := r.PostForm.Get("client_id")
	if id, secret, ok := r.BasicAuth(); ok {
		// RFC 6749 2.3.1: the credentials are form-encoded inside Basic.
		id, _ = urlUnescape(id)
		secret, _ = urlUnescape(secret)
		if r.PostForm.Has("client_secret") || (formID != "" && formID != id) {
			return clientAuth{}, terr(http.StatusBadRequest, "invalid_request", "use one client authentication method")
		}
		return clientAuth{ID: id, Secret: secret}, nil
	}
	if formID == "" {
		return clientAuth{}, terr(http.StatusUnauthorized, "invalid_client", "client_id is required")
	}
	return clientAuth{ID: formID, Secret: r.PostForm.Get("client_secret")}, nil
}

// token is POST /oauth/token (spec S-9, S-11).
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		s.tokenFail(w, r, "", terr(http.StatusBadRequest, "invalid_request", "the body must be a form"))
		return
	}
	ca, te := readClientAuth(r)
	if te != nil {
		s.tokenFail(w, r, "", te)
		return
	}
	var (
		resp  tokenResponse
		grant = r.PostForm.Get("grant_type")
		err   error
	)
	ctx := auth.WithActor(r.Context(), auth.Actor{ClientID: ca.ID})
	switch grant {
	case "authorization_code":
		resp, err = s.exchangeCode(ctx, r, ca)
	case "refresh_token":
		resp, err = s.refresh(ctx, r, ca)
	case "client_credentials":
		resp, err = s.clientCredentials(ctx, r, ca)
	default:
		err = terr(http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code, refresh_token or client_credentials")
	}
	if err != nil {
		var e *tokenError
		switch {
		case errors.As(err, &e):
		case errors.Is(err, ErrInvalidGrant), errors.Is(err, ErrNotFound):
			e = errBadGrant
		case errors.Is(err, ErrReused):
			s.o.Log.Warn("oauth credential reused; grant revoked", "client_id", ca.ID, "grant_type", grant)
			e = errBadGrant
		default:
			s.o.Log.Error("oauth token", "client_id", ca.ID, "grant_type", grant, "error", err)
			e = terr(http.StatusServiceUnavailable, "temporarily_unavailable", "the authorization server is unavailable")
		}
		s.tokenFail(w, r, ca.ID, e)
		return
	}
	s.o.Metrics.issued(grant)
	s.o.Log.Info("oauth token issued", "client_id", ca.ID, "grant_type", grant, "scope", resp.Scope)
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp) //nolint:gosec // G117: the token response is where tokens are issued
}

// tokenFail answers e, counting a failed client authentication or grant
// against the client and IP: past the limit the answer is 429.
func (s *Server) tokenFail(w http.ResponseWriter, r *http.Request, clientID string, e *tokenError) {
	s.o.Metrics.failed(e.Code)
	if clientID != "" && (e.Code == "invalid_client" || e.Code == "invalid_grant") {
		ok, err := s.o.Limiter.Allow(r.Context(), "client:"+clientID+":"+clientIP(r), clientFailLimit, clientFailWindow)
		if err == nil && !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(clientFailWindow.Seconds())))
			e = terr(http.StatusTooManyRequests, "slow_down", "too many failed attempts; retry later")
		}
	}
	writeTokenError(w, e)
}

// verifyPKCE reports whether verifier hashes to the S256 challenge.
func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// audience is the requested resources (each one of granted), or all of
// granted when none is requested.
func (s *Server) audience(requested, granted []string) ([]string, *tokenError) {
	if len(requested) == 0 {
		return slices.Clone(granted), nil
	}
	rs, ok := s.resources(requested)
	if !ok {
		return nil, terr(http.StatusBadRequest, "invalid_target", "unknown resource")
	}
	for _, r := range rs {
		if !slices.Contains(granted, r) {
			return nil, terr(http.StatusBadRequest, "invalid_target", "resource was not granted")
		}
	}
	return rs, nil
}

// newToken returns a prefixed credential and its stored form.
func newToken(prefix, kind string, base Token, expires time.Time) (string, Token) {
	plain, hash := auth.NewPrefixed(prefix)
	base.Hash, base.Kind, base.ExpiresAt = hash, kind, expires
	return plain, base
}

func (s *Server) exchangeCode(ctx context.Context, r *http.Request, ca clientAuth) (tokenResponse, error) {
	f := r.PostForm
	code, verifier, redirect := f.Get("code"), f.Get("code_verifier"), f.Get("redirect_uri")
	if code == "" || verifier == "" || redirect == "" {
		return tokenResponse{}, terr(http.StatusBadRequest, "invalid_request", "code, code_verifier and redirect_uri are required")
	}
	if ca.Secret != "" {
		return tokenResponse{}, terr(http.StatusUnauthorized, "invalid_client", "public clients do not authenticate with a secret")
	}
	var resp tokenResponse
	now := s.now()
	err := s.o.Store.RedeemCode(ctx, "oauth:"+ca.ID, auth.HashToken(code), func(q AuthRequest) (Grant, []Token, error) {
		if q.ClientID != ca.ID || q.RedirectURI != redirect || !verifyPKCE(verifier, q.CodeChallenge) {
			return Grant{}, nil, errBadGrant
		}
		aud, te := s.audience(f["resource"], q.Resources)
		if te != nil {
			return Grant{}, nil, te
		}
		base := Token{ClientID: q.ClientID, UserID: q.UserID, Scopes: q.Scopes, Resources: aud, CreatedAt: now}
		at, access := newToken(auth.PrefixAccess, TokenAccess, base, now.Add(s.o.AccessTTL))
		base.Resources = q.Resources
		rt, refresh := newToken(auth.PrefixRefresh, TokenRefresh, base, s.refreshExpiry(now, now))
		resp = tokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int64(s.o.AccessTTL.Seconds()),
			RefreshToken: rt, Scope: q.Scopes.String()}
		return Grant{UserID: q.UserID, ClientID: q.ClientID, Scopes: q.Scopes, Resources: q.Resources},
			[]Token{access, refresh}, nil
	})
	return resp, err
}

// refreshExpiry is a new refresh token's expiry: RefreshIdle from now, but
// never past RefreshMax after the grant was created.
func (s *Server) refreshExpiry(now, grantCreated time.Time) time.Time {
	return minTime(now.Add(s.o.RefreshIdle), grantCreated.Add(s.o.RefreshMax))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Server) refresh(ctx context.Context, r *http.Request, ca clientAuth) (tokenResponse, error) {
	f := r.PostForm
	rtPlain := f.Get("refresh_token")
	if rtPlain == "" {
		return tokenResponse{}, terr(http.StatusBadRequest, "invalid_request", "refresh_token is required")
	}
	var narrow auth.Scopes
	if v := f.Get("scope"); v != "" {
		var err error
		if narrow, err = ParseRequestScopes(v); err != nil {
			return tokenResponse{}, terr(http.StatusBadRequest, "invalid_scope", err.Error())
		}
	}
	var resp tokenResponse
	now := s.now()
	err := s.o.Store.Refresh(ctx, "oauth:"+ca.ID, auth.HashToken(rtPlain), s.o.RefreshMax, func(old Token, g Grant) ([]Token, error) {
		if old.ClientID != ca.ID {
			return nil, errBadGrant
		}
		scopes := g.Scopes
		if narrow != nil {
			for _, sc := range narrow {
				if !g.Scopes.Has(sc) {
					return nil, terr(http.StatusBadRequest, "invalid_scope", "scope "+string(sc)+" was not granted")
				}
			}
			scopes = narrow
		}
		aud, te := s.audience(f["resource"], g.Resources)
		if te != nil {
			return nil, te
		}
		base := Token{GrantID: g.ID, ClientID: g.ClientID, UserID: g.UserID, Scopes: scopes, Resources: aud, CreatedAt: now}
		at, access := newToken(auth.PrefixAccess, TokenAccess, base, now.Add(s.o.AccessTTL))
		base.Scopes, base.Resources = g.Scopes, g.Resources
		rt, refresh := newToken(auth.PrefixRefresh, TokenRefresh, base, s.refreshExpiry(now, g.CreatedAt))
		resp = tokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int64(s.o.AccessTTL.Seconds()),
			RefreshToken: rt, Scope: scopes.String()}
		return []Token{access, refresh}, nil
	})
	return resp, err
}

func (s *Server) clientCredentials(ctx context.Context, r *http.Request, ca clientAuth) (tokenResponse, error) {
	if ca.Secret == "" {
		return tokenResponse{}, terr(http.StatusUnauthorized, "invalid_client", "client authentication is required")
	}
	f := r.PostForm
	var requested auth.Scopes
	if v := f.Get("scope"); v != "" {
		var err error
		if requested, err = ParseRequestScopes(v); err != nil {
			return tokenResponse{}, terr(http.StatusBadRequest, "invalid_scope", err.Error())
		}
	}
	var resp tokenResponse
	now := s.now()
	err := s.o.Store.ClientCredentials(ctx, ca.ID, auth.HashToken(ca.Secret), func(c Client) (Token, error) {
		// The account's scopes, bounded by its role now.
		allowed := auth.Scopes{}
		for _, sc := range c.Scopes {
			if auth.ServiceOnly(sc) || auth.GrantableScopes(c.Role).Has(sc) {
				allowed = append(allowed, sc)
			}
		}
		scopes := allowed
		if requested != nil {
			for _, sc := range requested {
				if !allowed.Has(sc) {
					return Token{}, terr(http.StatusBadRequest, "invalid_scope", "scope "+string(sc)+" is not the account's")
				}
			}
			scopes = requested
		}
		aud, te := s.audience(f["resource"], []string{s.api, s.mcp})
		if te != nil {
			return Token{}, te
		}
		at, access := newToken(auth.PrefixAccess, TokenAccess,
			Token{ClientID: c.ID, Scopes: scopes, Resources: aud, CreatedAt: now}, now.Add(serviceTokenTTL))
		resp = tokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int64(serviceTokenTTL.Seconds()), Scope: scopes.String()}
		return access, nil
	})
	if errors.Is(err, ErrInvalidGrant) {
		return resp, terr(http.StatusUnauthorized, "invalid_client", "client authentication failed")
	}
	return resp, err
}

// revoke is POST /oauth/revoke (RFC 7009): it answers 200 whether or not
// the token was known.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("token") == "" {
		writeTokenError(w, terr(http.StatusBadRequest, "invalid_request", "token is required"))
		return
	}
	ca, te := readClientAuth(r)
	if te != nil {
		writeTokenError(w, te)
		return
	}
	ctx := auth.WithActor(r.Context(), auth.Actor{ClientID: ca.ID})
	if err := s.o.Store.RevokeToken(ctx, "oauth:"+ca.ID, auth.HashToken(r.PostForm.Get("token"))); err != nil {
		s.o.Log.Error("oauth revoke", "client_id", ca.ID, "error", err)
		writeTokenError(w, terr(http.StatusServiceUnavailable, "temporarily_unavailable", "the authorization server is unavailable"))
		return
	}
	s.o.Log.Info("oauth token revoked", "client_id", ca.ID)
	noStore(w)
	w.WriteHeader(http.StatusOK)
}

// urlUnescape decodes a form-encoded Basic credential; on error it keeps
// the raw value, which then fails authentication.
func urlUnescape(v string) (string, error) {
	out, err := url.QueryUnescape(v)
	if err != nil {
		return v, err
	}
	return out, nil
}
