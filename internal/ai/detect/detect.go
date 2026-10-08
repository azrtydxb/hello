// Package detect is the in-product AI agent's AIOps (spec ai-agent S-18 to
// S-21): deterministic detectors that read PostgreSQL and Valkey and return
// candidates, the findings lifecycle over them, and the `aiops` agent that
// asks the model only to explain and rank what the code found. No detector
// calls the model and the model never decides what is a problem.
package detect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/valkey-io/valkey-go"
)

// Severities of a candidate.
const (
	Info     = store.SeverityInfo
	Warning  = store.SeverityWarning
	Critical = store.SeverityCritical
)

// Candidate is one problem a detector found (spec S-18). Its ID is
// "<type>:<subject>" and is stable across runs, so a finding follows it.
type Candidate struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Subject  string         `json:"subject"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Evidence map[string]any `json:"evidence"`
}

// candidate builds a Candidate whose id is type:subject.
func candidate(typ, subject, severity, title string, evidence map[string]any) Candidate {
	if evidence == nil {
		evidence = map[string]any{}
	}
	return Candidate{ID: typ + ":" + subject, Type: typ, Subject: subject, Severity: severity, Title: title, Evidence: evidence}
}

// maxSamples is how many sample rows a candidate's evidence carries.
const maxSamples = 10

// ErrValkeyUnavailable is returned by the detectors that read Valkey when
// it cannot be reached; the run reports no candidates from them and says so.
var ErrValkeyUnavailable = errors.New("detect: valkey unavailable")

// Live is the shared live state the detectors read; *livestate.Store
// implements it.
type Live interface {
	AllBindings(ctx context.Context) ([]livestate.Binding, error)
	AllAuthFailures(ctx context.Context) ([]livestate.AuthFailure, error)
	TrunkStatus(ctx context.Context, id int64) (livestate.TrunkStatus, error)
}

// Members lists the cluster's nodes; *cluster.Store implements it.
type Members interface {
	Members(ctx context.Context) ([]cluster.Member, error)
}

// Env is what the detectors read.
type Env struct {
	DB    *sql.DB
	Store *store.Store
	Live  Live
	VK    valkey.Client
	// VKFunc yields the Valkey client once it has connected; it is used
	// while VK is nil, so detectors that need no Valkey run before it does.
	VKFunc  func() valkey.Client
	Cluster Members
	// AuthFailLimit is hello-sip's failed-auth throttle limit
	// (HELLO_SIP_AUTH_FAIL_LIMIT); 0 means its default, 10.
	AuthFailLimit int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	Log *slog.Logger
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Env) authFailLimit() int64 {
	if e.AuthFailLimit > 0 {
		return int64(e.AuthFailLimit)
	}
	return 10
}

// Detector is one detector: a name (its candidates' type, or the prefix of
// it) and a read-only pass.
type Detector struct {
	Name string
	Run  func(ctx context.Context, r *Run) ([]Candidate, error)
}

// Detectors are all the detectors, in the order they run.
func Detectors() []Detector {
	return []Detector{
		{"reg_failures", regFailures},
		{"auth_bruteforce", authBruteforce},
		{"trunk_down", trunkDown},
		{"trunk_asr_drop", trunkASRDrop},
		{"node_health", nodeHealth},
		{"trunk_capacity", trunkCapacity},
		{"call_quality", callQuality},
		{"config_smells", configSmells},
	}
}

// Run is one pass of the detectors at one instant; it caches the reads the
// detectors share.
type Run struct {
	*Env
	Now      time.Time
	attempts map[string][]livestate.RegisterAttempt
	attErr   error
	attRead  bool
	// observe, when set, receives each detector's duration.
	observe func(name string, d time.Duration)
}

// NewRun starts a pass at the environment's clock.
func NewRun(e *Env) *Run { return &Run{Env: e, Now: e.now()} }

// Result is what a pass found: candidates, and the detectors that failed
// with their errors (the other detectors still ran).
type Result struct {
	Candidates []Candidate
	Errors     map[string]error
}

// RunAll runs every detector. A detector's error is recorded under its name
// and never stops the others.
func RunAll(ctx context.Context, r *Run) Result {
	res := Result{Errors: map[string]error{}}
	for _, d := range Detectors() {
		start := time.Now()
		cs, err := d.Run(ctx, r)
		if r.observe != nil {
			r.observe(d.Name, time.Since(start))
		}
		if err != nil {
			res.Errors[d.Name] = fmt.Errorf("%s: %w", d.Name, err)
			continue
		}
		res.Candidates = append(res.Candidates, cs...)
	}
	return res
}

// Options configure the aiops agent.
type Options struct {
	// Interval is HELLO_AI_AIOPS_INTERVAL; 0 disables the agent.
	Interval time.Duration
	// ExplainMinInterval is HELLO_AI_EXPLAIN_MIN_INTERVAL: the model is asked
	// at most once per this interval.
	ExplainMinInterval time.Duration
	// Generate is the model call; nil leaves every finding unexplained.
	Generate Generate
	// Validator and Proposals check and store the proposals an explanation
	// carries; nil drops them.
	Validator proposal.Validator
	Proposals proposal.Store
	// Metrics receives detector durations and the findings gauge (spec
	// S-25); nil records none.
	Metrics *ai.Metrics
}

// AIOps is the `aiops` background agent (spec S-17, S-18): it runs every
// detector, stores the findings and, when the open set changed, asks the
// model to explain and rank them. It implements ai.Agent.
type AIOps struct {
	env *Env
	opt Options
	log *slog.Logger
}

// NewAIOps returns the agent; register it with the scheduler.
func NewAIOps(env *Env, opt Options) *AIOps {
	log := env.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AIOps{env: env, opt: opt, log: log}
}

// Name is the agent's name in the scheduler and its run rows.
func (a *AIOps) Name() string { return "aiops" }

// Interval is how often the agent runs.
func (a *AIOps) Interval() time.Duration { return a.opt.Interval }

// Run is one pass. A detector's failure is logged and leaves its findings
// as they were; only a failure to store, or every detector failing, fails
// the run.
func (a *AIOps) Run(ctx context.Context) (ai.Outcome, error) {
	r := NewRun(a.env)
	if m := a.opt.Metrics; m != nil {
		r.observe = m.Detector
	}
	before, err := a.env.Store.ListAIFindings(ctx, store.AIFindingFilter{Status: store.FindingOpen, Limit: 200})
	if err != nil {
		return ai.OutcomeFailed, err
	}
	res := RunAll(ctx, r)
	for name, err := range res.Errors {
		a.log.Warn("detector failed", "detector", name, "valkey_unavailable", errors.Is(err, ErrValkeyUnavailable), "error", err)
	}
	if len(res.Errors) == len(Detectors()) {
		return ai.OutcomeFailed, fmt.Errorf("every detector failed: %w", errors.Join(mapValues(res.Errors)...))
	}
	if err := persist(ctx, r, res); err != nil {
		return ai.OutcomeFailed, fmt.Errorf("store findings: %w", err)
	}
	after, err := a.env.Store.ListAIFindings(ctx, store.AIFindingFilter{Status: store.FindingOpen, Limit: 200})
	if err != nil {
		return ai.OutcomeFailed, err
	}
	a.setFindingsGauge(ctx, after)
	called, err := a.explain(ctx, r)
	switch {
	case ai.CodeOf(err) == ai.CodeBudgetExhausted:
		return ai.OutcomeSkippedBudget, nil
	case err != nil:
		return ai.OutcomeFailed, fmt.Errorf("explain findings: %w", err)
	case called || setFingerprint(before) != setFingerprint(after):
		return ai.OutcomeOK, nil
	}
	return ai.OutcomeNoChange, nil
}

func mapValues(m map[string]error) []error {
	out := make([]error, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// identity is who a finding's proposal reads as: the oldest admin (spec S-18
// leaves it to the detectors; a viewer-scoped replay is read-only whoever it
// is, and the reads are audited as the assistant).
func (a *AIOps) identity(ctx context.Context) (proposal.Identity, error) {
	id, err := a.env.Store.AIReadIdentity(ctx)
	if err != nil {
		return proposal.Identity{}, err
	}
	return proposal.Identity{UserID: id, TaskID: "aiops"}, nil
}

// setFindingsGauge refreshes hello_ai_findings from the open findings just
// read and the acknowledged ones.
func (a *AIOps) setFindingsGauge(ctx context.Context, open []store.AIFinding) {
	m := a.opt.Metrics
	if m == nil {
		return
	}
	acked, err := a.env.Store.ListAIFindings(ctx, store.AIFindingFilter{Status: store.FindingAcknowledged, Limit: 200})
	if err != nil {
		a.log.Warn("findings gauge", "error", err)
		return
	}
	n := map[[3]string]int{}
	for _, f := range append(open, acked...) {
		n[[3]string{f.Type, f.Severity, f.Status}]++
	}
	m.SetFindings(n)
}
