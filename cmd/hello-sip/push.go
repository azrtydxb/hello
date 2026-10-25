// The check-sync push queue: hello-control inserts a phone_pushes row and
// NOTIFYs hello_checksync; this node LISTENs so it claims and sends at once,
// and the rows themselves carry the work, so any node can take it.
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
)

// pushQueue implements sip.PushQueue on the control store, with a
// dedicated PostgreSQL connection for LISTEN.
type pushQueue struct {
	st  *store.Store
	cfg *pgx.ConnConfig
	log *slog.Logger
}

// Watch listens until ctx ends, reconnecting with backoff. Each (re)connect
// wakes once, so pushes queued while disconnected are claimed.
func (q pushQueue) Watch(ctx context.Context, wake func()) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := q.session(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		q.log.Warn("check-sync listener lost; reconnecting", "error", err)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (q pushQueue) session(ctx context.Context, wake func()) error {
	conn, err := pgx.ConnectConfig(ctx, q.cfg)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	// The channel name is store.PushChannel; LISTEN takes no parameters.
	if _, err := conn.Exec(ctx, "LISTEN hello_checksync"); err != nil {
		return err
	}
	wake()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		wake()
	}
}

func (q pushQueue) Claim(ctx context.Context, node string, perMinute int) ([]sip.PushJob, error) {
	jobs, err := q.st.ClaimPushes(ctx, node, perMinute)
	if err != nil {
		return nil, err
	}
	out := make([]sip.PushJob, len(jobs))
	for i, j := range jobs {
		out[i] = sip.PushJob{ID: j.ID, Kind: j.Kind, Vendor: j.Vendor, Device: j.Device}
	}
	return out, nil
}

func (q pushQueue) Finish(ctx context.Context, id int64, r sip.PushResult) error {
	return q.st.FinishPush(ctx, id, r.Status, r.Detail, r.Contacts)
}
