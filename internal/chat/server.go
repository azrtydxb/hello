// Package chat is the LAN chat widget's listener (spec messaging-channels
// M-28, M-29, M-29a to M-29c): the widget script, its sandboxed frame and the
// JSON and event-stream endpoints a visitor's browser calls. It is its own
// http.Server with its own middleware, limiter keys and metrics, and serves
// nothing but /chat/* and /healthz: the admin API, the UI and everything else
// of hello-control are unreachable on it.
//
// A visitor is anonymous. A random token, kept in the frame's localStorage and
// sent in the Authorization header (never in a URL), authenticates each call.
// Nothing on the listener trusts an Origin header: anyone can run curl, so the
// abuse controls (a challenge, layered limits, caps, a blocklist) are
// server-side and hold whatever the caller claims to be.
//
// Message text, names, email addresses, tokens and addresses are never logged
// and never in a metric label.
package chat

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/store"
)

//go:embed assets/*
var assets embed.FS

// Backend is the persistence the listener needs; *store.Store implements it.
type Backend interface {
	ChatSiteByKey(ctx context.Context, key string) (store.ChatSite, error)
	ChatTurnstileSecret(ctx context.Context, siteID int64) (string, error)
	ChatRetentionDays(ctx context.Context, site store.ChatSite) (int, error)
	ChatIPHash(ip string, now time.Time) ([]byte, error)
	ChatAvailability(ctx context.Context, site store.ChatSite, now time.Time) (bool, string, error)
	ChatSession(ctx context.Context, in store.ChatSessionInput) (store.ChatSessionResult, error)
	ChatAuth(ctx context.Context, siteID int64, token string, ipHash []byte) (store.ChatVisitor, error)
	ChatSend(ctx context.Context, in store.ChatSendInput) (store.ChatSendResult, error)
	ChatHistory(ctx context.Context, visitorID, after int64, limit int) ([]store.ChatLine, error)
	ChatEvents(ctx context.Context, visitorID, after int64, limit int) ([]store.ChatEvent, error)
	ChatLatestEventID(ctx context.Context, visitorID int64) (int64, error)
	ChatTyping(ctx context.Context, site store.ChatSite, visitorID int64) error
	ChatEnd(ctx context.Context, site store.ChatSite, visitorID int64) error
	ChatTranscript(ctx context.Context, visitorID int64) (string, []store.ChatLine, error)
}

var _ Backend = (*store.Store)(nil)

// Config is what the listener is built from.
type Config struct {
	Backend Backend
	Limiter Limiter
	// Key signs proof-of-work nonces; every replica must share it.
	Key []byte
	// Enabled is the global kill switch (HELLO_MSG_CHAT_ENABLED).
	Enabled bool
	// TrustedProxies may set X-Forwarded-For; the client is the last hop that
	// is not one of them.
	TrustedProxies []netip.Prefix
	// MaxInFlight is the per-pod concurrency limit (default 500).
	MaxInFlight int
	// MaxStreams is the per-pod limit of open event streams (default 2000).
	MaxStreams int
	// Mail sends a transcript; nil turns transcripts off.
	Mail      func(ctx context.Context, to, subject, body string) error
	Turnstile *Turnstile
	Metrics   *Metrics
	Log       *slog.Logger
	Now       func() time.Time
	// Poll, Keepalive and StreamIdle are the event stream's timings (defaults
	// 1s, 15s, 30m); variables for tests.
	Poll       time.Duration
	Keepalive  time.Duration
	StreamIdle time.Duration
}

// Server is the chat listener's handler.
type Server struct {
	cfg      Config
	pow      *Pow
	adaptive *Adaptive
	mux      *http.ServeMux

	inflight atomic.Int64
	streams  atomic.Int64

	cacheMu sync.Mutex
	cache   map[string]cachedSite

	streamMu sync.Mutex
	live     map[int64]struct{}
}

type cachedSite struct {
	site store.ChatSite
	err  error
	at   time.Time
}

const (
	siteCacheTTL = 5 * time.Second
	maxBody      = 16 << 10
	reqDeadline  = 10 * time.Second
)

