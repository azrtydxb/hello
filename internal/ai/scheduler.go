package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrUnknownAgent is a run-now request for an agent that is not registered.
var ErrUnknownAgent = errors.New("unknown agent")

// agentRunTimeout bounds one agent run.
const agentRunTimeout = 10 * time.Minute

// Scheduler runs the background agents (spec S-17). Each replica ticks every
// 5 s; an agent is due on a run-now request, when its last run started an
// interval ago, or, never run, AgentStartDelay after this replica started.
// A run holds the agent's advisory lock on a dedicated connection, so runs
// never overlap across replicas, and is recorded in ai_agent_runs.
type Scheduler struct {
	s      *Service
	mu     sync.Mutex
	agents map[string]Agent
}

// Register adds an agent; a later one with the same name replaces it.
// Register before Service.Run.
func (sc *Scheduler) Register(a Agent) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.agents[a.Name()] = a
}

func (sc *Scheduler) list() []Agent {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make([]Agent, 0, len(sc.agents))
	for _, a := range sc.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (sc *Scheduler) agent(name string) (Agent, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	a, ok := sc.agents[name]
	return a, ok
}

func (sc *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(sc.s.timing.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sc.Tick(ctx)
	}
}

// Tick runs every due agent once, one after another. A failing database
// skips the tick.
func (sc *Scheduler) Tick(ctx context.Context) {
	for _, a := range sc.list() {
		if ctx.Err() != nil {
			return
		}
		if err := sc.runIfDue(ctx, a); err != nil {
			sc.s.log.Warn("ai: scheduler", "agent", a.Name(), "error", err)
		}
	}
}

// due reports whether a should run now and, for the agents list, when it
// is next due (nil when disabled).
func (sc *Scheduler) due(ctx context.Context, a Agent) (due bool, next *time.Time, err error) {
	iv := a.Interval()
	if iv <= 0 {
		return false, nil, nil
	}
	now := sc.s.now()
	requested, err := sc.s.st.AgentRunRequested(ctx, a.Name())
	if err != nil {
		return false, nil, err
	}
	last, err := sc.s.st.LastAgentRun(ctx, a.Name())
	if err != nil {
		return false, nil, err
	}
	at := sc.s.started.Add(sc.s.cfg.AgentStartDelay)
	if last != nil {
		at = last.StartedAt.Add(iv)
	}
	if requested {
		at = now
	}
	return requested || !now.Before(at), &at, nil
}

func (sc *Scheduler) runIfDue(ctx context.Context, a Agent) error {
	s := sc.s
	if due, _, err := sc.due(ctx, a); err != nil || !due {
		return err
	}
	unlock, ok, err := s.st.TryAILock(ctx, "hello:ai:agent:"+a.Name())
	if err != nil {
		return err
	}
	if !ok {
		// Another replica runs it now; its run row is the record.
		s.metrics.agentRuns.WithLabelValues(a.Name(), string(OutcomeSkippedLocked)).Inc()
		return nil
	}
	defer unlock()
	// Re-check under the lock: another replica may just have finished it.
	if due, _, err := sc.due(ctx, a); err != nil || !due {
		return err
	}
	if _, err := s.st.CloseOrphanRuns(ctx, a.Name(), s.now()); err != nil {
		return err
	}
	if _, err := s.st.TakeAgentRequest(ctx, a.Name()); err != nil {
		return err
	}
	id, err := s.st.StartAgentRun(ctx, a.Name(), s.replica, s.now())
	if err != nil {
		return err
	}
	acc := &runAcc{}
	rctx, cancel := context.WithTimeout(withRun(ctx, acc), agentRunTimeout)
	outcome, runErr := safeAgent(rctx, a)
	cancel()
	if runErr != nil && outcome != OutcomeSkippedBudget {
		outcome = OutcomeFailed
	}
	if outcome == "" {
		outcome = OutcomeOK
	}
	acc.mu.Lock()
	r := AgentRun{FinishedAt: ptr(s.now()), Outcome: outcome, Tokens: acc.tokens.Load(), Detail: acc.detail}
	acc.mu.Unlock()
	if runErr != nil {
		r.Error = runErr.Error()
		s.log.Warn("ai: agent run failed", "agent", a.Name(), "error", runErr)
	}
	s.metrics.agentRuns.WithLabelValues(a.Name(), string(outcome)).Inc()
	wctx, wcancel := storeCtx()
	defer wcancel()
	return s.st.FinishAgentRun(wctx, id, r)
}

func safeAgent(ctx context.Context, a Agent) (o Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			o, err = OutcomeFailed, fmt.Errorf("agent panicked: %v", r)
		}
	}()
	return a.Run(ctx)
}

func ptr[T any](v T) *T { return &v }

// AgentInfo is an agent as listAIAgents shows it (spec S-23).
type AgentInfo struct {
	Name         string
	Interval     time.Duration
	LastRun      *AgentRun
	NextDueAt    *time.Time
	RunRequested bool
}

// Agents lists the registered agents with their last run and next due time.
func (sc *Scheduler) Agents(ctx context.Context) ([]AgentInfo, error) {
	var out []AgentInfo
	for _, a := range sc.list() {
		last, err := sc.s.st.LastAgentRun(ctx, a.Name())
		if err != nil {
			return nil, err
		}
		req, err := sc.s.st.AgentRunRequested(ctx, a.Name())
		if err != nil {
			return nil, err
		}
		_, next, err := sc.due(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, AgentInfo{Name: a.Name(), Interval: a.Interval(), LastRun: last, NextDueAt: next, RunRequested: req})
	}
	return out, nil
}

// RequestRun records a run-now request for name, which the next tick on
// any replica runs once; ErrUnknownAgent when it is not registered.
func (sc *Scheduler) RequestRun(ctx context.Context, name string, userID int64) (time.Time, error) {
	if _, ok := sc.agent(name); !ok || !sc.s.enabled {
		return time.Time{}, ErrUnknownAgent
	}
	return sc.s.st.RequestAgentRun(ctx, name, userID, sc.s.now())
}
