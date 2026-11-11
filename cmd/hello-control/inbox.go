package main

// The messaging inbox's background work (spec messaging-channels M-19, M-22):
// retention and snooze wake-ups on one replica, and the email an agent gets
// when a customer text waits and no inbox of theirs is open. Neither logs a
// message or a number.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/store"
)

const (
	inboxLeaseKey      = "hello:msg:lease:maintain"
	inboxMaintainEvery = 30 * time.Second
	inboxNoticeEvery   = 30 * time.Second
)

// runInboxMaintenance wakes snoozed conversations and purges what retention
// allows, on the replica holding the lease (any replica when the lease store
// is unavailable: every step is idempotent).
func runInboxMaintenance(ctx context.Context, st *store.Store, l lease, node string, m *messaging.Metrics, log *slog.Logger) {
	t := time.NewTicker(inboxMaintainEvery)
	defer t.Stop()
	for {
		held, err := l.AcquireLease(ctx, inboxLeaseKey, node, 3*inboxMaintainEvery)
		if held || err != nil {
			if r, err := st.MaintainMessaging(ctx); err != nil {
				if ctx.Err() == nil {
					log.Warn("inbox maintenance", "error", err)
				}
			} else if r.Conversations > 0 {
				m.Purged.WithLabelValues("conversations").Add(float64(r.Conversations))
				log.Info("inbox retention purged conversations", "count", r.Conversations)
			}
		}
		// Every replica reports the pool, whoever ran the maintenance.
		if s, err := st.GetInboxStats(ctx); err == nil {
			m.Unassigned.Set(float64(s.Unassigned))
			m.UnassignedOldest.Set(float64(s.OldestUnassignedSeconds))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runInboxNotifier emails agents about customer texts that wait. Each event
// is looked at once (the store marks it), so any number of replicas may run
// it. The email names the channel and says to open the inbox; it never
// carries the text or the number.
func runInboxNotifier(ctx context.Context, env mailer.Settings, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(inboxNoticeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		notices, err := st.ClaimInboxNotices(ctx, 50)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("inbox notices", "error", err)
			}
			continue
		}
		if len(notices) == 0 {
			continue
		}
		s, source, err := api.EffectiveSMTP(ctx, st, env)
		if err != nil || source == api.SMTPNone || s.Validate() != nil {
			continue // no usable SMTP: the notices are dropped, not retried
		}
		for _, n := range notices {
			what := "A customer text is waiting"
			if n.Unassigned {
				what = "A customer text is waiting for someone to take it"
			}
			msg, err := mailer.BuildNotice(s.From, n.To, "New customer text in the Hello inbox",
				fmt.Sprintf("%s on %s.\n\nOpen the Hello inbox to read and answer it.\n", what, n.ChannelName), time.Now())
			if err != nil {
				continue
			}
			if err := mailer.Deliver(ctx, s, n.To, msg); err != nil && ctx.Err() == nil {
				log.Warn("inbox notice email", "conversation", n.ConversationID, "error", err)
			}
		}
	}
}
