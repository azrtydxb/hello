package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/store"
)

const siteKey = "0123456789abcdef01234567"

type rig struct {
	t    *testing.T
	fake *fakeBackend
	srv  *Server
	logs *bytes.Buffer
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func newRig(t *testing.T, mod func(*store.ChatSite), cfg func(*Config)) *rig {
	t.Helper()
	f := newFake()
	f.addSite(siteKey, mod)
	logs := &bytes.Buffer{}
	clk := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	c := Config{Backend: f, Key: []byte("0123456789abcdef0123456789abcdef"), Enabled: true,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: clk.Now,
		Poll: 5 * time.Millisecond, Keepalive: 20 * time.Millisecond}
	if cfg != nil {
		cfg(&c)
	}
	r := &rig{t: t, fake: f, srv: New(c), logs: logs}
	return r
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &m)
	return m
}

func (g *rig) do(method, path string, body any, mod func(*http.Request)) resp {
	g.t.Helper()
	var rd io.Reader
	if s, ok := body.(string); ok {
		rd = strings.NewReader(s)
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "192.0.2.10:5555"
	if mod != nil {
		mod(req)
	}
	w := httptest.NewRecorder()
	g.srv.ServeHTTP(w, req)
	return resp{w}
}

func auth(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// newSession gets a challenge, solves it and creates a visitor.
func (g *rig) newSession(mod func(map[string]any)) resp {
	g.t.Helper()
	ch := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil)
	if ch.Code != 200 {
		g.t.Fatalf("challenge: %d %s", ch.Code, ch.Body)
	}
	c := ch.json()["challenge"].(map[string]any)
	body := map[string]any{"site": siteKey}
	if c["kind"] == "pow" {
		nonce, bits := c["nonce"].(string), int(c["bits"].(float64))
		body["challenge"] = map[string]any{"nonce": nonce, "solution": Solve(nonce, bits)}
	}
	if mod != nil {
		mod(body)
	}
	return g.do("POST", "/chat/v1/session", body, nil)
}

func (g *rig) token() string {
	g.t.Helper()
	r := g.newSession(nil)
	if r.Code != 200 || r.json()["state"] != "open" {
		g.t.Fatalf("session: %d %s", r.Code, r.Body)
	}
	return r.json()["visitorToken"].(string)
}

// TestChatAbuseControls fails if a session is created without a valid
// challenge when one is configured, a limit of M-29a.2 is exceeded without a
// 429, a body over 2000 characters is accepted, a blocked visitor reaches an
// agent, a visitor token appears in a URL or a log, the frame response lacks
// the site's frame-ancestors, or the Origin header changes any outcome.
func TestChatAbuseControls(t *testing.T) {
	t.Run("challenge", func(t *testing.T) {
		g := newRig(t, nil, nil)
		if r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, nil); r.Code != 400 {
			t.Fatalf("no solution: %d", r.Code)
		}
		ch := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["challenge"].(map[string]any)
		nonce, bits := ch["nonce"].(string), int(ch["bits"].(float64))
		bad := map[string]any{"site": siteKey, "challenge": map[string]any{"nonce": nonce, "solution": "not-a-solution"}}
		if r := g.do("POST", "/chat/v1/session", bad, nil); r.Code != 400 {
			t.Fatalf("wrong solution: %d", r.Code)
		}
		good := map[string]any{"site": siteKey, "challenge": map[string]any{"nonce": nonce, "solution": Solve(nonce, bits)}}
		if r := g.do("POST", "/chat/v1/session", good, nil); r.Code != 200 || r.json()["state"] != "open" {
			t.Fatalf("right solution: %d %s", r.Code, r.Body)
		}
		if r := g.do("POST", "/chat/v1/session", good, nil); r.Code != 400 {
			t.Fatalf("a solved nonce works once: %d", r.Code)
		}
		other := func(r *http.Request) { r.RemoteAddr = "192.0.2.77:1" }
		ch2 := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["challenge"].(map[string]any)
		n2 := ch2["nonce"].(string)
		moved := map[string]any{"site": siteKey, "challenge": map[string]any{"nonce": n2, "solution": Solve(n2, int(ch2["bits"].(float64)))}}
		if r := g.do("POST", "/chat/v1/session", moved, other); r.Code != 400 {
			t.Fatalf("a nonce is bound to the address it was issued to: %d", r.Code)
		}
	})

	t.Run("nonce below the site difficulty", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.PowBits = 18 }, nil)
		easy, _ := g.srv.pow.Issue(1, "192.0.2.10", 14)
		body := map[string]any{"site": siteKey, "challenge": map[string]any{"nonce": easy, "solution": Solve(easy, 14)}}
		if r := g.do("POST", "/chat/v1/session", body, nil); r.Code != 400 {
			t.Fatalf("an easier nonce than the site's setting was accepted: %d", r.Code)
		}
	})

	t.Run("none challenge", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone }, nil)
		if r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, nil); r.Code != 200 {
			t.Fatalf("%d", r.Code)
		}
	})

	t.Run("session limits", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone }, nil)
		for i := 0; i < 5; i++ {
			if r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, nil); r.Code != 200 {
				t.Fatalf("session %d: %d", i, r.Code)
			}
		}
		r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, nil)
		if r.Code != 429 || r.Header().Get("Retry-After") == "" || r.Body.Len() != 0 {
			t.Fatalf("the sixth session in a minute: %d retry=%q body=%q", r.Code, r.Header().Get("Retry-After"), r.Body.String())
		}
		// Another address has its own allowance.
		other := func(r *http.Request) { r.RemoteAddr = "192.0.2.99:9" }
		if r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, other); r.Code != 200 {
			t.Fatalf("another address: %d", r.Code)
		}
	})

	t.Run("site session limit", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone }, nil)
		var limited bool
		for i := 0; i < 210; i++ {
			ip := func(r *http.Request) {
				r.RemoteAddr = netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 51, byte(i / 250), byte(i%250 + 1)}), 1).String()
			}
			if r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, ip); r.Code == 429 {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("more than 200 sessions a minute on one site were accepted")
		}
	})

	t.Run("message limits and size", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		send := func(i int, text string) resp {
			return g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": text, "clientMsgId": "m" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + time.Duration(i).String()}, auth(tok))
		}
		if r := send(0, strings.Repeat("a", 2001)); r.Code != 400 {
			t.Fatalf("2001 characters: %d", r.Code)
		}
		if r := send(1, strings.Repeat("é", 2000)); r.Code != 201 {
			t.Fatalf("2000 characters (4000 bytes) must be accepted: %d %s", r.Code, r.Body)
		}
		if r := send(2, "\x00\x01\u202e"); r.Code != 400 {
			t.Fatalf("a message of control characters only: %d", r.Code)
		}
		var limited bool
		for i := 10; i < 40; i++ {
			if r := send(i, "hello"); r.Code == 429 {
				limited = true
				if r.Header().Get("Retry-After") == "" || r.Body.Len() != 0 {
					t.Fatalf("429 needs Retry-After and no body: %q %q", r.Header().Get("Retry-After"), r.Body.String())
				}
				break
			}
		}
		if !limited {
			t.Fatal("more than 20 messages a minute from one visitor were accepted")
		}
	})

	t.Run("message cleaning", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.StripLinks = true }, nil)
		tok := g.token()
		r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": " hi\x00 see https://evil.example/x and www.evil.test now ", "clientMsgId": "c1"}, auth(tok))
		if r.Code != 201 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		got := g.fake.sent[0].Text
		if got != "hi see [link removed] and [link removed] now" {
			t.Fatalf("text = %q", got)
		}
	})

	t.Run("blocked visitor reaches nobody", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		g.fake.blocked[g.fake.visitors[tok].ID] = true
		r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "hello", "clientMsgId": "b1"}, auth(tok))
		if r.Code != 200 || r.json()["state"] != "closed" {
			t.Fatalf("a blocked visitor gets a normal-looking closed answer: %d %s", r.Code, r.Body)
		}
		if len(g.fake.sent) != 0 {
			t.Fatal("a blocked visitor's message reached the backend")
		}
		s := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey, "visitorToken": tok}, nil)
		if s.Code != 200 || s.json()["state"] != "closed" {
			t.Fatalf("a blocked visitor's session: %d %s", s.Code, s.Body)
		}
	})

	t.Run("honeypot", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone }, nil)
		r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey, "website": "http://spam"}, nil)
		if r.Code != 200 || r.json()["state"] != "closed" || len(g.fake.sessions) != 0 {
			t.Fatalf("honeypot: %d %s sessions=%d", r.Code, r.Body, len(g.fake.sessions))
		}
	})

	t.Run("token only in a header and never logged", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		if r := g.do("GET", "/chat/v1/history?site="+siteKey+"&token="+tok, nil, nil); r.Code != 401 {
			t.Fatalf("a token in the URL authenticated: %d", r.Code)
		}
		if r := g.do("GET", "/chat/v1/history?site="+siteKey, nil, auth(tok)); r.Code != 200 {
			t.Fatalf("header token: %d", r.Code)
		}
		g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "private words", "clientMsgId": "l1"}, auth(tok))
		for _, secret := range []string{tok, "private words", "192.0.2.10"} {
			if strings.Contains(g.logs.String(), secret) {
				t.Fatalf("log contains %q:\n%s", secret, g.logs.String())
			}
		}
	})

	t.Run("frame carries the site's frame-ancestors", func(t *testing.T) {
		g := newRig(t, nil, nil)
		r := g.do("GET", "/chat/v1/frame?site="+siteKey, nil, nil)
		csp := r.Header().Get("Content-Security-Policy")
		if r.Code != 200 || !strings.Contains(csp, "frame-ancestors https://intranet.lan;") || !strings.Contains(csp, "script-src 'self';") ||
			!strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "form-action 'none'") {
			t.Fatalf("frame: %d csp=%q", r.Code, csp)
		}
		g2 := newRig(t, func(s *store.ChatSite) { s.AllowedOrigins = nil }, nil)
		if csp := g2.do("GET", "/chat/v1/frame?site="+siteKey, nil, nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none';") {
			t.Fatalf("no origins must mean none: %q", csp)
		}
		g3 := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeTurnstile }, nil)
		if csp := g3.do("GET", "/chat/v1/frame?site="+siteKey, nil, nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "challenges.cloudflare.com") {
			t.Fatalf("a turnstile site needs Cloudflare's host: %q", csp)
		}
		if r := g.do("GET", "/chat/v1/frame?site=ffffffffffffffffffffffff", nil, nil); r.Code != 404 {
			t.Fatalf("unknown site: %d", r.Code)
		}
	})

	t.Run("origin changes nothing", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		do := func(origin string) (int, string) {
			r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "same", "clientMsgId": "o-" + origin[:1]}, func(q *http.Request) {
				auth(tok)(q)
				if origin != "none" {
					q.Header.Set("Origin", origin)
				}
			})
			return r.Code, r.json()["state"].(string)
		}
		c1, s1 := do("none")
		c2, s2 := do("https://evil.example")
		c3, s3 := do("https://intranet.lan")
		if c1 != c2 || c2 != c3 || s1 != s2 || s2 != s3 {
			t.Fatalf("outcome differs by Origin: %d/%s %d/%s %d/%s", c1, s1, c2, s2, c3, s3)
		}
	})

	t.Run("content type and size", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		r := g.do("POST", "/chat/v1/messages?site="+siteKey, `{"text":"x"}`, func(q *http.Request) { auth(tok)(q); q.Header.Set("Content-Type", "text/plain") })
		if r.Code != 415 {
			t.Fatalf("text/plain: %d", r.Code)
		}
		big := `{"text":"` + strings.Repeat("a", 17<<10) + `","clientMsgId":"x"}`
		if r := g.do("POST", "/chat/v1/messages?site="+siteKey, big, auth(tok)); r.Code != 413 {
			t.Fatalf("over 16 KiB: %d", r.Code)
		}
		if r := g.do("POST", "/chat/v1/messages?site="+siteKey, `{"text":"x"} {"y":1}`, auth(tok)); r.Code != 400 {
			t.Fatalf("trailing data: %d", r.Code)
		}
	})

	t.Run("unanswered cap is a 429", func(t *testing.T) {
		g := newRig(t, nil, nil)
		tok := g.token()
		g.srv.cfg.Backend = &capBackend{fakeBackend: g.fake}
		r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "hi", "clientMsgId": "u1"}, auth(tok))
		if r.Code != 429 {
			t.Fatalf("%d", r.Code)
		}
	})

	t.Run("overload is a 503", func(t *testing.T) {
		g := newRig(t, nil, func(c *Config) { c.MaxInFlight = 1 })
		g.srv.inflight.Store(1)
		r := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil)
		if r.Code != 503 {
			t.Fatalf("%d", r.Code)
		}
	})

	t.Run("hidden while offline", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.OfflineMode = "hidden"; s.Challenge = store.ChallengeNone }, nil)
		g.fake.online = false
		r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey}, nil)
		if r.json()["state"] != "hidden" || len(g.fake.sessions) != 0 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})

	t.Run("offline site takes an email even when it collects nothing", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone; s.Collect = "off" }, nil)
		g.fake.online = false
		r := g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey, "email": "me@example.com", "name": "Me", "consent": true}, nil)
		if r.Code != 200 || len(g.fake.sessions) != 1 || g.fake.sessions[0].Email != "me@example.com" {
			t.Fatalf("%d %s %+v", r.Code, r.Body, g.fake.sessions)
		}
	})

	t.Run("contact rules", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeNone; s.Collect = "required" }, nil)
		for name, body := range map[string]map[string]any{
			"nothing":        {"site": siteKey},
			"no consent":     {"site": siteKey, "name": "A", "email": "a@example.com"},
			"bad email":      {"site": siteKey, "name": "A", "email": "not an email", "consent": true},
			"display name":   {"site": siteKey, "name": "A", "email": "A <a@example.com>", "consent": true},
			"missing email":  {"site": siteKey, "name": "A", "consent": true},
			"newline header": {"site": siteKey, "name": "A", "email": "a@example.com\nBcc: x@y.z", "consent": true},
		} {
			if r := g.do("POST", "/chat/v1/session", body, nil); r.Code != 400 {
				t.Fatalf("%s: %d", name, r.Code)
			}
			g.srv.cfg.Limiter = NewLimiter(nil, nil) // each case gets a fresh allowance
		}
		ok := map[string]any{"site": siteKey, "name": "A", "email": "a@example.com", "consent": true}
		if r := g.do("POST", "/chat/v1/session", ok, nil); r.Code != 200 {
			t.Fatalf("valid contact: %d %s", r.Code, r.Body)
		}
	})
}

