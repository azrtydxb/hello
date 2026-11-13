// Package provider is the one interface Hello sends customer texts through
// when the carrier is a web API (spec messaging-channels M-1), and the
// registry the carrier adapters join. An adapter is one subpackage; adding a
// carrier is one file and a golden-payload test.
//
// This build is LAN-only (spec Default 6), so the interface has no webhook
// parsing or media fetching yet: those arrive with the public listener. What
// it has is what works from inside the LAN: validate the credentials, list
// the numbers, send a text and read a message's delivery status.
//
// Credentials and message text never reach a log or an error string.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Config is what an adapter needs of a channel: the account and its secret.
// It is sealed at rest and opened only to build an adapter.
type Config struct {
	AccountSID string `json:"accountSid"`
	AuthToken  string `json:"authToken"`
	// MessagingServiceSID, when set, sends through the carrier's sender pool
	// instead of the number the conversation is on.
	MessagingServiceSID string `json:"messagingServiceSid,omitempty"`
}

// Caps are what a carrier can do.
type Caps struct {
	// MaxBody is the longest text in characters the carrier accepts in one
	// message (it segments the rest itself).
	MaxBody int
	// MMS is whether media can be sent (never, in the LAN-only build).
	MMS bool
	// StatusWindow is how long after sending the carrier still answers for
	// a message's status; polling stops after it.
	StatusWindow time.Duration
	// OptOut is who handles STOP: "provider" blocks the recipient itself.
	OptOut string
}

// Outbound is a text to send.
type Outbound struct {
	// ID is Hello's own message id, for the carrier's callback when the
	// public listener exists; unused while status is polled.
	ID   int64
	From string // E.164 number the text leaves from
	To   string // E.164
	Body string
}

// Sent is the carrier's answer to an accepted text.
type Sent struct {
	ProviderID string
	// Status is the normalised state at acceptance: queued, sent, delivered
	// or failed.
	Status string
}

// Delivery states an adapter reports.
const (
	StatusQueued    = "queued"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// State is a message's delivery state as the carrier reports it.
type State struct {
	Status string // queued, sent, delivered or failed
	// ErrorCode is the carrier's error code when the state is failed.
	ErrorCode string
}

// Final reports whether the state can no longer change.
func (s State) Final() bool { return s.Status == StatusDelivered || s.Status == StatusFailed }

// Number is a number the account owns.
type Number struct {
	E164  string `json:"number"`
	Label string `json:"label"`
	SMS   bool   `json:"sms"`
	MMS   bool   `json:"mms"`
	Voice bool   `json:"voice"`
}

// SMSProvider is a carrier reached through a web API.
type SMSProvider interface {
	Kind() string
	Caps() Caps
	// Validate checks that the credentials work and the numbers can be listed.
	Validate(ctx context.Context) error
	// Send hands a text to the carrier. It never retries: an answer that
	// does not say whether the carrier accepted the text is ErrAmbiguous.
	Send(ctx context.Context, m Outbound) (Sent, error)
	// Status reads a sent message's delivery state.
	Status(ctx context.Context, providerID string) (State, error)
	// Numbers lists the numbers of the account.
	Numbers(ctx context.Context) ([]Number, error)
}

// Errors an adapter returns.
var (
	// ErrAuth: the carrier refused the credentials (HTTP 401 or 403). The
	// channel stops sending until an administrator fixes them.
	ErrAuth = errors.New("provider: credentials refused")
	// ErrAmbiguous: the carrier may or may not have accepted the request (a
	// timeout, a dropped connection, a 5xx, an unreadable answer). Hello does
	// not send again.
	ErrAmbiguous = errors.New("provider: outcome unknown")
	// ErrNotFound: the carrier has no such message (it expired).
	ErrNotFound = errors.New("provider: not found")
	// ErrUnknownKind: no adapter is registered for the provider.
	ErrUnknownKind = errors.New("provider: unknown provider")
)

// Rejected is a definite refusal: the carrier did not accept the text and
// will not send it. Code is the carrier's error code (a short token).
type Rejected struct {
	Code string
}

func (e *Rejected) Error() string { return "provider: rejected (" + e.Code + ")" }

// Carrier error codes Hello acts on.
const (
	// CodeOptedOut is Twilio's 21610: the recipient replied STOP to the
	// number, and the carrier blocks every send to them.
	CodeOptedOut = "21610"
	// CodeRateLimited is the carrier's own throttle (HTTP 429).
	CodeRateLimited = "rate_limited"
)

// Factory builds an adapter for a channel's credentials.
type Factory func(cfg Config) (SMSProvider, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds (or replaces) the adapter of kind. Adapters call it from
// init; tests call it to aim an adapter at a fake carrier.
func Register(kind string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	factories[kind] = f
}

// Known reports whether an adapter is registered for kind.
func Known(kind string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[kind]
	return ok
}

// Kinds returns the registered kinds, sorted.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New builds the adapter of kind for cfg.
func New(kind string, cfg Config) (SMSProvider, error) {
	mu.RLock()
	f, ok := factories[kind]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	return f(cfg)
}
