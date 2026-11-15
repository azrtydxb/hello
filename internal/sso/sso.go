// Package sso is single sign-on (spec integrations I-26 to I-30): OIDC
// sign-in through the customer's own Google Workspace or Microsoft Entra
// client, the identities it links to users, the sso_required policy with
// break-glass administrators, and the LDAP bind login of the users an AD
// connector syncs. Every assertion is checked and every failure is closed:
// a sign-in that cannot be proven ends without a session and with a generic
// answer.
package sso

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Providers.
const (
	ProviderGoogle = "google"
	ProviderEntra  = "entra"
)

// ValidProvider reports whether p names a provider.
func ValidProvider(p string) bool { return p == ProviderGoogle || p == ProviderEntra }

// Errors. Their text is never shown to someone signing in; the API answers a
// failed sign-in generically.
var (
	ErrNotFound = errors.New("sso: not found")
	// ErrInvalid wraps a rejected admin input; its message is safe to show.
	ErrInvalid = errors.New("sso: invalid")
	// ErrUnavailable is SSO without HELLO_PUBLIC_URL or without Valkey.
	ErrUnavailable = errors.New("sso: unavailable")
	// ErrProviderOff is a sign-in through a provider that is not enabled.
	ErrProviderOff = errors.New("sso: provider not enabled")
	// ErrState is an unknown, used, expired or foreign state.
	ErrState = errors.New("sso: invalid state")
	// ErrDenied is an IdP error answer or a refused code exchange.
	ErrDenied = errors.New("sso: denied by the identity provider")
	// ErrToken is an ID token that failed a check.
	ErrToken = errors.New("sso: id token refused")
	// ErrNoUser is a valid identity that maps to no user.
	ErrNoUser = errors.New("sso: no matching user")
	// ErrUserDisabled is a disabled user.
	ErrUserDisabled = errors.New("sso: user disabled")
	// ErrLinked is an identity already linked to another user, or a user
	// that already has an identity at the provider.
	ErrLinked = errors.New("sso: identity already linked")
	// ErrNoBreakGlass is turning sso_required on with no enabled break-glass
	// admin (I-29).
	ErrNoBreakGlass = errors.New("sso: no break-glass admin")
	// ErrLastBreakGlass is removing the last enabled break-glass admin.
	ErrLastBreakGlass = errors.New("sso: last break-glass admin")
	// ErrThrottled is a sign-in refused by a rate limit.
	ErrThrottled = errors.New("sso: throttled")
)

// ValidationError names the failing fields of an admin input.
type ValidationError struct{ Fields []FieldError }

