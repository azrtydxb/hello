// Package crm is Hello's CRM connector framework (spec integrations I-47 to
// I-49, I-53, I-54).
//
// A Connector speaks one CRM's API: it finds a contact by phone number,
// writes a call into the CRM and patches it later. The framework around it
// is generic: it keeps the OAuth tokens sealed and refreshed (Tokens), caches
// and bounds lookups so a slow CRM never delays a call (Lookup), turns call
// events into durable logging jobs with retries and a dead-letter state
// (Hook and Worker), mints recording links and renews them (Renewer).
// Connectors register a Factory under their kind, so the later ones (Salesforce,
// Pipedrive, Zoho, ...) plug in without touching the framework.
package crm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Capability flags of a connector (spec I-47).
type Capabilities struct {
	Lookup      bool `json:"lookup"`
	LogCalls    bool `json:"logCalls"`
	Recordings  bool `json:"recordings"`
	ClickToCall bool `json:"clickToCall"`
	ScreenPop   bool `json:"screenPop"`
	Ticket      bool `json:"ticket"`
	// UserScope marks a connector each user signs in to with their own
	// account (Outlook, Google): its lookups search the callee's own
	// contacts, not an organisation's.
	UserScope bool `json:"userScope"`
	// Calendar marks a connector that can say whether the user is busy
	// (the Calendar interface).
	Calendar bool `json:"calendar"`
}

