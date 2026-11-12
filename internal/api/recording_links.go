package api

// Recording links and the lookup API (spec integrations I-43 to I-45). A
// link is a Hello-signed proxy URL, https://<host>/r/<token>.wav: the object
// store's endpoint is cluster-internal, so a presigned URL could not be
// followed by a CRM user. The token is 256 random bits and only its SHA-256
// is stored. /r/ is a public path outside /api/v1: it checks the hash,
// expiry, revocation and use limit, streams the audio with Range support,
// and is rate limited per token and client address.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// Link lifetimes: a link lasts 30 days unless the caller asks otherwise, from
// one hour to a year.
const (
	defaultLinkTTL = 30 * 24 * time.Hour
	minLinkTTL     = time.Hour
	maxLinkTTL     = 365 * 24 * time.Hour
	// linkRatePerMinute is how many requests one token may make per client
	// address per minute.
	linkRatePerMinute = 30
)

// LinkMetrics counts recording link use (hello_recording_link_hits_total) and
// reports the live links (hello_recording_links_active).
type LinkMetrics struct {
	Hits *prometheus.CounterVec
}

// NewLinkMetrics registers the series on reg. active returns the number of
// links that can still be used and is called on each scrape.
func NewLinkMetrics(reg prometheus.Registerer, active func() float64) *LinkMetrics {
	m := &LinkMetrics{Hits: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_recording_link_hits_total",
		Help: "Recording link requests by result: ok, unknown, revoked, expired, used_up, rate_limited or storage_error.",
	}, []string{"result"})}
	reg.MustRegister(m.Hits, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "hello_recording_links_active", Help: "Recording links that can still be used.",
	}, active))
	return m
}

func (m *LinkMetrics) hit(result string) {
	if m != nil {
		m.Hits.WithLabelValues(result).Inc()
	}
}

// linkLimiter is a fixed-window counter per key, kept in memory (so the limit
// holds per replica).
type linkLimiter struct {
	mu      sync.Mutex
	windows map[string]*linkWindow
	now     func() time.Time
}

type linkWindow struct {
	start time.Time
	n     int
}

func newLinkLimiter() *linkLimiter {
	return &linkLimiter{windows: map[string]*linkWindow{}, now: time.Now}
}

// allow counts one request on key and reports whether it is within limit.
func (l *linkLimiter) allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.windows) > 10000 {
		for k, w := range l.windows {
			if now.Sub(w.start) >= time.Minute {
				delete(l.windows, k)
			}
		}
	}
	w := l.windows[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &linkWindow{start: now}
		l.windows[key] = w
	}
	w.n++
	return w.n <= limit
}

// linkTokenRe is what a token looks like: 32 bytes, unpadded base64url.
var linkTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// lookupNumberRe is what the number filter accepts: digits with the usual
// separators and an optional leading plus.
var lookupNumberRe = regexp.MustCompile(`^\+?[0-9 ()./-]{1,31}$`)

func hashLinkToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// callerAddr is the client address nginx saw.
func callerAddr(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// publicRecording is GET /r/{token}.wav. Every refusal for a token that does
// not work is the same 404, so the response does not say whether a token ever
// existed; the reason is only counted.
func (s *server) publicRecording(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "private, no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Content-Type-Options", "nosniff")
	token, ok := strings.CutSuffix(r.PathValue("token"), ".wav")
	if !ok || !linkTokenRe.MatchString(token) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if !s.links.allow(callerAddr(r)+"|"+token, linkRatePerMinute) || !s.links.allow(callerAddr(r), 10*linkRatePerMinute) {
		s.LinkMetrics.hit("rate_limited")
		h.Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	object, _, res, err := s.Store.UseRecordingLink(r.Context(), hashLinkToken(token))
	if err != nil {
		s.internal(w, "recording link: redeem", err)
		return
	}
	if res != store.LinkOK {
		s.LinkMetrics.hit(string(res))
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if s.Objects == nil {
		s.LinkMetrics.hit("storage_error")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	obj, err := s.Objects.OpenAudio(r.Context(), RecordingsBucket, object)
	if err != nil {
		s.Log.Error("recording link: open audio", "error", err)
		s.LinkMetrics.hit("storage_error")
		writeError(w, http.StatusBadGateway, "upstream", "object storage is unavailable; try again shortly")
		return
	}
	defer func() { _ = obj.Body.Close() }()
	s.LinkMetrics.hit("ok")
	h.Set("Content-Type", "audio/wav")
	http.ServeContent(w, r, "", obj.ModTime, obj.Body)
}

// linkBody is the optional body of POST /recordings/{id}/links.
type linkBody struct {
	TTLHours *int `json:"ttlHours"`
	MaxUses  *int `json:"maxUses"`
}

// callerExtensions is the set of extensions a credential may act on; ok is
// false when it is unrestricted. A user and a token with an extensionScope
// are restricted as for events.
func (s *server) callerExtensions(r *http.Request) (exts []string, restricted bool, err error) {
	exts, err = s.eventExtensions(r, actor(r))
	return exts, exts != nil, err
}

// mintExtensions is callerExtensions for minting and revoking links, where a
// viewer is limited to the extensions linked to them as a user is: only
// operators, administrators and unrestricted service accounts reach any call.
func (s *server) mintExtensions(r *http.Request) (exts []string, restricted bool, err error) {
	a := actor(r)
	exts, restricted, err = s.callerExtensions(r)
	if err != nil || restricted || a.Kind == auth.KindService || a.Role.AtLeast(auth.RoleOperator) {
		return exts, restricted, err
	}
	linked, err := s.Store.UserExtensions(r.Context(), a.UserID)
	if err != nil {
		return nil, false, err
	}
	exts = make([]string, 0, len(linked))
	for _, e := range linked {
		exts = append(exts, e.Number)
	}
	return exts, true, nil
}

// involves reports whether any of the numbers is in allowed.
func involves(allowed, parties []string) bool {
	for _, p := range parties {
		if slices.Contains(allowed, p) {
			return true
		}
	}
	return false
}

// publicBase is scheme://host of the request as the client addressed it.
func publicBase(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *server) createRecordingLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ttl, maxUses := defaultLinkTTL, 0
	if r.ContentLength != 0 {
		var b linkBody
		if !decode(w, r, &b) {
			return
		}
		if b.TTLHours != nil {
			d := time.Duration(*b.TTLHours) * time.Hour
			if *b.TTLHours < 1 || d < minLinkTTL || d > maxLinkTTL {
				badRequest(w, "ttlHours must be 1 to 8760")
				return
			}
			ttl = d
		}
		if b.MaxUses != nil {
			if *b.MaxUses < 0 || *b.MaxUses > 1000000 {
				badRequest(w, "maxUses must be 0 (unlimited) to 1000000")
				return
			}
			maxUses = *b.MaxUses
		}
	}
	if _, err := s.Store.GetRecording(r.Context(), id); err != nil {
		s.storeError(w, "recording", err)
		return
	}
	allowed, restricted, err := s.mintExtensions(r)
	if err != nil {
		s.internal(w, "recording link: caller extensions", err)
		return
	}
	if restricted {
		parties, err := s.Store.RecordingParties(r.Context(), id)
		if err != nil {
			s.storeError(w, "recording", err)
			return
		}
		if !involves(allowed, parties) {
			writeError(w, http.StatusForbidden, "forbidden_role", "you were not part of that call")
			return
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.internal(w, "recording link: token", err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(ttl)
	l, err := s.Store.CreateRecordingLink(r.Context(), actor(r).String(), id, hashLinkToken(token), expires, maxUses)
	if err != nil {
		s.storeError(w, "recording", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": l.ID, "recordingId": l.RecordingID, "url": publicBase(r) + "/r/" + token + ".wav",
		"expiresAt": l.ExpiresAt, "maxUses": l.MaxUses,
	})
}

func (s *server) revokeRecordingLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := s.Store.GetRecordingLink(r.Context(), id)
	if err != nil {
		s.storeError(w, "recording link", err)
		return
	}
	allowed, restricted, err := s.mintExtensions(r)
	if err != nil {
		s.internal(w, "recording link: caller extensions", err)
		return
	}
	if restricted && l.CreatedBy != actor(r).String() {
		parties, err := s.Store.RecordingParties(r.Context(), l.RecordingID)
		if err != nil {
			s.storeError(w, "recording link", err)
			return
		}
		if !involves(allowed, parties) {
			writeError(w, http.StatusForbidden, "forbidden_role", "you were not part of that call")
			return
		}
	}
	if err := s.Store.RevokeRecordingLink(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "recording link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lookupFilter reads the query shared by both lookups and applies the
// caller's extension restriction. It answers 400 itself.
func (s *server) lookupFilter(w http.ResponseWriter, r *http.Request) (f store.LookupFilter, before int64, limit int, ok bool) {
	q := r.URL.Query()
	f.Number = strings.TrimSpace(q.Get("number"))
	if f.Number != "" && !lookupNumberRe.MatchString(f.Number) {
		badRequest(w, "number must be a phone number")
		return f, 0, 0, false
	}
	if f.Extension = q.Get("extension"); f.Extension != "" && !numberRe.MatchString(f.Extension) {
		badRequest(w, "extension must be an extension number")
		return f, 0, 0, false
	}
	f.CallID = q.Get("callId")
	if len(f.CallID) > 256 {
		badRequest(w, "callId is too long")
		return f, 0, 0, false
	}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				badRequest(w, name+" must be an RFC 3339 time")
				return f, 0, 0, false
			}
			*dst = t
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.To.After(f.From) {
		badRequest(w, "to must be after from")
		return f, 0, 0, false
	}
	switch q.Get("recorded") {
	case "", "false":
	case "true":
		f.Recorded = true
	default:
		badRequest(w, `recorded must be "true" or "false"`)
		return f, 0, 0, false
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "before must be a positive id")
			return f, 0, 0, false
		}
		before = n
	}
	limit = defaultCDRLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxCDRLimit {
			badRequest(w, "limit must be 1-200")
			return f, 0, 0, false
		}
		limit = n
	}
	allowed, restricted, err := s.callerExtensions(r)
	if err != nil {
		s.internal(w, "lookup: caller extensions", err)
		return f, 0, 0, false
	}
	f.Restrict, f.Allowed = restricted, allowed
	if restricted && f.Extension != "" && !slices.Contains(allowed, f.Extension) {
		writeError(w, http.StatusForbidden, "forbidden_role", fmt.Sprintf("extension %s is not one of yours", f.Extension))
		return f, 0, 0, false
	}
	return f, before, limit, true
}

func (s *server) lookupCalls(w http.ResponseWriter, r *http.Request) {
	f, before, limit, ok := s.lookupFilter(w, r)
	if !ok {
		return
	}
	cs, next, err := s.Store.LookupCalls(r.Context(), f, before, limit)
	if err != nil {
		s.internal(w, "lookup calls", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cs, "next": next})
}

func (s *server) lookupRecordings(w http.ResponseWriter, r *http.Request) {
	f, before, limit, ok := s.lookupFilter(w, r)
	if !ok {
		return
	}
	f.Recorded = false // every row is a recording
	rs, next, err := s.Store.LookupRecordings(r.Context(), f, before, limit)
	if err != nil {
		s.internal(w, "lookup recordings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rs, "next": next})
}
