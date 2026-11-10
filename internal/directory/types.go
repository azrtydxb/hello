// Package directory is the directory sync engine (spec integrations I-1 to
// I-19): connectors yield one source-user model, one engine maps it, plans the
// changes as a diff, applies them and deprovisions by disabling, never by
// deleting. The LDAP connector is the first; later connectors (Google, Entra,
// SCIM) plug into the same Driver and Store.
package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Connector kinds. A kind is a Driver; later phases register more.
const KindLDAP = "ldap"

// Modes (I-5).
const (
	ModeAuthoritative = "authoritative"
	ModeCreateOnly    = "create_only"
	ModeLinkOnly      = "link_only"
)

// Run kinds and statuses (directory_sync_runs).
const (
	RunPreview   = "preview"
	RunApply     = "apply"
	RunScheduled = "scheduled"

	StatusQueued            = "queued"
	StatusRunning           = "running"
	StatusPreview           = "preview"
	StatusSucceeded         = "succeeded"
	StatusFailed            = "failed"
	StatusNeedsConfirmation = "needs_confirmation"
	StatusStale             = "stale"
)

// Change actions (directory_sync_changes).
const (
	ActionCreate   = "create"
	ActionUpdate   = "update"
	ActionLink     = "link"
	ActionAdopt    = "adopt"
	ActionDisable  = "disable"
	ActionEnable   = "enable"
	ActionConflict = "conflict"
	ActionSkip     = "skip"
	ActionError    = "error"
)

// Conflict reasons (I-9), each distinct and reportable.
const (
	ReasonExtensionLinked   = "extension_linked"
	ReasonExtensionKind     = "extension_kind"
	ReasonExtensionDup      = "duplicate_extension"
	ReasonEmailInUse        = "email_in_use"
	ReasonUsernameTaken     = "username_collision"
	ReasonClaimed           = "claimed_by_other_connector"
	ReasonExtensionInvalid  = "extension_invalid"
	ReasonExtensionMissing  = "extension_missing"
	ReasonUsernameInvalid   = "username_invalid"
	ReasonLastAdmin         = "last_admin"
	ReasonInactive          = "inactive_in_source"
	ReasonAbsent            = "absent_from_source"
	ReasonAbsentPending     = "absent_pending"
	ReasonLinkOnlyNoMatch   = "link_only_no_match"
	ReasonLocalAdmin        = "local_admin_not_adopted"
	ReasonAdoptNotConfirmed = "adopt_not_confirmed"
	ReasonAlreadyLinked     = "already_linked"
)

// Classified connector error codes (I-14, I-19).
const (
	CodeAuthFailed   = "auth_failed"
	CodeTLSUntrusted = "tls_untrusted"
	CodeUnreachable  = "unreachable"
	CodeTimeout      = "timeout"
	CodeQuota        = "quota"
	CodeBadFilter    = "bad_filter"
	CodeTLSRequired  = "tls_required"
	CodeEmptyResult  = "empty_result"
	CodeUserCap      = "user_cap_exceeded"
	CodeReplicaLost  = "replica_lost"
	CodeInternal     = "internal"
)

// Errors the engine and stores return.
var (
	ErrNotFound = errors.New("directory: not found")
	// ErrInvalid wraps a rejected input; its message is safe to show.
	ErrInvalid = errors.New("directory: invalid")
	// ErrBusy is a run already in progress for the connector.
	ErrBusy = errors.New("directory: a run is in progress")
	// ErrStale is an apply of a preview that is too old or whose source changed.
	ErrStale = errors.New("directory: stale preview")
	// ErrNotApplicable is an apply or confirm of a run in the wrong state.
	ErrNotApplicable = errors.New("directory: run is not in a state that allows this")
	// ErrLastAdmin is a change that would leave no enabled admin.
	ErrLastAdmin = errors.New("directory: last admin")
	// ErrHasUsers is a connector delete while users are still linked to it.
	ErrHasUsers = errors.New("directory: connector still has linked users")
	// ErrDisabled is a run asked of a disabled connector.
	ErrDisabled = errors.New("directory: connector is disabled")
)

// Error is a classified, scrubbed failure: Detail is short and never echoes a
// credential or a server's response body.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// ValidationError is a rejected input naming each failing field.
type ValidationError struct{ Fields []FieldError }

// FieldError is one failing field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return "directory: validation failed" }
func (e *ValidationError) Unwrap() error { return ErrInvalid }

func fieldErr(path, format string, args ...any) *ValidationError {
	return &ValidationError{Fields: []FieldError{{Path: path, Message: fmt.Sprintf(format, args...)}}}
}

// SourceUser is the one record every connector yields (I-2). ExternalID is the
// stable link (AD objectGUID); Attrs is the raw attribute bag the mappings read,
// keyed by lower-case attribute name; Groups are stable group ids.
type SourceUser struct {
	ExternalID string              `json:"externalId"`
	Attrs      map[string][]string `json:"attrs"`
	Groups     []string            `json:"groups"`
}

// SourceGroup is a directory group: a stable id with its name for display.
type SourceGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Result is what a full fetch returns.
type Result struct {
	Users  []SourceUser
	Groups []SourceGroup
}

// TestResult is the answer to a connection test (I-19). It carries no secret.
type TestResult struct {
	OK bool `json:"ok"`
	// ErrorCode and ErrorDetail classify a failed test (scrubbed, I-14).
	ErrorCode   string `json:"errorCode"`
	ErrorDetail string `json:"errorDetail"`
	// ServerCertSubject and ServerCertExpires describe the certificate the
	// server presented.
	ServerCertSubject string     `json:"serverCertSubject"`
	ServerCertExpires *time.Time `json:"serverCertExpires"`
	// MatchedUsers is how many users the first page matched.
	MatchedUsers int `json:"matchedUsers"`
	// Sample is up to 20 users with their raw and mapped values, the data the
	// mapping editor shows.
	Sample []SampleRow `json:"sample"`
	// Groups lists up to 50 groups under the group base, for the group rules.
	Groups []SourceGroup `json:"groups"`
}

// SampleRow is one user of a mapping preview.
type SampleRow struct {
	ExternalID string              `json:"externalId"`
	Raw        map[string][]string `json:"raw"`
	Mapped     Mapped              `json:"mapped"`
}

// Driver is a connector kind. Implementations hold no per-connector state; the
// stored config and the opened secret are passed in.
type Driver interface {
	// Validate checks and normalises a stored config (defaults filled in).
	Validate(cfg json.RawMessage, o ValidateOptions) (json.RawMessage, error)
	// Identity names what a sealed secret is bound to (host and bind DN): a
	// change of it demands the secret again.
	Identity(cfg json.RawMessage) string
	// DefaultMappings are the suggested mappings for the kind.
	DefaultMappings() Mappings
	// Fetch reads every user (and the groups the rules need). It fails with
	// CodeUserCap when more than limit users match.
	Fetch(ctx context.Context, cfg json.RawMessage, secret string, o FetchOptions) (*Result, error)
	// Test connects, binds and reads one small page.
	Test(ctx context.Context, cfg json.RawMessage, secret string, o FetchOptions) (*TestResult, error)
}

// ValidateOptions are deployment switches a driver's validation honours.
type ValidateOptions struct {
	// AllowPlaintext is HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP (lab only).
	AllowPlaintext bool
	// GroupRules says group mapping is on, so the group search must be set.
	GroupRules bool
}

// FetchOptions bound one fetch.
type FetchOptions struct {
	// Limit is the user cap of the run.
	Limit int
	// NeedGroups lists the group ids the rules refer to; nested membership is
	// resolved only for these.
	NeedGroups []string
	// Attributes are the source attributes the mappings read.
	Attributes []string
	// AllowPlaintext is HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP (lab only).
	AllowPlaintext bool
	// Dial replaces the guarded dialer (tests only).
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Allow are the private ranges the connection may reach
	// (HELLO_INTEGRATIONS_ALLOWED_CIDRS).
	Allow []netip.Prefix
}

