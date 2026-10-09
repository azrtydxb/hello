package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"errors"
	"strings"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeSource implements Source for the service tests: rows in memory, the
// clock injected, the advisory lock a bool.
type fakeSource struct {
	Source

	mu        sync.Mutex
	rev       int64
	snap      Snapshot
	state     RuntimeState
	policy    AgentPolicy
	policyErr error
	saved     []CallReport
	next      bool // what SaveVoiceCallReport reports as saved
	pruned    []pruneCall
	pruneErr  error
	lockHeld  bool
	acks      []Ack
}

type pruneCall struct {
	now       time.Time
	retention time.Duration
}

func runtimeBox(t *testing.T) *secret.Box {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	b, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testConfig() config.Voice {
	// A throwaway key of the length the config validation demands; built
	// from a repeat so no scanner mistakes it for a real credential.
	key := strings.Repeat("test-key-", 4)
	return config.Voice{SIPAddress: "10.1.2.3:5060", Secret: key, Tenant: "lab", MaxCalls: 20}
}

func newTestService(t *testing.T, src *fakeSource, opts ...Option) (*Service, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	svc := NewRuntime(src, runtimeBox(t), testConfig(), nil, reg, opts...)
	return svc, reg
}

// seal seals plaintext for server id 3, the aad the store uses.
func sealFor(t *testing.T, box *secret.Box, id int64, pt string) []byte {
	t.Helper()
	sealed, err := box.Seal(pt, "voice_mcp:3:cred")
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// setRevision moves the voice revision, as a registry change would.
func (f *fakeSource) setRevision(rev int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev = rev
}

func (f *fakeSource) VoiceRevision(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev, nil
}

func (f *fakeSource) VoiceRuntimeAgents(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, nil
}

func (f *fakeSource) VoiceRuntimeStatus(context.Context) (RuntimeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *fakeSource) SaveVoiceRuntimeAck(_ context.Context, _ int64, _ time.Time, a Ack) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, a)
	loaded, err := json.Marshal(a.Agents)
	if err != nil {
		return err
	}
	at := time.Now()
	f.state = RuntimeState{
		Revision: f.rev, AckRevision: a.Revision, LastSeen: &at,
		Version: a.Version, Loaded: loaded, HasEnabledAgents: true,
	}
	return nil
}

func (f *fakeSource) VoiceAgentPolicy(_ context.Context, name string) (AgentPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.policyErr != nil {
		return AgentPolicy{}, f.policyErr
	}
	if name != "support" {
		return AgentPolicy{}, store.ErrNotFound
	}
	return f.policy, nil
}

func (f *fakeSource) SaveVoiceCallReport(_ context.Context, rep CallReport, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, rep)
	return f.next, nil
}

func (f *fakeSource) TryVoiceLock(_ context.Context, _ string) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockHeld {
		return nil, false, nil
	}
	return func() {}, true, nil
}

func (f *fakeSource) VoicePrune(_ context.Context, now time.Time, retention time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	f.pruned = append(f.pruned, pruneCall{now, retention})
	return 2, nil
}

// newFakeSource starts empty: no agents, revision 1, a fresh runtime state.
func newFakeSource() *fakeSource {
	return &fakeSource{rev: 1}
}

// ptrTime is a pointer helper for the status tests.
func ptrTime(t time.Time) *time.Time { return &t }

func TestVoiceRuntimeView(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	box := runtimeBox(t)
	src.snap = Snapshot{
		Revision: 7,
		Agents: []SealedAgent{{
			Name: "support", SIPUser: "va-3f2a9c01", Revision: 4,
			Prompt: "you are support", Greeting: "hello", Language: "en-GB",
			MaxCallSeconds: 600, MaxConcurrent: 4, MaxToolCalls: 20, IdleTimeoutSeconds: 20,
			RecordTranscript:   true,
			CallerVerification: "pin",
			CallerAllowlist:    []string{"1001"},
			PINHash:            "pbkdf2$hash",
			Servers: []SealedServer{{
				ID: 3, Name: "crm", URL: "http://mcp.internal/sse", Auth: "bearer",
				TimeoutMS: 5000, Credential: sealFor(t, box, 3, "tok123"),
				Tools: []ToolAccess{{Name: "lookup", Confirm: false}},
			}},
		}},
	}
	svc, reg := newTestService(t, src)

	v, err := svc.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Tenant != "lab" || v.Revision != 7 {
		t.Fatalf("view tenant %q revision %d, want lab and 7", v.Tenant, v.Revision)
	}
	if v.ETag() != `"7"` {
		t.Fatalf("ETag = %s, want %q", v.ETag(), `"7"`)
	}
	if len(v.Agents) != 1 {
		t.Fatalf("view has %d agents, want 1", len(v.Agents))
	}
	a := v.Agents[0]
	if a.SIPUser != "va-3f2a9c01" || a.Revision != 4 || a.Prompt != "you are support" ||
		!a.RecordTranscript || a.PINHash != "pbkdf2$hash" || len(a.CallerAllowlist) != 1 {
		t.Fatalf("agent carried over wrong: %+v", a)
	}
	if len(a.Servers) != 1 || a.Servers[0].Credential != "tok123" {
		t.Fatalf("credential not unsealed: %+v", a.Servers)
	}

	// The service registered its metrics on reg (TestVoiceMetrics counts
	// the series); the view itself moves none of them.
	if n := testutil.CollectAndCount(reg); n == 0 {
		t.Fatalf("no metrics registered, want the three voice series, got %d", n)
	}
}