// Contact is a CRM contact found by phone number.
type Contact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Company string `json:"company,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Outcome is how a call ended, in the CRM-neutral terms connectors map onto
// their own status values.
type Outcome string

// The outcomes.
const (
	OutcomeAnswered  Outcome = "answered"
	OutcomeNoAnswer  Outcome = "no_answer"
	OutcomeBusy      Outcome = "busy"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

// CallLog is one call as a connector writes it.
type CallLog struct {
	CallID    string
	Direction string // inbound or outbound
	// Number is the other party's number; From and To are the call's ends.
	Number, From, To string
	Extension        string
	StartedAt        time.Time
	AnsweredAt       time.Time
	EndedAt          time.Time
	// Duration is the talk time of an answered call and the ring time of an
	// unanswered one.
	Duration time.Duration
	Outcome  Outcome
	// Missed is set for a call.missed event.
	Missed bool
	// OwnerID is the CRM user that owns the call; empty leaves it unowned.
	OwnerID string
	// Contact is the contact found for Number, when there is one.
	Contact *Contact
	// RecordingURL is a recording link, empty while there is none.
	RecordingURL string
}

// Conn is what a connector needs to call its CRM: the connector's settings
// and a valid access token.
type Conn struct {
	Config       Config
	AccessToken  string
	ExternalAcct string
}

// Config is a connector's settings as a connector sees them.
type Config struct {
	Kind         string
	ClientID     string
	ClientSecret string
	// Extra holds the connector's own settings (HubSpot's region).
	Extra map[string]string
}

// Tokens are the OAuth tokens a code exchange or a refresh returns.
type Tokens struct {
	Access, Refresh string
	ExpiresAt       time.Time
	Scopes          []string
	// Account is the CRM account the tokens belong to (HubSpot's hub id).
	Account string
}

// Connector is one CRM.
type Connector interface {
	Kind() string
	Capabilities() Capabilities
	// Lookup finds the contact for a number; (nil, nil) means none.
	Lookup(ctx context.Context, c Conn, number string) (*Contact, error)
	// LogCall writes a call and returns the CRM's reference to it.
	LogCall(ctx context.Context, c Conn, l CallLog) (ref string, err error)
	// UpdateCallLog rewrites a call already logged (a recording link, the
	// final duration); it is idempotent.
	UpdateCallLog(ctx context.Context, c Conn, ref string, l CallLog) error
	// ResolveUser maps a Hello user's verified email to the CRM user id;
	// empty means the CRM has no such user.
	ResolveUser(ctx context.Context, c Conn, email string) (string, error)
}

// Busy is what a calendar says about a moment.
type Busy struct {
	// Busy is set while an event the user attends, or an out-of-office
	// block, covers the moment.
	Busy bool
	// Until is when the busy stretch ends (the latest end of the events that
	// overlap it); zero when not busy.
	Until time.Time
}

// Calendar is implemented by connectors that read a user's calendar.
type Calendar interface {
	// Busy reports whether the user is busy at the given moment.
	Busy(ctx context.Context, c Conn, at time.Time) (Busy, error)
}

// OAuth is implemented by connectors that authorise with OAuth 2.
type OAuth interface {
	// AuthURL is where the browser is sent to approve the app.
	AuthURL(cfg Config, redirectURI, state string) string
	// Exchange trades the authorization code for tokens.
	Exchange(ctx context.Context, cfg Config, redirectURI, code string) (Tokens, error)
	// Refresh trades a refresh token for new tokens.
	Refresh(ctx context.Context, cfg Config, refresh string) (Tokens, error)
}

// Factory builds a connector. hc is the SSRF-safe HTTP client.
type Factory func(hc *http.Client) Connector

var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a connector kind; connector packages call it from init or
// the program's main wires it. It panics on a duplicate kind.
func Register(kind string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[kind]; dup {
		panic("crm: duplicate connector kind " + kind)
	}
	registry[kind] = f
}

// Kinds lists the registered connector kinds.
func Kinds() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New builds the connector of a kind; ok is false for an unknown kind.
func New(kind string, hc *http.Client) (Connector, bool) {
	regMu.RLock()
	f, ok := registry[kind]
	regMu.RUnlock()
	if !ok {
		return nil, false
	}
	return f(hc), true
}

// APIError is a failed call to a CRM.
type APIError struct {
	Status int
	// Retry is set for a failure that may pass: a network error, a 5xx, 408
	// or 429.
	Retry bool
	// After is the CRM's Retry-After.
	After time.Duration
	Msg   string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "crm: " + e.Msg
	}
	return fmt.Sprintf("crm: status %d: %s", e.Status, e.Msg)
}

// ErrAuth means the CRM refused the access token (401): the framework
// refreshes once, then marks the connection needs_reauth.
var ErrAuth = errors.New("crm: access token refused")

// ErrNeedsReauth means the connection cannot be refreshed any more.
var ErrNeedsReauth = errors.New("crm: connection needs re-authorisation")

// Status of a connection.
const (
	StatusConnected   = "connected"
	StatusNeedsReauth = "needs_reauth"
	StatusRevoked     = "revoked"
)

// Status of a call log job.
const (
	LogWaiting = "waiting"
	LogPending = "pending"
	LogLogged  = "logged"
	LogUpdate  = "update"
	LogDead    = "dead"
	LogSkipped = "skipped"
)

// Retry schedule of a logging job, the same as webhook deliveries: the first
// attempt is immediate, then 10 s, 30 s, 2 min, 10 min, 1 h, 4 h and 12 h,
// each jittered by plus or minus 20 percent.
var Delays = []time.Duration{
	10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
	time.Hour, 4 * time.Hour, 12 * time.Hour,
}

// MaxAttempts is the attempts a job gets before it is dead.
var MaxAttempts = len(Delays) + 1

// MaxRetryAfter caps how long a CRM's Retry-After may postpone a retry.
const MaxRetryAfter = time.Hour

// RetryDelay is the wait after n failed attempts (n starts at 1); ok is
// false when the job is spent. rnd is uniform in [0, 1) and may be nil.
func RetryDelay(n int, rnd func() float64) (time.Duration, bool) {
	if n < 1 || n > len(Delays) {
		return 0, false
	}
	d := Delays[n-1]
	if rnd != nil {
		d = time.Duration(float64(d) * (0.8 + 0.4*rnd()))
	}
	return d, true
}

func msDuration(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }
