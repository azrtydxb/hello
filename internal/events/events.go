// Package events is Hello's call-event pipeline (spec integrations I-34 to
// I-36).
//
// hello-sip describes what happens to a call as small Events and hands them
// to an Emitter, which appends them to the Valkey stream hello:events from a
// bounded in-memory queue: the SIP path never waits on Valkey or PostgreSQL.
// hello-control runs a Service that consumes the stream with a consumer
// group, stores each event once in integration_events, runs the transaction
// hooks later phases add (webhook deliveries) and fans the live stream out to
// the SSE clients of its own pod.
//
// Delivery is at least once and the event id is the dedupe key. The cursor of
// an event is its Valkey stream id; it orders events globally and is what a
// client sends back as Last-Event-ID.
package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// APIVersion is the envelope version; changes are additive only.
const APIVersion = "1"

// Event types (the registry below is their single source).
const (
	CallRinging        = "call.ringing"
	CallAnswered       = "call.answered"
	CallEnded          = "call.ended"
	CallMissed         = "call.missed"
	CallRecordingReady = "call.recording.ready"
	CallCompleted      = "call.completed"
	// VoicemailReceived is a voicemail message stored in a box (spec I-52,
	// the Slack notification of P17).
	VoicemailReceived = "voicemail.received"
	// CallOriginateProgress is a state change of a click-to-call (spec I-37).
	CallOriginateProgress = "call.originate.progress"

	// The messaging inbox's events (spec messaging-channels M-23). Their
	// data is an InboxData: ids and states, the customer's number only as a
	// keyed hash, never message text.
	ConversationCreated  = "conversation.created"
	ConversationAssigned = "conversation.assigned"
	ConversationResolved = "conversation.resolved"
	MessageReceived      = "message.received"
	MessageSent          = "message.sent"
	MessageStatus        = "message.status"
	MessageOptOut        = "message.optout"
)

// InboxPrefixes are the type prefixes of the inbox's events. Reading them
// needs the messages scope and the operator role: they say who texts the
// business and when, if not what.
var InboxPrefixes = []string{"conversation.", "message."}