type capBackend struct{ *fakeBackend }

func (capBackend) ChatSend(context.Context, store.ChatSendInput) (store.ChatSendResult, error) {
	return store.ChatSendResult{}, store.ErrChatLimit
}

// TestChatTurnstile fails if a Turnstile site accepts a session without a
// verified token, verifies with the wrong secret or fails open when
// Cloudflare cannot be reached.
func TestChatTurnstile(t *testing.T) {
	var secrets []string
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		secrets = append(secrets, r.Form.Get("secret"))
		_ = json.NewEncoder(w).Encode(map[string]bool{"success": r.Form.Get("response") == "good"})
	}))
	defer cf.Close()
	g := newRig(t, func(s *store.ChatSite) { s.Challenge = store.ChallengeTurnstile; s.TurnstileSiteKey = "0xAAA" },
		func(c *Config) { c.Turnstile = &Turnstile{Client: cf.Client(), Endpoint: cf.URL} })
	g.fake.secret = "sekrit"
	post := func(tok string) resp {
		return g.do("POST", "/chat/v1/session", map[string]any{"site": siteKey, "challenge": map[string]any{"turnstile": tok}}, nil)
	}
	if r := post("bad"); r.Code != 400 {
		t.Fatalf("bad token: %d", r.Code)
	}
	if r := post(""); r.Code != 400 {
		t.Fatalf("no token: %d", r.Code)
	}
	if r := post("good"); r.Code != 200 || r.json()["state"] != "open" {
		t.Fatalf("good token: %d %s", r.Code, r.Body)
	}
	if len(secrets) == 0 || secrets[0] != "sekrit" {
		t.Fatalf("verified with %v", secrets)
	}
	ch := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["challenge"].(map[string]any)
	if ch["kind"] != "turnstile" || ch["siteKey"] != "0xAAA" || ch["nonce"] != nil {
		t.Fatalf("challenge = %v", ch)
	}
	cf.Close()
	if r := post("good"); r.Code != 503 {
		t.Fatalf("an unreachable Cloudflare must not let a session through: %d", r.Code)
	}
}

