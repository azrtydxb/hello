package api

// Click-to-call (spec integrations I-37 to I-39): POST /calls/originate
// validates, applies the abuse limits and queues a request for a hello-sip
// node, which rings the user's phone first and dials the destination when it
// answers. GET reads the state, DELETE cancels. The administrator's policy
// (allowed destinations, auto-answer) is /integrations/click-to-call.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/originate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// OriginateStore is the persistence of click-to-call requests and the
// policy; *store.Store implements it.
type OriginateStore interface {
	CreateOriginate(ctx context.Context, o store.OriginateRecord) error
	GetOriginate(ctx context.Context, id string) (store.OriginateRecord, error)
	SetOriginateState(ctx context.Context, id, state, reason, correlationID string) error
	GetOriginateSettings(ctx context.Context) (store.OriginateSettings, error)
	PutOriginateSettings(ctx context.Context, actor string, in store.OriginateSettings) error
}

// OriginateLive is the shared state of click-to-call: the queue to the SIP
// nodes, the live state of requests, idempotency keys and the limit counters;
// *LazyValkey implements it.
type OriginateLive interface {
	originate.Backend
	EnqueueOriginate(ctx context.Context, r livestate.OriginateRequest) error
	PutOriginateState(ctx context.Context, st livestate.OriginateState) error
	GetOriginateState(ctx context.Context, id string) (livestate.OriginateState, bool, error)
	CancelOriginate(ctx context.Context, id string) error
	LookupIdempotent(ctx context.Context, key string) (string, bool, error)
	Idempotent(ctx context.Context, key, id string) (string, bool, error)
}

// OriginateMetrics are hello-control's click-to-call series.
type OriginateMetrics struct {
	RateLimited *prometheus.CounterVec
}

// NewOriginateMetrics registers hello_integration_rate_limited_total on reg.
func NewOriginateMetrics(reg prometheus.Registerer) *OriginateMetrics {
	m := &OriginateMetrics{RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_integration_rate_limited_total",
		Help: "Integration requests refused by a rate limit, by the limit that was exceeded.",
	}, []string{"scope"})}
	reg.MustRegister(m.RateLimited)
	return m
}

var (
	dialableRe = regexp.MustCompile(`^[0-9+*#]{1,64}$`)
	// idemKeyRe is what an idempotency key may be.
	idemKeyRe = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
	// prefixRe is a country prefix of the click-to-call policy.
	prefixRe = regexp.MustCompile(`^\+?[0-9]{1,15}$`)
)

// originateAnomalyThreshold is how many refusals by the limits one credential
// gets in ten minutes before an originate.anomaly audit row is raised.
const originateAnomalyThreshold = 5

func (s *server) originateReady(w http.ResponseWriter) bool {
	if s.Originate == nil || s.OriginateLive == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "click-to-call needs Valkey and a running hello-sip node")
		return false
	}
	return true
}

// originClient names the credential behind a request: the OAuth client, the
// service account, the token, or the console.
func originClient(a auth.Actor) string {
	switch {
	case a.ClientID != "":
		return a.ClientID
	case a.Kind == auth.KindService:
		return "service:" + a.ServiceName
	case a.TokenID != 0:
		return a.String()
	}
	return "console"
}

type originateBody struct {
	Destination    string  `json:"destination"`
	Extension      *string `json:"extension"`
	DeviceID       *int64  `json:"deviceId"`
	CallerID       *string `json:"callerId"`
	AutoAnswer     bool    `json:"autoAnswer"`
	IdempotencyKey string  `json:"idempotencyKey"`
}

