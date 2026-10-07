package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
)

// AIAccess is the authorization server's management side (spec
// ai-external-access S-8, S-11, S-12); *oauth.Server implements it.
type AIAccess interface {
	Resources() (api, mcp string)
	MetadataURL(resource string) string
	Issuer() string
	DCR() bool

	Request(ctx context.Context, id string) (oauth.ConsentView, error)
	Approve(ctx context.Context, a auth.Actor, id string, scopes auth.Scopes) (string, error)
	Deny(ctx context.Context, a auth.Actor, id string) (string, error)

	Grants(ctx context.Context, a auth.Actor) ([]oauth.Grant, error)
	RevokeGrant(ctx context.Context, a auth.Actor, id int64) error

	ServiceAccounts(ctx context.Context) ([]oauth.Client, error)
	ServiceAccount(ctx context.Context, id string) (oauth.Client, error)
	CreateServiceAccount(ctx context.Context, a auth.Actor, in oauth.NewServiceAccount) (oauth.Client, error)
	UpdateServiceAccount(ctx context.Context, a auth.Actor, id string, ch oauth.ServiceAccountChange) (oauth.Client, error)
	DeleteServiceAccount(ctx context.Context, a auth.Actor, id string) error
	AddSecret(ctx context.Context, a auth.Actor, id string, expires *time.Time) (string, oauth.Secret, error)
	RevokeSecret(ctx context.Context, a auth.Actor, id string, secretID int64) error
}

var _ AIAccess = (*oauth.Server)(nil)

// MCPProtocolVersions are the MCP protocol versions /mcp negotiates.
var MCPProtocolVersions = []string{"2026-07-28", "2025-11-25"}

// scopeDescriptions are the plain-language lines of each grantable scope.
var scopeDescriptions = []struct {
	Name        auth.Scope `json:"name"`
	Description string     `json:"description"`
}{
	{auth.ScopeRead, "Read configuration, live calls, registrations and call records."},
	{auth.ScopeWrite, "Change configuration: extensions, devices, voicemail, recordings, announcements, ring groups, feature codes, trunks, routes, phones, templates and firmware."},
	{auth.ScopeAdmin, "Manage credentials and the cluster: API tokens, service accounts, other users' connected apps, vendor redirect credentials and node drain."},
	{auth.ScopeSecrets, "Reveal or rotate secrets in plaintext: device SIP secrets, phone tokens and phone admin passwords."},
}

// aiSettings is GET /api/v1/ai/settings.
func (s *server) aiSettings(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{
		"enabled": s.AI != nil, "publicUrl": "", "issuer": "", "apiUrl": "", "mcpUrl": "",
		"authorizationServerMetadataUrl": "", "apiMetadataUrl": "", "mcpMetadataUrl": "",
		"protocolVersions": MCPProtocolVersions, "dcr": false, "scopes": scopeDescriptions,
	}
	if s.AI != nil {
		apiURL, mcpURL := s.AI.Resources()
		iss := s.AI.Issuer()
		out["publicUrl"], out["issuer"], out["apiUrl"], out["mcpUrl"] = iss, iss, apiURL, mcpURL
		out["authorizationServerMetadataUrl"] = iss + "/.well-known/oauth-authorization-server"
		out["apiMetadataUrl"], out["mcpMetadataUrl"] = s.AI.MetadataURL(apiURL), s.AI.MetadataURL(mcpURL)
		out["dcr"] = s.AI.DCR()
	}
	writeJSON(w, http.StatusOK, out)
}

// aiEnabled answers 404 when external AI access is off.
func (s *server) aiEnabled(w http.ResponseWriter) bool {
	if s.AI == nil {
		writeError(w, http.StatusNotFound, "not_found", "external AI access is not enabled (HELLO_PUBLIC_URL)")
		return false
	}
	return true
}

// aiError answers an authorization server error.
func (s *server) aiError(w http.ResponseWriter, what string, err error) {
	var in *oauth.InputError
	switch {
	case errors.As(err, &in):
		badRequest(w, in.Msg)
	case errors.Is(err, oauth.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, oauth.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+": already exists")
	case errors.Is(err, oauth.ErrTooManySecrets):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		s.internal(w, what, err)
	}
}

func scopeStrings(ss auth.Scopes) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// Consent (spec S-8).

func (s *server) getOAuthRequest(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	v, err := s.AI.Request(r.Context(), r.PathValue("id"))
	if err != nil {
		s.aiError(w, "authorization request", err)
		return
	}
	a := actor(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "clientId": v.ClientID, "clientName": v.ClientName, "clientUri": v.ClientURI,
		"clientHost": hostOf(v.ClientID), "redirectUri": v.RedirectURI, "redirectHost": hostOf(v.RedirectURI),
		"verified": v.Verified, "scopes": scopeStrings(v.Scopes), "resources": v.Resources, "expiresAt": v.ExpiresAt,
		"grantableScopes": scopeStrings(auth.GrantableScopes(a.Role)),
	})
}

