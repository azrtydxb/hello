package oauth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
)

// authorize is GET /oauth/authorize (spec S-8). Until the client and its
// redirect URI are validated, errors are shown on Hello's own page; after,
// they are redirected to the client with state and iss.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	if clientID == "" {
		errorPage(w, http.StatusBadRequest, "The request has no client_id.")
		return
	}
	c, err := s.resolveClient(r.Context(), clientID)
	if err != nil {
		var ie *InputError
		if errors.As(err, &ie) {
			errorPage(w, http.StatusBadRequest, ie.Msg)
			return
		}
		s.o.Log.Error("oauth authorize: load client", "error", err)
		errorPage(w, http.StatusServiceUnavailable, "The authorization server is unavailable; try again later.")
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" || !redirectMatches(c.RedirectURIs, redirect) {
		errorPage(w, http.StatusBadRequest, "The redirect_uri is not registered for this client.")
		return
	}
	state := q.Get("state")
	// The redirect URI is registered for the client (checked above), so
	// redirecting errors to it is the protocol, not an open redirect.
	fail := func(code, desc string) {
		to := s.clientRedirect(redirect, url.Values{"error": {code}, "error_description": {desc}, "state": {state}})
		http.Redirect(w, r, to, http.StatusFound) //nolint:gosec // G710: registered redirect URI, see above
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "response_type must be code")
		return
	}
	challenge := q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || !validChallenge(challenge) {
		fail("invalid_request", "PKCE with code_challenge_method S256 is required")
		return
	}
	scopes := auth.Scopes{auth.ScopeRead}
	if v := q.Get("scope"); v != "" {
		scopes, err = ParseRequestScopes(v)
		if err != nil {
			fail("invalid_scope", err.Error())
			return
		}
	}
	resources, ok := s.resources(q["resource"])
	if !ok {
		fail("invalid_target", "resource must be "+s.api+" or "+s.mcp)
		return
	}
	req := AuthRequest{
		ID: rand.Text(), ClientID: c.ID, RedirectURI: redirect, State: state, CodeChallenge: challenge,
		Scopes: scopes, Resources: resources, ExpiresAt: s.now().Add(requestTTL),
	}
	if err := s.o.Store.CreateRequest(r.Context(), req); err != nil {
		s.o.Log.Error("oauth authorize: store request", "error", err)
		fail("temporarily_unavailable", "the authorization server is unavailable")
		return
	}
	s.o.Log.Info("oauth authorization requested", "client_id", c.ID, "scope", scopes.String())
	http.Redirect(w, r, s.public+"/oauth/consent?request="+url.QueryEscape(req.ID), http.StatusFound)
}

// validChallenge reports whether c has the shape of an S256 challenge: 43
// base64url characters.
func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	return strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") == ""
}

// ParseRequestScopes parses the scopes a client asks for: known scopes
// other than session, at least one.
func ParseRequestScopes(v string) (auth.Scopes, error) {
	ss, err := auth.ParseScopes(v)
	if err != nil {
		return nil, err
	}
	if slices.Contains(ss, auth.ScopeSession) {
		return nil, errors.New("scope session cannot be requested")
	}
	if len(ss) == 0 {
		return nil, errors.New("scope is empty")
	}
	return ss, nil
}

