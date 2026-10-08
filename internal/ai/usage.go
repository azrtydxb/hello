package ai

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// day is the UTC date usage is summed under.
func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// backgroundLimit is the tokens background work may use per day (spec
// S-16): BackgroundBudgetPercent of the daily budget.
func (s *Service) backgroundLimit() int64 {
	return s.cfg.DailyTokenBudget * int64(s.cfg.BackgroundBudgetPercent) / 100
}

// checkBudget refuses a call once today's tokens across replicas reach the
// budget: the background share for background work, all of it for
// interactive work.
func (s *Service) checkBudget(ctx context.Context, background bool) error {
	u, err := s.st.AIUsage(ctx, day(s.now()))
	if err != nil {
		return &Error{Code: CodeProviderError, Message: "read the token budget", Err: err}
	}
	limit, reason := s.cfg.DailyTokenBudget, "budget"
	if background {
		limit, reason = s.backgroundLimit(), "background_budget"
	}
	if u.Total() >= limit {
		s.metrics.busy.WithLabelValues(reason).Inc()
		return &Error{Code: CodeBudgetExhausted, Message: "the daily AI token budget is spent"}
	}
	return nil
}

// recordUsage upserts one call's tokens into today's row and moves the
// token and budget metrics. It runs after every call, failed ones too.
func (s *Service) recordUsage(ctx context.Context, feature string, u Usage) {
	if u == (Usage{}) {
		return
	}
	s.metrics.tokens.WithLabelValues(feature, "input").Add(float64(u.InputTokens))
	s.metrics.tokens.WithLabelValues(feature, "output").Add(float64(u.OutputTokens))
	s.metrics.tokens.WithLabelValues(feature, "reasoning").Add(float64(u.ReasoningTokens))
	addRunTokens(ctx, u.Total())
	// The call's own context may have timed out; the usage must still land.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	d := day(s.now())
	if err := s.st.AddAIUsage(ctx, d, u); err != nil {
		s.log.Warn("ai: record usage", "feature", feature, "error", err)
		return
	}
	if today, err := s.st.AIUsage(ctx, d); err == nil && s.cfg.DailyTokenBudget > 0 {
		s.metrics.budgetUsed.Set(float64(today.Total()) / float64(s.cfg.DailyTokenBudget))
	}
}

// runAcc collects the tokens and detail of one agent run from the calls
// and the agent made under its context.
type runAcc struct {
	tokens atomic.Int64
	mu     sync.Mutex
	detail map[string]any
}

type runKey struct{}

func withRun(ctx context.Context, r *runAcc) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

func addRunTokens(ctx context.Context, n int64) {
	if r, ok := ctx.Value(runKey{}).(*runAcc); ok {
		r.tokens.Add(n)
	}
}

// SetRunDetail records key in the current agent run's detail (spec S-17,
// for example a detector's error or valkey_unavailable); outside a run it
// does nothing. v must marshal to JSON and never hold a prompt or response.
func SetRunDetail(ctx context.Context, key string, v any) {
	r, ok := ctx.Value(runKey{}).(*runAcc)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.detail == nil {
		r.detail = map[string]any{}
	}
	r.detail[key] = v
}
