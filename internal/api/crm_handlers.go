package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/routing"
)

// CRM connectors (spec integrations I-47, I-48, I-50). The connector
// settings, the OAuth flow and the logging jobs are administered here; the
// secrets (client secret, tokens) are write-only and never returned.

// CRMService is the CRM administration; *crm.Service satisfies it. Nil
// answers 503 on the CRM routes.
type CRMService interface {
	List(ctx context.Context) ([]crm.ConnectorView, error)
	Get(ctx context.Context, id int64) (crm.ConnectorView, error)
	Create(ctx context.Context, actor string, in crm.Settings) (crm.ConnectorView, error)
	Update(ctx context.Context, actor string, id int64, in crm.Settings) (crm.ConnectorView, error)
	Delete(ctx context.Context, actor string, id int64) error
	Connect(ctx context.Context, actor string, id int64) (string, error)
	Callback(ctx context.Context, state, code string) (int64, error)
	Disconnect(ctx context.Context, actor string, id int64) error
	Test(ctx context.Context, actor string, id int64) (crm.TestResult, error)
	Logs(ctx context.Context, id int64, status string, limit int, before int64) ([]crm.LogRow, error)
	RetryLog(ctx context.Context, actor string, id int64) (crm.LogRow, error)
	Mappings(ctx context.Context, id int64) ([]crm.UserMapping, error)
	SetMapping(ctx context.Context, actor string, id, userID int64, externalID string) error
	Engagement(ctx context.Context, actor, kind, callID, ref string, exts []string, restricted bool) (bool, error)
	Setup(ctx context.Context, id int64) ([]crm.SetupItem, error)
	SidebarApp(ctx context.Context, actor string, id int64) (name string, data []byte, err error)
}

func (s *server) crmReady(w http.ResponseWriter) bool {
	if s.CRM == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "CRM connectors are not configured")
		return false
	}
	return true
}

// crmError answers a failed CRM operation.
func (s *server) crmError(w http.ResponseWriter, what string, err error) {
	var ve *crm.ValidationError
	switch {
	case errors.As(err, &ve):
		keys := make([]string, 0, len(ve.Fields))
		for k := range ve.Fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var f fieldErrs
		for _, k := range keys {
			f = append(f, routing.FieldError{Path: k, Message: ve.Fields[k]})
		}
		writeFields(w, f)
	case errors.Is(err, crm.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, crm.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+": a connector with that name exists, or the job is not dead or skipped")
	case errors.Is(err, crm.ErrNoPublicURL):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "HELLO_PUBLIC_URL is not set, so the CRM sign-in cannot return to Hello")
	case errors.Is(err, crm.ErrNotOAuth):
		writeError(w, http.StatusConflict, "conflict", "this connector does not sign in with OAuth")
	case errors.Is(err, crm.ErrNoCredentials):
		writeError(w, http.StatusConflict, "conflict", "set the client id and client secret first")
	case errors.Is(err, crm.ErrNoApp):
		writeError(w, http.StatusConflict, "conflict", "this connector has no sidebar app")
	case errors.Is(err, crm.ErrNotConnected):
		writeError(w, http.StatusConflict, "conflict", "the connector is not connected")
	case errors.Is(err, crm.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "at most 6 tests per minute")
	case errors.Is(err, crm.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden_role", "the call is not one of yours")
	default:
		s.internal(w, what, err)
	}
}

// crmBody is the create and update body.
type crmBody struct {
	Kind          *string           `json:"kind"`
	Name          *string           `json:"name"`
	Enabled       *bool             `json:"enabled"`
	Config        map[string]string `json:"config"`
	ClientSecret  *string           `json:"clientSecret"`
	Recordings    *bool             `json:"recordings"`
	LinkTTLHours  *int              `json:"linkTtlHours"`
	LogMissed     *bool             `json:"logMissed"`
	FallbackOwner *string           `json:"fallbackOwner"`
}

func (b crmBody) settings() crm.Settings {
	return crm.Settings{Kind: b.Kind, Name: b.Name, Enabled: b.Enabled, Config: b.Config, ClientSecret: b.ClientSecret,
		Recordings: b.Recordings, LinkTTLHours: b.LinkTTLHours, LogMissed: b.LogMissed, FallbackOwner: b.FallbackOwner}
}

func (s *server) listCRMConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	vs, err := s.CRM.List(r.Context())
	if err != nil {
		s.crmError(w, "list CRM connectors", err)
		return
	}
	writeJSON(w, http.StatusOK, items(vs))
}

