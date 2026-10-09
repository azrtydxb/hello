package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/voice"
)

// voiceSource is a fake voice.Source for the handler tests.
type voiceSource struct {
	voice.Source

	mu       sync.Mutex
	rev      int64
	snap     voice.Snapshot
	state    voice.RuntimeState
	policy   voice.AgentPolicy
	saved    []voice.CallReport
	next     bool
	lockHeld bool
}

func newVoiceSource() *voiceSource {
	return &voiceSource{rev: 5, policy: voice.AgentPolicy{RecordTranscript: true}}
}

func (f *voiceSource) VoiceRevision(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev, nil
}

func (f *voiceSource) VoiceRuntimeAgents(context.Context) (voice.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := f.snap
	snap.Revision = f.rev // the snapshot always carries the current revision
	return snap, nil
}

func (f *voiceSource) VoiceRuntimeStatus(context.Context) (voice.RuntimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *voiceSource) SaveVoiceRuntimeAck(_ context.Context, _ int64, _ time.Time, a voice.Ack) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	at := time.Now()
	f.state = voice.RuntimeState{Revision: f.rev, AckRevision: a.Revision,
		LastSeen: &at, Version: a.Version, HasEnabledAgents: true,
	}
	b, _ := json.Marshal(a.Agents)
	f.state.Loaded = b
	return nil
}

func (f *voiceSource) VoiceAgentPolicy(_ context.Context, name string) (voice.AgentPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "support" {
		return voice.AgentPolicy{}, store.ErrNotFound
	}
	return f.policy, nil
}

func (f *voiceSource) SaveVoiceCallReport(_ context.Context, rep voice.CallReport, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, rep)
	return f.next, nil
}

func (f *voiceSource) TryVoiceLock(_ context.Context, _ string) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockHeld {
		return nil, false, nil
	}
	return func() {}, true, nil
}

func (f *voiceSource) VoicePrune(context.Context, time.Time, time.Duration) (int64, error) {
	return 0, nil
}

func (f *voiceSource) currentRev() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev
}

func (f *voiceSource) setSnapshot(snap voice.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

func (f *voiceSource) bump() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
}

// voiceStub is the API store the voice tests need: bearer credentials and
// the audit sink, nothing else.
type voiceStub struct {
	Store

	mu     sync.Mutex
	tokens map[string]auth.Actor
	audits []string
}

func newVoiceStub(tokens ...*tokenGrant) *voiceStub {
	s := &voiceStub{tokens: map[string]auth.Actor{}}
	for _, tg := range tokens {
		s.tokens[string(auth.HashToken(tg.plain))] = tg.actor
	}
	return s
}

