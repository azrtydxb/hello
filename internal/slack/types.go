// Package slack posts missed calls and voicemail messages to a Slack channel
// (spec integrations P17, I-52 and the connector table).
//
// A destination is one Slack target: an incoming webhook URL (the app is
// bound to one channel) or a bot token with a channel for chat.postMessage.
// The secret is sealed under HELLO_SECRET_KEY and never leaves the process.
// Events are queued in the event ingest transaction, so a message is never
// lost between the call and the post, and a worker on every control replica
// sends the due ones with the same retry schedule as webhook deliveries and
// a dead-letter state. Posts carry the number and the caller name the
// carrier sent; no recording audio and no SIP data ever goes to Slack.
package slack

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Modes of a destination.
const (
	ModeWebhook = "webhook"
	ModeBot     = "bot"
)

// Post states.
const (
	StatusPending   = "pending"
	StatusRetrying  = "retrying"
	StatusSucceeded = "succeeded"
	StatusDead      = "dead"
)

// Disable reasons.
const (
	ReasonManual   = "manual"
	ReasonFailures = "consecutive_failures"
	ReasonAuth     = "auth_failed"
	ReasonGone     = "destination_gone"
)

// EventTypes are the events a destination may subscribe to.
var EventTypes = []string{events.CallMissed, events.VoicemailReceived}

// MaxDestinations bounds the destinations of one deployment.
const MaxDestinations = 50

// Errors.
var (
	ErrNotFound = errors.New("slack: not found")
	ErrConflict = errors.New("slack: a destination with that name exists")
	// ErrInvalid wraps a rejected input.
	ErrInvalid = errors.New("slack: invalid")
	// ErrRateLimited is a test ping sent too often.
	ErrRateLimited = errors.New("slack: too many tests")
)

// Destination is one Slack target. The sealed secret never leaves the
// process (json:"-").
type Destination struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Channel string `json:"channel"`
	// Events are the event types posted; Extensions limits to events about
	// these extensions (empty is all).
	Events     []string `json:"events"`
	Extensions []string `json:"extensions"`
	Enabled    bool     `json:"enabled"`
	// DisabledReason is why the destination is off; empty while enabled.
	DisabledReason      string     `json:"disabledReason"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt"`
	LastFailureAt       *time.Time `json:"lastFailureAt"`
	LastError           string     `json:"lastError"`
	CreatedBy           string     `json:"createdBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	Pending             int64      `json:"pending"`
	Dead                int64      `json:"dead"`
	// HasSecret is always true on a stored destination.
	HasSecret bool `json:"hasSecret"`

	SecretEnc []byte `json:"-"`
}

// Post is one event owed to one destination.
type Post struct {
	ID            int64      `json:"id"`
	DestinationID int64      `json:"destinationId"`
	EventID       string     `json:"eventId"`
	EventType     string     `json:"eventType"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	LastError     string     `json:"lastError"`
	DeliveredAt   *time.Time `json:"deliveredAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// PostQuery selects posts, newest first.
type PostQuery struct {
	DestinationID int64
	Status        string
	Before        int64
	Limit         int
}

// Claim is a post a worker holds the lease of.
type Claim struct {
	Post
	Mode      string
	Channel   string
	SecretEnc []byte
	// SeriesAttempt is the tries since the last manual retry.
	SeriesAttempt int
	// Payload is the stored event.
	Payload []byte
}

// Outcome classifies one attempt.
type Outcome int

// The outcomes.
const (
	OutcomeSuccess Outcome = iota
	OutcomeRetry
	OutcomeDead
	// OutcomeDisable is dead and switches the destination off (the token is
	// revoked or the webhook is gone).
	OutcomeDisable
)

// Finish records one attempt.
type Finish struct {
	PostID        int64
	Now           time.Time
	Outcome       Outcome
	Error         string
	NextAttemptAt time.Time
	// Reason is the disable reason of an OutcomeDisable.
	Reason      string
	MaxFailures int
}

// Store is the persistence; *PG is PostgreSQL and *Mem serves the tests.
type Store interface {
	NextID(ctx context.Context) (int64, error)
	Create(ctx context.Context, actor string, d Destination) (Destination, error)
	Get(ctx context.Context, id int64) (Destination, error)
	List(ctx context.Context) ([]Destination, error)
	// Update writes name, channel, filters and enabled; a non-nil SecretEnc
	// replaces the secret. resetHealth zeroes the failure counter.
	Update(ctx context.Context, actor string, d Destination, resetHealth bool) (Destination, error)
	Delete(ctx context.Context, actor string, id int64) error

	// Enqueue adds a post for every enabled destination the event matches;
	// tx is the ingest transaction (nil in Mem).
	Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error
	Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Claim, error)
	Finish(ctx context.Context, f Finish) error
	ListPosts(ctx context.Context, q PostQuery) ([]Post, error)
	// Retry makes a dead post due now with a fresh series.
	Retry(ctx context.Context, actor string, id int64, now time.Time) (Post, error)
	Purge(ctx context.Context, succeededBefore, deadBefore time.Time) (int64, error)
}
