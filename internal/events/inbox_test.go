package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestInboxEventJSON fails if an inbox event's data carries the call fields,
// does not read back as inbox data, or if a call event's JSON changed.
func TestInboxEventJSON(t *testing.T) {
	ev := Event{APIVersion: APIVersion, ID: "evt_1", Type: MessageReceived, CreatedAt: time.Unix(0, 0).UTC(),
		Data: Data{Inbox: &InboxData{ConversationID: 7, MessageID: 9, ChannelKind: "sms", Direction: "in", PeerKey: "pk_ab"}}}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"callId", `"from"`, `"to"`, `"peer"`, `"body"`} {
		if strings.Contains(s, bad) {
			t.Errorf("inbox event JSON %s carries %s", s, bad)
		}
	}
	if !strings.Contains(s, `"conversationId":7`) || !strings.Contains(s, `"peerKey":"pk_ab"`) {
		t.Errorf("inbox event JSON %s", s)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Data.Inbox == nil || back.Data.Inbox.MessageID != 9 || back.Data.CallID != "" {
		t.Fatalf("read back %+v", back.Data)
	}
	call := New(CallRinging, Data{CallID: "c1", Extension: "101", From: Party{Number: "100"}, To: Party{Number: "101"}}, "", time.Unix(0, 0))
	cb, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cb), `"data":{"callId":"c1","extension":"101","from":{"number":"100"},"to":{"number":"101"}}`) {
		t.Errorf("call event JSON changed: %s", cb)
	}
	var cback Event
	if err := json.Unmarshal(cb, &cback); err != nil || cback.Data.Inbox != nil || cback.Data.CallID != "c1" {
		t.Fatalf("call read back %+v, %v", cback.Data, err)
	}
}

// TestInboxTypes fails if an inbox type is not registered or not recognised
// as one, or if Hide does not drop it.
func TestInboxTypes(t *testing.T) {
	for _, typ := range []string{ConversationCreated, ConversationAssigned, ConversationResolved, MessageReceived, MessageSent, MessageStatus, MessageOptOut} {
		if !KnownType(typ) || !IsInboxType(typ) {
			t.Errorf("%s: known %v inbox %v", typ, KnownType(typ), IsInboxType(typ))
		}
	}
	if IsInboxType(CallRinging) {
		t.Error("call.ringing is an inbox type")
	}
	if ts, err := ParseTypes("message.*,conversation.created"); err != nil || len(ts) != 2 {
		t.Errorf("ParseTypes = %v, %v", ts, err)
	}
	f := Filter{Hide: InboxPrefixes}
	if f.Matches(Event{Type: MessageSent}) || !f.Matches(Event{Type: CallRinging}) {
		t.Error("Hide does not drop exactly the inbox events")
	}
}
