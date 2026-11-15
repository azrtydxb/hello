package inboxevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/messaging"
)

type fakeData struct {
	err  error
	seen []messaging.Event
}

func (f *fakeData) InboxEventData(_ context.Context, e messaging.Event) (events.InboxData, bool, error) {
	f.seen = append(f.seen, e)
	if f.err != nil {
		return events.InboxData{ConversationID: e.ConversationID, MessageID: e.MessageID}, false, f.err
	}
	return events.InboxData{ConversationID: e.ConversationID, MessageID: e.MessageID, ChannelID: 3, ChannelKind: "sms",
		Status: e.Status, PeerKey: "pk_1", AssigneeUserID: e.AssigneeUserID}, true, nil
}

type fakeOut struct {
	mu  sync.Mutex
	got []events.Event
}

func (f *fakeOut) Emit(e events.Event) { f.mu.Lock(); f.got = append(f.got, e); f.mu.Unlock() }

// TestSinkEmits fails if an inbox event does not reach the pipeline as a
// registered event with its data, if a new conversation is not announced
// first, if an unknown type or a missing conversation is emitted, if a failed
// read drops the event, or if the event carries a number or text.
func TestSinkEmits(t *testing.T) {
	data, out := &fakeData{}, &fakeOut{}
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	s := &Sink{Data: data, Out: out, Now: func() time.Time { return at }}
	s.Emit(context.Background(), messaging.Event{Type: events.MessageReceived, ConversationID: 5, MessageID: 8, NewConversation: true})
	if len(out.got) != 2 || out.got[0].Type != events.ConversationCreated || out.got[1].Type != events.MessageReceived {
		t.Fatalf("emitted %+v", out.got)
	}
	for _, e := range out.got {
		if e.Data.Inbox == nil || e.Data.Inbox.ConversationID != 5 || e.APIVersion != events.APIVersion || !e.CreatedAt.Equal(at) {
			t.Fatalf("event %+v", e)
		}
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), `"peer"`) || strings.Contains(string(b), `"body"`) {
			t.Errorf("event carries content: %s", b)
		}
	}
	if out.got[0].ID == out.got[1].ID {
		t.Error("two events share an id")
	}
	// The id is stable for the same event at the same time (a redelivery).
	a := New(messaging.Event{Type: events.MessageSent, ConversationID: 1, MessageID: 2}, events.InboxData{Status: "queued"}, at)
	b := New(messaging.Event{Type: events.MessageSent, ConversationID: 1, MessageID: 2}, events.InboxData{Status: "queued"}, at)
	c := New(messaging.Event{Type: events.MessageSent, ConversationID: 1, MessageID: 2}, events.InboxData{Status: "sent"}, at)
	if a.ID != b.ID || a.ID == c.ID {
		t.Errorf("ids %s %s %s", a.ID, b.ID, c.ID)
	}

	out.got = nil
	s.Emit(context.Background(), messaging.Event{Type: "conversation.updated", ConversationID: 5})
	s.Emit(context.Background(), messaging.Event{Type: events.MessageSent})
	if len(out.got) != 0 {
		t.Fatalf("emitted %+v for an unknown type or no conversation", out.got)
	}

	data.err = errors.New("db down")
	s.Emit(context.Background(), messaging.Event{Type: events.ConversationAssigned, ConversationID: 6, AssigneeUserID: 4})
	if len(out.got) != 1 || out.got[0].Data.Inbox.ConversationID != 6 {
		t.Fatalf("a failed read dropped the event: %+v", out.got)
	}
}
