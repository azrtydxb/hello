package ai

import (
	"context"
	"sync"
	"time"
)

// recentErrors keeps this replica's last 10 call errors: code and time
// only, never prompt or response text (spec S-23).
type recentErrors struct {
	mu   sync.Mutex
	list []CallError
}

// CallError is one failed call on the status page.
type CallError struct {
	Code string
	At   time.Time
}

func (r *recentErrors) add(code string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append([]CallError{{Code: code, At: at}}, r.list...)
	if len(r.list) > 10 {
		r.list = r.list[:10]
	}
}

func (r *recentErrors) snapshot() []CallError {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CallError(nil), r.list...)
}

// Status is getAIStatus (spec S-23). It holds the endpoint's host, never
// its path or the API key.
type Status struct {
	Enabled              bool
	Reason               string
	Provider, Model      string
	EndpointHost         string
	EndpointPrivate      bool
	StructuredOutput     string
	TokensToday          int64
	DailyTokenBudget     int64
	BackgroundTokenLimit int64
	ConcurrencyInUse     int
	MaxConcurrency       int
	Agents               []AgentInfo
	RecentErrors         []CallError
	Counts               Counts
}

// Status reports the service; while AI is off it says why and reads no
// table.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{
		Enabled: s.enabled, Reason: s.reason, Provider: s.cfg.Provider, Model: s.cfg.Model,
		EndpointHost: s.host, EndpointPrivate: s.private, StructuredOutput: s.cfg.StructuredOutput,
		DailyTokenBudget: s.cfg.DailyTokenBudget, BackgroundTokenLimit: s.backgroundLimit(),
		ConcurrencyInUse: s.limits.inUse(), MaxConcurrency: max(s.cfg.MaxConcurrency, 1),
		RecentErrors: s.errs.snapshot(),
	}
	if !s.enabled {
		return st, nil
	}
	u, err := s.st.AIUsage(ctx, day(s.now()))
	if err != nil {
		return st, err
	}
	st.TokensToday = u.Total()
	if st.Agents, err = s.Scheduler.Agents(ctx); err != nil {
		return st, err
	}
	st.Counts, err = s.st.AICounts(ctx)
	return st, err
}
