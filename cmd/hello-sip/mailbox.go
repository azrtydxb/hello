// The mailbox-event listener: hello-control NOTIFYs hello_mwi (payload: box
// id) when the HTTP API marks a voicemail message heard or deletes it, and
// this node LISTENs so it can send MWI at once instead of waiting for its
// safety-net re-read.
package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// mailboxEvents implements sip.MailboxEvents over a dedicated PostgreSQL
// connection (LISTEN needs a session of its own).
type mailboxEvents struct {
	cfg *pgx.ConnConfig
	log *slog.Logger
}

// Watch listens until ctx ends, reconnecting with backoff. Each (re)connect
// reports box 0, so events lost while disconnected are caught up.
func (m mailboxEvents) Watch(ctx context.Context, changed func(boxID int64)) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := m.session(ctx, changed)
		if ctx.Err() != nil {
			return
		}
		m.log.Warn("mailbox listener lost; reconnecting", "error", err)
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

func (m mailboxEvents) session(ctx context.Context, changed func(int64)) error {
	conn, err := pgx.ConnectConfig(ctx, m.cfg)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	// The channel name is store.MailboxChannel; LISTEN takes no parameters.
	if _, err := conn.Exec(ctx, "LISTEN hello_mwi"); err != nil {
		return err
	}
	changed(0)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		box, err := strconv.ParseInt(n.Payload, 10, 64)
		if err != nil || box <= 0 {
			m.log.Warn("mailbox event with a bad payload", "payload", n.Payload)
			continue
		}
		changed(box)
	}
}