func (s *voiceStub) TokenActor(_ context.Context, hash []byte) (auth.Actor, error) {
	if a, ok := s.tokens[string(hash)]; ok {
		return a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (s *voiceStub) SessionActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

func (s *voiceStub) Audit(_ context.Context, actor, action, resource, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, actor+" "+action+" "+resource+" "+id)
	return nil
}

// tokenGrant is one bearer credential the stub accepts.
type tokenGrant struct {
	plain string
	actor auth.Actor
}

func grant(plain string, scopes auth.Scopes, role auth.Role) *tokenGrant {
	return &tokenGrant{plain: plain, actor: auth.Actor{
		Kind: auth.KindPersonalToken, Scopes: scopes, Role: role,
		UserID: 7, Username: "talking-agent",
	}}
}

// runtimeEnv is one API with the voice runtime wired.
type runtimeEnv struct {
	t    *testing.T
	h    http.Handler
	src  *voiceSource
	stub *voiceStub
	svc  *voice.Service
}

func newRuntimeEnv(t *testing.T) *runtimeEnv {
	t.Helper()
	src := newVoiceSource()
	stub := newVoiceStub(
		grant("runtime-token", auth.Scopes{auth.ScopeVoiceRuntime}, auth.RoleViewer),
		grant("read-token", auth.Scopes{auth.ScopeRead}, auth.RoleViewer),
	)
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	svc := voice.NewRuntime(src, box, config.Voice{SIPAddress: "10.1.2.3:5060",
		Secret: strings.Repeat("test-key-", 4), Tenant: "lab", MaxCalls: 20}, nil, nil)
	h := Handler(Config{Store: stub, VoiceRuntime: svc})
	return &runtimeEnv{t: t, h: h, src: src, stub: stub, svc: svc}
}

// call does one request with an optional bearer token.
func (e *runtimeEnv) call(method, target, token, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestVoiceRuntimeAPI(t *testing.T) {
	t.Parallel()
	e := newRuntimeEnv(t)

	t.Run("scope", func(t *testing.T) {
		// The scope gate: no credentials 401, a read token 403, and a
		// voice-runtime token reads nothing else (spec S-18).
		if rec := e.call("GET", "/api/v1/voice-runtime/agents", "", ""); rec.Code != 401 {
			t.Fatalf("without credentials = %d, want 401", rec.Code)
		}
		if rec := e.call("GET", "/api/v1/voice-runtime/agents", "read-token", ""); rec.Code != 403 {
			t.Fatalf("read token = %d, want 403 insufficient_scope", rec.Code)
		}
		if rec := e.call("GET", "/api/v1/extensions", "runtime-token", ""); rec.Code != 403 {
			t.Fatalf("voice-runtime token on extensions = %d, want 403", rec.Code)
		}
	})

	t.Run("view", func(t *testing.T) {
		// The view: tenant, revision, the unsealed credential, ETag and
		// no-store (spec S-19).
		e.setAgent(t)
		rec := e.call("GET", "/api/v1/voice-runtime/agents", "runtime-token", "")
		if rec.Code != 200 {
			t.Fatalf("agents = %d %s", rec.Code, rec.Body)
		}
		var v voice.View
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.Tenant != "lab" || v.Revision != 5 || len(v.Agents) != 1 ||
			len(v.Agents[0].Servers) != 1 || v.Agents[0].Servers[0].Credential != "tok123" {
			t.Fatalf("view wrong: %s", rec.Body)
		}
		if got := rec.Header().Get("ETag"); got != `"5"` {
			t.Fatalf("ETag = %q, want %q", got, `"5"`)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
		if len(e.stub.audits) == 0 || !strings.Contains(e.stub.audits[0], "voice-runtime-read") {
			t.Fatalf("the fetch was not audited: %v", e.stub.audits)
		}
		if strings.Contains(rec.Body.String(), "Bearer") {
			t.Fatal("the view body carries a credential header value")
		}
	})

	t.Run("ifNoneMatch", func(t *testing.T) {
		e.setAgent(t)
		req := httptest.NewRequest("GET", "/api/v1/voice-runtime/agents", nil)
		req.Header.Set("Authorization", "Bearer runtime-token")
		req.Header.Set("If-None-Match", `"5"`)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 304 {
			t.Fatalf("If-None-Match = %d, want 304", rec.Code)
		}
	})

	t.Run("longpoll", func(t *testing.T) {
		// A long poll wakes on a revision change well inside 2 s (spec
		// S-19).
		go func() {
			time.Sleep(100 * time.Millisecond)
			e.src.bump()
		}()
		start := time.Now()
		rec := e.call("GET", "/api/v1/voice-runtime/agents?wait=25&revision=5", "runtime-token", "")
		if d := time.Since(start); rec.Code != 200 || d > 2*time.Second {
			t.Fatalf("long poll = %d after %s, want 200 inside 2 s", rec.Code, d)
		}

		// The wait runs out with no change: 304. The revision above moved
		// to 6, so the poll waits on the current one.
		start = time.Now()
		rec = e.call("GET", fmt.Sprintf("/api/v1/voice-runtime/agents?wait=0.3&revision=%d",
			e.src.currentRev()), "runtime-token", "")
		if d := time.Since(start); rec.Code != 304 || d < 250*time.Millisecond {
			t.Fatalf("expired wait = %d after %s, want 304 after the wait", rec.Code, d)
		}

		// A malformed wait is ignored: the document gives this operation
		// no 400, so the handler answers the current view at once.
		if rec := e.call("GET", "/api/v1/voice-runtime/agents?wait=soon", "runtime-token", ""); rec.Code != 200 {
			t.Fatalf("bad wait = %d, want 200", rec.Code)
		}
	})
}

// setAgent puts one enabled agent with a sealed credential in the fake
// store, at revision 5.
func (e *runtimeEnv) setAgent(t *testing.T) {
	t.Helper()
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("tok123", "voice_mcp:3:cred")
	if err != nil {
		t.Fatal(err)
	}
	e.src.setSnapshot(voice.Snapshot{Revision: 5, Agents: []voice.SealedAgent{{
		Name: "support", SIPUser: "va-3f2a9c01", Revision: 2,
		Prompt: "p", Greeting: "g", Language: "en-GB",
		MaxCallSeconds: 600, MaxConcurrent: 4, MaxToolCalls: 20, IdleTimeoutSeconds: 20,
		CallerVerification: "allowlist", CallerAllowlist: []string{"1001"},
		Servers: []voice.SealedServer{{ID: 3, Name: "crm", URL: "http://mcp/x",
			Auth: "bearer", Credential: sealed}},
	}}})
}

func TestVoiceRuntimeStatus(t *testing.T) {
	t.Parallel()
	e := newRuntimeEnv(t)

	// The status before any ack: green only when no agent is enabled
	// (spec S-22, S-30).
	e.src.state = voice.RuntimeState{Revision: 5, HasEnabledAgents: false}
	rec := e.call("GET", "/api/v1/voice/status", "read-token", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["healthy"] != true {
		t.Fatalf("status not green without agents: %s", rec.Body)
	}

	// An ack moves last seen, the version, the revision and the per-agent
	// state (spec S-20).
	if rec := e.call("POST", "/api/v1/voice-runtime/ack", "runtime-token",
		`{"revision":5,"version":"ta-1","agents":[{"name":"support","state":"loaded","revision":2}]}`); rec.Code != 204 {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body)
	}
	rec = e.call("GET", "/api/v1/voice/status", "read-token", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["healthy"] != true || st["version"] != "ta-1" {
		t.Fatalf("status after ack wrong: %s", rec.Body)
	}
	if agents, ok := st["agents"].([]any); !ok || len(agents) != 1 {
		t.Fatalf("status agents wrong: %s", rec.Body)
	}

	// The seventh ack in a minute is refused (spec S-20).
	for i := 0; i < 5; i++ { // the first ack of the minute already went above
		if rec := e.call("POST", "/api/v1/voice-runtime/ack", "runtime-token", `{"revision":5}`); rec.Code != 204 {
			t.Fatalf("ack %d = %d, want 204", i, rec.Code)
		}
	}
	if rec := e.call("POST", "/api/v1/voice-runtime/ack", "runtime-token", `{"revision":5}`); rec.Code != 403 {
		t.Fatalf("seventh ack = %d, want 403 rate_limited", rec.Code)
	}

	// Red after two minutes of silence while an enabled agent exists
	// (spec S-30) is Service.Status's decision, covered in internal/voice;
	// here the runtime singleton surfaces it.
}

func TestVoiceCallReport(t *testing.T) {
	t.Parallel()
	e := newRuntimeEnv(t)

	// A report is accepted before its CDR exists and is idempotent on its
	// correlation id (spec S-21): the fake store reports saved=false the
	// second time, as ON CONFLICT DO NOTHING would.
	e.src.next = true
	body := `{"correlationId":"c-1","agentName":"support","outcome":"answered",
		"summary":"reset the password","toolCalls":[{"tool":"crm.reset","ok":true}],
		"tokensIn":120,"tokensOut":45,"transcript":"caller said things"}`
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token", body); rec.Code != 204 {
		t.Fatalf("report = %d %s", rec.Code, rec.Body)
	}
	if len(e.src.saved) != 1 || e.src.saved[0].Transcript == "" {
		t.Fatalf("report stored wrong: %+v", e.src.saved)
	}
	e.src.next = false
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token", body); rec.Code != 204 {
		t.Fatalf("repeat report = %d, want 204", rec.Code)
	}
	if len(e.src.saved) != 2 {
		t.Fatalf("the repeat never reached the store")
	}

	// The transcript is dropped unless the agent records transcripts
	// (spec S-21, S-23).
	e.src.policy = voice.AgentPolicy{RecordTranscript: false}
	e.src.next = true
	body2 := `{"correlationId":"c-2","agentName":"support","outcome":"failed","transcript":"secret words"}`
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token", body2); rec.Code != 204 {
		t.Fatalf("report without transcript = %d %s", rec.Code, rec.Body)
	}
	if got := e.src.saved[len(e.src.saved)-1].Transcript; got != "" {
		t.Fatalf("transcript stored without record_transcript: %q", got)
	}
	e.src.policy = voice.AgentPolicy{RecordTranscript: true}

	// A report above 128 KiB is 413 (spec S-21).
	big := `{"correlationId":"c-3","agentName":"support","outcome":"answered","summary":"` +
		strings.Repeat("x", voice.MaxReportBody) + `"}`
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token", big); rec.Code != 413 {
		t.Fatalf("huge report = %d, want 413", rec.Code)
	}
	// A transcript above 64 KiB is 413 too.
	long := `{"correlationId":"c-4","agentName":"support","outcome":"answered","transcript":"` +
		strings.Repeat("x", voice.MaxTranscript+10) + `"}`
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token", long); rec.Code != 413 {
		t.Fatalf("long transcript = %d, want 413", rec.Code)
	}

	// An unknown agent and a bad outcome are refused (spec S-21).
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token",
		`{"correlationId":"c-5","agentName":"ghost","outcome":"answered"}`); rec.Code != 400 {
		t.Fatalf("unknown agent = %d, want 400", rec.Code)
	}
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token",
		`{"correlationId":"c-6","agentName":"support","outcome":"perfect"}`); rec.Code != 400 {
		t.Fatalf("bad outcome = %d, want 400", rec.Code)
	}
	// An unknown field is refused.
	if rec := e.call("POST", "/api/v1/voice-runtime/calls", "runtime-token",
		`{"correlationId":"c-7","agentName":"support","outcome":"answered","error":"x"}`); rec.Code != 400 {
		t.Fatalf("unknown field = %d, want 400", rec.Code)
	}
}
