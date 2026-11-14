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
	"strings"
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
	// APIKey is a Telnyx v2 API key; Twilio uses AccountSID and AuthToken.
	APIKey string `json:"apiKey,omitempty"`
	// MessagingProfileID, when set, sends through that Telnyx messaging
	// profile (its sender pool and settings) instead of the number alone.
	MessagingProfileID string `json:"messagingProfileId,omitempty"`
}

// Secret is the part of the configuration that must never be shown or
// logged: the auth token of a Twilio account, the API key of a Telnyx one.
func (c Config) Secret() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return c.AuthToken
}

// Hint is a non-secret tail that tells accounts apart: the last four
// characters of the Twilio account SID or of the Telnyx messaging profile.
func (c Config) Hint() string {
	h := c.AccountSID
	if h == "" {
		h = c.MessagingProfileID
	}
	if len(h) > 4 {
		h = h[len(h)-4:]
	}
	return h
}

// Pooled reports whether texts leave through a carrier sender pool (a Twilio
// Messaging Service or a Telnyx messaging profile).
func (c Config) Pooled() bool { return c.MessagingServiceSID != "" || c.MessagingProfileID != "" }

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
	// ID is the carrier's own identifier of the number; it is used to look
	// the number up in the carrier's registration records.
	ID    string `json:"-"`
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

// Registrar is implemented by an adapter that can read the state of the
// customer's sender registration (spec M-1, "Registration"): A2P 10DLC brand
// and campaign for US long codes, toll-free verification. Hello only reports
// it; the customer registers in the carrier's own console.
type Registrar interface {
	// Registration reads the account's brands, campaigns and verifications
	// and says where each of the given numbers stands. A section the
	// carrier would not answer is named in Unavailable and its numbers are
	// RegUnknown; the call fails only when nothing could be read.
	Registration(ctx context.Context, numbers []Number) (Registration, error)
}

// Registration states, the same for every carrier.
const (
	RegApproved     = "approved"
	RegPending      = "pending"
	RegRejected     = "rejected"
	RegUnregistered = "unregistered"
	RegUnknown      = "unknown"
)

// Number kinds a registration is about.
const (
	// RegKind10DLC is a US long code: it needs a brand and a campaign.
	RegKind10DLC = "10dlc"
	// RegKindTollFree is a US or Canadian toll-free number: it needs
	// toll-free verification.
	RegKindTollFree = "toll_free"
	// RegKindOther is a number no registration API is read for (other
	// countries); its state is always RegUnknown.
	RegKindOther = "other"
)

// Brand is an A2P 10DLC brand.
type Brand struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Campaign is an A2P 10DLC campaign.
type Campaign struct {
	ID      string `json:"id"`
	BrandID string `json:"brandId"`
	UseCase string `json:"useCase"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}

// NumberReg is where one number stands.
type NumberReg struct {
	Number string `json:"number"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Registration is the carrier's view of the customer's sender registration.
type Registration struct {
	Brands    []Brand     `json:"brands"`
	Campaigns []Campaign  `json:"campaigns"`
	Numbers   []NumberReg `json:"numbers"`
	// Unavailable names the sections the carrier did not answer (brands,
	// campaigns, tollfree), for instance because the API key lacks the
	// permission.
	Unavailable []string `json:"unavailable"`
}

// IsTollFree reports whether e164 is a North American toll-free number.
func IsTollFree(e164 string) bool {
	if len(e164) != 12 || !strings.HasPrefix(e164, "+1") {
		return false
	}
	switch e164[2:5] {
	case "800", "833", "844", "855", "866", "877", "888":
		return true
	}
	return false
}

// KindOf says which registration a number needs: toll-free verification for
// a North American toll-free number, 10DLC for any other +1 number, and
// nothing Hello can read for the rest of the world.
func KindOf(e164 string) string {
	switch {
	case IsTollFree(e164):
		return RegKindTollFree
	case strings.HasPrefix(e164, "+1") && len(e164) == 12:
		return RegKind10DLC
	}
	return RegKindOther
}

// Clean bounds free text a carrier sent (a failure reason, a brand name) to
// n printable characters before it is stored or shown.
func Clean(s string, n int) string {
	var b strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(s) {
		if r < ' ' || r == 0x7f {
			continue
		}
		if count >= n {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
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