// TestLANChatListenerIsolation fails if any /api/v1, /oauth, /mcp, /hooks, /m
// or UI path answers on the chat listener, or if the global kill switch lets
// a chat request through.
func TestLANChatListenerIsolation(t *testing.T) {
	g := newRig(t, nil, nil)
	for _, p := range []string{"/", "/index.html", "/api/v1/me", "/api/v1/inbox/conversations", "/oauth/authorize", "/oauth/token",
		"/.well-known/oauth-authorization-server", "/mcp", "/hooks/sms/abc", "/m/token", "/assets/index.js", "/metrics", "/chat", "/chat/v2/session",
		"/chat/v1/", "/chat/v1/../api/v1/me", "/chat/v1/attachments"} {
		for _, m := range []string{"GET", "POST"} {
			r := g.do(m, p, `{}`, nil)
			if r.Code != 404 && r.Code != 405 && r.Code != 301 && r.Code != 307 {
				t.Fatalf("%s %s answered %d", m, p, r.Code)
			}
			if r.Code == 301 || r.Code == 307 || r.Code == 405 {
				continue
			}
			if r.Body.Len() > 0 && strings.Contains(r.Body.String(), "{") && !strings.Contains(r.Body.String(), "404") {
				t.Fatalf("%s %s: unexpected body %q", m, p, r.Body)
			}
		}
	}
	if r := g.do("GET", "/healthz", nil, nil); r.Code != 200 {
		t.Fatalf("healthz %d", r.Code)
	}
	off := newRig(t, nil, func(c *Config) { c.Enabled = false })
	for _, p := range []string{"/chat/v1/widget.js", "/chat/v1/frame?site=" + siteKey, "/chat/v1/challenge?site=" + siteKey} {
		if r := off.do("GET", p, nil, nil); r.Code != 503 {
			t.Fatalf("kill switch: %s answered %d", p, r.Code)
		}
	}
	if r := off.do("GET", "/healthz", nil, nil); r.Code != 200 {
		t.Fatalf("healthz must answer with chat off: %d", r.Code)
	}
}

