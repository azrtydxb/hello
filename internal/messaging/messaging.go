// Package messaging is the pure part of Hello's unified inbox (spec
// messaging-channels): the opt-out keyword engine, text splitting and
// validation, the event sink and the values the store, the API and the SIP
// nodes share. It holds no state and talks to nothing; the conversation
// model lives in internal/store, the HTTP surface in internal/api.
//
// Message text is customer data: nothing here logs it.
package messaging

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Channel kinds and the providers a channel can have. Only sip_trunk exists
// in the first phase; the carrier adapters of later phases add theirs.
const (
	KindSMS     = "sms"
	KindWhats   = "whatsapp"
	KindWebchat = "webchat"

	ProviderSIPTrunk = "sip_trunk"
)

// Conversation states.
const (
	StatusOpen     = "open"
	StatusPending  = "pending"
	StatusSnoozed  = "snoozed"
	StatusResolved = "resolved"
	StatusSpam     = "spam"
)

// Message kinds, directions, senders and delivery states.
const (
	KindMessage = "message"
	KindNote    = "note"
	KindSystem  = "system"

	DirIn  = "in"
	DirOut = "out"

	SenderCustomer = "customer"
	SenderUser     = "user"
	SenderAI       = "ai"
	SenderSystem   = "system"

	DeliveryQueued    = "queued"
	DeliverySending   = "sending"
	DeliverySent      = "sent"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
	DeliveryUnknown   = "unknown"
)

// Limits.
const (
	// MaxBodyBytes is the longest text an agent can send in one go; it is
	// split into parts the carrier path accepts.
	MaxBodyBytes = 2000
	// MaxNoteBytes is the longest internal note.
	MaxNoteBytes = 4000
	// MaxPartBytes is the longest part of a split text: one SIP MESSAGE
	// (store.MaxMessageBytes), so a part is never refused for its size.
	MaxPartBytes = 700
	// MaxTags and MaxTagLen bound a conversation's tags.
	MaxTags   = 10
	MaxTagLen = 32
)

// DefaultOptOutKeywords is the set a body must equal, whole, to opt out
// (Twilio's documented default set).
var DefaultOptOutKeywords = []string{"stop", "stopall", "unsubscribe", "cancel", "end", "quit"}

var (
	optInKeywords = []string{"start", "unstop"}
	helpKeywords  = []string{"help", "info"}
)

// Keyword is what an inbound text means to the opt-out engine.
type Keyword int

// The keyword classes.
const (
	KeywordNone Keyword = iota
	KeywordOptOut
	KeywordOptIn
	KeywordHelp
)

// ClassifyKeyword reads an inbound text. Only the whole message, trimmed,
// case-folded and without surrounding punctuation, counts: "stop", "Stop."
// and "STOP!" opt out, "please stop calling me at 5" does not. optOut is the
// address's keyword list (DefaultOptOutKeywords when empty).
func ClassifyKeyword(body string, optOut []string) Keyword {
	w := strings.ToLower(strings.TrimFunc(body, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}))
	if w == "" || strings.IndexFunc(w, unicode.IsSpace) >= 0 {
		return KeywordNone
	}
	if len(optOut) == 0 {
		optOut = DefaultOptOutKeywords
	}
	for _, k := range optOut {
		if w == strings.ToLower(k) {
			return KeywordOptOut
		}
	}
	for _, k := range optInKeywords {
		if w == k {
			return KeywordOptIn
		}
	}
	for _, k := range helpKeywords {
		if w == k {
			return KeywordHelp
		}
	}
	return KeywordNone
}

// CleanBody validates and normalises a text typed by a person: valid UTF-8,
// no NUL or other control characters (line breaks and tabs stay), trimmed.
// ok is false for an empty text, a text over max bytes or one that is not
// text.
func CleanBody(s string, max int) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	var b strings.Builder
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || len(out) > max {
		return "", false
	}
	return out, true
}

// SplitText cuts s into parts of at most max bytes on UTF-8 boundaries,
// preferring to break after a space or line break in the last quarter of a
// part. A text that fits is returned whole.
func SplitText(s string, max int) []string {
	if max < 4 {
		max = 4
	}
	if len(s) <= max {
		return []string{s}
	}
	var parts []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = max
		}
		if i := strings.LastIndexAny(s[:cut], " \n"); i >= max*3/4 {
			cut = i + 1
		}
		parts = append(parts, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}

// CleanTags normalises a tag list: trimmed, lower-cased, printable, at most
// MaxTagLen characters each, no duplicates, at most MaxTags. ok is false when
// a tag is not acceptable.
func CleanTags(in []string) (out []string, ok bool) {
	out = []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || utf8.RuneCountInString(t) > MaxTagLen || !utf8.ValidString(t) {
			return nil, false
		}
		for _, r := range t {
			if !unicode.IsPrint(r) {
				return nil, false
			}
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, len(out) <= MaxTags
}

// Event is one thing that happened in the inbox, for the integrations event
// pipeline of a later phase. It carries ids and states, never text or
// numbers.
type Event struct {
	Type           string // conversation.created, conversation.assigned, conversation.resolved, message.received, message.sent, message.status, message.optout
	ConversationID int64
	MessageID      int64
	ChannelKind    string
	Direction      string
	Status         string
	AssigneeUserID int64
}

// EventSink receives inbox events. The no-op sink is the default until the
// integrations pipeline lands, so the swap is one line.
type EventSink interface {
	Emit(ctx context.Context, e Event)
}

// NopSink drops every event.
type NopSink struct{}

// Emit implements EventSink.
func (NopSink) Emit(context.Context, Event) {}

// DefaultOptOutText is the confirmation sent when an address has none of its
// own.
const DefaultOptOutText = "You have been unsubscribed and will receive no more messages. Reply START to subscribe again."