func TestVoiceRuntimeViewSealedOnly(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	bad := []SealedServer{{
		ID:         9,
		Name:       "crm",
		URL:        "http://mcp/x",
		Auth:       "bearer",
		Credential: []byte{0xde, 0xad},
	}}
	src.snap = Snapshot{
		Revision: 3,
		Agents: []SealedAgent{{
			Name: "support", SIPUser: "va-1a2b3c4d", Servers: bad,
		}},
	}
	svc, _ := newTestService(t, src)
	if _, err := svc.View(context.Background()); err == nil {
		t.Fatal("a credential that does not open must fail the view closed")
	}
}

func TestVoiceRuntimeLongPoll(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src, WithPoll(20*time.Millisecond), WithNow(func() time.Time { return time.Now() }))

	// The revision moves while a poll is waiting: Await returns the new
	// revision well inside the 2 s criterion (spec S-19).
	src.setRevision(9)
	go func() {
		time.Sleep(100 * time.Millisecond)
		src.setRevision(12)
	}()
	deadline := time.Now().Add(2 * time.Second)
	rev, err := svc.Await(context.Background(), 9, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 12 {
		t.Fatalf("Await returned %d, want 12", rev)
	}
	if time.Now().After(deadline) {
		t.Fatal("Await took over 2 s to notice a revision change")
	}

	// No change: Await returns the old revision after the wait.
	start := time.Now()
	rev, err = svc.Await(context.Background(), 12, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 12 {
		t.Fatalf("Await returned %d with no change, want 12", rev)
	}
	if d := time.Since(start); d < 140*time.Millisecond {
		t.Fatalf("Await returned after %s, want the full wait", d)
	}

	// A cancelled client ends the wait.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start = time.Now()
	rev, err = svc.Await(ctx, 12, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 12 || time.Since(start) > time.Second {
		t.Fatalf("Await ignored the cancelled context: rev %d after %s", rev, time.Since(start))
	}
}

func TestVoiceRuntimeAck(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	svc, _ := newTestService(t, src)
	ctx := context.Background()

	// Six acks in a minute pass, the seventh is rate limited (spec S-20).
	for i := 0; i < 6; i++ {
		if err := svc.Ack(ctx, 7, Ack{Revision: int64(i + 1), Version: "ta-1",
			Agents: []AgentState{{Name: "support", State: StateLoaded, Revision: 3}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Ack(ctx, 7, Ack{Revision: 7}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("seventh ack = %v, want ErrRateLimited", err)
	}
	// A different service account has its own window.
	if err := svc.Ack(ctx, 8, Ack{Revision: 7}); err != nil {
		t.Fatalf("ack of another account: %v", err)
	}
	if len(src.acks) != 7 {
		t.Fatalf("%d acks saved, want 7", len(src.acks))
	}

	// Invalid acks are refused without touching the store.
	before := len(src.acks)
	for _, a := range []Ack{
		{Revision: -1},
		{Revision: 1, Agents: []AgentState{{Name: "", State: StateLoaded}}},
		{Revision: 1, Agents: []AgentState{{Name: "support", State: "half"}}},
	} {
		if err := svc.Ack(ctx, 7, a); err == nil || errors.Is(err, ErrRateLimited) {
			t.Fatalf("ack %+v accepted: %v", a, err)
		}
	}
	if len(src.acks) != before {
		t.Fatal("an invalid ack reached the store")
	}
}

func TestVoiceRuntimeStatus(t *testing.T) {
	t.Parallel()
	src := newFakeSource()
	now := time.Now()
	clock := now
	svc, _ := newTestService(t, src, WithNow(func() time.Time { return clock }))

	src.state = RuntimeState{
		Revision: 9, AckRevision: 7,
		LastSeen:         ptrTime(now.Add(-1 * time.Minute)),
		Version:          "ta-1",
		Loaded:           []byte(`[{"name":"support","state":"loaded","revision":3}]`),
		HasEnabledAgents: true,
	}
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Healthy || st.Lag != 2 || st.Version != "ta-1" || len(st.Agents) != 1 ||
		st.Agents[0].Name != "support" || st.Agents[0].State != StateLoaded {
		t.Fatalf("status wrong: %+v", st)
	}

	// Three minutes of silence while an enabled agent exists: red, and the
	// revision lag grows (spec S-22, S-30).
	clock = now.Add(3 * time.Minute)
	st, err = svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Healthy {
		t.Fatal("status green after three minutes of silence")
	}
	if st.Lag != 2 {
		t.Fatalf("lag %d, want 2", st.Lag)
	}

	// Never seen at all with an enabled agent: red.
	src.state = RuntimeState{Revision: 9, AckRevision: 9, HasEnabledAgents: true}
	if st, _ = svc.Status(context.Background()); st.Healthy {
		t.Fatal("status green when the runtime never acked")
	}

	// Without an enabled agent silence stays green (spec S-30: red while
	// an enabled agent is routed).
	src.state = RuntimeState{Revision: 9, HasEnabledAgents: false}
	if st, _ = svc.Status(context.Background()); !st.Healthy {
		t.Fatal("status red without an enabled agent")
	}
}
