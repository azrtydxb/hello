// Package proposal holds the in-product AI agent's proposals (spec
// ai-agent S-10 to S-14): changes to Hello's configuration, as allowlisted
// OpenAPI operations validated against the embedded document and shown as a
// before/after diff. Nothing here changes configuration; an operator applies
// a proposal, replayed through Hello's own API with their credentials.
//
// This file is the contract (plan ai-agent, Shared contracts 6) the
// assistant and the detectors build on in parallel; the implementations are
// in allowlist.go, validate.go, diff.go, fingerprint.go, store.go and
// apply.go.
package proposal

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// MaxActions is the most actions one proposal carries.
const MaxActions = 8

// Status is an ai_proposals.status.
type Status string

// The statuses (spec S-10).
const (
	StatusOpen       Status = "open"
	StatusApplied    Status = "applied"
	StatusFailed     Status = "failed"
	StatusStale      Status = "stale"
	StatusDismissed  Status = "dismissed"
	StatusSuperseded Status = "superseded"
)

// Dismissal reasons (spec S-13).
const (
	ReasonNotNeeded = "not_needed"
	ReasonWrong     = "wrong"
	ReasonLater     = "later"
	ReasonOther     = "other"
)

// SourceAssistant is the source of a proposal the chat assistant made; a
// detector's is "finding:<detector>".
const SourceAssistant = "assistant"

// Action is one allowlisted operation. Before and After are filled by
// Validate: Before is the target read through its GET (empty for a create),
// After the target as the action leaves it (the body for PUT and POST, the
// merge for PATCH, empty for DELETE).
type Action struct {
	OperationID string            `json:"operationId"`
	PathParams  map[string]string `json:"pathParams"`
	Body        json.RawMessage   `json:"body,omitempty"`
	Before      json.RawMessage   `json:"before,omitempty"`
	After       json.RawMessage   `json:"after,omitempty"`
	// Destructive marks a delete; References lists what points at the
	// deleted resource, or what the deleted route used, so the diff shows
	// what disappears with it. Both are filled by Validate.
	Destructive bool        `json:"destructive,omitempty"`
	References  []Reference `json:"references,omitempty"`
}

// Reference is one resource connected to the target of a delete.
type Reference struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// Draft is a proposal before it is stored: what the assistant or a
// detector's explanation returns. Ids are UUID strings.
type Draft struct {
	// Source is SourceAssistant or "finding:<detector>".
	Source, Title, Rationale string
	// SessionID or FindingID link the proposal to where it came from.
	SessionID, FindingID *string
	// CreatedBy is the user the proposal was made for; nil for a detector.
	CreatedBy *int64
	Actions   []Action
}

// Failure says why a proposal became failed (spec S-13).
type Failure struct {
	Index   int    `json:"index"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Applied lists the positions of the actions applied before it.
	Applied []int `json:"applied"`
}

// Proposal is a stored proposal.
type Proposal struct {
	ID, Source, Title, Rationale, Fingerprint string
	SessionID, FindingID                      *string
	Actions                                   []Action
	ConfigRevision                            int64
	Status                                    Status
	CreatedBy                                 *int64
	CreatedAt, UpdatedAt                      time.Time
	AppliedBy, DismissedBy                    *int64
	AppliedAt, DismissedAt                    *time.Time
	DismissReason, DismissText                *string
	Failure                                   *Failure
}

// Identity is who a validation reads as: the user the assistant works for
// and its task, replayed under auth.WithAgent so the reads are read-only
// and audited as the assistant. A detector's proposal reads as the user the
// detector's explanation is made for (Task 6 decides which).
type Identity = auth.Agent

// Validator checks a draft before it is stored (spec S-12) and fills each
// action's Before and After. A failure is an error the model is asked to
// fix; an invalid draft is never stored.
type Validator interface {
	Validate(ctx context.Context, ident Identity, d *Draft) error
}

// Store keeps, applies and dismisses proposals.
type Store interface {
	// Upsert stores a validated draft: an open proposal with the same
	// fingerprint is refreshed, an older open proposal for the same targets
	// from the same session or finding is superseded, and a fingerprint
	// dismissed within 7 days is refused with ErrSuppressed. It returns the
	// proposal's id.
	Upsert(ctx context.Context, d Draft) (id string, err error)
	// Apply replays an open proposal's actions in order with the applier's
	// own Cookie and Authorization headers (only those two are copied),
	// answering ErrNotOpen for a proposal that is not open.
	Apply(ctx context.Context, id string, userHeader http.Header) (Proposal, error)
	// Dismiss closes an open proposal with a reason (ReasonNotNeeded,
	// ReasonWrong, ReasonLater or ReasonOther) and optional text of at most
	// 500 characters.
	Dismiss(ctx context.Context, id string, userID int64, reason, text string) error
}

// Errors the HTTP layer maps: ErrNotOpen to 409 proposal_not_open,
// ErrSuppressed is not an error to the user (the proposal is silently not
// made again).
var (
	ErrNotOpen    = errorString("proposal is not open")
	ErrSuppressed = errorString("an equal proposal was dismissed in the last 7 days")
)

type errorString string

func (e errorString) Error() string { return string(e) }
