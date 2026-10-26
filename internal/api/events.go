package api

// The call-event stream and history (spec integrations I-36). hello-sip
// emits the events, hello-control stores them (internal/events) and serves
// them here: a server-sent event stream with Last-Event-ID resume, a
// single-use ticket for clients that cannot send an Authorization header, and
// a paged history.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
)

// EventService is the reading side of the call-event pipeline;
// *events.Service serves it from PostgreSQL and Valkey, *events.Memory from
// memory (tests).
type EventService interface {
	Available() bool
	Acquire(credential string) (release func(), err error)
	Subscribe(f events.Filter) *events.Sub
	Replay(ctx context.Context, after events.Cursor, f events.Filter, fn func(events.Entry) error) error
	History(ctx context.Context, q events.Query) ([]events.Entry, error)
	PutTicket(ctx context.Context, hash string, payload []byte) error
	TakeTicket(ctx context.Context, hash string) ([]byte, bool, error)
}

// eventsReady answers 503 when the event pipeline is not wired or Valkey is
// still connecting.
func (s *server) eventsReady(w http.ResponseWriter) bool {
	if s.Events == nil || !s.Events.Available() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the event stream is not available")
		return false
	}
	return true
}

// eventExtensions is the set of extensions the credential may see events of;
// nil means every extension. A user sees the extensions linked to them, a
// service account the ones in its extensionScope; the viewer, operator and
// admin roles see all.
func (s *server) eventExtensions(r *http.Request, a auth.Actor) ([]string, error) {
	switch {
	case a.ExtensionScope != nil:
		return a.ExtensionScope, nil
	case a.Kind == auth.KindService:
		return nil, nil // a service account without an extensionScope sees all
	case a.Role == auth.RoleUser:
		exts, err := s.Store.UserExtensions(r.Context(), a.UserID)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(exts)) // non-nil: a user without extensions sees nothing
		for _, e := range exts {
			out = append(out, e.Number)
		}
		return out, nil
	}
	return nil, nil
}

// credentialKey names the credential for the per-credential stream limit.
func credentialKey(a auth.Actor) string {
	switch {
	case a.TokenID != 0:
		return "token:" + strconv.FormatInt(a.TokenID, 10)
	case a.Kind == auth.KindService || a.ClientID != "":
		return "client:" + a.ClientID + ":" + strconv.FormatInt(a.UserID, 10)
	}
	return "user:" + strconv.FormatInt(a.UserID, 10)
}

// eventFilter reads types, extension and the allowed set of a request.
func (s *server) eventFilter(w http.ResponseWriter, r *http.Request) (events.Filter, bool) {
	q := r.URL.Query()
	types, err := events.ParseTypes(q.Get("types"))
	if err != nil {
		badRequest(w, err.Error())
		return events.Filter{}, false
	}
	ext := q.Get("extension")
	if ext != "" && !numberRe.MatchString(ext) {
		badRequest(w, "extension must be an extension number")
		return events.Filter{}, false
	}
	allowed, err := s.eventExtensions(r, actor(r))
	if err != nil {
		s.internal(w, "events: extensions of the caller", err)
		return events.Filter{}, false
	}
	return events.Filter{Types: types, Extension: ext, Allowed: allowed}, true
}

// eventFrame is one server-sent event.
func eventFrame(e events.Entry) []byte {
	b, _ := json.Marshal(e.Event)
	return fmt.Appendf(nil, "id: %s\nevent: %s\ndata: %s\n\n", e.Cursor, e.Type, b)
}

// eventsAllowedRefresh is how often a user's stream re-reads the extensions
// linked to them, so a link change takes effect without a reconnect.
var eventsAllowedRefresh = time.Minute

