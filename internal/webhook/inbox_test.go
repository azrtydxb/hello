package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

var inboxSeq int64

// inboxEvent is a messaging inbox event with a fresh id.
func inboxEvent(typ string) events.Event {
	inboxSeq++
	return events.Event{APIVersion: events.APIVersion, ID: fmt.Sprintf("evt_inbox_%d_%d", time.Now().UnixNano(), inboxSeq),
		Type: typ, CreatedAt: time.Now().UTC(),
		Data: events.Data{Inbox: &events.InboxData{ConversationID: 40 + inboxSeq, MessageID: 90 + inboxSeq, ChannelKind: "sms", PeerKey: "pk_x"}}}
}

// TestInboxOptions fails if an inbox event reaches an endpoint with the
// customer's number or text it did not ask for, lacks them when it did, if
// the text is added to anything but message.received and message.sent, if a
// failed read sends the event anyway or counts against the receiver, or if
// setting the options is not audited.
func TestInboxOptions(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ctx := context.Background()
		var fail bool
		r.svc.Enrich = func(_ context.Context, d *events.InboxData, peer, body bool) error {
			if fail {
				return errors.New("db busy")
			}
			if peer {
				d.Peer = "+3247500000"
			}
			if body {
				d.Body = "hello there"
			}
			return nil
		}
		plain, _ := r.endpoint(EndpointInput{Name: "plain", Events: []string{"message.*"}})
		full, _ := r.endpoint(EndpointInput{Name: "full", URL: r.recv.srv.URL + "/full", Events: []string{"message.*", "conversation.*"},
			IncludePeer: true, IncludeMessageBody: true})
		if !full.IncludePeer || !full.IncludeMessageBody || plain.IncludePeer {
			t.Fatalf("options not stored: %+v %+v", full, plain)
		}
		if n := r.env.audit(t, "include_message_body true"); n != 1 {
			t.Errorf("include_message_body audit rows = %d, want 1", n)
		}
		r.env.emit(t, inboxEvent(events.MessageReceived))
		r.env.emit(t, inboxEvent(events.MessageStatus))
		r.env.emit(t, inboxEvent(events.ConversationCreated))
		if n := r.run(); n != 5 {
			t.Fatalf("attempts = %d, want 5", n)
		}
		type got struct{ peer, body string }
		seen := map[string][]got{}
		for _, q := range r.recv.requests() {
			var ev events.Event
			if err := json.Unmarshal(q.Body, &ev); err != nil || ev.Data.Inbox == nil {
				t.Fatalf("delivery %s: %v", q.Body, err)
			}
			key := q.Header.Get(HeaderEventType)
			if ev.Data.Inbox.PeerKey != "pk_x" {
				key += "?"
			}
			if ev.Data.Inbox.Peer != "" {
				key += "+peer"
			}
			seen[key] = append(seen[key], got{ev.Data.Inbox.Peer, ev.Data.Inbox.Body})
		}
		if len(seen["message.received"]) != 1 || seen["message.received"][0].body != "" {
			t.Errorf("plain endpoint's message.received = %+v", seen["message.received"])
		}
		if g := seen["message.received+peer"]; len(g) != 1 || g[0].body != "hello there" {
			t.Errorf("full endpoint's message.received = %+v", g)
		}
		if g := seen["message.status+peer"]; len(g) != 1 || g[0].body != "" {
			t.Errorf("message.status carried a body: %+v", g)
		}
		if g := seen["conversation.created+peer"]; len(g) != 1 || g[0].body != "" {
			t.Errorf("conversation.created = %+v", g)
		}

		fail = true
		r.env.emit(t, inboxEvent(events.MessageSent))
		before := len(r.recv.requests())
		r.run()
		if len(r.recv.requests()) != before+1 { // the plain endpoint still gets it
			t.Errorf("deliveries after a failed read = %d, want %d", len(r.recv.requests()), before+1)
		}
		ds := r.deliveries(full.ID, StatusRetrying)
		if len(ds) != 1 || ds[0].LastError != "enrich_failed" {
			t.Fatalf("full endpoint after a failed read: %+v", ds)
		}
		ep, err := r.svc.GetEndpoint(ctx, full.ID)
		if err != nil || ep.ConsecutiveFailures != 0 {
			t.Errorf("a failed read counted against the receiver: %+v, %v", ep, err)
		}

		off := false
		if _, err := r.svc.Update(ctx, "user:test", full.ID, Patch{IncludeMessageBody: &off}); err != nil {
			t.Fatal(err)
		}
		if n := r.env.audit(t, "include_message_body false"); n != 1 {
			t.Errorf("include_message_body false audit rows = %d, want 1", n)
		}
		name := "renamed"
		if _, err := r.svc.Update(ctx, "user:test", full.ID, Patch{Name: &name}); err != nil {
			t.Fatal(err)
		}
		if n := r.env.audit(t, "include_message_body false") + r.env.audit(t, "include_peer false"); n != 1 {
			t.Errorf("a rename recorded an option change: %d rows", n)
		}
	})
}
