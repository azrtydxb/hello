// The SIP MESSAGE queue: messages live in PostgreSQL (the messages table).
// hello-control inserts API sends and NOTIFYs hello_messages; this node
// LISTENs so it delivers at once, and claims rows with SKIP LOCKED, so any
// node may take the work and exactly one does.
package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
)

// messageQueue implements sip.MessageQueue on the control store.
type messageQueue struct {
	st  *store.Store
	cfg *pgx.ConnConfig
	log *slog.Logger
}

var _ sip.MessageQueue = messageQueue{}

func (q messageQueue) Watch(ctx context.Context, wake func()) {
	listen(ctx, q.cfg, q.log, listenMessage, "message", wake)
}

func (q messageQueue) Send(ctx context.Context, in sip.MessageSend) (sip.MessageSent, error) {
	sent, err := q.st.SendMessage(ctx, store.MessageInput{
		Sender: in.Sender, SenderKind: in.SenderKind, SenderName: in.SenderName,
		TargetKind: in.TargetKind, Target: in.Target, ContentType: in.ContentType, Body: in.Body,
		TrunkID: in.TrunkID, ClaimBy: in.ClaimBy,
	})
	if err != nil {
		return sip.MessageSent{}, mapMessageErr(err)
	}
	out := sip.MessageSent{Recipients: sent.Recipients}
	for _, j := range sent.Jobs {
		out.Jobs = append(out.Jobs, jobOf(j))
	}
	return out, nil
}

func (q messageQueue) Pending(ctx context.Context) ([]string, bool, error) {
	return q.st.MessagePending(ctx)
}

func (q messageQueue) Claim(ctx context.Context, node string, extensions []string, external bool, limit int) ([]sip.MessageJob, error) {
	jobs, err := q.st.ClaimMessages(ctx, node, extensions, external, limit)
	if err != nil {
		return nil, err
	}
	out := make([]sip.MessageJob, len(jobs))
	for i, j := range jobs {
		out[i] = jobOf(j)
	}
	return out, nil
}

func (q messageQueue) Finish(ctx context.Context, id int64, result, detail string) error {
	return q.st.FinishMessage(ctx, id, result, detail)
}

func (q messageQueue) Maintain(ctx context.Context) error {
	_, _, err := q.st.MaintainMessages(ctx)
	return err
}

func jobOf(j store.MessageJob) sip.MessageJob {
	return sip.MessageJob{
		ID: j.ID, Sender: j.Sender, SenderKind: j.SenderKind, SenderName: j.SenderName,
		Recipient: j.Recipient, RecipientKind: j.RecipientKind, Scope: j.Scope, ScopeRef: j.ScopeRef,
		ContentType: j.ContentType, Body: j.Body, TrunkID: j.TrunkID, Attempts: j.Attempts, CreatedAt: j.CreatedAt,
	}
}

// mapMessageErr turns the store's refusals into the SIP node's.
func mapMessageErr(err error) error {
	switch {
	case errors.Is(err, store.ErrMessageTarget):
		return sip.ErrMsgTarget
	case errors.Is(err, store.ErrMessageForbidden):
		return sip.ErrMsgForbidden
	case errors.Is(err, store.ErrMessageRate):
		return sip.ErrMsgRate
	case errors.Is(err, store.ErrMessagesDisabled):
		return sip.ErrMsgDisabled
	case errors.Is(err, store.ErrMessageTooLarge):
		return sip.ErrMsgTooLarge
	case errors.Is(err, store.ErrNoExternalTrunk):
		return sip.ErrMsgNoTrunk
	}
	return err
}
