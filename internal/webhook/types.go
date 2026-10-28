package webhook

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Delivery states.
const (
	StatusPending   = "pending"
	StatusRetrying  = "retrying"
	StatusSucceeded = "succeeded"
	StatusDead      = "dead"
)

// Endpoint disable reasons.
const (
	ReasonManual   = "manual"
	ReasonFailures = "consecutive_failures"
	ReasonGone     = "endpoint_gone"
)

// Errors the store and service return.
var (
	ErrNotFound = errors.New("webhook: not found")
	// ErrInFlight is a redelivery of a delivery a worker is sending now.
	ErrInFlight = errors.New("webhook: delivery is being sent")
	// ErrInvalid wraps a rejected input; its message is safe to show.
	ErrInvalid = errors.New("webhook: invalid")
)

// SecretWindow is how long the previous secret keeps signing after a
// rotation, so a receiver can switch without dropping events.
const SecretWindow = 24 * time.Hour

// Endpoint is a subscriber URL with its filter and health. The sealed
// secrets never leave the process.
type Endpoint struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Events are exact types or prefix patterns (call.*); empty is all.
	Events []string `json:"events"`
	// Extensions limits the endpoint to events about these extensions;
	// empty is all.
	Extensions   []string `json:"extensions"`
	Enabled      bool     `json:"enabled"`
	AllowPrivate bool     `json:"allowPrivate"`
	// DisabledReason is why the endpoint is off: manual, consecutive_failures
	// or endpoint_gone; empty while enabled.
	DisabledReason      string `json:"disabledReason"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	// PreviousSecretExpires is when the secret before the last rotation stops
	// signing; nil outside a rotation window.
	PreviousSecretExpires *time.Time `json:"previousSecretExpiresAt"`
	LastSuccessAt         *time.Time `json:"lastSuccessAt"`
	LastFailureAt         *time.Time `json:"lastFailureAt"`
	LastStatus            *int       `json:"lastStatus"`
	CreatedBy             string     `json:"createdBy"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
	// Pending counts deliveries waiting or retrying; Dead the dead letters.
	Pending int64 `json:"pending"`
	Dead    int64 `json:"dead"`

	// SecretEnc and PreviousSecretEnc are sealed under the AAD "webhook:<id>".
	SecretEnc         []byte `json:"-"`
	PreviousSecretEnc []byte `json:"-"`
}

