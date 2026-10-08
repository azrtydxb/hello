package oauth

import (
	"context"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

// Store errors. Implementations return them (wrapped or not) so the server
// can answer the right OAuth error.
var (
	// ErrNotFound: the client, request, grant, service account or secret
	// does not exist (or is not the caller's).
	ErrNotFound = errors.New("oauth: not found")
	// ErrInvalidGrant: the code or refresh token is unknown, expired,
	// spent or revoked.
	ErrInvalidGrant = errors.New("oauth: invalid grant")
	// ErrReused: a spent code or refresh token was presented again; the
	// store has already revoked the grant it belongs to.
	ErrReused = errors.New("oauth: credential reused")
	// ErrTooManySecrets: a service account already has two live secrets.
	ErrTooManySecrets = errors.New("oauth: a service account has at most two live secrets")
	// ErrConflict: a service account with that name exists.
	ErrConflict = errors.New("oauth: conflict")
)

// Client kinds (oauth_clients.kind).
const (
	ClientCIMD    = "cimd"
	ClientDCR     = "dcr"
	ClientService = "service"
)

// Client is a registered or fetched OAuth client, or a service account.
type Client struct {
	ID           string
	Kind         string
	Name         string
	ClientURI    string
	RedirectURIs []string
	// Metadata is the fetched or registered client document.
	Metadata []byte
	// CacheUntil is when a client ID metadata document must be refetched.
	CacheUntil time.Time
	CreatedAt  time.Time
	LastUsedAt *time.Time

	// Service accounts only.
	Description string
	Role        auth.Role
	Scopes      auth.Scopes
	Enabled     bool
	CreatedBy   *int64
	Secrets     []Secret
}

// Secret is a service-account client secret's metadata; only its hash is
// stored.
type Secret struct {
	ID         int64
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// AuthRequest is a pending authorization request (oauth_requests).
type AuthRequest struct {
	ID            string
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scopes        auth.Scopes
	Resources     []string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	// UserID is the approving user, set with the code.
	UserID int64
}

// Grant is a user's consent to a client.
type Grant struct {
	ID         int64
	UserID     int64
	Username   string
	ClientID   string
	ClientName string
	ClientKind string
	Scopes     auth.Scopes
	Resources  []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Token kinds (oauth_tokens.kind).
const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

// Token is an issued access or refresh token; Hash is its SHA-256.
type Token struct {
	Hash      []byte
	Kind      string
	GrantID   int64 // 0 for client credentials
	ClientID  string
	UserID    int64 // 0 for client credentials
	Scopes    auth.Scopes
	Resources []string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ServiceAccountChange is a partial update of a service account.
type ServiceAccountChange struct {
	Description *string
	Role        *auth.Role
	Scopes      *auth.Scopes
	Enabled     *bool
}

// Store is the authorization server's persistence, implemented by
// *store.Store over the 00008_ai_access tables. Every change that is a
// credential, client, grant or consent change runs in one transaction with
// its audit row; the audit row's actor is the actor argument and its via
// is the client of the auth.Actor in ctx, if any.
type Store interface {
	// Client returns a client by id (ErrNotFound).
	Client(ctx context.Context, id string) (Client, error)
	// SaveCIMDClient inserts or refreshes a fetched client ID metadata
	// document.
	SaveCIMDClient(ctx context.Context, c Client) error
	// RegisterClient inserts a dynamically registered public client.
	RegisterClient(ctx context.Context, actor string, c Client) error

	// CreateRequest stores a validated authorization request.
	CreateRequest(ctx context.Context, r AuthRequest) error
	// Request returns an unexpired request still awaiting consent.
	Request(ctx context.Context, id string) (AuthRequest, error)
	// ApproveRequest records the user's consent with scopes and the code's
	// hash, valid until codeExpires; ErrNotFound when the request is gone,
	// expired or already decided.
	ApproveRequest(ctx context.Context, actor, id string, userID int64, scopes auth.Scopes, codeHash []byte, codeExpires time.Time) error
	// DenyRequest deletes a request awaiting consent.
	DenyRequest(ctx context.Context, actor, id string) error

	// RedeemCode spends an authorization code and, in the same
	// transaction, creates the grant and tokens issue returns for it. An
	// error from issue rolls back and leaves the code unspent. A code spent
	// before returns ErrReused after revoking every grant (and its tokens)
	// the request's user gave the client since the request; an unknown or
	// expired code returns ErrInvalidGrant.
	RedeemCode(ctx context.Context, actor string, codeHash []byte,
		issue func(AuthRequest) (Grant, []Token, error)) error
	// Refresh spends a refresh token and stores the tokens issue returns,
	// in one transaction. A spent token returns ErrReused after revoking its
	// grant; an unknown, expired or revoked token, or one whose grant is
	// revoked or older than maxAge, returns ErrInvalidGrant.
	Refresh(ctx context.Context, actor string, hash []byte, maxAge time.Duration,
		issue func(old Token, g Grant) ([]Token, error)) error
	// ClientCredentials checks a service account's secret (live, enabled
	// account) and stores the token issue returns; ErrInvalidGrant when
	// the secret does not authenticate clientID.
	ClientCredentials(ctx context.Context, clientID string, secretHash []byte,
		issue func(Client) (Token, error)) error
	// RevokeToken revokes an access or refresh token (a refresh token
	// revokes its grant); unknown tokens are not an error (RFC 7009).
	RevokeToken(ctx context.Context, actor string, hash []byte) error

	// Grants lists live grants, of userID or of everyone when userID is 0.
	Grants(ctx context.Context, userID int64) ([]Grant, error)
	// RevokeGrant revokes a grant and its tokens; userID 0 means any
	// user's (admin). ErrNotFound when there is no such live grant.
	RevokeGrant(ctx context.Context, actor string, id, userID int64) error

	// ServiceAccounts lists the service accounts with their secrets.
	ServiceAccounts(ctx context.Context) ([]Client, error)
	// ServiceAccount returns one service account (ErrNotFound).
	ServiceAccount(ctx context.Context, id string) (Client, error)
	// CreateServiceAccount inserts c (kind service); ErrConflict when the
	// name is taken.
	CreateServiceAccount(ctx context.Context, actor string, c Client) (Client, error)
	// UpdateServiceAccount applies ch; disabling revokes its tokens.
	UpdateServiceAccount(ctx context.Context, actor, id string, ch ServiceAccountChange) (Client, error)
	// DeleteServiceAccount deletes the account, its secrets and tokens.
	DeleteServiceAccount(ctx context.Context, actor, id string) error
	// AddClientSecret stores a secret's hash; ErrTooManySecrets when two
	// live ones exist.
	AddClientSecret(ctx context.Context, actor, clientID string, hash []byte, expires *time.Time) (Secret, error)
	// RevokeClientSecret revokes a secret and the account's access tokens.
	RevokeClientSecret(ctx context.Context, actor, clientID string, secretID int64) error

	// PruneOAuth deletes requests, tokens and grants expired or revoked
	// before cutoff, and dynamically registered clients that never
	// completed a grant within a day or were unused for 30 days.
	PruneOAuth(ctx context.Context, cutoff time.Time) (int64, error)
}