type originateStatus struct {
	OriginateID   string    `json:"originateId"`
	State         string    `json:"state"`
	Reason        string    `json:"reason,omitempty"`
	Extension     string    `json:"extension"`
	Destination   string    `json:"destination"`
	CorrelationID string    `json:"correlationId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// originateExtension is the extension a request rings first: the caller's own
// unless the caller may name another. ok is false when it answered.
func (s *server) originateExtension(w http.ResponseWriter, r *http.Request, a auth.Actor, named *string) (store.Extension, bool) {
	ctx := r.Context()
	exts, err := s.Store.ListExtensions(ctx)
	if err != nil {
		s.internal(w, "originate: list extensions", err)
		return store.Extension{}, false
	}
	byNumber := func(n string) (store.Extension, bool) {
		for _, e := range exts {
			if e.Number == n {
				return e, true
			}
		}
		return store.Extension{}, false
	}
	// The extensions the caller may ring: any for an operator or administrator
	// and a service account without an extensionScope; a service account's
	// extensionScope; otherwise the extensions linked to the user.
	anyExt := (a.Kind == auth.KindService && a.ExtensionScope == nil) || (a.Kind != auth.KindService && a.Role.AtLeast(auth.RoleOperator))
	var own []string
	switch {
	case anyExt:
	case a.Kind == auth.KindService:
		own = a.ExtensionScope
	default:
		mine, err := s.Store.UserExtensions(ctx, a.UserID)
		if err != nil {
			s.internal(w, "originate: extensions of the caller", err)
			return store.Extension{}, false
		}
		own = make([]string, 0, len(mine))
		for _, e := range mine {
			own = append(own, e.Number)
		}
	}
	allowed := func(n string) bool { return anyExt || containsString(own, n) }
	if named != nil {
		if !numberRe.MatchString(*named) {
			writeFields(w, fieldErrsOf("extension", "must be an extension number"))
			return store.Extension{}, false
		}
		e, ok := byNumber(*named)
		switch {
		case !allowed(*named):
			writeError(w, http.StatusForbidden, "forbidden_role", "that extension is not one this credential may place calls for")
			return store.Extension{}, false
		case !ok:
			writeError(w, http.StatusNotFound, "not_found", "no such extension")
			return store.Extension{}, false
		}
		return e, true
	}
	// No extension named: the caller's single linked extension.
	var mine []string
	if a.UserID != 0 && a.Kind != auth.KindService {
		linked, err := s.Store.UserExtensions(ctx, a.UserID)
		if err != nil {
			s.internal(w, "originate: extensions of the caller", err)
			return store.Extension{}, false
		}
		for _, e := range linked {
			mine = append(mine, e.Number)
		}
	} else if len(own) == 1 {
		mine = own
	}
	switch len(mine) {
	case 0:
		if anyExt || a.Kind == auth.KindService {
			writeFields(w, fieldErrsOf("extension", "required: this credential is not linked to an extension"))
		} else {
			writeError(w, http.StatusForbidden, "no_extension", "your account is not linked to an extension, so there is no phone to ring; ask an administrator to link it")
		}
		return store.Extension{}, false
	case 1:
		if e, ok := byNumber(mine[0]); ok {
			return e, true
		}
		writeError(w, http.StatusNotFound, "not_found", "no such extension")
		return store.Extension{}, false
	}
	writeFields(w, fieldErrsOf("extension", "required: the account is linked to several extensions"))
	return store.Extension{}, false
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// originateRouting decides the destination for the extension against the
// saved routing: it reports an emergency number, and the caller IDs the
// extension may present.
func (s *server) originateRouting(ctx context.Context, ext store.Extension, dest string) (emergency bool, callerIDs []string, err error) {
	callerIDs = []string{ext.Number}
	if ext.ExternalNumber != "" {
		callerIDs = append(callerIDs, ext.ExternalNumber)
	}
	snap, err := s.Store.RoutingConfig(ctx)
	if err != nil {
		return false, nil, err
	}
	table, errs := s.Router.Compile(snap.Config)
	if len(errs) > 0 || table == nil {
		return false, callerIDs, errors.New("the saved routing configuration does not compile")
	}
	d := table.Decide(routing.Call{FromExtension: ext.Number, Number: dest, CallerID: ext.Number, At: time.Now()}, nil)
	if d.CallerID != "" {
		callerIDs = append(callerIDs, d.CallerID)
	}
	return d.Emergency, callerIDs, nil
}

func newOriginateID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "orig_" + hex.EncodeToString(raw[:]), nil
}

// originateCall is POST /api/v1/calls/originate.
func (s *server) originateCall(w http.ResponseWriter, r *http.Request) {
	if !s.originateReady(w) {
		return
	}
	a := actor(r)
	var b originateBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	if !dialableRe.MatchString(b.Destination) {
		f.add("destination", "must be 1-64 characters of digits, +, * and # (a SIP address is not a destination)")
	}
	key := firstNonEmpty(b.IdempotencyKey, r.Header.Get("Idempotency-Key"))
	if key != "" && !idemKeyRe.MatchString(key) {
		f.add("idempotencyKey", "must be 1-128 printable characters")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	ext, ok := s.originateExtension(w, r, a, b.Extension)
	if !ok {
		return
	}
	var deviceUser string
	if b.DeviceID != nil {
		d, err := s.Store.GetDevice(r.Context(), *b.DeviceID)
		if err != nil || d.ExtensionID != ext.ID {
			writeFields(w, fieldErrsOf("deviceId", "must be a device of the extension"))
			return
		}
		deviceUser = d.SIPUsername
	}
	settings, err := s.Originate.GetOriginateSettings(r.Context())
	if err != nil {
		s.internal(w, "originate: settings", err)
		return
	}
	if b.AutoAnswer && !settings.AutoAnswerAllowed {
		writeFields(w, fieldErrsOf("autoAnswer", "the administrator has not enabled auto-answer for click-to-call"))
		return
	}
	emergency, callerIDs, err := s.originateRouting(r.Context(), ext, b.Destination)
	if err != nil {
		s.internal(w, "originate: routing", err)
		return
	}
	cid := ""
	if b.CallerID != nil && *b.CallerID != "" {
		cid = *b.CallerID
	}
	switch {
	case emergency:
		s.originateRefused(r, a, ext.Number, "emergency")
		writeError(w, http.StatusUnprocessableEntity, "emergency_not_allowed", "click-to-call does not place emergency calls: it carries no location; dial from the phone")
		return
	case cid != "" && !containsString(callerIDs, cid):
		s.originateRefused(r, a, ext.Number, "caller_id")
		writeError(w, http.StatusUnprocessableEntity, "caller_id_not_allowed", "callerId must be a number the routing allows this extension to present")
		return
	}

	live := s.OriginateLive
	credential := credentialKey(a)
	if key != "" {
		// A replay is the same request: no new limits, no new call.
		if id, found, err := live.LookupIdempotent(r.Context(), credential+":"+key); err == nil && found {
			s.originateReplay(w, r, id)
			return
		}
	}
	id, err := newOriginateID()
	if err != nil {
		s.internal(w, "originate: id", err)
		return
	}
	lim := &originate.Limiter{B: live, L: s.originateLimits()}
	denied, err := lim.Check(r.Context(), credential, ext.Number, b.Destination, id)
	if err != nil {
		s.Log.Warn("originate: limits unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	if denied != nil {
		if s.OriginateMetrics != nil {
			s.OriginateMetrics.RateLimited.WithLabelValues(denied.Scope).Inc()
		}
		s.originateAnomaly(r, a, credential, ext.Number)
		w.Header().Set("Retry-After", originate.RetryAfterSeconds(denied.RetryAfter))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many click-to-call requests ("+denied.Scope+"); see Retry-After")
		return
	}
	rec := store.OriginateRecord{ID: id, IdempotencyKey: key, Extension: ext.Number, Destination: b.Destination, CallerID: cid,
		Client: originClient(a), Actor: a.String()}
	if err := s.Originate.CreateOriginate(r.Context(), rec); err != nil {
		_ = lim.Release(r.Context(), ext.Number, id)
		s.internal(w, "originate: record", err)
		return
	}
	if key != "" {
		if stored, created, err := live.Idempotent(r.Context(), credential+":"+key, id); err == nil && !created {
			// Another request with the key won the race: it is the call.
			_ = lim.Release(r.Context(), ext.Number, id)
			_ = s.Originate.SetOriginateState(r.Context(), id, livestate.OriginateFailed, livestate.OriginateCancelled, "")
			s.originateReplay(w, r, stored)
			return
		}
	}
	now := time.Now().UTC()
	rq := livestate.OriginateRequest{
		ID: id, Extension: ext.Number, DeviceID: deviceUser, Destination: b.Destination, CallerID: cid, AutoAnswer: b.AutoAnswer,
		Client: rec.Client, Actor: rec.Actor, Restrict: settings.Destinations == store.OriginateRestricted,
		Prefixes: settings.CountryPrefixes, CreatedAt: now,
	}
	err = live.PutOriginateState(r.Context(), livestate.OriginateState{ID: id, State: livestate.OriginateQueued, UpdatedAt: now})
	if err == nil {
		err = live.EnqueueOriginate(r.Context(), rq)
	}
	if err != nil {
		s.Log.Warn("originate: queue", "error", err)
		_ = lim.Release(r.Context(), ext.Number, id)
		_ = s.Originate.SetOriginateState(r.Context(), id, livestate.OriginateFailed, livestate.OriginateNodeGone, "")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	if err := s.Store.Audit(r.Context(), a.String(), "originate", "call", id+" "+ext.Number+" "+redactNumber(b.Destination)); err != nil {
		s.Log.Warn("audit originate", "error", err)
	}
	writeJSON(w, http.StatusAccepted, originateStatus{
		OriginateID: id, State: livestate.OriginateQueued, Extension: ext.Number, Destination: b.Destination,
		CreatedAt: now, UpdatedAt: now,
	})
}

// redactNumber keeps the first digits of a destination for the audit row:
// enough to tell a country, never the full number.
func redactNumber(n string) string {
	if len(n) <= 4 {
		return n
	}
	return n[:4] + strings.Repeat("*", len(n)-4)
}

func (s *server) originateLimits() originate.Limits {
	if s.OriginateLimits != nil {
		return *s.OriginateLimits
	}
	return originate.DefaultLimits()
}

// originateRefused audits a request that was refused outright.
func (s *server) originateRefused(r *http.Request, a auth.Actor, ext, why string) {
	if err := s.Store.Audit(r.Context(), a.String(), "originate_refused", "call", ext+" "+why); err != nil {
		s.Log.Warn("audit originate refused", "error", err)
	}
}

// originateAnomaly raises one originate.anomaly audit row when a credential is
// refused by the limits again and again: a stolen token being tried, or a
// script gone wrong.
func (s *server) originateAnomaly(r *http.Request, a auth.Actor, credential, ext string) {
	window := time.Now().Truncate(10 * time.Minute)
	n, err := s.OriginateLive.Incr(r.Context(), "hello:originate:refused:"+credential+":"+window.Format("200601021504"), 11*time.Minute)
	if err != nil || n != originateAnomalyThreshold {
		return
	}
	if err := s.Store.Audit(r.Context(), a.String(), "originate.anomaly", "call", ext); err != nil {
		s.Log.Warn("audit originate anomaly", "error", err)
	}
}

// originateStatusOf is a request's status: the live state when there is one,
// else what the row last recorded. A terminal live state is copied into the
// row.
func (s *server) originateStatusOf(ctx context.Context, rec store.OriginateRecord) originateStatus {
	st := originateStatus{OriginateID: rec.ID, State: rec.State, Reason: rec.Reason, Extension: rec.Extension, Destination: rec.Destination,
		CorrelationID: rec.CorrelationID, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt}
	if s.OriginateLive == nil {
		return st
	}
	live, ok, err := s.OriginateLive.GetOriginateState(ctx, rec.ID)
	if err != nil || !ok {
		return st
	}
	st.State, st.Reason, st.UpdatedAt = live.State, live.Reason, live.UpdatedAt
	if live.CorrelationID != "" {
		st.CorrelationID = live.CorrelationID
	}
	if live.State != rec.State || live.Reason != rec.Reason || (live.CorrelationID != "" && live.CorrelationID != rec.CorrelationID) {
		if err := s.Originate.SetOriginateState(ctx, rec.ID, live.State, live.Reason, live.CorrelationID); err != nil {
			s.Log.Warn("originate: copy state", "id", rec.ID, "error", err)
		}
	}
	return st
}

// originateReplay answers a request whose idempotency key was seen.
func (s *server) originateReplay(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := s.Originate.GetOriginate(r.Context(), id)
	if err != nil {
		s.storeError(w, "originate request", err)
		return
	}
	if rec.Actor != actor(r).String() && !actor(r).Role.AtLeast(auth.RoleOperator) {
		writeError(w, http.StatusNotFound, "not_found", "originate request: not found")
		return
	}
	writeJSON(w, http.StatusAccepted, s.originateStatusOf(r.Context(), rec))
}

// ownedOriginate loads the request named in the path when the caller may see
// it: the one who started it, or an operator or administrator. Anyone else
// gets 404, so ids are not probed.
func (s *server) ownedOriginate(w http.ResponseWriter, r *http.Request) (store.OriginateRecord, bool) {
	rec, err := s.Originate.GetOriginate(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, "originate request", err)
		return store.OriginateRecord{}, false
	}
	a := actor(r)
	if rec.Actor != a.String() && (!a.Role.AtLeast(auth.RoleOperator) || a.Kind == auth.KindService) {
		writeError(w, http.StatusNotFound, "not_found", "originate request: not found")
		return store.OriginateRecord{}, false
	}
	return rec, true
}

// getOriginate is GET /api/v1/calls/originate/{id}.
func (s *server) getOriginate(w http.ResponseWriter, r *http.Request) {
	if !s.originateReady(w) {
		return
	}
	rec, ok := s.ownedOriginate(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.originateStatusOf(r.Context(), rec))
}

// cancelOriginate is DELETE /api/v1/calls/originate/{id}: hang up wherever
// the request is, ringing or connected.
func (s *server) cancelOriginate(w http.ResponseWriter, r *http.Request) {
	if !s.originateReady(w) {
		return
	}
	rec, ok := s.ownedOriginate(w, r)
	if !ok {
		return
	}
	st := s.originateStatusOf(r.Context(), rec)
	if livestate.OriginateTerminal(st.State) {
		writeError(w, http.StatusConflict, "conflict", "the request is already over ("+st.State+")")
		return
	}
	if err := s.OriginateLive.CancelOriginate(r.Context(), rec.ID); err != nil {
		s.Log.Warn("originate: cancel", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state is unavailable")
		return
	}
	// A request no node has taken yet is closed here: nobody will see the flag.
	if st.State == livestate.OriginateQueued {
		_ = s.OriginateLive.PutOriginateState(r.Context(), livestate.OriginateState{ID: rec.ID, State: livestate.OriginateFailed, Reason: livestate.OriginateCancelled})
		_ = s.OriginateLive.ReleaseOriginateSetup(r.Context(), rec.Extension, rec.ID)
		st.State, st.Reason = livestate.OriginateFailed, livestate.OriginateCancelled
	}
	writeJSON(w, http.StatusAccepted, st)
}

// clickToCallSettings is GET/PUT /api/v1/integrations/click-to-call.
func (s *server) getClickToCall(w http.ResponseWriter, r *http.Request) {
	if s.Originate == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "click-to-call settings are not available")
		return
	}
	in, err := s.Originate.GetOriginateSettings(r.Context())
	if err != nil {
		s.internal(w, "click-to-call settings", err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *server) putClickToCall(w http.ResponseWriter, r *http.Request) {
	if s.Originate == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "click-to-call settings are not available")
		return
	}
	var in store.OriginateSettings
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if in.Destinations != store.OriginateInherit && in.Destinations != store.OriginateRestricted {
		f.add("destinations", "must be inherit or restricted")
	}
	if len(in.CountryPrefixes) > 100 {
		f.add("countryPrefixes", "at most 100 prefixes")
	}
	for _, p := range in.CountryPrefixes {
		if !prefixRe.MatchString(p) {
			f.add("countryPrefixes", "each prefix must be digits, optionally starting with +")
			break
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	if err := s.Originate.PutOriginateSettings(r.Context(), actor(r).String(), in); err != nil {
		s.internal(w, "click-to-call settings", err)
		return
	}
	s.getClickToCall(w, r)
}
