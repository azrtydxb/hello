package ai_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countAgent counts its runs and the most that ever overlapped.
type countAgent struct {
	name     string
	interval time.Duration
	hold     time.Duration
	runs     atomic.Int32
	inside   atomic.Int32
	overlap  atomic.Int32
	run      func(ctx context.Context) (ai.Outcome, error)
}

func (a *countAgent) Name() string            { return a.name }
func (a *countAgent) Interval() time.Duration { return a.interval }
func (a *countAgent) Run(ctx context.Context) (ai.Outcome, error) {
	a.runs.Add(1)
	if n := a.inside.Add(1); n > 1 {
		a.overlap.Store(n)
	}
	defer a.inside.Add(-1)
	time.Sleep(a.hold)
	if a.run != nil {
		return a.run(ctx)
	}
	return ai.OutcomeNoChange, nil
}

// schedulerCase runs the scheduler story against st (in memory or
// PostgreSQL): never-run start delay, interval, restart, run-now, no
// overlap across two replicas, outcomes and tokens recorded.
func schedulerCase(t *testing.T, st ai.Store, runs func() []ai.AgentRun) {
	ctx := context.Background()
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	cfg := aifake.Config()
	mk := func(replica string, m provider.LanguageModel) *ai.Service {
		t.Helper()
		if m == nil {
			m = &aitest.MockModel{Caps: provider.Capabilities{NativeJSON: true}}
		}
		s, err := ai.NewWith(cfg, st, nil, nil, nil, ai.Options{Replica: replica, Model: m, Now: clk.now})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	a := &countAgent{name: "count", interval: time.Minute}
	s1 := mk("r1", nil)
	s1.Scheduler.Register(a)
	s1.Scheduler.Register(&countAgent{name: "off", interval: 0})

	s1.Scheduler.Tick(ctx)
	if a.runs.Load() != 0 {
		t.Fatal("ran before the start delay")
	}
	clk.add(2 * time.Minute)
	s1.Scheduler.Tick(ctx)
	if a.runs.Load() != 1 {
		t.Fatalf("runs after the start delay = %d", a.runs.Load())
	}
	s1.Scheduler.Tick(ctx)
	if a.runs.Load() != 1 {
		t.Fatal("ran again inside its interval")
	}

	// A restart (a new service on the same store) keeps the interval.
	clk.add(30 * time.Second)
	s2 := mk("r2", nil)
	s2.Scheduler.Register(a)
	s2.Scheduler.Tick(ctx)
	if a.runs.Load() != 1 {
		t.Fatal("a restart re-ran the agent before its interval")
	}
	clk.add(30 * time.Second)
	s2.Scheduler.Tick(ctx)
	if a.runs.Load() != 2 {
		t.Fatalf("runs after the interval = %d", a.runs.Load())
	}

	// Run-now runs once, on whichever replica ticks first.
	if _, err := s1.Scheduler.RequestRun(ctx, "count", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Scheduler.RequestRun(ctx, "nope", 0); !errors.Is(err, ai.ErrUnknownAgent) {
		t.Errorf("unknown agent: %v", err)
	}
	infos, err := s1.Scheduler.Agents(ctx)
	if err != nil || len(infos) != 2 || !infos[0].RunRequested || infos[1].NextDueAt != nil {
		t.Fatalf("agents = %+v, %v", infos, err)
	}
	s2.Scheduler.Tick(ctx)
	s1.Scheduler.Tick(ctx)
	if a.runs.Load() != 3 {
		t.Fatalf("run-now ran %d times", a.runs.Load()-2)
	}

	// Two replicas tick at once: one runs, never both.
	slow := &countAgent{name: "slow", interval: time.Minute, hold: 200 * time.Millisecond}
	s1.Scheduler.Register(slow)
	s2.Scheduler.Register(slow)
	clk.add(5 * time.Minute)
	var wg sync.WaitGroup
	for _, s := range []*ai.Service{s1, s2} {
		wg.Add(1)
		go func() { defer wg.Done(); s.Scheduler.Tick(ctx) }()
	}
	wg.Wait()
	if slow.runs.Load() != 1 || slow.overlap.Load() != 0 {
		t.Fatalf("two replicas: runs %d, overlap %d", slow.runs.Load(), slow.overlap.Load())
	}

	// Outcomes: failure, panic and tokens of a model call made in the run.
	model := &aitest.MockModel{Responses: []*provider.Response{aifake.JSON(answer{Answer: "x"})}, Caps: provider.Capabilities{NativeJSON: true}}
	s3 := mk("r3", model)
	failing := &countAgent{name: "failing", interval: time.Minute, run: func(context.Context) (ai.Outcome, error) {
		return ai.OutcomeOK, errors.New("detector broke")
	}}
	panicky := &countAgent{name: "panicky", interval: time.Minute, run: func(context.Context) (ai.Outcome, error) { panic("boom") }}
	spender := &countAgent{name: "spender", interval: time.Minute, run: func(ctx context.Context) (ai.Outcome, error) {
		ai.SetRunDetail(ctx, "detectors", 3)
		_, _, err := ai.Generate(ctx, s3, ai.Call[answer]{Feature: "aiops_explain", Background: true, Prompt: "q"})
		return ai.OutcomeOK, err
	}}
	for _, ag := range []ai.Agent{failing, panicky, spender} {
		s3.Scheduler.Register(ag)
	}
	clk.add(5 * time.Minute)
	s3.Scheduler.Tick(ctx)
	byAgent := map[string]ai.AgentRun{}
	for _, r := range runs() {
		if r.Outcome == "" || r.FinishedAt == nil {
			t.Errorf("run row without an outcome: %+v", r)
		}
		byAgent[r.Agent] = r
	}
	if r := byAgent["failing"]; r.Outcome != ai.OutcomeFailed || r.Error != "detector broke" {
		t.Errorf("failing run = %+v", r)
	}
	if r := byAgent["panicky"]; r.Outcome != ai.OutcomeFailed {
		t.Errorf("panicking run = %+v", r)
	}
	if r := byAgent["spender"]; r.Outcome != ai.OutcomeOK || r.Tokens != 15 || r.Detail["detectors"] == nil {
		t.Errorf("spender run = %+v", r)
	}
	if r := byAgent["count"]; r.Outcome != ai.OutcomeNoChange {
		t.Errorf("count run = %+v", r)
	}
}

// TestScheduler (spec S-17) fails if two replicas run one agent at once, a
// restart re-runs an agent before its interval, run-now does not run once,
// a disabled agent is due, or a run row lacks its outcome — in memory here
// and against PostgreSQL in TestSchedulerPostgres.
func TestScheduler(t *testing.T) {
	st := aifake.NewStore()
	schedulerCase(t, st, st.AgentRuns)
}