// resources normalises the requested resource indicators (RFC 8707) and
// checks each is one of Hello's two; none means both.
func (s *Server) resources(in []string) ([]string, bool) {
	if len(in) == 0 {
		return []string{s.api, s.mcp}, true
	}
	var out []string
	for _, r := range in {
		n, ok := s.normalizeResource(r)
		if !ok {
			return nil, false
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, true
}

// normalizeResource compares r with the resource identifiers after RFC
// 3986 normalisation: scheme and host case, a default port, a trailing
// slash.
func (s *Server) normalizeResource(r string) (string, bool) {
	u, err := url.Parse(r)
	if err != nil || u.Fragment != "" || u.RawQuery != "" || u.User != nil {
		return "", false
	}
	host := strings.ToLower(u.Host)
	scheme := strings.ToLower(u.Scheme)
	host = strings.TrimSuffix(host, map[string]string{"https": ":443", "http": ":80"}[scheme])
	n := scheme + "://" + host + strings.TrimSuffix(u.EscapedPath(), "/")
	for _, known := range []string{s.api, s.mcp} {
		if n == known {
			return known, true
		}
	}
	return "", false
}

// clientRedirect appends v and iss (RFC 9207) to the client's redirect URI.
func (s *Server) clientRedirect(redirect string, v url.Values) string {
	u, _ := url.Parse(redirect)
	q := u.Query()
	for k, vs := range v {
		if len(vs) == 1 && vs[0] == "" {
			continue
		}
		q[k] = vs
	}
	q.Set("iss", s.public)
	u.RawQuery = q.Encode()
	return u.String()
}

// resolveClient returns a registered client, or fetches (and caches) a
// client ID metadata document. Its InputErrors are for the error page.
func (s *Server) resolveClient(ctx context.Context, id string) (Client, error) {
	c, err := s.o.Store.Client(ctx, id)
	switch {
	case err == nil && c.Kind == ClientService:
		return Client{}, inputErr("Service accounts use the client credentials grant, not authorization.")
	case err == nil && (c.Kind != ClientCIMD || c.CacheUntil.After(s.now())):
		return c, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return Client{}, err
	}
	if !isCIMDClientID(id) {
		return Client{}, inputErr("The client is not registered and its client_id is not a metadata document URL.")
	}
	fetched, ferr := s.cimd.fetch(ctx, id, s.now())
	if ferr != nil {
		s.o.Log.Warn("oauth client metadata document refused", "client_id", id, "reason", ferr.Error())
		return Client{}, inputErr("The client's metadata document was refused: " + ferr.Error() + ".")
	}
	if err := s.o.Store.SaveCIMDClient(ctx, fetched); err != nil {
		return Client{}, err
	}
	return fetched, nil
}

// Request returns the pending authorization request id for the consent
// screen.
func (s *Server) Request(ctx context.Context, id string) (ConsentView, error) {
	q, err := s.o.Store.Request(ctx, id)
	if err != nil {
		return ConsentView{}, err
	}
	c, err := s.o.Store.Client(ctx, q.ClientID)
	if err != nil {
		return ConsentView{}, err
	}
	return ConsentView{
		ID: q.ID, ClientID: c.ID, ClientName: c.Name, ClientURI: c.ClientURI, RedirectURI: q.RedirectURI,
		Verified: c.Kind != ClientDCR, Scopes: q.Scopes, Resources: q.Resources, ExpiresAt: q.ExpiresAt,
	}, nil
}

// Approve grants the request id with scopes (a subset of the requested and
// of a's GrantableScopes) and returns the client redirect carrying the code.
// Only a browser session may approve.
func (s *Server) Approve(ctx context.Context, a auth.Actor, id string, scopes auth.Scopes) (string, error) {
	if a.Kind != auth.KindSession {
		return "", inputErr("consent needs a browser session")
	}
	q, err := s.o.Store.Request(ctx, id)
	if err != nil {
		return "", err
	}
	if len(scopes) == 0 {
		return "", inputErr("approve at least one scope")
	}
	grantable := auth.GrantableScopes(a.Role)
	for _, sc := range scopes {
		switch {
		case sc == auth.ScopeSession:
			return "", inputErr("scope session cannot be granted")
		case !q.Scopes.Has(sc):
			return "", inputErr("scope " + string(sc) + " was not requested")
		case !grantable.Has(sc):
			return "", inputErr("your role cannot grant scope " + string(sc))
		}
	}
	code, hash := auth.NewToken()
	if err := s.o.Store.ApproveRequest(ctx, a.String(), id, a.UserID, scopes, hash, s.now().Add(codeTTL)); err != nil {
		return "", err
	}
	s.o.Log.Info("oauth consent approved", "client_id", q.ClientID, "actor", a.String(), "scope", scopes.String())
	return s.clientRedirect(q.RedirectURI, url.Values{"code": {code}, "state": {q.State}}), nil
}

// Deny refuses the request id and returns the client redirect carrying
// access_denied.
func (s *Server) Deny(ctx context.Context, a auth.Actor, id string) (string, error) {
	if a.Kind != auth.KindSession {
		return "", inputErr("consent needs a browser session")
	}
	q, err := s.o.Store.Request(ctx, id)
	if err != nil {
		return "", err
	}
	if err := s.o.Store.DenyRequest(ctx, a.String(), id); err != nil {
		return "", err
	}
	s.o.Log.Info("oauth consent denied", "client_id", q.ClientID, "actor", a.String())
	return s.clientRedirect(q.RedirectURI, url.Values{"error": {"access_denied"}, "state": {q.State}}), nil
}
