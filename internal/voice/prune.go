package voice

import (
	"context"
	"time"
)

// Retention and locking for call reports (spec S-23): transcripts are
// cleared past their agent's transcript_retention_days, and a report (its
// summary with it) is deleted past the CDR retention. Prunes run hourly and
// are serialised across replicas with the advisory lock.
const (
	// PruneLock is the advisory lock key, like the ai prune's.
	PruneLock = "hello:voice:prune"
	// PruneInterval is how often a replica tries to prune.
	PruneInterval = time.Hour
)

// Prune deletes the voice call reports past their retention (spec S-23)
// under pg_try_advisory_lock(hashtext('hello:voice:prune')); ran is false
// when another replica holds the lock.
func (s *Service) Prune(ctx context.Context) (ran bool, err error) {
	unlock, ok, err := s.src.TryVoiceLock(ctx, PruneLock)
	if err != nil || !ok {
		return false, err
	}
	defer unlock()
	n, err := s.src.VoicePrune(ctx, s.now(), s.retain)
	if err != nil {
		return true, err
	}
	if n > 0 {
		s.log.Info("voice: pruned call reports", "rows", n)
	}
	return true, nil
}

// PruneLoop prunes every PruneInterval until ctx is done. The process
// wiring starts it beside the other loops; a replica that finds the lock
// held does nothing.
func (s *Service) PruneLoop(ctx context.Context) {
	t := time.NewTicker(PruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Prune(ctx); err != nil {
			s.log.Warn("voice: prune", "error", err)
		}
	}
}