func (s *server) approveOAuthRequest(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	var in struct {
		Scopes []string `json:"scopes"`
	}
	if !decode(w, r, &in) {
		return
	}
	scopes, err := auth.ParseScopes(strings.Join(in.Scopes, " "))
	if err != nil {
		badRequest(w, "scopes: "+err.Error())
		return
	}
	redirect, err := s.AI.Approve(r.Context(), actor(r), r.PathValue("id"), scopes)
	if err != nil {
		s.aiError(w, "authorization request", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": redirect})
}

func (s *server) denyOAuthRequest(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	redirect, err := s.AI.Deny(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		s.aiError(w, "authorization request", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": redirect})
}

// Connected apps (spec S-12).

type grantJSON struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"userId"`
	Username   string     `json:"username"`
	ClientID   string     `json:"clientId"`
	ClientName string     `json:"clientName"`
	ClientHost string     `json:"clientHost"`
	Verified   bool       `json:"verified"`
	Scopes     []string   `json:"scopes"`
	Resources  []string   `json:"resources"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func (s *server) listGrants(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	gs, err := s.AI.Grants(r.Context(), actor(r))
	if err != nil {
		s.aiError(w, "grants", err)
		return
	}
	out := make([]grantJSON, len(gs))
	for i, g := range gs {
		out[i] = grantJSON{
			ID: g.ID, UserID: g.UserID, Username: g.Username, ClientID: g.ClientID, ClientName: g.ClientName,
			ClientHost: hostOf(g.ClientID), Verified: g.ClientKind != oauth.ClientDCR, Scopes: scopeStrings(g.Scopes),
			Resources: g.Resources, CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt,
		}
	}
	writeJSON(w, http.StatusOK, items(out))
}

func (s *server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.AI.RevokeGrant(r.Context(), actor(r), id); err != nil {
		s.aiError(w, "grant", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Service accounts (spec S-11).

type secretJSON struct {
	ID         int64      `json:"id"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type serviceAccountJSON struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Role        auth.Role    `json:"role"`
	Scopes      []string     `json:"scopes"`
	Enabled     bool         `json:"enabled"`
	CreatedAt   time.Time    `json:"createdAt"`
	LastUsedAt  *time.Time   `json:"lastUsedAt"`
	Secrets     []secretJSON `json:"secrets"`
}

func accountJSON(c oauth.Client) serviceAccountJSON {
	out := serviceAccountJSON{
		ID: c.ID, Name: c.Name, Description: c.Description, Role: c.Role, Scopes: scopeStrings(c.Scopes),
		Enabled: c.Enabled, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt, Secrets: []secretJSON{},
	}
	for _, sc := range c.Secrets {
		out.Secrets = append(out.Secrets, secretJSON{ID: sc.ID, CreatedAt: sc.CreatedAt, ExpiresAt: sc.ExpiresAt, LastUsedAt: sc.LastUsedAt})
	}
	return out
}

func (s *server) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	cs, err := s.AI.ServiceAccounts(r.Context())
	if err != nil {
		s.aiError(w, "service accounts", err)
		return
	}
	out := make([]serviceAccountJSON, len(cs))
	for i, c := range cs {
		out[i] = accountJSON(c)
	}
	writeJSON(w, http.StatusOK, items(out))
}

func parseRoleScopes(role *string, scopes []string) (*auth.Role, *auth.Scopes, error) {
	var (
		rp *auth.Role
		sp *auth.Scopes
	)
	if role != nil {
		r, err := auth.ParseRole(*role)
		if err != nil {
			return nil, nil, &oauth.InputError{Msg: "role must be viewer, operator or admin"}
		}
		rp = &r
	}
	if scopes != nil {
		ss, err := auth.ParseScopes(strings.Join(scopes, " "))
		if err != nil {
			return nil, nil, &oauth.InputError{Msg: "scopes: " + err.Error()}
		}
		sp = &ss
	}
	return rp, sp, nil
}

func (s *server) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Role        *string  `json:"role"`
		Scopes      []string `json:"scopes"`
		Enabled     *bool    `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == nil || in.Scopes == nil {
		badRequest(w, "role and scopes are required")
		return
	}
	role, scopes, err := parseRoleScopes(in.Role, in.Scopes)
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	c, err := s.AI.CreateServiceAccount(r.Context(), actor(r), oauth.NewServiceAccount{
		Name: in.Name, Description: in.Description, Role: *role, Scopes: *scopes, Enabled: in.Enabled == nil || *in.Enabled,
	})
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	writeJSON(w, http.StatusCreated, accountJSON(c))
}

func (s *server) getServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	c, err := s.AI.ServiceAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	writeJSON(w, http.StatusOK, accountJSON(c))
}

func (s *server) updateServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	var in struct {
		Description *string  `json:"description"`
		Role        *string  `json:"role"`
		Scopes      []string `json:"scopes"`
		Enabled     *bool    `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	role, scopes, err := parseRoleScopes(in.Role, in.Scopes)
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	c, err := s.AI.UpdateServiceAccount(r.Context(), actor(r), r.PathValue("id"), oauth.ServiceAccountChange{
		Description: in.Description, Role: role, Scopes: scopes, Enabled: in.Enabled,
	})
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	writeJSON(w, http.StatusOK, accountJSON(c))
}

func (s *server) deleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	if err := s.AI.DeleteServiceAccount(r.Context(), actor(r), r.PathValue("id")); err != nil {
		s.aiError(w, "service account", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) createClientSecret(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	var in struct {
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	plain, sc, err := s.AI.AddSecret(r.Context(), actor(r), id, in.ExpiresAt)
	if err != nil {
		s.aiError(w, "service account", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": sc.ID, "clientId": id, "secret": plain, "createdAt": sc.CreatedAt, "expiresAt": sc.ExpiresAt,
	})
}

func (s *server) deleteClientSecret(w http.ResponseWriter, r *http.Request) {
	if !s.aiEnabled(w) {
		return
	}
	sid, err := strconv.ParseInt(r.PathValue("secretId"), 10, 64)
	if err != nil || sid <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err := s.AI.RevokeSecret(r.Context(), actor(r), r.PathValue("id"), sid); err != nil {
		s.aiError(w, "client secret", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
