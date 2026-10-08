package ai

import (
	"context"
	"time"
)

// Retention of S-26, applied by the store's PruneAI.
const (
	RetainTasks     = 24 * time.Hour
	RetainRuns      = 30 * 24 * time.Hour
	RetainSamples   = 7 * 24 * time.Hour
	RetainFindings  = 30 * 24 * time.Hour  // resolved or dismissed
	RetainProposals = 90 * 24 * time.Hour  // applied, failed, stale, dismissed or superseded
	RetainSessions  = 30 * 24 * time.Hour  // idle, with their messages
	RetainUsage     = 400 * 24 * time.Hour // ai_usage days
)

func (s *Service) pruneLoop(ctx context.Context) {
	t := time.NewTicker(s.timing.prune)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Prune(ctx); err != nil {
			s.log.Warn("ai: prune", "error", err)
		}
	}
}

// Prune deletes the AI rows past their retention (spec S-26) under
// pg_try_advisory_lock(hashtext('hello:ai:prune')); ran is false when
// another replica holds the lock.
func (s *Service) Prune(ctx context.Context) (ran bool, err error) {
	unlock, ok, err := s.st.TryAILock(ctx, "hello:ai:prune")
	if err != nil || !ok {
		return false, err
	}
	defer unlock()
	n, err := s.st.PruneAI(ctx, s.now())
	if err != nil {
		return true, err
	}
	s.log.Info("ai: pruned", "rows", n)
	return true, nil
}
