package api

// The SCIM token and status routes (spec integrations P10, I-23 to I-25). The
// SCIM protocol itself is served at /scim/v2 (outside /api/v1, so not in the
// route table or the OpenAPI document) by the SCIM service, authenticated by
// the hello_scim_ tokens issued here.

import (
	"context"
	"errors"
	"net/http"

	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/scim"
)

// SCIMService is the SCIM service provider; *scim.Service satisfies it. Nil
// answers 404 on /scim/v2 and 503 on the token and status routes.
type SCIMService interface {
	http.Handler
	IssueToken(ctx context.Context, actor string, connectorID int64) (scim.Token, string, error)
	ListTokens(ctx context.Context, connectorID int64) ([]scim.Token, error)
	RevokeToken(ctx context.Context, actor string, id int64) (scim.Token, error)
	Status(ctx context.Context, connectorID int64) (scim.Status, error)
	BaseURL() string
}

func (s *server) scimDisabled(w http.ResponseWriter) bool {
	if s.SCIM == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "SCIM is not configured (it needs HELLO_PUBLIC_URL)")
		return true
	}
	return false
}

func (s *server) scimError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, directory.ErrNotFound), errors.Is(err, scim.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, scim.ErrWrongKind):
		writeError(w, http.StatusConflict, "not_scim", "the connector is not a SCIM connector")
	case errors.Is(err, scim.ErrTooMany):
		writeError(w, http.StatusConflict, "too_many_tokens", "the connector has two active SCIM tokens; revoke one first")
	default:
		s.internal(w, what, err)
	}
}

// scimTokenCreated is the answer to an issue: the token, shown this once.
type scimTokenCreated struct {
	scim.Token
	Secret  string `json:"token"`
	BaseURL string `json:"baseUrl"`
}

func (s *server) issueSCIMToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.scimDisabled(w) {
		return
	}
	t, secret, err := s.SCIM.IssueToken(r.Context(), actor(r).String(), id)
	if err != nil {
		s.scimError(w, "issue SCIM token", err)
		return
	}
	writeJSON(w, http.StatusCreated, scimTokenCreated{Token: t, Secret: secret, BaseURL: s.SCIM.BaseURL()})
}

func (s *server) listSCIMTokens(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.scimDisabled(w) {
		return
	}
	ts, err := s.SCIM.ListTokens(r.Context(), id)
	if err != nil {
		s.scimError(w, "SCIM tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ts))
}

func (s *server) revokeSCIMToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.scimDisabled(w) {
		return
	}
	if _, err := s.SCIM.RevokeToken(r.Context(), actor(r).String(), id); err != nil {
		s.scimError(w, "SCIM token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getSCIMStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.scimDisabled(w) {
		return
	}
	st, err := s.SCIM.Status(r.Context(), id)
	if err != nil {
		s.scimError(w, "SCIM status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// scimNotConfigured answers /scim/v2 without the SCIM service, in the SCIM
// error form identity providers read.
func scimNotConfigured(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"SCIM is not configured on this Hello (HELLO_PUBLIC_URL)"}` + "\n"))
}