// Delivery is one event owed to one endpoint.
type Delivery struct {
	ID         int64  `json:"id"`
	EndpointID int64  `json:"endpointId"`
	EventID    string `json:"eventId"`
	EventType  string `json:"eventType"`
	Status     string `json:"status"`
	// Attempts counts every try, redeliveries included.
	Attempts int `json:"attempts"`
	// NextAttemptAt is when a pending or retrying delivery is next tried.
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	LastStatus    *int       `json:"lastStatus"`
	LastError     string     `json:"lastError"`
	LastLatencyMs *int       `json:"lastLatencyMs"`
	DeliveredAt   *time.Time `json:"deliveredAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Attempt is one entry of a delivery's log.
type Attempt struct {
	Attempt int `json:"attempt"`
	// Ref is the Hello-Delivery header of the attempt.
	Ref       string    `json:"deliveryId"`
	At        time.Time `json:"at"`
	Status    *int      `json:"status"`
	Error     string    `json:"error"`
	LatencyMs int       `json:"latencyMs"`
}

// DeliveryDetail is a delivery with its attempt log and, while the event is
// still stored, the event it carries.
type DeliveryDetail struct {
	Delivery
	Log []Attempt `json:"log"`
	// Event is the payload of the event (nil once the event is purged).
	Event []byte `json:"-"`
}

// DeliveryQuery selects deliveries, newest first.
type DeliveryQuery struct {
	// EndpointID limits to one endpoint (0: all).
	EndpointID int64
	// Status limits to one state ("" : all).
	Status string
	// Before returns deliveries with a smaller id (0: from the newest).
	Before int64
	Limit  int
}

// Claim is a delivery a worker holds the lease of.
type Claim struct {
	Delivery
	// SeriesAttempt is the tries made since the last redelivery.
	SeriesAttempt int
	URL           string
	AllowPrivate  bool
	// Sealed secrets and the end of the previous one's window.
	SecretEnc, PreviousSecretEnc []byte
	PreviousSecretExpires        *time.Time
	// Payload is the event as stored; nil when it has been purged.
	Payload []byte
}

// Finish records the outcome of one attempt.
type Finish struct {
	DeliveryID int64
	Now        time.Time
	// Ref is the Hello-Delivery header the attempt carried.
	Ref       string
	Outcome   Outcome
	Status    int // 0 when no response came
	Error     string
	LatencyMs int
	// NextAttemptAt is when a retried delivery is tried again.
	NextAttemptAt time.Time
	// MaxFailures is the consecutive failures that disable the endpoint.
	MaxFailures int
	// Internal marks a failure that is Hello's, not the receiver's (the
	// event was purged, the secret cannot be opened): the endpoint's failure
	// count and health are left alone.
	Internal bool
}

// FinishResult tells the dispatcher what the store did beyond the delivery.
type FinishResult struct {
	// Disabled is set when this attempt disabled the endpoint; Reason says
	// why. Endpoint is the endpoint as it is now.
	Disabled bool
	Reason   string
	Endpoint Endpoint
}

// PurgeResult counts what a retention pass removed.
type PurgeResult struct {
	Succeeded, Dead, Dropped int64
}

// Counts are the gauges the metrics report.
type Counts struct {
	Pending, Dead, DisabledEndpoints int64
}

// Store is the persistence of endpoints and deliveries; *PG is the
// PostgreSQL implementation and *Mem an in-memory one for tests.
type Store interface {
	// NextEndpointID reserves an endpoint id, so the secret can be sealed
	// under its AAD before the row exists.
	NextEndpointID(ctx context.Context) (int64, error)
	// CreateEndpoint inserts e (its ID from NextEndpointID). When an endpoint
	// with the same URL and events exists it returns that one and false.
	CreateEndpoint(ctx context.Context, actor string, e Endpoint) (Endpoint, bool, error)
	GetEndpoint(ctx context.Context, id int64) (Endpoint, error)
	ListEndpoints(ctx context.Context) ([]Endpoint, error)
	// UpdateEndpoint writes name, URL, filters, enabled, allowPrivate and
	// disabledReason; resetHealth also zeroes the failure counter.
	UpdateEndpoint(ctx context.Context, actor string, e Endpoint, resetHealth bool) (Endpoint, error)
	// SetSecrets stores the sealed current and previous secrets.
	SetSecrets(ctx context.Context, actor string, id int64, current, previous []byte, previousExpires *time.Time) (Endpoint, error)
	DeleteEndpoint(ctx context.Context, actor string, id int64) error

	// Enqueue adds a delivery for every enabled endpoint the event matches;
	// the PostgreSQL store does it in the ingest transaction tx.
	Enqueue(ctx context.Context, tx *sql.Tx, e events.Entry) error
	// Claim leases up to limit due deliveries for lease, at most perEndpoint
	// in flight per endpoint across all callers.
	Claim(ctx context.Context, now time.Time, limit, perEndpoint int, lease time.Duration) ([]Claim, error)
	Finish(ctx context.Context, f Finish) (FinishResult, error)

	ListDeliveries(ctx context.Context, q DeliveryQuery) ([]Delivery, error)
	GetDelivery(ctx context.Context, id int64) (DeliveryDetail, error)
	// Redeliver makes a delivery due now with a fresh retry series.
	Redeliver(ctx context.Context, actor string, id int64, now time.Time) (Delivery, error)
	// RedeliverDead does that for every dead letter of an endpoint.
	RedeliverDead(ctx context.Context, actor string, endpointID int64, now time.Time) (int64, error)

	// Purge deletes finished deliveries older than the cutoffs and the
	// oldest dead letters beyond deadCap per endpoint.
	Purge(ctx context.Context, succeededBefore, deadBefore time.Time, deadCap int) (PurgeResult, error)
	Counts(ctx context.Context) (Counts, error)
}