// Connector is a directory connection with its mapping, rules and state. The
// sealed secret never leaves the store; HasCredential says one is set.
type Connector struct {
	ID              int64           `json:"id"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	Mode            string          `json:"mode"`
	ScheduleMinutes int             `json:"scheduleMinutes"`
	Config          json.RawMessage `json:"config"`
	Mappings        Mappings        `json:"mappings"`
	GroupRules      GroupRules      `json:"groupRules"`
	Options         Options         `json:"options"`
	// AutoProvision is reserved for range auto-provisioning; it must be off.
	AutoProvision json.RawMessage `json:"autoProvision"`

	HasCredential      bool       `json:"hasCredential"`
	CredentialFP       string     `json:"-"`
	Status             string     `json:"status"`
	LastRunAt          *time.Time `json:"lastRunAt"`
	LastSuccessAt      *time.Time `json:"lastSuccessAt"`
	LastErrorCode      string     `json:"lastErrorCode"`
	LastErrorDetail    string     `json:"lastErrorDetail"`
	ConsecutiveFailure int        `json:"consecutiveFailures"`
	LinkedUsers        int        `json:"linkedUsers"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// Options are the engine settings every kind shares.
type Options struct {
	// AbsenceRuns is how many consecutive complete runs a user may be absent
	// from before it is disabled (1 to 10, default 2).
	AbsenceRuns int `json:"absenceRuns"`
	// DisableDevices disables a disabled user's devices (default on).
	DisableDevices *bool `json:"disableDevices"`
	// AllowAdminFromDirectory lets a group rule grant admin (T-8, default off).
	AllowAdminFromDirectory bool `json:"allowAdminFromDirectory"`
	// RunTimeoutMinutes bounds a run (default 15) and UserCap its size
	// (default 20,000).
	RunTimeoutMinutes int `json:"runTimeoutMinutes"`
	UserCap           int `json:"userCap"`
}

// Defaults.
const (
	DefaultAbsenceRuns = 2
	DefaultRunTimeout  = 15
	DefaultUserCap     = 20000
	MaxUserCap         = 200000
	PreviewTTL         = 15 * time.Minute
	GuardPercent       = 20
	GuardMinimum       = 5
)

func (o Options) absenceRuns() int {
	if o.AbsenceRuns <= 0 {
		return DefaultAbsenceRuns
	}
	return o.AbsenceRuns
}

func (o Options) disableDevices() bool { return o.DisableDevices == nil || *o.DisableDevices }

func (o Options) runTimeout() time.Duration {
	if o.RunTimeoutMinutes <= 0 {
		return DefaultRunTimeout * time.Minute
	}
	return time.Duration(o.RunTimeoutMinutes) * time.Minute
}

func (o Options) userCap() int {
	if o.UserCap <= 0 {
		return DefaultUserCap
	}
	return o.UserCap
}

// Run is one sync run.
type Run struct {
	ID          int64          `json:"id"`
	ConnectorID int64          `json:"connectorId"`
	Kind        string         `json:"kind"`
	Status      string         `json:"status"`
	RequestedBy string         `json:"requestedBy"`
	CreatedAt   time.Time      `json:"createdAt"`
	StartedAt   *time.Time     `json:"startedAt"`
	FinishedAt  *time.Time     `json:"finishedAt"`
	Replica     string         `json:"replica"`
	Counts      map[string]int `json:"counts"`
	DiffHash    string         `json:"diffHash"`
	ErrorCode   string         `json:"errorCode"`
	ErrorDetail string         `json:"errorDetail"`
}

// Diff is one field's before and after. Secrets are never fields.
type Diff struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Change is one row of a run: what the sync does, or did, to one user.
type Change struct {
	ID          int64           `json:"id"`
	RunID       int64           `json:"runId"`
	Action      string          `json:"action"`
	UserID      *int64          `json:"userId"`
	ExternalID  string          `json:"externalId"`
	Username    string          `json:"username"`
	FieldDiffs  map[string]Diff `json:"fieldDiffs"`
	Reason      string          `json:"reason"`
	Overridable bool            `json:"overridable"`
	CreatedAt   time.Time       `json:"createdAt"`

	op *Op
}

// ChangeFilter selects the changes of a run.
type ChangeFilter struct {
	Action string
	After  int64
	Limit  int
}

// Decisions are an administrator's choices on an apply (audited): the
// external ids of the adoptions to confirm and of the conflicts to override.
type Decisions struct {
	Adopt    []string `json:"adopt"`
	AdoptAll bool     `json:"adoptAll"`
	Override []string `json:"override"`
}