// FieldError is one failing field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string { return "sso: validation failed" }
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Provider is one OIDC provider's configuration. The client secret never
// leaves the store; HasClientSecret says one is set.
type Provider struct {
	Provider        string    `json:"provider"`
	Enabled         bool      `json:"enabled"`
	ClientID        string    `json:"clientId"`
	HasClientSecret bool      `json:"hasClientSecret"`
	Tenant          string    `json:"tenant"`
	Domains         []string  `json:"domains"`
	JIT             bool      `json:"jit"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Issuer is the pinned issuer of p (I-26): Google's, or the v2.0 issuer of
// the one Entra tenant.
func (p Provider) Issuer() string {
	if p.Provider == ProviderEntra {
		return "https://login.microsoftonline.com/" + p.Tenant + "/v2.0"
	}
	return "https://accounts.google.com"
}

// PublicProvider is what the login page may know of a provider.
type PublicProvider struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

// DisplayName names a provider on the login page.
func DisplayName(p string) string {
	if p == ProviderEntra {
		return "Microsoft"
	}
	return "Google"
}

// BreakGlassUser is a break-glass admin as the settings list it.
type BreakGlassUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Enabled  bool   `json:"enabled"`
}

// Settings is the SSO configuration page.
type Settings struct {
	Providers   []Provider `json:"providers"`
	SSORequired bool       `json:"ssoRequired"`
	// BreakGlass are the admins who may always use the password form.
	BreakGlass []BreakGlassUser `json:"breakGlass"`
	// RedirectURIs are the callback URLs to register at each provider.
	RedirectURIs map[string]string `json:"redirectUris"`
	// Available is false without HELLO_PUBLIC_URL.
	Available bool `json:"available"`
}

// ProviderInput is a change of one provider; nil keeps a field.
type ProviderInput struct {
	Provider     string    `json:"provider"`
	Enabled      *bool     `json:"enabled"`
	ClientID     *string   `json:"clientId"`
	ClientSecret *string   `json:"clientSecret"`
	Tenant       *string   `json:"tenant"`
	Domains      *[]string `json:"domains"`
	JIT          *bool     `json:"jit"`
}

// SettingsInput is a PUT of the settings.
type SettingsInput struct {
	Providers   []ProviderInput `json:"providers"`
	SSORequired *bool           `json:"ssoRequired"`
}

// SetsSecret reports whether the input sets a client secret (which needs
// the secrets scope).
func (in SettingsInput) SetsSecret() bool {
	for _, p := range in.Providers {
		if p.ClientSecret != nil {
			return true
		}
	}
	return false
}

// Identity is an identity at a provider linked to a user.
type Identity struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"userId"`
	Provider    string     `json:"provider"`
	Subject     string     `json:"subject"`
	Tenant      string     `json:"tenant"`
	Email       string     `json:"email"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

// User is a user as sign-in sees it.
type User struct {
	ID       int64
	Username string
	Role     string
	Enabled  bool
	// ConnectorID, ConnectorKind and ExternalID are set for a synced user;
	// ConnectorTenant is the Entra connector's tenant id.
	ConnectorID     int64
	ConnectorKind   string
	ConnectorTenant string
	ExternalID      string
	BreakGlass      bool
}

// Store is the persistence of SSO.
type Store interface {
	ListProviders(ctx context.Context) ([]Provider, error)
	GetProvider(ctx context.Context, name string) (Provider, error)
	// SaveProvider stores p; a non-nil secret replaces the sealed client
	// secret.
	SaveProvider(ctx context.Context, actor string, p Provider, secret *string) (Provider, error)
	ProviderSecret(ctx context.Context, name string) (string, error)

	SSORequired(ctx context.Context) (bool, error)
	// SetSSORequired turns the policy on or off; on needs an enabled
	// break-glass admin (ErrNoBreakGlass).
	SetSSORequired(ctx context.Context, actor string, on bool) error
	BreakGlassUsers(ctx context.Context) ([]BreakGlassUser, error)

	// IdentityUser is the user linked to (provider, subject).
	IdentityUser(ctx context.Context, provider, subject string) (User, error)
	// SyncedUser is the user a connector of kind synced under externalID.
	SyncedUser(ctx context.Context, kind, externalID string) (User, error)
	// SyncedUsersByEmail are the synced users whose email is email
	// (case-insensitive).
	SyncedUsersByEmail(ctx context.Context, email string) ([]User, error)
	GetUser(ctx context.Context, id int64) (User, error)
	// Link links id to the user, audited; ErrLinked when either side is
	// already linked.
	Link(ctx context.Context, actor string, id Identity) error
	TouchIdentity(ctx context.Context, provider, subject string, at time.Time) error
	ListIdentities(ctx context.Context, userID int64) ([]Identity, error)
	Unlink(ctx context.Context, actor string, userID int64, provider string) error
	// CreateUser makes a just-in-time user (role user, no local password)
	// linked to id.
	CreateUser(ctx context.Context, actor, username, email, display string, id Identity) (User, error)
	Audit(ctx context.Context, actor, action, resource, id string) error
}

// Directory is what LDAP bind login needs of the directory engine.
type Directory interface {
	BindLogin(ctx context.Context, connectorID int64, externalID, password string) error
}

// Service is SSO.
type Service struct {
	Store Store
	// PublicURL is HELLO_PUBLIC_URL; empty makes SSO unavailable.
	PublicURL string
	// States holds the single-use sign-in state (Valkey).
	States StateStore
	// Throttle counts LDAP bind login failures and OIDC start/callback
	// requests (Valkey).
	Throttle Throttle
	// Directory runs LDAP bind logins; nil refuses them.
	Directory Directory
	// HTTP fetches discovery documents, keys and tokens; nil is the
	// SSRF-guarded client.
	HTTP *http.Client
	// IssuerFor replaces the pinned issuer (tests only).
	IssuerFor func(p Provider) string
	Metrics   *Metrics
	Log       *slog.Logger
	// Now replaces the clock in tests.
	Now func() time.Time

	disc discovery
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s *Service) available() bool { return s.PublicURL != "" && s.States != nil }

// RedirectURI is the callback URL of provider p.
func (s *Service) RedirectURI(p string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/auth/oidc/" + p + "/callback"
}

// ConsentRedirectURI is the callback of a directory connector's consent.
func (s *Service) ConsentRedirectURI() string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/directory/oauth/callback"
}

// PublicProviders are the enabled, configured providers for the login page;
// none without HELLO_PUBLIC_URL.
func (s *Service) PublicProviders(ctx context.Context) ([]PublicProvider, error) {
	out := []PublicProvider{}
	if !s.available() {
		return out, nil
	}
	ps, err := s.Store.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Enabled && p.ClientID != "" && p.HasClientSecret {
			out = append(out, PublicProvider{Provider: p.Provider, Name: DisplayName(p.Provider)})
		}
	}
	return out, nil
}

// Settings returns the configuration page.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	st := Settings{Available: s.PublicURL != "", RedirectURIs: map[string]string{}}
	byName := map[string]Provider{}
	ps, err := s.Store.ListProviders(ctx)
	if err != nil {
		return st, err
	}
	for _, p := range ps {
		byName[p.Provider] = p
	}
	for _, name := range []string{ProviderGoogle, ProviderEntra} {
		p, ok := byName[name]
		if !ok {
			p = Provider{Provider: name}
		}
		if p.Domains == nil {
			p.Domains = []string{}
		}
		st.Providers = append(st.Providers, p)
		if s.PublicURL != "" {
			st.RedirectURIs[name] = s.RedirectURI(name)
		}
	}
	if s.PublicURL != "" {
		st.RedirectURIs["directoryConsent"] = s.ConsentRedirectURI()
	}
	if st.SSORequired, err = s.Store.SSORequired(ctx); err != nil {
		return st, err
	}
	if st.BreakGlass, err = s.Store.BreakGlassUsers(ctx); err != nil {
		return st, err
	}
	return st, nil
}

var (
	guidRe      = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	domainRe    = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)
	googleCID   = regexp.MustCompile(`^[0-9A-Za-z._-]{1,200}\.apps\.googleusercontent\.com$`)
	secretValid = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)
)

// PutSettings validates and stores a change of the settings, audited.
func (s *Service) PutSettings(ctx context.Context, actor string, in SettingsInput) (Settings, error) {
	var v ValidationError
	type change struct {
		p      Provider
		secret *string
	}
	var changes []change
	seen := map[string]bool{}
	for i, pi := range in.Providers {
		path := "providers[" + itoa(i) + "]"
		if !ValidProvider(pi.Provider) {
			v.Fields = append(v.Fields, FieldError{path + ".provider", "must be google or entra"})
			continue
		}
		if seen[pi.Provider] {
			v.Fields = append(v.Fields, FieldError{path + ".provider", "listed twice"})
			continue
		}
		seen[pi.Provider] = true
		p, err := s.Store.GetProvider(ctx, pi.Provider)
		if errors.Is(err, ErrNotFound) {
			p, err = Provider{Provider: pi.Provider, Domains: []string{}}, nil
		}
		if err != nil {
			return Settings{}, err
		}
		if pi.Enabled != nil {
			p.Enabled = *pi.Enabled
		}
		if pi.ClientID != nil {
			p.ClientID = strings.TrimSpace(*pi.ClientID)
		}
		if pi.Tenant != nil {
			p.Tenant = strings.ToLower(strings.TrimSpace(*pi.Tenant))
		}
		if pi.Domains != nil {
			p.Domains = []string{}
			for _, d := range *pi.Domains {
				if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
					p.Domains = append(p.Domains, d)
				}
			}
		}
		if pi.JIT != nil {
			p.JIT = *pi.JIT
		}
		add := func(f, msg string) { v.Fields = append(v.Fields, FieldError{path + "." + f, msg}) }
		switch p.Provider {
		case ProviderGoogle:
			if p.ClientID != "" && !googleCID.MatchString(p.ClientID) {
				add("clientId", "must be a Google OAuth client id (ends in .apps.googleusercontent.com)")
			}
			if p.Tenant != "" {
				add("tenant", "only applies to Entra")
			}
			if len(p.Domains) > 20 {
				add("domains", "at most 20 domains")
			}
			for _, d := range p.Domains {
				if !domainRe.MatchString(d) || len(d) > 253 {
					add("domains", "each must be a domain name like example.com")
					break
				}
			}
			// Without hd any Google account would be asked to match: the
			// Workspace domains are what makes the email trustworthy (I-28).
			if p.Enabled && len(p.Domains) == 0 {
				add("domains", "name at least one Google Workspace domain before enabling")
			}
		case ProviderEntra:
			if p.ClientID != "" && !guidRe.MatchString(p.ClientID) {
				add("clientId", "must be the application (client) id, a GUID")
			}
			if strings.EqualFold(p.Tenant, "common") || strings.EqualFold(p.Tenant, "organizations") || strings.EqualFold(p.Tenant, "consumers") {
				add("tenant", "must be your own tenant id; common, organizations and consumers are refused")
			} else if p.Tenant != "" && !guidRe.MatchString(p.Tenant) {
				add("tenant", "must be the directory (tenant) id, a GUID")
			}
			if len(p.Domains) > 0 {
				add("domains", "only applies to Google")
			}
			if p.Enabled && p.Tenant == "" {
				add("tenant", "required before enabling")
			}
		}
		if pi.ClientSecret != nil && !secretValid.MatchString(*pi.ClientSecret) {
			add("clientSecret", "must be 1 to 512 printable characters without spaces")
		}
		if p.Enabled && p.ClientID == "" {
			add("clientId", "required before enabling")
		}
		if p.Enabled && !p.HasClientSecret && pi.ClientSecret == nil {
			add("clientSecret", "required before enabling")
		}
		changes = append(changes, change{p, pi.ClientSecret})
	}
	if len(v.Fields) > 0 {
		return Settings{}, &v
	}
	if in.SSORequired != nil && *in.SSORequired && !s.available() {
		return Settings{}, &ValidationError{Fields: []FieldError{{"ssoRequired", "single sign-on needs HELLO_PUBLIC_URL"}}}
	}
	for _, c := range changes {
		if _, err := s.Store.SaveProvider(ctx, actor, c.p, c.secret); err != nil {
			return Settings{}, err
		}
		s.disc.forget(c.p.Issuer())
	}
	if in.SSORequired != nil {
		if err := s.Store.SetSSORequired(ctx, actor, *in.SSORequired); err != nil {
			return Settings{}, err
		}
	}
	return s.Settings(ctx)
}

// ProviderEnabled is the directory engine's LoginCheck: nil when provider p
// is enabled and configured.
func (s *Service) ProviderEnabled(ctx context.Context, p string) error {
	if !s.available() {
		return ErrUnavailable
	}
	pr, err := s.Store.GetProvider(ctx, p)
	if err != nil {
		return err
	}
	if !pr.Enabled || pr.ClientID == "" || !pr.HasClientSecret {
		return ErrProviderOff
	}
	return nil
}

// Identities lists a user's linked identities.
func (s *Service) Identities(ctx context.Context, userID int64) ([]Identity, error) {
	return s.Store.ListIdentities(ctx, userID)
}

// Unlink removes a user's identity at provider, audited.
func (s *Service) Unlink(ctx context.Context, actor string, userID int64, provider string) error {
	if !ValidProvider(provider) {
		return ErrNotFound
	}
	return s.Store.Unlink(ctx, actor, userID, provider)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