// IsInboxType reports whether t is an inbox event type.
func IsInboxType(t string) bool {
	for _, p := range InboxPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// TypeInfo describes one event type for the console, the documentation and
// filter validation.
type TypeInfo struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Registry lists the event types hello-sip emits. Later phases add their own
// (call.originate.progress, webhook.test) next to the code that emits them.
var Registry = []TypeInfo{
	{CallRinging, "An extension is ringing; a ring group yields one event per ringing member."},
	{CallAnswered, "An extension answered, or a local caller's call was answered."},
	{CallEnded, "The call ended for an extension."},
	{CallMissed, "A call reached an extension and nobody answered (reason: no_answer, busy, dnd, cancelled or unreachable)."},
	{CallRecordingReady, "A recording of the call was stored; carries recordingId and durationMs."},
	{CallCompleted, "The call detail record was stored; carries cdrId."},
	{VoicemailReceived, "A caller left a voicemail message in the box of an extension; carries the caller, the box extension and durationMs. The audio is never part of an event."},
	{CallOriginateProgress, "A click-to-call changed state (queued, ringing_agent, agent_answered, dialing_target, connected, ended or failed); carries originateId, state and, for a failure, reason."},
	{ConversationCreated, "A customer conversation started in the messaging inbox (an inbound text or chat, or an agent's first contact); carries conversationId, channel, number and the hashed peerKey."},
	{ConversationAssigned, "A conversation was assigned to an agent (by the router, a claim or a supervisor) or returned to the pool; carries assigneeUserId (absent when unassigned) and queueId."},
	{ConversationResolved, "A conversation was resolved."},
	{MessageReceived, "A customer's text or chat message arrived; carries messageId. The text is included only for a webhook endpoint with includeMessageBody."},
	{MessageSent, "An agent's reply was queued for sending; carries messageId and status."},
	{MessageStatus, "A sent message changed delivery state (sent, delivered, failed or unknown); carries status and, for a failure, errorCode."},
	{MessageOptOut, "A customer opted out (STOP) or the carrier reported them opted out."},
}

// KnownType reports whether t is a registered event type.
func KnownType(t string) bool {
	return slices.ContainsFunc(Registry, func(i TypeInfo) bool { return i.Type == t })
}

// Party is one end of a call.
type Party struct {
	Number    string `json:"number,omitempty"`
	Name      string `json:"name,omitempty"`
	Extension string `json:"extension,omitempty"`
}

// Data is the payload of every call event. Fields that do not apply to a
// type are omitted. Recording audio, SIP bodies and DTMF are never in an
// event.
type Data struct {
	// CallID is the CDR correlation id.
	CallID string `json:"callId"`
	// Direction is internal, inbound or outbound.
	Direction string `json:"direction,omitempty"`
	// Extension is the extension the event is about: the subject.
	Extension string `json:"extension,omitempty"`
	// UserID is the user the extension is linked to, resolved when the event
	// is stored (zero when no user owns the extension).
	UserID int64 `json:"userId,omitempty"`
	// Sequence orders the events of one call: it only ever increases for a
	// callId, across subjects and across a takeover, but is not contiguous.
	// Delivery order is best effort; receivers order by it.
	Sequence   int64     `json:"sequence,omitempty"`
	From       Party     `json:"from"`
	To         Party     `json:"to"`
	Trunk      string    `json:"trunk,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	AnsweredAt time.Time `json:"answeredAt,omitzero"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
	DurationMs int64     `json:"durationMs,omitempty"`
	BillableMs int64     `json:"billableMs,omitempty"`
	// Disposition is answered, no_answer, busy, cancelled, unavailable or
	// failed on call.ended and call.completed.
	Disposition string `json:"disposition,omitempty"`
	// Reason explains call.missed: no_answer, busy, dnd, cancelled or
	// unreachable.
	Reason      string `json:"reason,omitempty"`
	OriginateID string `json:"originateId,omitempty"`
	// State is the click-to-call state on call.originate.progress.
	State       string `json:"state,omitempty"`
	CDRID       int64  `json:"cdrId,omitempty"`
	RecordingID int64  `json:"recordingId,omitempty"`

	// Inbox is the data of an inbox event (conversation.*, message.*). When
	// it is set the event's data is the InboxData alone: the call fields
	// above are not part of it.
	Inbox *InboxData `json:"-"`
}

// InboxData is the data of an inbox event. It holds ids, states and times;
// the customer's number appears only as PeerKey, a keyed hash that tells
// customers apart without naming them. Peer and Body are filled in at
// delivery for a webhook endpoint that asked for them (includePeer,
// includeMessageBody) and are never stored with the event.
type InboxData struct {
	ConversationID int64 `json:"conversationId"`
	MessageID      int64 `json:"messageId,omitempty"`
	ChannelID      int64 `json:"channelId,omitempty"`
	// ChannelKind is sms, whatsapp or webchat; Provider the carrier.
	ChannelKind string `json:"channelKind,omitempty"`
	Provider    string `json:"provider,omitempty"`
	AddressID   int64  `json:"addressId,omitempty"`
	// Address is the business's own number or chat site the conversation is
	// on.
	Address string `json:"address,omitempty"`
	// Direction is in or out (message events).
	Direction string `json:"direction,omitempty"`
	// Status is the message's delivery state (message events) or the
	// conversation's state (conversation events).
	Status    string `json:"status,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	// AssigneeUserID is the agent the conversation is assigned to.
	AssigneeUserID int64 `json:"assigneeUserId,omitempty"`
	QueueID        int64 `json:"queueId,omitempty"`
	// PeerKind is phone or visitor.
	PeerKind string `json:"peerKind,omitempty"`
	// PeerKey is a keyed hash of the customer's number or chat visitor id.
	PeerKey string `json:"peerKey,omitempty"`
	// Peer is the customer's number (or visitor:<id>), only for an endpoint
	// with includePeer.
	Peer string `json:"peer,omitempty"`
	// Body is the message text, only for an endpoint with
	// includeMessageBody and only on message.received and message.sent.
	Body string `json:"body,omitempty"`
}

// MarshalJSON writes the inbox data for an inbox event and the call fields
// otherwise.
func (d Data) MarshalJSON() ([]byte, error) {
	if d.Inbox != nil {
		return json.Marshal(d.Inbox)
	}
	type plain Data
	return json.Marshal(plain(d))
}

// UnmarshalJSON reads either form: data with a conversationId is inbox data.
func (d *Data) UnmarshalJSON(b []byte) error {
	type plain Data
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*d = Data(p)
	var probe struct {
		ConversationID int64 `json:"conversationId"`
	}
	if json.Unmarshal(b, &probe) == nil && probe.ConversationID != 0 {
		var in InboxData
		if err := json.Unmarshal(b, &in); err != nil {
			return err
		}
		d.Inbox = &in
	}
	return nil
}

// Event is one call event as emitted and delivered.
type Event struct {
	APIVersion string `json:"apiVersion"`
	// ID is unique per event and stable across redelivery: receivers dedupe
	// on it.
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
	Data      Data      `json:"data"`
}

// Entry is a stored or streamed event with its cursor.
type Entry struct {
	// Cursor orders events and resumes a stream (Last-Event-ID).
	Cursor string `json:"cursor"`
	Event
}

// New returns an event of type t for data, with a deterministic id derived
// from the call, the type, the subject extension and extra (a recording id),
// so an event emitted twice (a takeover, a retried CDR write) is stored once.
func New(t string, d Data, extra string, at time.Time) Event {
	if at.IsZero() {
		at = time.Now()
	}
	return Event{APIVersion: APIVersion, ID: ID(d.CallID, t, d.Extension, extra), Type: t, CreatedAt: at.UTC(), Data: d}
}

// ID is the event id for a call, type, subject and discriminator.
func ID(callID, typ, ext, extra string) string {
	h := sha256.Sum256([]byte(callID + "\x00" + typ + "\x00" + ext + "\x00" + extra))
	return "evt_" + hex.EncodeToString(h[:16])
}

// Cursor is a parsed stream id: milliseconds and sequence.
type Cursor struct{ MS, Seq int64 }

// ParseCursor parses "ms-seq". An empty string is the zero cursor, before
// every event.
func ParseCursor(s string) (Cursor, error) {
	if s == "" || s == "0" {
		return Cursor{}, nil
	}
	ms, seq, ok := strings.Cut(s, "-")
	if !ok {
		seq = "0"
	}
	m, err1 := strconv.ParseInt(ms, 10, 64)
	q, err2 := strconv.ParseInt(seq, 10, 64)
	if err1 != nil || err2 != nil || m < 0 || q < 0 {
		return Cursor{}, fmt.Errorf("events: invalid cursor %q", s)
	}
	return Cursor{m, q}, nil
}

// String is the stream id form.
func (c Cursor) String() string {
	return strconv.FormatInt(c.MS, 10) + "-" + strconv.FormatInt(c.Seq, 10)
}

// Zero reports whether c is before every event.
func (c Cursor) Zero() bool { return c == Cursor{} }

// Compare orders cursors: negative when c is before o.
func (c Cursor) Compare(o Cursor) int {
	switch {
	case c.MS != o.MS:
		if c.MS < o.MS {
			return -1
		}
		return 1
	case c.Seq != o.Seq:
		if c.Seq < o.Seq {
			return -1
		}
		return 1
	}
	return 0
}