// TestChatAssets fails if the widget loader is not served as a public,
// cross-origin script, builds markup from text, or the frame script does.
func TestChatAssets(t *testing.T) {
	g := newRig(t, nil, nil)
	r := g.do("GET", "/chat/v1/widget.js", nil, nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "public, max-age=3600" || r.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" ||
		!strings.HasPrefix(r.Header().Get("Content-Type"), "application/javascript") {
		t.Fatalf("widget.js: %d %v", r.Code, r.Header())
	}
	if n := r.Body.Len(); n > 10*1024 {
		t.Fatalf("widget.js is %d bytes, over the 10 KB budget", n)
	}
	if !strings.Contains(r.Body.String(), "sandbox") || !strings.Contains(r.Body.String(), "e.origin !== base") {
		t.Fatal("the loader must sandbox the frame and check origins")
	}
	for _, p := range []string{"widget.js", "frame.js", "pow.js"} {
		b, err := assets.ReadFile("assets/" + p)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
			if strings.Contains(string(b), banned) {
				t.Fatalf("%s uses %s", p, banned)
			}
		}
	}
	for _, p := range []string{"frame.js", "frame.css", "pow.js"} {
		if r := g.do("GET", "/chat/v1/"+p, nil, nil); r.Code != 200 || r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d", p, r.Code)
		}
	}
}

