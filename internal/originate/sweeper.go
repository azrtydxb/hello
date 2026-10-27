package originate

import (
	"context"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// A hello-sip node that dies after taking a request leaves it queued or
// ringing for good: the sweeper (run by one hello-control at a time) closes
// such a request failed node_unavailable after StaleAfter, copies the states
// the nodes post into the rows, frees the extension's setup slot, and purges
// finished rows after Retention.
const (
	// StaleAfter is how long a request may sit in a state before its node is
	// presumed dead (spec: 60 s).
	StaleAfter = 60 * time.Second
	// ConnectedMax closes a connected request whose end was never posted
	// (its node died and nobody took the call over).
	ConnectedMax = 12 * time.Hour
	// Retention is how long finished requests are kept.
	Retention = 30 * 24 * time.Hour
	// Interval is the sweep period.
	Interval = 10 * time.Second
)

// SweepStore is the persistence the sweeper uses; *store.Store implements it.
type SweepStore interface {
	OpenOriginates(ctx context.Context, idle time.Duration, limit int) ([]store.OriginateRecord, error)
	SetOriginateState(ctx context.Context, id, state, reason, correlationID string) error
	PurgeOriginates(ctx context.Context, age time.Duration) (int64, error)
}

// SweepLive is the live state the sweeper reads and corrects.
type SweepLive interface {
	GetOriginateState(ctx context.Context, id string) (livestate.OriginateState, bool, error)
	PutOriginateState(ctx context.Context, st livestate.OriginateState) error
	ReleaseOriginateSetup(ctx context.Context, extension, id string) error
}

// Locker serialises the sweeper across hello-control pods; *store.Store
// implements it.
type Locker interface {
	TryLock(ctx context.Context, key string) (release func(), ok bool, err error)
}

// Sweeper closes orphaned requests and keeps the rows in step.
type Sweeper struct {
	Store SweepStore
	Live  SweepLive
	Lock  Locker
	Log   *slog.Logger
	Now   func() time.Time
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run sweeps every Interval until ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n++
		s.Once(ctx, n%360 == 0)
	}
}

// Once runs one sweep; purge also deletes old finished rows.
func (s *Sweeper) Once(ctx context.Context, purge bool) {
	if s.Lock != nil {
		release, ok, err := s.Lock.TryLock(ctx, "originate-sweep")
		if err != nil || !ok {
			return
		}
		defer release()
	}
	recs, err := s.Store.OpenOriginates(ctx, 2*time.Second, 200)
	if err != nil {
		s.Log.Warn("originate sweep: list", "error", err)
		return
	}
	for _, rec := range recs {
		s.one(ctx, rec)
	}
	if purge {
		if n, err := s.Store.PurgeOriginates(ctx, Retention); err != nil {
			s.Log.Warn("originate sweep: purge", "error", err)
		} else if n > 0 {
			s.Log.Info("originate sweep: purged finished requests", "count", n)
		}
	}
}

func (s *Sweeper) one(ctx context.Context, rec store.OriginateRecord) {
	live, ok, err := s.Live.GetOriginateState(ctx, rec.ID)
	if err != nil {
		return // Valkey unreachable: decide nothing
	}
	state, reason, corr, since := rec.State, rec.Reason, rec.CorrelationID, rec.UpdatedAt
	if ok {
		state, reason, since = live.State, live.Reason, live.UpdatedAt
		if live.CorrelationID != "" {
			corr = live.CorrelationID
		}
	}
	if !livestate.OriginateTerminal(state) {
		limit := StaleAfter
		if state == livestate.OriginateConnected {
			limit = ConnectedMax
		}
		if s.now().Sub(since) >= limit {
			if state == livestate.OriginateConnected {
				state, reason = livestate.OriginateEnded, ""
			} else {
				state, reason = livestate.OriginateFailed, livestate.OriginateNodeGone
			}
			if err := s.Live.PutOriginateState(ctx, livestate.OriginateState{ID: rec.ID, State: state, Reason: reason, CorrelationID: corr}); err != nil {
				s.Log.Warn("originate sweep: close", "id", rec.ID, "error", err)
			}
		}
	}
	if state != rec.State || reason != rec.Reason || corr != rec.CorrelationID {
		if err := s.Store.SetOriginateState(ctx, rec.ID, state, reason, corr); err != nil {
			s.Log.Warn("originate sweep: copy", "id", rec.ID, "error", err)
		}
	}
	if livestate.OriginateTerminal(state) {
		if err := s.Live.ReleaseOriginateSetup(ctx, rec.Extension, rec.ID); err != nil {
			s.Log.Debug("originate sweep: release", "id", rec.ID, "error", err)
		}
	}
}