// New returns the handler.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 500
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 2000
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Second
	}
	if cfg.Keepalive <= 0 {
		cfg.Keepalive = 15 * time.Second
	}
	if cfg.StreamIdle <= 0 {
		cfg.StreamIdle = 30 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewLimiter(nil, cfg.Log)
	}
	if cfg.Metrics == nil {
		cfg.Metrics = &Metrics{}
	}
	s := &Server{cfg: cfg, pow: NewPow(cfg.Key, cfg.Now), cache: map[string]cachedSite{}, live: map[int64]struct{}{}, mux: http.NewServeMux()}
	s.adaptive = NewAdaptive(func() {
		if s.cfg.Metrics.ChallengeRaised != nil {
			s.cfg.Metrics.ChallengeRaised.Inc()
		}
	})
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /chat/v1/widget.js", s.asset("widget.js", "application/javascript; charset=utf-8", true))
	s.mux.HandleFunc("GET /chat/v1/frame.js", s.asset("frame.js", "application/javascript; charset=utf-8", false))
	s.mux.HandleFunc("GET /chat/v1/pow.js", s.asset("pow.js", "application/javascript; charset=utf-8", false))
	s.mux.HandleFunc("GET /chat/v1/frame.css", s.asset("frame.css", "text/css; charset=utf-8", false))
	s.mux.HandleFunc("GET /chat/v1/frame", s.frame)
	s.mux.HandleFunc("GET /chat/v1/challenge", s.api(s.challenge))
	s.mux.HandleFunc("POST /chat/v1/session", s.api(s.session))
	s.mux.HandleFunc("POST /chat/v1/messages", s.api(s.messages))
	s.mux.HandleFunc("POST /chat/v1/typing", s.api(s.typing))
	s.mux.HandleFunc("POST /chat/v1/end", s.api(s.end))
	s.mux.HandleFunc("POST /chat/v1/transcript", s.api(s.transcript))
	s.mux.HandleFunc("GET /chat/v1/history", s.api(s.history))
	s.mux.HandleFunc("GET /chat/v1/events", s.events)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path != "/healthz" && !s.cfg.Enabled {
		s.cfg.Metrics.reject("disabled")
		http.Error(w, "", http.StatusServiceUnavailable)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (m *Metrics) reject(reason string) {
	if m != nil && m.Rejected != nil {
		m.Rejected.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) session(result string) {
	if m != nil && m.Sessions != nil {
		m.Sessions.WithLabelValues(result).Inc()
	}
}

func (m *Metrics) message(result string) {
	if m != nil && m.Messages != nil {
		m.Messages.WithLabelValues(result).Inc()
	}
}

func (m *Metrics) limited(rule string) {
	if m != nil && m.RateLimited != nil {
		m.RateLimited.WithLabelValues(rule).Inc()
	}
}

func (s *Server) now() time.Time { return s.cfg.Now() }

// asset serves a static file of the widget.
func (s *Server) asset(name, ctype string, cross bool) http.HandlerFunc {
	body, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic(err) // embedded: a build error, not a runtime one
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Cache-Control", "public, max-age=3600")
		if cross {
			// The loader is a script a page on another origin includes.
			h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		}
		_, _ = w.Write(body)
	}
}

// frame serves the chat UI document with the site's own content security
// policy: it may be framed only by the site's allowed origins.
func (s *Server) frame(w http.ResponseWriter, r *http.Request) {
	site, err := s.siteByKey(r.Context(), r.URL.Query().Get("site"))
	if err != nil || !site.Enabled {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	body, _ := assets.ReadFile("assets/frame.html")
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", frameCSP(site))
	_, _ = w.Write(body)
}

// frameCSP is the frame document's policy (M-28). Turnstile cannot render
// without Cloudflare's script and frame, so a Turnstile site also allows
// that one host; every other site allows nothing beyond itself.
func frameCSP(site store.ChatSite) string {
	script, frameSrc := "script-src 'self'", ""
	if site.Challenge == store.ChallengeTurnstile {
		script += " https://challenges.cloudflare.com"
		frameSrc = "; frame-src https://challenges.cloudflare.com"
	}
	return "frame-ancestors " + messaging.FrameAncestors(site.AllowedOrigins) +
		"; default-src 'none'; " + script + "; style-src 'self'; connect-src 'self'; img-src 'self' data:" +
		"; base-uri 'none'; form-action 'none'" + frameSrc
}

// siteByKey reads a site, remembering the answer for a few seconds so a
// flood of requests is not a flood of queries. An unknown key is remembered
// too.
func (s *Server) siteByKey(ctx context.Context, key string) (store.ChatSite, error) {
	if len(key) != 24 {
		return store.ChatSite{}, store.ErrNotFound
	}
	now := s.now()
	s.cacheMu.Lock()
	if c, ok := s.cache[key]; ok && now.Sub(c.at) < siteCacheTTL {
		s.cacheMu.Unlock()
		return c.site, c.err
	}
	s.cacheMu.Unlock()
	site, err := s.cfg.Backend.ChatSiteByKey(ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.ChatSite{}, err
	}
	s.cacheMu.Lock()
	if len(s.cache) > 10000 {
		clear(s.cache)
	}
	s.cache[key] = cachedSite{site: site, err: err, at: now}
	s.cacheMu.Unlock()
	return site, err
}

// api wraps a JSON endpoint: the concurrency limit, the request deadline, the
// body cap and the content type.
func (s *Server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.inflight.Add(1) > int64(s.cfg.MaxInFlight) {
			s.inflight.Add(-1)
			s.cfg.Metrics.reject("busy")
			w.Header().Set("Retry-After", "5")
			http.Error(w, "", http.StatusServiceUnavailable)
			return
		}
		defer s.inflight.Add(-1)
		ctx, cancel := context.WithTimeout(r.Context(), reqDeadline)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
				writeJSON(w, http.StatusUnsupportedMediaType, errBody("content_type"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		h(w, r)
	}
}

func errBody(code string) map[string]string { return map[string]string{"error": code} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads the JSON body; anything unreadable, oversize or with trailing
// data is refused.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil || dec.More() {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too_large"))
			return false
		}
		writeJSON(w, http.StatusBadRequest, errBody("bad_request"))
		return false
	}
	return true
}

// clientIP is the caller's address: the connection's peer, or behind trusted
// proxies the last X-Forwarded-For hop that is not one of them. A raw
// X-Forwarded-For from anyone else is ignored.
func (s *Server) clientIP(r *http.Request) string {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	ip := ap.Addr().Unmap()
	if !inPrefixes(ip, s.cfg.TrustedProxies) {
		return ip.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hops = append(hops, p)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		ip = a.Unmap()
		if !inPrefixes(ip, s.cfg.TrustedProxies) {
			break
		}
	}
	return ip.String()
}

func inPrefixes(ip netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// allow applies one limit. Over it, the caller gets a 429 with Retry-After
// and no body: nothing explains the rule.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, rule, key string, limit int, window time.Duration) bool {
	if s.cfg.Limiter.Allow(r.Context(), rule+":"+key, limit, window) {
		return true
	}
	s.cfg.Metrics.limited(rule)
	w.Header().Set("Retry-After", strconv.Itoa(int(min(window, time.Minute).Seconds())))
	w.WriteHeader(http.StatusTooManyRequests)
	return false
}

// siteConfig is what the widget is told about a site.
type siteConfig struct {
	Name          string `json:"name"`
	Greeting      string `json:"greeting"`
	Accent        string `json:"accent"`
	Position      string `json:"position"`
	Collect       string `json:"collect"`
	Offline       bool   `json:"offline"`
	Hidden        bool   `json:"hidden"`
	PrivacyURL    string `json:"privacyUrl"`
	RetentionDays int    `json:"retentionDays"`
	Transcripts   bool   `json:"transcripts"`
	MaxChars      int    `json:"maxChars"`
}

func (s *Server) config(ctx context.Context, site store.ChatSite) (siteConfig, error) {
	online, _, err := s.cfg.Backend.ChatAvailability(ctx, site, s.now())
	if err != nil {
		return siteConfig{}, err
	}
	days, err := s.cfg.Backend.ChatRetentionDays(ctx, site)
	if err != nil {
		return siteConfig{}, err
	}
	return siteConfig{
		Name: site.Name, Greeting: site.Greeting, Accent: site.Accent, Position: site.Position, Collect: site.Collect,
		Offline: !online, Hidden: !online && site.OfflineMode == "hidden", PrivacyURL: site.PrivacyURL,
		RetentionDays: days, Transcripts: site.Transcripts && s.cfg.Mail != nil, MaxChars: messaging.MaxChatChars,
	}, nil
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.cfg.Log.Error("chat: "+what, "error", err)
	writeJSON(w, http.StatusInternalServerError, errBody("internal"))
}

// challenge tells the frame what to solve before it can open a session, and
// what the site looks like. An unknown or disabled site is a 404.
func (s *Server) challenge(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.allow(w, r, "bootstrap", ip, 60, time.Minute) {
		return
	}
	site, err := s.siteByKey(r.Context(), r.URL.Query().Get("site"))
	if errors.Is(err, store.ErrNotFound) || err == nil && !site.Enabled {
		writeJSON(w, http.StatusNotFound, errBody("not_found"))
		return
	}
	if err != nil {
		s.fail(w, "site lookup", err)
		return
	}
	cfg, err := s.config(r.Context(), site)
	if err != nil {
		s.fail(w, "site config", err)
		return
	}
	ch := map[string]any{"kind": site.Challenge}
	switch site.Challenge {
	case store.ChallengePoW:
		nonce, bits := s.pow.Issue(site.ID, ip, site.PowBits+s.adaptive.Extra(site.ID, s.now()))
		ch["nonce"], ch["bits"] = nonce, bits
	case store.ChallengeTurnstile:
		ch["siteKey"] = site.TurnstileSiteKey
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "challenge": ch})
}

