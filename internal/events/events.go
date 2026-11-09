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
)

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
	Extension  string    `json:"extension,omitempty"`
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
	CDRID       int64  `json:"cdrId,omitempty"`
	RecordingID int64  `json:"recordingId,omitempty"`
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
