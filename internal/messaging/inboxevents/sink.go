// Package inboxevents connects the messaging inbox to the integrations event
// pipeline (spec messaging-channels M-23): every inbox event becomes an
// events.Event on the Valkey stream hello-control ingests, so it reaches
// the signed webhooks (with their retries and dead-letter queue), the
// automation triggers (Zapier, n8n, Make) and the event stream like a call
// event does.
//
// An event carries ids, states and a keyed hash of the customer; the
// customer's number and the text are added only at webhook delivery, for an
// endpoint that asked for them. Nothing here logs a number or a text.
package inboxevents

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/messaging"
)

// Data reads what an event carries; *store.Store implements it.
type Data interface {
	InboxEventData(ctx context.Context, e messaging.Event) (events.InboxData, bool, error)
}

// Emitter queues an event for the stream without blocking; *events.Emitter
// implements it.
type Emitter interface {
	Emit(events.Event)
}

// readTimeout bounds the read of an event's data: the action that produced
// it has committed and must not wait long on its event.
const readTimeout = 2 * time.Second

// Sink is the messaging.EventSink that feeds the pipeline.
type Sink struct {
	Data Data
	Out  Emitter
	Log  *slog.Logger
	// Now is replaced in tests.
	Now func() time.Time
}

func (s *Sink) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Emit implements messaging.EventSink. An event of the same action that
// created the conversation is preceded by conversation.created.
func (s *Sink) Emit(ctx context.Context, e messaging.Event) {
	if !events.KnownType(e.Type) || e.ConversationID == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	at := s.now()
	if e.NewConversation {
		c := messaging.Event{Type: events.ConversationCreated, ConversationID: e.ConversationID, ChannelKind: e.ChannelKind}
		s.Out.Emit(New(c, s.data(rctx, c), at))
	}
	s.Out.Emit(New(e, s.data(rctx, e), at))
}

// data reads the event's data; a failed read still emits what e had, so a
// receiver learns of the event, with the conversation id to look it up.
func (s *Sink) data(ctx context.Context, e messaging.Event) events.InboxData {
	d, _, err := s.Data.InboxEventData(ctx, e)
	if err != nil && s.Log != nil {
		s.Log.Warn("inbox event: reading its data", "type", e.Type, "conversation", e.ConversationID, "error", err)
	}
	return d
}

// New builds the pipeline event for an inbox event. Its id is derived from
// the conversation, the type, the message, the state, the assignee and the
// time, so a stream redelivery is stored once.
func New(e messaging.Event, d events.InboxData, at time.Time) events.Event {
	extra := strconv.FormatInt(e.MessageID, 10) + "/" + d.Status + "/" + strconv.FormatInt(d.AssigneeUserID, 10) + "/" +
		strconv.FormatInt(at.UnixNano(), 10)
	return events.Event{
		APIVersion: events.APIVersion,
		ID:         events.ID("conversation:"+strconv.FormatInt(e.ConversationID, 10), e.Type, "", extra),
		Type:       e.Type,
		CreatedAt:  at.UTC(),
		Data:       events.Data{Inbox: &d},
	}
}

// compile-time check.
var _ messaging.EventSink = (*Sink)(nil)