type sessionReq struct {
	Site         string `json:"site"`
	VisitorToken string `json:"visitorToken"`
	Challenge    struct {
		Nonce     string `json:"nonce"`
		Solution  string `json:"solution"`
		Turnstile string `json:"turnstile"`
	} `json:"challenge"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Consent  bool   `json:"consent"`
	Page     string `json:"page"`
	Referrer string `json:"referrer"`
	// Website is the honeypot: a person never sees the field, so any value
	// means a script filled it.
	Website string `json:"website"`
}

// session creates or resumes a visitor.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	var req sessionReq
	if !decode(w, r, &req) {
		return
	}
	if req.Website != "" {
		s.cfg.Metrics.session("honeypot")
		writeJSON(w, http.StatusOK, map[string]any{"state": "closed"})
		return
	}
	ip := s.clientIP(r)
	if !s.allow(w, r, "bootstrap", ip, 60, time.Minute) {
		return
	}
	site, err := s.siteByKey(r.Context(), req.Site)
	if errors.Is(err, store.ErrNotFound) || err == nil && !site.Enabled {
		writeJSON(w, http.StatusNotFound, errBody("not_found"))
		return
	}
	if err != nil {
		s.fail(w, "site lookup", err)
		return
	}
	now := s.now()
	ipHash, err := s.cfg.Backend.ChatIPHash(ip, now)
	if err != nil {
		s.fail(w, "address hash", err)
		return
	}
	token := req.VisitorToken
	resume := false
	offline := false
	if token != "" {
		if _, err := s.cfg.Backend.ChatAuth(r.Context(), site.ID, token, ipHash); err == nil {
			resume = true
		} else if !errors.Is(err, store.ErrNotFound) {
			s.fail(w, "visitor lookup", err)
			return
		}
	}
	if !resume {
		token = ""
		if !s.allow(w, r, "session_min", ip, 5, time.Minute) || !s.allow(w, r, "session_hour", ip, 30, time.Hour) ||
			!s.allow(w, r, "session_site", strconv.FormatInt(site.ID, 10), 200, time.Minute) {
			s.cfg.Metrics.session("rate_limited")
			return
		}
		s.adaptive.Observe(site.ID, now)
		ok, err := s.solved(r.Context(), site, req, ip)
		if err != nil {
			s.cfg.Log.Warn("chat: challenge could not be checked", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, errBody("challenge_unavailable"))
			return
		}
		if !ok {
			s.cfg.Metrics.session("challenge_failed")
			writeJSON(w, http.StatusBadRequest, errBody("challenge"))
			return
		}
		cfg, err := s.config(r.Context(), site)
		if err != nil {
			s.fail(w, "site config", err)
			return
		}
		offline = cfg.Offline
		if cfg.Hidden {
			s.cfg.Metrics.session("hidden")
			writeJSON(w, http.StatusOK, map[string]any{"state": "hidden"})
			return
		}
	}
	in := store.ChatSessionInput{Site: site, Token: token, IPHash: ipHash, UAFamily: messaging.UAFamily(r.UserAgent()), Page: messaging.PageHint(req.Page)}
	// A site that collects nothing still takes an email address while nobody
	// is here to answer, so the reply has somewhere to go.
	if site.Collect != "off" || offline {
		name, nok := messaging.CleanChatName(req.Name)
		email := ""
		eok := true
		if strings.TrimSpace(req.Email) != "" {
			email, eok = messaging.CleanChatEmail(req.Email)
		}
		if !nok || !eok || (site.Collect == "required" && !resume && (name == "" || email == "")) ||
			((name != "" || email != "") && !req.Consent) {
			writeJSON(w, http.StatusBadRequest, errBody("contact"))
			return
		}
		in.Name, in.Email, in.Consent = name, email, req.Consent
	}
	res, err := s.cfg.Backend.ChatSession(r.Context(), in)
	if err != nil {
		s.fail(w, "session", err)
		return
	}
	if res.Closed {
		// Indistinguishable from a session but for its state; nothing was
		// stored and the token is worthless.
		s.cfg.Metrics.session("closed")
		writeJSON(w, http.StatusOK, map[string]any{"state": "closed", "visitorToken": fakeToken()})
		return
	}
	cfg, err := s.config(r.Context(), site)
	if err != nil {
		s.fail(w, "site config", err)
		return
	}
	out := map[string]any{"state": "open", "config": cfg}
	if res.ConversationID != 0 {
		out["conversationId"] = res.ConversationID
	}
	if res.Created {
		out["visitorToken"] = res.Token
		s.cfg.Metrics.session("created")
	} else {
		s.cfg.Metrics.session("resumed")
	}
	writeJSON(w, http.StatusOK, out)
}

func fakeToken() string {
	t, _ := newToken()
	return t
}

// solved checks the site's challenge.
func (s *Server) solved(ctx context.Context, site store.ChatSite, req sessionReq, ip string) (bool, error) {
	switch site.Challenge {
	case store.ChallengeNone:
		return true, nil
	case store.ChallengeTurnstile:
		if s.cfg.Turnstile == nil {
			return false, errors.New("turnstile is not available")
		}
		secret, err := s.cfg.Backend.ChatTurnstileSecret(ctx, site.ID)
		if err != nil {
			return false, err
		}
		return s.cfg.Turnstile.Verify(ctx, secret, req.Challenge.Turnstile, ip)
	default:
		// The difficulty the nonce carries is the one issued (site setting
		// plus any adaptive rise); it may never be below the site's setting.
		return s.pow.Verify(req.Challenge.Nonce, req.Challenge.Solution, site.ID, ip, site.PowBits) == nil, nil
	}
}

// visitor authenticates a call: the site named in the query and the token in
// the Authorization header. ok is false after the response was written.
func (s *Server) visitor(w http.ResponseWriter, r *http.Request) (store.ChatSite, store.ChatVisitor, bool) {
	var none store.ChatVisitor
	site, err := s.siteByKey(r.Context(), r.URL.Query().Get("site"))
	if errors.Is(err, store.ErrNotFound) || err == nil && !site.Enabled {
		writeJSON(w, http.StatusNotFound, errBody("not_found"))
		return site, none, false
	}
	if err != nil {
		s.fail(w, "site lookup", err)
		return site, none, false
	}
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || tok == "" || len(tok) > 128 {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return site, none, false
	}
	ipHash, err := s.cfg.Backend.ChatIPHash(s.clientIP(r), s.now())
	if err != nil {
		s.fail(w, "address hash", err)
		return site, none, false
	}
	v, err := s.cfg.Backend.ChatAuth(r.Context(), site.ID, tok, ipHash)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return site, none, false
	}
	if err != nil {
		s.fail(w, "visitor lookup", err)
		return site, none, false
	}
	return site, v, true
}

func vkey(v store.ChatVisitor) string { return strconv.FormatInt(v.ID, 10) }

var closedBody = map[string]any{"state": "closed"}

type messageReq struct {
	Text        string `json:"text"`
	ClientMsgID string `json:"clientMsgId"`
}

// messages takes a visitor's message.
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	site, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	var req messageReq
	if !decode(w, r, &req) {
		return
	}
	if v.Blocked {
		// A blocked visitor gets a normal answer and reaches nobody.
		s.cfg.Metrics.message("closed")
		writeJSON(w, http.StatusOK, closedBody)
		return
	}
	if !s.allow(w, r, "msg_min", vkey(v), 20, time.Minute) || !s.allow(w, r, "msg_day", vkey(v), 300, 24*time.Hour) ||
		!s.allow(w, r, "msg_site_day", strconv.FormatInt(site.ID, 10), 10000, 24*time.Hour) {
		s.cfg.Metrics.message("rate_limited")
		return
	}
	text, tok := messaging.CleanChatText(req.Text)
	if tok && site.StripLinks {
		text, tok = messaging.CleanChatText(messaging.StripLinks(text))
	}
	if !tok || req.ClientMsgID == "" || len(req.ClientMsgID) > 64 || !printableID(req.ClientMsgID) {
		s.cfg.Metrics.message("invalid")
		writeJSON(w, http.StatusBadRequest, errBody("invalid"))
		return
	}
	online, _, err := s.cfg.Backend.ChatAvailability(r.Context(), site, s.now())
	if err != nil {
		s.fail(w, "availability", err)
		return
	}
	res, err := s.cfg.Backend.ChatSend(r.Context(), store.ChatSendInput{
		Site: site, VisitorID: v.ID, Text: text, ClientMsgID: req.ClientMsgID, Offline: !online,
		NoNew: !online && site.OfflineMode == "hidden",
	})
	var verr *store.ValidationError
	switch {
	case errors.As(err, &verr):
		s.cfg.Metrics.message("email_required")
		writeJSON(w, http.StatusBadRequest, errBody("email_required"))
		return
	case errors.Is(err, store.ErrChatLimit):
		s.cfg.Metrics.limited("unanswered")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	case errors.Is(err, store.ErrChatFull):
		s.cfg.Metrics.message("full")
		writeJSON(w, http.StatusServiceUnavailable, errBody("full"))
		return
	case errors.Is(err, store.ErrChannelUnavailable), errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody("not_found"))
		return
	case err != nil:
		s.fail(w, "send", err)
		return
	}
	switch {
	case res.Closed:
		s.cfg.Metrics.message("closed")
		writeJSON(w, http.StatusOK, closedBody)
	case res.Duplicate:
		s.cfg.Metrics.message("duplicate")
		writeJSON(w, http.StatusOK, map[string]any{"state": "open", "id": res.MessageID, "conversationId": res.ConversationID})
	default:
		s.cfg.Metrics.message("stored")
		writeJSON(w, http.StatusCreated, map[string]any{"state": "open", "id": res.MessageID, "conversationId": res.ConversationID})
	}
}

func printableID(s string) bool {
	for _, r := range s {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}

// typing tells the agents the visitor is typing.
func (s *Server) typing(w http.ResponseWriter, r *http.Request) {
	site, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	if v.Blocked {
		writeJSON(w, http.StatusOK, closedBody)
		return
	}
	if !s.allow(w, r, "typing", vkey(v), 30, time.Minute) {
		return
	}
	if err := s.cfg.Backend.ChatTyping(r.Context(), site, v.ID); err != nil {
		s.fail(w, "typing", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// end closes the visitor's chat.
func (s *Server) end(w http.ResponseWriter, r *http.Request) {
	site, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	if v.Blocked {
		writeJSON(w, http.StatusOK, closedBody)
		return
	}
	if !s.allow(w, r, "end", vkey(v), 10, time.Minute) {
		return
	}
	if err := s.cfg.Backend.ChatEnd(r.Context(), site, v.ID); err != nil {
		s.fail(w, "end", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// history returns what the visitor can see of their chats.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	if v.Blocked {
		writeJSON(w, http.StatusOK, map[string]any{"state": "closed", "lines": []store.ChatLine{}})
		return
	}
	if !s.allow(w, r, "history", vkey(v), 60, time.Minute) {
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") != "" && (err != nil || after < 0) {
		writeJSON(w, http.StatusBadRequest, errBody("bad_request"))
		return
	}
	lines, err := s.cfg.Backend.ChatHistory(r.Context(), v.ID, after, 100)
	if err != nil {
		s.fail(w, "history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": "open", "lines": lines})
}

// transcript emails the visitor their chat, when the site allows it and the
// visitor gave an address.
func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	site, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	if !site.Transcripts || s.cfg.Mail == nil {
		writeJSON(w, http.StatusNotFound, errBody("not_found"))
		return
	}
	if v.Blocked {
		writeJSON(w, http.StatusOK, closedBody)
		return
	}
	if !s.allow(w, r, "transcript", vkey(v), 3, 24*time.Hour) {
		return
	}
	email, lines, err := s.cfg.Backend.ChatTranscript(r.Context(), v.ID)
	if err != nil {
		s.fail(w, "transcript", err)
		return
	}
	if email == "" || len(lines) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("no_email"))
		return
	}
	var b strings.Builder
	for _, l := range lines {
		who := "You"
		if l.From == "agent" {
			who = "Support"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", l.At.UTC().Format("2006-01-02 15:04 UTC"), who, l.Text)
	}
	if err := s.cfg.Mail(r.Context(), email, "Your chat with "+site.Name, b.String()); err != nil {
		s.cfg.Log.Warn("chat: transcript email failed", "error", err)
		writeJSON(w, http.StatusBadGateway, errBody("email_failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// claimStream takes the visitor's one stream slot on this pod. A reload
// reconnects before the server has noticed the old connection is gone, so a
// taken slot is waited for briefly before the newcomer is refused.
func (s *Server) claimStream(ctx context.Context, id int64) bool {
	for i := 0; i < 10; i++ {
		s.streamMu.Lock()
		if _, dup := s.live[id]; !dup {
			s.live[id] = struct{}{}
			s.streamMu.Unlock()
			return true
		}
		s.streamMu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// events is the visitor's live feed: server-sent events whose ids come from
// chat_events, so a stream that reconnects with Last-Event-ID loses and
// repeats nothing on whichever pod it lands. The token travels in the
// Authorization header (the widget streams with fetch). One stream per visitor
// and a bounded number per pod; a stream with nothing to say for 30 minutes
// is closed.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	_, v, ok := s.visitor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	if v.Blocked {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: closed\ndata: {}\n\n"))
		_ = rc.Flush()
		return
	}
	if !s.allow(w, r, "events", vkey(v), 10, time.Minute) {
		return
	}
	if s.streams.Load() >= int64(s.cfg.MaxStreams) {
		s.cfg.Metrics.reject("busy")
		h.Set("Retry-After", "10")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !s.claimStream(ctx, v.ID) {
		h.Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	s.streams.Add(1)
	if s.cfg.Metrics.Streams != nil {
		s.cfg.Metrics.Streams.Inc()
	}
	defer func() {
		s.streamMu.Lock()
		delete(s.live, v.ID)
		s.streamMu.Unlock()
		s.streams.Add(-1)
		if s.cfg.Metrics.Streams != nil {
			s.cfg.Metrics.Streams.Dec()
		}
	}()
	var after int64
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, errBody("bad_request"))
			return
		}
		after = n
	} else {
		latest, err := s.cfg.Backend.ChatLatestEventID(ctx, v.ID)
		if err != nil {
			s.fail(w, "stream", err)
			return
		}
		after = latest
	}
	_ = rc.SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil || rc.Flush() != nil {
		return
	}
	poll := time.NewTicker(s.cfg.Poll)
	defer poll.Stop()
	keep := time.NewTicker(s.cfg.Keepalive)
	defer keep.Stop()
	lastSaid := s.now()
	for {
		evs, err := s.cfg.Backend.ChatEvents(ctx, v.ID, after, 100)
		if err != nil {
			if ctx.Err() == nil {
				s.cfg.Log.Error("chat: reading stream events", "error", err)
			}
			return
		}
		for _, e := range evs {
			after = e.ID
			data := []byte("{}")
			if e.Line != nil {
				data, _ = json.Marshal(e.Line)
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data); err != nil {
				return
			}
			lastSaid = s.now()
		}
		if len(evs) > 0 && rc.Flush() != nil {
			return
		}
		if len(evs) == 100 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-keep.C:
			if s.now().Sub(lastSaid) >= s.cfg.StreamIdle {
				return
			}
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case <-poll.C:
		}
	}
}
