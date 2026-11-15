package scim

import (
	"context"
	"errors"
	"time"
)

// Token is a SCIM bearer token's metadata. The token itself is shown once,
// when issued; only its SHA-256 is stored.
type Token struct {
	ID          int64      `json:"id"`
	ConnectorID int64      `json:"connectorId"`
	Hint        string     `json:"hint"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	RevokedAt   *time.Time `json:"revokedAt"`
}

// groupRef is a group a user is in.
type groupRef struct {
	ID         string
	ExternalID string
	Name       string
}

// memberRef is a member of a group.
type memberRef struct {
	ID   string
	Name string
}

// Counts are how many resources a connector holds.
type Counts struct {
	Users        int `json:"users"`
	ActiveUsers  int `json:"activeUsers"`
	DeletedUsers int `json:"deletedUsers"`
	Groups       int `json:"groups"`
}

// Errors the stores return.
var (
	ErrNotFound  = errors.New("scim: not found")
	ErrConflict  = errors.New("scim: name or externalId already used")
	ErrTooMany   = errors.New("scim: too many active tokens")
	ErrWrongKind = errors.New("scim: not a scim connector")
)

// MaxActiveTokens is how many tokens a connector may have at once: two, so a
// token can be replaced without a gap.
const MaxActiveTokens = 2

// Store keeps the pushed resources and the tokens. Resource reads skip
// deleted users unless asked.
type Store interface {
	// Lock serialises the writes of one connector (across replicas); it
	// shares the directory engine's per-connector lock key, so a manual run
	// and a push never interleave.
	Lock(ctx context.Context, connectorID int64) (unlock func(), err error)

	LookupToken(ctx context.Context, hash []byte) (Token, error)
	TouchToken(ctx context.Context, id int64, at time.Time) error
	// CreateToken stores a token's hash; ErrTooMany when the connector has
	// MaxActiveTokens already, ErrWrongKind when it is not a scim one.
	CreateToken(ctx context.Context, actor string, connectorID int64, hash []byte, hint string) (Token, error)
	ListTokens(ctx context.Context, connectorID int64) ([]Token, error)
	RevokeToken(ctx context.Context, actor string, id int64) (Token, error)

	// GetResource returns one resource; a deleted one only when withDeleted.
	GetResource(ctx context.Context, connectorID int64, typ, id string, withDeleted bool) (Resource, error)
	// FindResources returns the live resources of a type, narrowed by the
	// hint when given, oldest first.
	FindResources(ctx context.Context, connectorID int64, typ string, hint *Hint) ([]Resource, error)
	// FindDeleted returns a deleted user with this userName or externalId
	// (case-insensitive name), to be revived.
	FindDeleted(ctx context.Context, connectorID int64, typ, name, externalID string) (*Resource, error)
	// SaveResource inserts (Version 0) or updates a resource; it fails with
	// ErrConflict when a live resource of the connector has its name or
	// externalId, and with ErrNotFound when the version moved (a concurrent
	// write). members, when not nil, replaces a group's members.
	SaveResource(ctx context.Context, r Resource, members []string) (Resource, error)
	// MarkDeleted marks a user deleted (it keeps its row) or deletes a group.
	MarkDeleted(ctx context.Context, connectorID int64, typ, id string) error
	// Members returns the members of groups, in the order added.
	Members(ctx context.Context, groupIDs []string) (map[string][]memberRef, error)
	// GroupsOf returns the groups of users.
	GroupsOf(ctx context.Context, userIDs []string) (map[string][]groupRef, error)
	// Existing returns which of the ids are users of the connector.
	Existing(ctx context.Context, connectorID int64, ids []string) (map[string]bool, error)
	Counts(ctx context.Context, connectorID int64) (Counts, error)
	// LastUsed is when a token of the connector was last used.
	LastUsed(ctx context.Context, connectorID int64) (*time.Time, error)
}