// TestChatClientIP fails if a raw X-Forwarded-For from an untrusted caller is
// believed, or a trusted proxy's is not.
func TestChatClientIP(t *testing.T) {
	g := newRig(t, nil, func(c *Config) { c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")} })
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	if got := g.srv.clientIP(mk("192.0.2.1:1", "6.6.6.6")); got != "192.0.2.1" {
		t.Fatalf("untrusted caller: %s", got)
	}
	if got := g.srv.clientIP(mk("10.1.0.5:1", "203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("trusted proxy: %s", got)
	}
	if got := g.srv.clientIP(mk("10.1.0.5:1", "6.6.6.6, 203.0.113.9, 10.1.0.7")); got != "203.0.113.9" {
		t.Fatalf("chain: %s", got)
	}
}

// TestChatTranscript fails if a transcript is sent when the site has it off,
// to anyone but the visitor's own address, or beyond three a day.
func TestChatTranscript(t *testing.T) {
	var sentTo []string
	mail := func(_ context.Context, to, subject, body string) error {
		sentTo = append(sentTo, to)
		if !strings.Contains(subject, "Support") || !strings.Contains(body, "hello there") {
			t.Fatalf("subject=%q body=%q", subject, body)
		}
		return nil
	}
	g := newRig(t, func(s *store.ChatSite) { s.Transcripts = true }, func(c *Config) { c.Mail = mail })
	tok := g.token()
	v := g.fake.visitors[tok]
	g.fake.email = "me@example.com"
	g.fake.lines[v.ID] = []store.ChatLine{{ID: 1, From: "visitor", Text: "hello there", At: time.Now()}}
	for i := 0; i < 3; i++ {
		if r := g.do("POST", "/chat/v1/transcript?site="+siteKey, `{}`, auth(tok)); r.Code != 204 {
			t.Fatalf("transcript %d: %d", i, r.Code)
		}
	}
	if r := g.do("POST", "/chat/v1/transcript?site="+siteKey, `{}`, auth(tok)); r.Code != 429 {
		t.Fatalf("a fourth transcript in a day: %d", r.Code)
	}
	for _, to := range sentTo {
		if to != "me@example.com" {
			t.Fatalf("sent to %q", to)
		}
	}
	off := newRig(t, nil, func(c *Config) { c.Mail = mail })
	tok = off.token()
	if r := off.do("POST", "/chat/v1/transcript?site="+siteKey, `{}`, auth(tok)); r.Code != 404 {
		t.Fatalf("transcripts off: %d", r.Code)
	}
}

// TestChatSSEResume fails if Last-Event-ID resume loses or repeats events
// across two pods, a second stream per visitor is allowed, or a stream is not
// bounded per pod.
func TestChatSSEResume(t *testing.T) {
	f := newFake()
	f.addSite(siteKey, nil)
	tok := "tok-resume-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	v := f.addVisitor(tok)
	mkSrv := func(mod func(*Config)) *httptest.Server {
		c := Config{Backend: f, Key: []byte("0123456789abcdef0123456789abcdef"), Enabled: true, Poll: 5 * time.Millisecond, Keepalive: 30 * time.Millisecond}
		if mod != nil {
			mod(&c)
		}
		return httptest.NewServer(New(c))
	}
	podA, podB := mkSrv(nil), mkSrv(nil)
	defer podA.Close()
	defer podB.Close()

	for i := 1; i <= 3; i++ {
		f.push(v.ID, "message", &store.ChatLine{ID: int64(i), From: "agent", Text: "m" + string(rune('0'+i)), At: time.Now()})
	}
	read := func(base, lastID string, want int) (ids []string, texts []string) {
		req, _ := http.NewRequest("GET", base+"/chat/v1/events?site="+siteKey, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for len(ids) < want {
			n, err := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			for {
				i := strings.Index(string(buf), "\n\n")
				if i < 0 {
					break
				}
				frame := string(buf[:i])
				buf = buf[i+2:]
				var id, data string
				for _, l := range strings.Split(frame, "\n") {
					if v, ok := strings.CutPrefix(l, "id: "); ok {
						id = v
					}
					if v, ok := strings.CutPrefix(l, "data: "); ok {
						data = v
					}
				}
				if id != "" {
					ids = append(ids, id)
					var line store.ChatLine
					_ = json.Unmarshal([]byte(data), &line)
					texts = append(texts, line.Text)
				}
			}
			if err != nil {
				break
			}
		}
		return
	}
	ids, texts := read(podA.URL, "0", 3)
	if strings.Join(ids, ",") != "1,2,3" || strings.Join(texts, ",") != "m1,m2,m3" {
		t.Fatalf("pod A from 0: %v %v", ids, texts)
	}
	// The visitor reconnects to the other pod having seen event 2.
	ids, _ = read(podB.URL, "2", 1)
	if strings.Join(ids, ",") != "3" {
		t.Fatalf("pod B from 2 must give exactly 3, got %v", ids)
	}
	// A new event after the stream opened arrives once.
	go func() {
		time.Sleep(60 * time.Millisecond)
		f.push(v.ID, "message", &store.ChatLine{ID: 4, From: "agent", Text: "m4", At: time.Now()})
	}()
	ids, _ = read(podA.URL, "3", 1)
	if strings.Join(ids, ",") != "4" {
		t.Fatalf("live event: %v", ids)
	}

	// One stream per visitor.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", podA.URL+"/chat/v1/events?site="+siteKey, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	first, err := http.DefaultClient.Do(req)
	if err != nil || first.StatusCode != 200 {
		t.Fatalf("first stream: %v", err)
	}
	req2, _ := http.NewRequest("GET", podA.URL+"/chat/v1/events?site="+siteKey, nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	second, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a second stream for the same visitor: %d", second.StatusCode)
	}
	cancel()
	_ = first.Body.Close()

	// Bounded per pod.
	capped := mkSrv(func(c *Config) { c.MaxStreams = 0; c.MaxStreams = 1 })
	defer capped.Close()
	tok2 := "tok-resume-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	f.addVisitor(tok2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r1, _ := http.NewRequestWithContext(ctx2, "GET", capped.URL+"/chat/v1/events?site="+siteKey, nil)
	r1.Header.Set("Authorization", "Bearer "+tok)
	a, err := http.DefaultClient.Do(r1)
	if err != nil || a.StatusCode != 200 {
		t.Fatalf("capped first: %v", err)
	}
	r2, _ := http.NewRequest("GET", capped.URL+"/chat/v1/events?site="+siteKey, nil)
	r2.Header.Set("Authorization", "Bearer "+tok2)
	b, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Body.Close()
	if b.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("beyond the per-pod stream cap: %d", b.StatusCode)
	}
	cancel2()
	_ = a.Body.Close()

	// A bad token is not a stream.
	r3, _ := http.NewRequest("GET", podA.URL+"/chat/v1/events?site="+siteKey, nil)
	r3.Header.Set("Authorization", "Bearer nope")
	c, err := http.DefaultClient.Do(r3)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Body.Close()
	if c.StatusCode != 401 {
		t.Fatalf("bad token: %d", c.StatusCode)
	}
}

// TestChatMessageFlow fails if a visitor's message does not reach the backend
// with the cleaned text and the offline flag, an offline hidden site lets a
// new conversation start, or history and typing and end are not wired.
func TestChatMessageFlow(t *testing.T) {
	g := newRig(t, nil, nil)
	tok := g.token()
	r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "  hello  ", "clientMsgId": "a1"}, auth(tok))
	if r.Code != 201 || r.json()["state"] != "open" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if len(g.fake.sent) != 1 || g.fake.sent[0].Text != "hello" || g.fake.sent[0].Offline || g.fake.sent[0].ClientMsgID != "a1" {
		t.Fatalf("%+v", g.fake.sent)
	}
	g.fake.online = false
	g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "again", "clientMsgId": "a2"}, auth(tok))
	if !g.fake.sent[1].Offline || g.fake.sent[1].NoNew {
		t.Fatalf("offline collect site: %+v", g.fake.sent[1])
	}
	if r := g.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "x"}, auth(tok)); r.Code != 400 {
		t.Fatalf("a message with no client id: %d", r.Code)
	}
	if r := g.do("POST", "/chat/v1/typing?site="+siteKey, `{}`, auth(tok)); r.Code != 204 || g.fake.typed != 1 {
		t.Fatalf("typing %d", r.Code)
	}
	if r := g.do("POST", "/chat/v1/end?site="+siteKey, `{}`, auth(tok)); r.Code != 204 || g.fake.ended != 1 {
		t.Fatalf("end %d", r.Code)
	}
	if r := g.do("GET", "/chat/v1/history?site="+siteKey+"&after=x", nil, auth(tok)); r.Code != 400 {
		t.Fatalf("history after=x: %d", r.Code)
	}
	if r := g.do("POST", "/chat/v1/typing?site="+siteKey, `{}`, nil); r.Code != 401 {
		t.Fatalf("no token: %d", r.Code)
	}
	if r := g.do("POST", "/chat/v1/typing?site=ffffffffffffffffffffffff", `{}`, auth(tok)); r.Code != 404 {
		t.Fatalf("unknown site: %d", r.Code)
	}
	h := newRig(t, func(s *store.ChatSite) { s.OfflineMode = "hidden" }, nil)
	ht := h.token()
	h.fake.online = false
	h.do("POST", "/chat/v1/messages?site="+siteKey, map[string]any{"text": "x", "clientMsgId": "h1"}, auth(ht))
	if !h.fake.sent[0].NoNew {
		t.Fatal("a hidden site must not start a conversation while offline")
	}
	d := newRig(t, func(s *store.ChatSite) { s.Enabled = false }, nil)
	if r := d.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil); r.Code != 404 {
		t.Fatalf("a disabled site answers like an unknown one: %d", r.Code)
	}
}

var _ = messaging.MaxChatChars
