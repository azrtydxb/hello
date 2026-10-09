package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/prometheus/client_golang/prometheus"
)

// Source is the persistence the runtime needs (spec voice-agents S-19 to
// S-23). *store.Store implements it: these are the runtime rows of
// internal/store/voice.go, the store half Task 2 owns. Everything but the
// view's unsealed credentials is ordinary registry data.
type Source interface {
	// VoiceRevision returns the global voice revision, the view's ETag.
	VoiceRevision(ctx context.Context) (int64, error)
	// VoiceRuntimeAgents returns the revision and every enabled agent with
	// its persona, limits, caller verification and attachments, credentials
	// still sealed. Read in one snapshot so the ETag matches the agents.
	VoiceRuntimeAgents(ctx context.Context) (Snapshot, error)
	// VoiceRuntimeStatus returns the runtime singleton (what the last ack
	// recorded) plus the current revision and whether an enabled agent
	// exists, which decides whether silence turns the status red (S-30).
	VoiceRuntimeStatus(ctx context.Context) (RuntimeState, error)
	// SaveVoiceRuntimeAck records the runtime's last ack, replacing the
	// previous one (S-20).
	SaveVoiceRuntimeAck(ctx context.Context, accountID int64, at time.Time, ack Ack) error
	// VoiceAgentPolicy reports how the agent reports calls; ErrNotFound
	// (store.ErrNotFound) for an unknown name.
	VoiceAgentPolicy(ctx context.Context, name string) (AgentPolicy, error)
	// SaveVoiceCallReport stores a report idempotently on its correlation
	// id and reports whether it was new (S-21). It attaches to the CDR by
	// join, so a report may arrive before hello-sip writes the CDR.
	SaveVoiceCallReport(ctx context.Context, rep CallReport, at time.Time) (saved bool, err error)
	// TryVoiceLock takes pg_try_advisory_lock(hashtext(key)) on a
	// connection of its own, held until unlock; ok is false when another
	// replica holds it.
	TryVoiceLock(ctx context.Context, key string) (unlock func(), ok bool, err error)
	// VoicePrune clears transcripts past their agent's retention and
	// deletes reports past cdrRetention (S-23); it returns the rows touched.
	VoicePrune(ctx context.Context, now time.Time, cdrRetention time.Duration) (int64, error)
}

// Snapshot is one consistent read of the runtime view's inputs.
type Snapshot struct {
	Revision int64
	Agents   []SealedAgent
}

