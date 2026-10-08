package ai

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

// Reason the service is off although configured: the endpoint resolves to a
// public address without HELLO_AI_ALLOW_PUBLIC_ENDPOINT (spec S-3).
const ReasonEndpointNotPrivate = CodeEndpointNotPublic

// Service is one replica's bounded AI service (spec S-16): the model, the
// limits, the budget, interactive tasks, the scheduler and pruning. A
// Service that is off (Enabled false) starts nothing and opens no
// connection; Generate and Tasks.Start fail with ai_disabled.
type Service struct {
	cfg     config.AIAgent
	st      Store
	log     *slog.Logger
	model   provider.LanguageModel
	replica string
	now     func() time.Time
	started time.Time

	enabled bool
	reason  string
	host    string
	private bool

	vk atomic.Pointer[valkey.Client]

	limits  *limits
	metrics *Metrics
	errs    recentErrors

	// Tasks runs interactive work (spec S-15); Scheduler the background
	// agents (S-17).
	Tasks     *Tasks
	Scheduler *Scheduler

	// base is the lifetime of task goroutines: cancelled when Run returns.
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	timing timing
}

// timing is how often the background loops run; tests shorten it.
type timing struct {
	heartbeat, stale, tick, prune time.Duration
}

var defaultTiming = timing{heartbeat: 5 * time.Second, stale: 15 * time.Second, tick: 5 * time.Second, prune: time.Hour}

// Options are what New does not take from the configuration. The zero value
// is production.
type Options struct {
	// Replica names this hello-control instance in tasks, runs and the
	// heartbeat key; empty means the host name.
	Replica string
	// Model replaces the provider's model; only tests set it (with
	// go-ai-sdk's aitest mock), so the privacy check is skipped for it.
	Model provider.LanguageModel
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	resolver resolver
	dial     dialFunc
	timing   timing
}

// New returns the AI service for cfg (spec S-1–S-3). It is off, with the
// reason Status reports, when cfg is not configured or the endpoint is not
// private; an unknown provider is an error. vk may be nil until Valkey
// connects (SetValkey).
func New(cfg config.AIAgent, st Store, vk valkey.Client, reg prometheus.Registerer, log *slog.Logger) (*Service, error) {
	return NewWith(cfg, st, vk, reg, log, Options{})
}

// NewWith is New with options.
func NewWith(cfg config.AIAgent, st Store, vk valkey.Client, reg prometheus.Registerer, log *slog.Logger, o Options) (*Service, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Replica == "" {
		o.Replica, _ = os.Hostname()
	}
	if o.resolver == nil {
		o.resolver = systemResolver
	}
	if o.dial == nil {
		o.dial = netDial
	}
	if o.timing == (timing{}) {
		o.timing = defaultTiming
	}
	s := &Service{cfg: cfg, st: st, log: log, replica: o.Replica, now: o.Now, started: o.Now(), timing: o.timing}
	s.base, s.cancel = context.WithCancel(context.Background())
	if vk != nil {
		s.vk.Store(&vk)
	}
	s.metrics = newMetrics(reg)
	s.limits = newLimits(cfg, o.Now)
	s.Tasks = &Tasks{s: s}
	s.Scheduler = &Scheduler{s: s, agents: map[string]Agent{}}

	s.enabled, s.reason = cfg.Enabled()
	if hp, host, err := endpointHost(cfg.BaseURL); err == nil {
		s.host = hp
		if s.enabled && o.Model == nil {
			var resolved bool
			s.private, resolved = o.resolver.checkAtStart(context.Background(), host)
			if resolved && !s.private && !cfg.AllowPublic {
				s.enabled, s.reason = false, ReasonEndpointNotPrivate
			}
		}
	} else if s.enabled {
		return nil, err
	}
	if o.Model != nil {
		s.private = true
	}
	if s.enabled {
		s.model = o.Model
		if s.model == nil {
			m, err := newModel(cfg, privacyDialer(o.resolver, o.dial, cfg.AllowPublic))
			if err != nil {
				return nil, err
			}
			s.model = m
		}
	}
	s.metrics.enabled.Set(b2f(s.enabled))
	return s, nil
}

// Enabled reports whether AI runs and, when not, why: not_configured,
// incomplete_configuration or endpoint_not_private.
func (s *Service) Enabled() (bool, string) { return s.enabled, s.reason }

// Replica is this instance's name.
func (s *Service) Replica() string { return s.replica }

// Metrics is the S-25 metric set, for the other AI packages.
func (s *Service) Metrics() *Metrics { return s.metrics }

// SetValkey installs the Valkey client once it connects; heartbeats and
// the stale-task check wait for it.
func (s *Service) SetValkey(c valkey.Client) {
	if c != nil {
		s.vk.Store(&c)
	}
}

func (s *Service) valkey() valkey.Client {
	if p := s.vk.Load(); p != nil {
		return *p
	}
	return nil
}

// Run starts the heartbeat, the stale-task check, the scheduler and
// pruning, and blocks until ctx ends; then it fails this replica's
// unfinished tasks with instance_stopped. It returns at once when AI is off.
func (s *Service) Run(ctx context.Context) {
	if !s.enabled {
		return
	}
	var wg sync.WaitGroup
	for _, loop := range []func(context.Context){s.heartbeatLoop, s.Scheduler.loop, s.pruneLoop} {
		wg.Add(1)
		go func() { defer wg.Done(); loop(ctx) }()
	}
	<-ctx.Done()
	wg.Wait()
	s.cancel()
	s.wg.Wait()
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if n, err := s.st.FailAITasks(stop, s.replica, CodeInstanceStopped, "the instance stopped", s.now()); err != nil {
		s.log.Warn("ai: fail own tasks at stop", "error", err)
	} else if n > 0 {
		s.log.Info("ai: failed own tasks at stop", "tasks", n)
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