func (s *server) createCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	var b crmBody
	if !decode(w, r, &b) {
		return
	}
	if b.Kind == nil || b.Name == nil {
		badRequest(w, "kind and name are required")
		return
	}
	v, err := s.CRM.Create(r.Context(), actor(r).String(), b.settings())
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.CRM.Get(r.Context(), id)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) updateCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b crmBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.CRM.Update(r.Context(), actor(r).String(), id, b.settings())
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.CRM.Delete(r.Context(), actor(r).String(), id); err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// downloadCRMApp is GET /integrations/crm/{id}/app: the ticket-sidebar app of
// a helpdesk connector, as a zip the customer uploads to their own account.
func (s *server) downloadCRMApp(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	name, data, err := s.CRM.SidebarApp(r.Context(), actor(r).String(), id)
	if err != nil {
		s.crmError(w, "CRM sidebar app", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) connectCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	u, err := s.CRM.Connect(r.Context(), actor(r).String(), id)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	// A connector that signs in with client credentials is connected by now:
	// there is no browser step and the address is empty.
	writeJSON(w, http.StatusOK, map[string]any{"authorizeUrl": u, "connected": u == ""})
}

// getCRMSetup answers what the customer configures in the CRM for a
// connector: the Salesforce Call Center definition, the Dynamics channel
// provider values. None of it is secret.
func (s *server) getCRMSetup(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := s.CRM.Setup(r.Context(), id)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusOK, items(it))
}

func (s *server) disconnectCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.CRM.Disconnect(r.Context(), actor(r).String(), id); err != nil {
		s.crmError(w, "CRM connection", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) testCRMConnector(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.CRM.Test(r.Context(), actor(r).String(), id)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// crmCallback is where the CRM sends the browser back after the sign-in. It
// is public (a cross-site redirect carries no Strict session cookie); the
// single-use state it names was minted for an administrator and is the only
// credential. It always redirects to the console.
func (s *server) crmCallback(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	q := r.URL.Query()
	target := "/integrations/crm?"
	if q.Get("error") != "" {
		target += "error=denied"
	} else {
		id, err := s.CRM.Callback(r.Context(), q.Get("state"), q.Get("code"))
		switch {
		case err == nil:
			target += "connected=" + strconv.FormatInt(id, 10)
		case errors.Is(err, crm.ErrBadState):
			target += "error=state"
		default:
			s.Log.Warn("crm: oauth callback failed", "connector", id, "error", err)
			target += "error=failed&connector=" + strconv.FormatInt(id, 10)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

// crmLogPage is a page of logging jobs.
type crmLogPage struct {
	Items      []crm.LogRow `json:"items"`
	NextBefore int64        `json:"nextBefore"`
}

var crmLogStatuses = []string{crm.LogWaiting, crm.LogPending, crm.LogLogged, crm.LogUpdate, crm.LogDead, crm.LogSkipped}

func (s *server) listCRMLogs(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !slices.Contains(crmLogStatuses, status) {
		badRequest(w, "status must be one of "+strings.Join(crmLogStatuses, ", "))
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			badRequest(w, "before must be a log id")
			return
		}
		before = n
	}
	rows, err := s.CRM.Logs(r.Context(), id, status, limit, before)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	page := crmLogPage{Items: rows}
	if len(rows) == limit {
		page.NextBefore = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) retryCRMLog(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	row, err := s.CRM.RetryLog(r.Context(), actor(r).String(), id)
	if err != nil {
		s.crmError(w, "CRM log", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *server) listCRMUsers(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ms, err := s.CRM.Mappings(r.Context(), id)
	if err != nil {
		s.crmError(w, "CRM connector", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ms))
}

func (s *server) putCRMUser(w http.ResponseWriter, r *http.Request) {
	if !s.crmReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || uid <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var b struct {
		ExternalUserID *string `json:"externalUserId"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ExternalUserID == nil {
		badRequest(w, "externalUserId is required (empty clears the mapping)")
		return
	}
	if err := s.CRM.SetMapping(r.Context(), actor(r).String(), id, uid, *b.ExternalUserID); err != nil {
		s.crmError(w, "CRM user mapping", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hubspotEngagement is the iframe's handshake: it tells the server which
// HubSpot engagement it created for a call, so the server patches that
// engagement instead of creating a second one.
func (s *server) hubspotEngagement(w http.ResponseWriter, r *http.Request) {
	s.crmEngagement(w, r, "hubspot", "engagementId", func(v string) bool { return digitsOnly(v) && len(v) <= 32 })
}

// salesforceEngagement is the Open CTI softphone's handshake: the Task its
// saveLog created for a call, which the server then patches (duration,
// recording link) instead of creating a second one.
func (s *server) salesforceEngagement(w http.ResponseWriter, r *http.Request) {
	s.crmEngagement(w, r, "salesforce", "recordId", func(v string) bool { return v != "" && len(v) <= 18 })
}

// crmEngagement records the CRM reference an embedded app created for a
// call. field names the reference in the body; the connector checks its
// shape.
func (s *server) crmEngagement(w http.ResponseWriter, r *http.Request, kind, field string, valid func(string) bool) {
	if !s.crmReady(w) {
		return
	}
	var b map[string]string
	if !decode(w, r, &b) {
		return
	}
	callID, ref := b["callId"], b[field]
	if callID == "" || len(callID) > 128 || !valid(ref) {
		badRequest(w, "callId and a valid "+field+" are required")
		return
	}
	a := actor(r)
	exts, err := s.eventExtensions(r, a)
	if err != nil {
		s.internal(w, "crm: extensions of the caller", err)
		return
	}
	restricted := exts != nil || a.Role == auth.RoleUser
	adopted, err := s.CRM.Engagement(r.Context(), a.String(), kind, callID, ref, exts, restricted)
	if err != nil {
		s.crmError(w, kind+" connector", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"adopted": adopted})
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