// SealedAgent is one enabled agent as the store hands it over: like
// RuntimeAgent but with the MCP credentials still sealed, so the plain
// values exist only after View unsealed them.
type SealedAgent struct {
	Name    string `json:"name"`
	SIPUser string `json:"sipUser"`

	Prompt         string   `json:"prompt"`
	Greeting       string   `json:"greeting"`
	Language       string   `json:"language"`
	Voice          string   `json:"voice"`
	VoiceReference string   `json:"voiceReference"`
	Style          string   `json:"style"`
	Temperature    *float64 `json:"temperature"`

	MaxCallSeconds     int `json:"maxCallSeconds"`
	MaxConcurrent      int `json:"maxConcurrent"`
	MaxToolCalls       int `json:"maxToolCalls"`
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`

	// Revision is the agent's own persona revision.
	Revision int64 `json:"revision"`

	RecordTranscript bool `json:"recordTranscript"`

	CallerVerification string   `json:"callerVerification"`
	CallerAllowlist    []string `json:"callerAllowlist"`
	PINHash            string   `json:"pinHash"`

	Servers []SealedServer `json:"servers"`
}

// SealedServer is one attached MCP server, its credential still sealed with
// additional data "voice_mcp:<id>:cred" (spec S-5).
type SealedServer struct {
	ID         int64        `json:"id"`
	Name       string       `json:"name"`
	URL        string       `json:"url"`
	Auth       string       `json:"auth"`
	TimeoutMS  int          `json:"timeoutMs"`
	HeaderName string       `json:"headerName,omitempty"`
	Credential []byte       `json:"-"`
	TokenURL   string       `json:"tokenUrl,omitempty"`
	ClientID   string       `json:"clientId,omitempty"`
	Scope      string       `json:"scope,omitempty"`
	Audience   string       `json:"audience,omitempty"`
	Tools      []ToolAccess `json:"tools"`
}

// AgentPolicy is what the runtime needs to know about an agent to file its
// report (spec S-21, S-23).
type AgentPolicy struct {
	RecordTranscript bool
}

// RuntimeState is the runtime singleton plus what decides its colour.
type RuntimeState struct {
	// Revision is the current voice revision; AckRevision is the one the
	// runtime last acked, so Revision-AckRevision is the lag.
	Revision, AckRevision int64
	LastSeen              *time.Time
	Version               string
	// Loaded is what the last ack recorded, as its agents array.
	Loaded json.RawMessage
	// HasEnabledAgents says whether anything is expected of the runtime.
	HasEnabledAgents bool
}

// Ack states, in the ack body and the status response.
const (
	StateLoaded  = "loaded"
	StateLoading = "loading"
	StateFailed  = "failed"
)

// Ack is a runtime load acknowledgement (spec S-20): the revision
// talking-agent loaded, its version, and per agent whether it loaded.
type Ack struct {
	Revision int64        `json:"revision"`
	Version  string       `json:"version"`
	Agents   []AgentState `json:"agents"`
}

// AgentState is one agent's state in an ack and in the status.
type AgentState struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Revision int64  `json:"revision,omitempty"`
}

// RedAfter is how long the runtime may stay silent, while an enabled agent
// exists, before the status turns red (spec S-22, S-30).
const RedAfter = 2 * time.Minute

// maxAckAgents bounds an ack body; the agent limit is far lower.
const maxAckAgents = 1000

// Errors Service reports; match with errors.Is.
var (
	// ErrRateLimited is past the ack rate limit (6 per minute, S-20).
	ErrRateLimited = errors.New("voice: too many acks")
	// ErrUnknownAgent is a report naming an agent that does not exist.
	ErrUnknownAgent = errors.New("voice: unknown agent")
)

// Service is the voice runtime: it builds the view talking-agent reads,
// takes its acks, files its call reports and prunes the ones past
// retention. Safe for concurrent use.
type Service struct {
	src     Source
	box     *secret.Box
	cfg     config.Voice
	log     *slog.Logger
	now     func() time.Time
	metrics *Metrics
	poll    time.Duration
	retain  time.Duration
	acks    *limiter
}

// Option customises a Service; the zero value is the production default.
type Option func(*Service)

// WithPoll sets how often a long poll re-reads the revision; the default,
// 500 ms, reaches talking-agent well inside the 2 s criterion (S-19).
func WithPoll(d time.Duration) Option {
	return func(s *Service) { s.poll = d }
}

// WithRetention sets how long call reports (their summaries) are kept. It
// follows the CDR retention (S-23); Hello does not prune CDRs yet, so the
// default is 30 days.
func WithRetention(d time.Duration) Option {
	return func(s *Service) { s.retain = d }
}

// WithNow sets the clock; tests use it to travel.
func WithNow(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewRuntime builds the runtime service. box unseals MCP credentials for the view
// and must be the box they were sealed with; reg may be nil.
func NewRuntime(src Source, box *secret.Box, cfg config.Voice, log *slog.Logger, reg prometheus.Registerer, opts ...Option) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		src:     src,
		box:     box,
		cfg:     cfg,
		log:     log,
		now:     time.Now,
		metrics: NewMetrics(reg),
		poll:    500 * time.Millisecond,
		retain:  30 * 24 * time.Hour,
		acks:    newLimiter(6, time.Minute),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// View builds the runtime view (spec S-19): the one place an MCP credential
// is unsealed. It is never cached and never logged.
func (s *Service) View(ctx context.Context) (View, error) {
	snap, err := s.src.VoiceRuntimeAgents(ctx)
	if err != nil {
		return View{}, err
	}
	agents := make([]RuntimeAgent, 0, len(snap.Agents))
	for _, a := range snap.Agents {
		servers, err := s.unseal(a)
		if err != nil {
			return View{}, err
		}
		agents = append(agents, RuntimeAgent{
			Name: a.Name, SIPUser: a.SIPUser, Revision: a.Revision,
			Prompt: a.Prompt, Greeting: a.Greeting, Language: a.Language,
			Voice: a.Voice, VoiceReference: a.VoiceReference, Style: a.Style,
			Temperature:    a.Temperature,
			MaxCallSeconds: a.MaxCallSeconds, MaxConcurrent: a.MaxConcurrent,
			MaxToolCalls: a.MaxToolCalls, IdleTimeoutSeconds: a.IdleTimeoutSeconds,
			RecordTranscript:   a.RecordTranscript,
			CallerVerification: a.CallerVerification, CallerAllowlist: a.CallerAllowlist, PINHash: a.PINHash,
			Servers: servers,
		})
	}
	return View{Tenant: s.cfg.Tenant, Revision: snap.Revision, Agents: agents}, nil
}

// unseal opens one agent's server credentials. A value that does not open
// is a wrong key or tampering: fail closed rather than serve half a view.
func (s *Service) unseal(a SealedAgent) ([]RuntimeServer, error) {
	servers := make([]RuntimeServer, 0, len(a.Servers))
	for _, srv := range a.Servers {
		out := RuntimeServer{
			Name: srv.Name, URL: srv.URL, Auth: srv.Auth, TimeoutMS: srv.TimeoutMS,
			HeaderName: srv.HeaderName, TokenURL: srv.TokenURL, ClientID: srv.ClientID,
			Scope: srv.Scope, Audience: srv.Audience, Tools: srv.Tools,
		}
		if len(srv.Credential) > 0 {
			pt, err := s.box.Open(srv.Credential, fmt.Sprintf("voice_mcp:%d:cred", srv.ID))
			if err != nil {
				return nil, fmt.Errorf("voice: cannot unseal the credential of MCP server %q: %w", srv.Name, err)
			}
			out.Credential = pt
		}
		servers = append(servers, out)
	}
	return servers, nil
}

// Revision returns the current voice revision.
func (s *Service) Revision(ctx context.Context) (int64, error) { return s.src.VoiceRevision(ctx) }

// Await long-polls (spec S-19): it returns as soon as the revision moved
// from revision, or after wait, reporting the revision it saw either way.
// The client's ctx may end it sooner.
func (s *Service) Await(ctx context.Context, revision int64, wait time.Duration) (int64, error) {
	cur, err := s.src.VoiceRevision(ctx)
	if err != nil || cur != revision || wait <= 0 {
		return cur, err
	}
	deadline := s.now().Add(wait)
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return cur, nil
		case <-tick.C:
		}
		if cur, err = s.src.VoiceRevision(ctx); err != nil || cur != revision {
			return cur, err
		}
		if !s.now().Before(deadline) {
			return cur, nil
		}
	}
}

// Ack records a runtime load (spec S-20). It is rate limited per service
// account and replaces the previous ack; nothing else in Hello reads it but
// the status. Validation runs before the rate limit, so a client that
// cannot count does not burn its window on refusals.
func (s *Service) Ack(ctx context.Context, accountID int64, a Ack) error {
	if a.Revision < 0 {
		return fmt.Errorf("voice: ack revision %d is negative", a.Revision)
	}
	if len(a.Agents) > maxAckAgents {
		return fmt.Errorf("voice: ack names %d agents, at most %d", len(a.Agents), maxAckAgents)
	}
	for _, ag := range a.Agents {
		if ag.Name == "" || len(ag.Name) > 64 {
			return fmt.Errorf("voice: ack agent name %q", ag.Name)
		}
		switch ag.State {
		case StateLoaded, StateLoading, StateFailed:
		default:
			return fmt.Errorf("voice: ack state %q is not loaded, loading or failed", ag.State)
		}
	}
	if !s.acks.allow(accountID, s.now()) {
		return ErrRateLimited
	}
	if err := s.src.SaveVoiceRuntimeAck(ctx, accountID, s.now(), a); err != nil {
		return err
	}
	s.metrics.lastSeen(0)
	return nil
}

// RuntimeStatus is what GET /voice/status shows (spec S-20, S-22, S-30).
type RuntimeStatus struct {
	Revision   int64        `json:"revision"`
	LastSeenAt *time.Time   `json:"lastSeenAt"`
	Version    string       `json:"version"`
	Healthy    bool         `json:"healthy"`
	Lag        int64        `json:"-"`
	Agents     []AgentState `json:"agents"`
}

// Status reports when the runtime was last seen, the revision it runs, the
// lag, and per agent what it last loaded. It is red (Healthy false) once
// the runtime has been silent for RedAfter while an enabled agent exists;
// Hello itself never depends on it (S-22).
func (s *Service) Status(ctx context.Context) (RuntimeStatus, error) {
	st, err := s.src.VoiceRuntimeStatus(ctx)
	if err != nil {
		return RuntimeStatus{}, err
	}
	var agents []AgentState
	if len(st.Loaded) > 0 {
		if err := json.Unmarshal(st.Loaded, &agents); err != nil {
			return RuntimeStatus{}, fmt.Errorf("voice: stored ack: %w", err)
		}
	}
	out := RuntimeStatus{
		Revision: st.Revision, LastSeenAt: st.LastSeen, Version: st.Version,
		Lag:     max(st.Revision-st.AckRevision, 0),
		Healthy: true,
	}
	if st.HasEnabledAgents && (st.LastSeen == nil || s.now().Sub(*st.LastSeen) > RedAfter) {
		out.Healthy = false
	}
	if agents == nil {
		agents = []AgentState{}
	}
	if agents == nil {
		agents = []AgentState{}
	}
	out.Agents = agents
	s.metrics.lastSeenSeconds(st.LastSeen, s.now())
	s.metrics.revisionLag(float64(out.Lag))
	return out, nil
}

// limiter is a per-key sliding-window rate limit, in memory: the ack rate
// limit guards one talking-agent's polling loop, not a distributed quota,
// and a restart resets it without harm (spec S-20).
type limiter struct {
	mu     sync.Mutex
	seen   map[int64][]time.Time
	limit  int
	window time.Duration
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{seen: map[int64][]time.Time{}, limit: limit, window: window}
}

func (l *limiter) allow(key int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.seen[key]
	fresh := hits[:0]
	for _, at := range hits {
		if now.Sub(at) <= l.window {
			fresh = append(fresh, at)
		}
	}
	if len(fresh) >= l.limit {
		l.seen[key] = fresh
		return false
	}
	l.seen[key] = append(fresh, now)
	return true
}
