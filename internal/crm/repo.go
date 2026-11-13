package crm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is a missing connector, connection or log.
var ErrNotFound = errors.New("crm: not found")

// ErrConflict is a duplicate (a connector name already taken, a connection
// that exists).
var ErrConflict = errors.New("crm: already exists")

// ConnectorRow is a connector as stored. The client secret is never part of
// it.
type ConnectorRow struct {
	ID            int64             `json:"id"`
	Kind          string            `json:"kind"`
	Name          string            `json:"name"`
	Enabled       bool              `json:"enabled"`
	Config        map[string]string `json:"config"`
	HasSecret     bool              `json:"hasClientSecret"`
	Recordings    bool              `json:"recordings"`
	LinkTTLHours  int               `json:"linkTtlHours"`
	LogMissed     bool              `json:"logMissed"`
	FallbackOwner string            `json:"fallbackOwner"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// ConnectorInput creates or replaces a connector's settings.
type ConnectorInput struct {
	Kind          string
	Name          string
	Enabled       bool
	Config        map[string]string
	Recordings    bool
	LinkTTLHours  int
	LogMissed     bool
	FallbackOwner string
}

// ConnectionRow is a connection without its tokens.
type ConnectionRow struct {
	ID              int64
	ConnectorID     int64
	Scope           string
	UserID          int64
	ExpiresAt       time.Time
	GrantedScopes   string
	ExternalAccount string
	Status          string
	LastError       string
	UpdatedAt       time.Time
}

// LogRow is one call logging job.
type LogRow struct {
	ID             int64           `json:"id"`
	ConnectorID    int64           `json:"connectorId"`
	CallID         string          `json:"callId"`
	Direction      string          `json:"direction"`
	Status         string          `json:"status"`
	ExternalID     string          `json:"externalId"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  time.Time       `json:"nextAttemptAt"`
	LastError      string          `json:"lastError"`
	Payload        json.RawMessage `json:"-"`
	EventType      string          `json:"eventType"`
	OwnerExtension string          `json:"ownerExtension"`
	RecordingID    int64           `json:"recordingId,omitempty"`
	// State is what a multi-step connector already did for this call (see
	// PartialError); never shown in the API.
	State         string     `json:"-"`
	LinkExpiresAt *time.Time `json:"linkExpiresAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// LogJob is a call to log, as the event hook enqueues it.
type LogJob struct {
	CallID         string
	Direction      string
	EventType      string
	Payload        []byte
	OwnerExtension string
}

// LogResult is how a worker settles a job.
type LogResult struct {
	Status        string
	ExternalID    string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	LinkExpiresAt *time.Time
	// ClearLink drops link_expires_at (a recording that no longer applies).
	ClearLink bool
	// State replaces the job's saved connector state when not nil (an empty
	// string clears it).
	State *string
	// RecordingSeen is the recording id the attempt worked from: a recording
	// that arrived meanwhile turns a logged job into an update.
	RecordingSeen int64
}

// Stat is a count of logging jobs or connections by status, for the gauges.
type Stat struct {
	Connector string
	Kind      string // "logs" or "connections"
	Status    string
	N         int
}

// UserRef is a Hello user with the extension their calls belong to.
type UserRef struct {
	ID        int64
	Username  string
	Email     string
	Extension string
}

// Mapping is one Hello user's CRM user.
type Mapping struct {
	UserID         int64
	ExternalUserID string
	Source         string
}

// Repo is the CRM persistence; PG is the PostgreSQL implementation and Mem
// serves the tests. The tx argument of the hook methods is the event ingest
// transaction (nil in Mem).
type Repo interface {
	ListConnectors(ctx context.Context) ([]ConnectorRow, error)
	GetConnector(ctx context.Context, id int64) (ConnectorRow, error)
	CreateConnector(ctx context.Context, in ConnectorInput, clientSecret string) (ConnectorRow, error)
	// UpdateConnector replaces the settings; a nil secret keeps the stored
	// one.
	UpdateConnector(ctx context.Context, id int64, in ConnectorInput, clientSecret *string) (ConnectorRow, error)
	DeleteConnector(ctx context.Context, id int64) error
	ClientSecret(ctx context.Context, id int64) (string, error)

	// OrgConnection returns the connector's org connection and its tokens.
	OrgConnection(ctx context.Context, connectorID int64) (ConnectionRow, Tokens, error)
	SaveOrgConnection(ctx context.Context, connectorID int64, t Tokens) (ConnectionRow, error)
	SetConnectionStatus(ctx context.Context, connectionID int64, status, lastError string) error
	DeleteOrgConnection(ctx context.Context, connectorID int64) error

	NewState(ctx context.Context, connectorID int64, actor string, ttl time.Duration) (string, error)
	TakeState(ctx context.Context, state string) (connectorID int64, actor string, err error)

	// Hook side.
	ActiveConnectors(ctx context.Context, tx *sql.Tx) ([]ConnectorRow, error)
	UpsertCall(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob) error
	AttachRecording(ctx context.Context, tx *sql.Tx, connectorID int64, callID string, recordingID int64) error
	AttachEngagement(ctx context.Context, connectorID int64, callID, ref string) (adopted bool, err error)
	Skip(ctx context.Context, tx *sql.Tx, connectorID int64, j LogJob, reason string) error

	// Worker side.
	ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]LogRow, error)
	Finish(ctx context.Context, id int64, r LogResult) error
	MarkRenewals(ctx context.Context, now time.Time, within, maxAge time.Duration) (int, error)
	PurgeStates(ctx context.Context, now time.Time) error

	// Stats counts jobs and connections by connector and status.
	Stats(ctx context.Context) ([]Stat, error)

	// Users.
	UserForExtension(ctx context.Context, ext string) (UserRef, bool, error)
	Users(ctx context.Context) ([]UserRef, error)
	// AdminEmails are the addresses of the administrators, for the
	// needs-reauth notice.
	AdminEmails(ctx context.Context) ([]string, error)
	Mappings(ctx context.Context, connectorID int64) ([]Mapping, error)
	SetMapping(ctx context.Context, connectorID, userID int64, externalUserID, source string) error
	DeleteMapping(ctx context.Context, connectorID, userID int64) error

	// Logs.
	ListLogs(ctx context.Context, connectorID int64, status string, limit int, beforeID int64) ([]LogRow, error)
	GetLog(ctx context.Context, id int64) (LogRow, error)
	RetryLog(ctx context.Context, id int64) error
	// CallInvolves reports whether an event of the call concerns one of the
	// extensions.
	CallInvolves(ctx context.Context, callID string, exts []string) (bool, error)
	// Audit records a configuration change or a connection event.
	Audit(ctx context.Context, actor, action, resource, resourceID string) error
}