func (s *server) eventStream(w http.ResponseWriter, r *http.Request) {
	if !s.eventsReady(w) {
		return
	}
	a := actor(r)
	f, ok := s.eventFilter(w, r)
	if !ok {
		return
	}
	after, err := events.ParseCursor(firstNonEmpty(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("lastEventId")))
	if err != nil {
		badRequest(w, "Last-Event-ID is not an event cursor")
		return
	}
	release, err := s.Events.Acquire(credentialKey(a))
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many open event streams")
		return
	}
	defer release()
	// Subscribe before replaying, so no event falls between the two.
	sub := s.Events.Subscribe(f)
	defer sub.Close()

	rc := http.NewResponseController(w)
	// A stream outlives any write deadline the server may set.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("retry: 3000\n: stream open\n\n")); err != nil || rc.Flush() != nil {
		return
	}
	last := after
	send := func(e events.Entry) error {
		c, _ := events.ParseCursor(e.Cursor)
		if !last.Zero() && c.Compare(last) <= 0 {
			return nil // already sent: the replay and the live feed overlap
		}
		last = c
		if _, err := w.Write(eventFrame(e)); err != nil {
			return err
		}
		return rc.Flush()
	}
	if !after.Zero() {
		if err := s.Events.Replay(r.Context(), after, f, send); err != nil {
			return // the client reconnects with the last id it saw
		}
	}
	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	var refresh <-chan time.Time
	if a.Role == auth.RoleUser && a.Kind != auth.KindService {
		t := time.NewTicker(eventsAllowedRefresh)
		defer t.Stop()
		refresh = t.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case <-refresh:
			if allowed, err := s.eventExtensions(r, a); err == nil {
				sub.SetAllowed(allowed)
			}
		case e, ok := <-sub.C:
			if !ok {
				return // dropped for lagging or shutting down: resume with Last-Event-ID
			}
			if err := send(e); err != nil {
				return
			}
		}
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// listEvents is the history, paged by cursor.
func (s *server) listEvents(w http.ResponseWriter, r *http.Request) {
	f, ok := s.eventFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	qq := events.Query{Filter: f, Desc: q.Get("order") == "desc", Limit: 100}
	switch q.Get("order") {
	case "", "asc", "desc":
	default:
		badRequest(w, "order must be asc or desc")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > events.MaxLimit {
			badRequest(w, "limit must be between 1 and "+strconv.Itoa(events.MaxLimit))
			return
		}
		qq.Limit = n
	}
	var err error
	if qq.After, err = events.ParseCursor(q.Get("after")); err != nil {
		badRequest(w, "after is not an event cursor")
		return
	}
	if qq.Before, err = events.ParseCursor(q.Get("before")); err != nil {
		badRequest(w, "before is not an event cursor")
		return
	}
	if s.Events == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the event stream is not available")
		return
	}
	page, err := s.Events.History(r.Context(), qq)
	if err != nil {
		s.internal(w, "events: history", err)
		return
	}
	next := ""
	if len(page) == qq.Limit {
		next = page[len(page)-1].Cursor
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []events.Entry `json:"items"`
		NextCursor string         `json:"nextCursor"`
	}{items(page).Items, next})
}

// ticketActor is the part of an actor a stream ticket carries.
type ticketActor struct {
	UserID         int64
	Username       string
	TokenID        int64
	Role           auth.Role
	Kind           auth.Kind
	Scopes         auth.Scopes
	ClientID       string
	ServiceName    string
	ExtensionScope []string
	Via            string
}

func hashTicket(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// createEventTicket mints a single-use ticket for GET /events/stream, for
// clients that cannot send an Authorization header (a browser EventSource of
// another origin). It is valid for 30 seconds and acts as the caller.
func (s *server) createEventTicket(w http.ResponseWriter, r *http.Request) {
	if !s.eventsReady(w) {
		return
	}
	a := actor(r)
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		s.internal(w, "events: ticket", err)
		return
	}
	ticket := "hello_tkt_" + hex.EncodeToString(raw)
	// Only the events scope travels: the ticket opens one stream and nothing
	// else.
	payload, _ := json.Marshal(ticketActor{UserID: a.UserID, Username: a.Username, TokenID: a.TokenID, Role: a.Role,
		Kind: a.Kind, Scopes: auth.Scopes{auth.ScopeEvents}, ClientID: a.ClientID, ServiceName: a.ServiceName,
		ExtensionScope: a.ExtensionScope, Via: a.Via})
	if err := s.Events.PutTicket(r.Context(), hashTicket(ticket), payload); err != nil {
		s.internal(w, "events: store ticket", err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{ticket, time.Now().Add(events.TicketTTL).UTC()})
}

// withEventTicket lets GET /events/stream authenticate with ?ticket= when the
// request carries no Authorization header; any other request takes the usual
// authentication (inner).
func (s *server) withEventTicket(inner, guarded http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" || r.Header.Get("Authorization") != "" || s.Events == nil {
			inner.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(ticket, "hello_tkt_") {
			auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired ticket")
			return
		}
		if !s.eventsReady(w) {
			return
		}
		raw, ok, err := s.Events.TakeTicket(r.Context(), hashTicket(ticket))
		var t ticketActor
		switch {
		case err != nil:
			s.internal(w, "events: redeem ticket", err)
			return
		case !ok || json.Unmarshal(raw, &t) != nil:
			auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired ticket")
			return
		}
		a := auth.Actor{UserID: t.UserID, Username: t.Username, TokenID: t.TokenID, Role: t.Role, Kind: t.Kind,
			Scopes: t.Scopes, ClientID: t.ClientID, ServiceName: t.ServiceName, ExtensionScope: t.ExtensionScope, Via: t.Via}
		guarded.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), a)))
	})
}
