package directory

// The Google Workspace connector (spec I-20, I-21): the Admin SDK Directory
// API over plain HTTPS. Three ways in, all read-only: a service account that
// holds a custom read-only admin role (default), a customer-created OAuth
// client an administrator consented to once (refresh token sealed), and
// domain-wide delegation (a service account impersonating an admin; offered,
// discouraged). The scopes come from an allow-list in code, whatever the
// customer granted. Google has no change-since filter on users.list, so every
// run is a full read.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Google auth modes (I-21).
const (
	GoogleServiceAccount = "service_account"
	GoogleOAuthAdmin     = "oauth_admin"
	GoogleDelegation     = "delegation"
)

// The read-only scopes, the only ones Hello ever asks Google for (T-16).
const (
	ScopeGoogleUsers   = "https://www.googleapis.com/auth/admin.directory.user.readonly"
	ScopeGoogleGroups  = "https://www.googleapis.com/auth/admin.directory.group.readonly"
	ScopeGoogleMembers = "https://www.googleapis.com/auth/admin.directory.group.member.readonly"
)

// GoogleScopes is the allow-list, in the order the consent screen shows them.
var GoogleScopes = []string{ScopeGoogleUsers, ScopeGoogleGroups, ScopeGoogleMembers}

// googleScopeAllowed reports whether s is on the allow-list.
func googleScopeAllowed(s string) bool {
	for _, a := range GoogleScopes {
		if s == a {
			return true
		}
	}
	return false
}

// Google's endpoints.
const (
	googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // G101: an endpoint URL, not a credential
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleAPIBase  = "https://admin.googleapis.com"
)

// GoogleConfig is the stored config of a Google Workspace connector.
type GoogleConfig struct {
	// AuthMode is service_account (default), oauth_admin or delegation.
	AuthMode string `json:"authMode"`
	// Customer is the Workspace customer id (default my_customer); Domain,
	// when set, lists only that domain's users.
	Customer string `json:"customer"`
	Domain   string `json:"domain"`
	// Subject is the administrator a delegated service account impersonates
	// (delegation only).
	Subject string `json:"subject"`
	// ClientID is the customer's OAuth client (oauth_admin only); its secret
	// and the refresh token are the sealed credential.
	ClientID string `json:"clientId"`
	// Query is an optional users.list query (for example orgUnitPath='/Sales').
	Query          string `json:"query"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// Google is the Workspace driver. The endpoint fields are empty in
// production; tests point them at fakes.
type Google struct {
	TokenURL string
	AuthURL  string
	APIBase  string
}

func (g Google) tokenURL() string { return or(g.TokenURL, googleTokenURL) }
func (g Google) authURL() string  { return or(g.AuthURL, googleAuthURL) }
func (g Google) apiBase() string  { return strings.TrimRight(or(g.APIBase, googleAPIBase), "/") }

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var (
	googleCustomerRe = regexp.MustCompile(`^(my_customer|C[0-9a-z]{4,20})$`)
	domainRe         = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)
	googleClientRe   = regexp.MustCompile(`^[0-9A-Za-z._-]{1,200}\.apps\.googleusercontent\.com$`)
)

// Validate implements Driver.
func (Google) Validate(raw json.RawMessage, _ ValidateOptions) (json.RawMessage, error) {
	var c GoogleConfig
	if err := strictJSON(raw, &c); err != nil {
		return nil, fieldErr("config", "not a valid Google Workspace config: unknown or mistyped field")
	}
	var v ValidationError
	add := func(path, msg string) { v.Fields = append(v.Fields, FieldError{"config." + path, msg}) }
	if c.AuthMode == "" {
		c.AuthMode = GoogleServiceAccount
	}
	c.Customer = strings.TrimSpace(c.Customer)
	if c.Customer == "" {
		c.Customer = "my_customer"
	}
	if !googleCustomerRe.MatchString(c.Customer) {
		add("customer", "must be my_customer or a customer id like C01abcd23")
	}
	c.Domain = strings.ToLower(strings.TrimSpace(c.Domain))
	if c.Domain != "" && (!domainRe.MatchString(c.Domain) || len(c.Domain) > 253) {
		add("domain", "not a domain name")
	}
	c.Subject = strings.TrimSpace(c.Subject)
	c.ClientID = strings.TrimSpace(c.ClientID)
	switch c.AuthMode {
	case GoogleServiceAccount:
		if c.Subject != "" {
			add("subject", "only applies to domain-wide delegation; a service account with a read-only admin role needs none")
		}
		if c.ClientID != "" {
			add("clientId", "only applies to oauth_admin")
		}
	case GoogleDelegation:
		if a, err := mail.ParseAddress(c.Subject); err != nil || a.Address != c.Subject {
			add("subject", "required for domain-wide delegation: the e-mail address of the administrator to impersonate")
		}
		if c.ClientID != "" {
			add("clientId", "only applies to oauth_admin")
		}
	case GoogleOAuthAdmin:
		if !googleClientRe.MatchString(c.ClientID) {
			add("clientId", "required: the OAuth client id (ends in .apps.googleusercontent.com)")
		}
		if c.Subject != "" {
			add("subject", "only applies to domain-wide delegation")
		}
	default:
		add("authMode", "must be service_account, oauth_admin or delegation")
	}
	if len(c.Query) > 512 || strings.ContainsAny(c.Query, "\x00\r\n") {
		add("query", "at most 512 characters on one line")
	}
	switch {
	case c.TimeoutSeconds == 0:
		c.TimeoutSeconds = 30
	case c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300:
		add("timeoutSeconds", "must be 1 to 300")
	}
	if len(v.Fields) > 0 {
		return nil, &v
	}
	return json.Marshal(c)
}

// strictJSON decodes raw ({} when empty) refusing unknown fields.
func strictJSON(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// Identity implements Driver: what the credential belongs to.
func (Google) Identity(raw json.RawMessage) string {
	var c GoogleConfig
	_ = json.Unmarshal(raw, &c)
	return "google|" + c.AuthMode + "|" + strings.ToLower(c.Customer) + "|" + strings.ToLower(c.Subject) + "|" + c.ClientID
}

// DefaultMappings implements Driver.
func (Google) DefaultMappings() Mappings {
	return Mappings{
		Username:    AttrMap{Sources: []string{"primaryEmail"}},
		Email:       AttrMap{Sources: []string{"primaryEmail"}},
		DisplayName: AttrMap{Sources: []string{"name.fullName"}},
		Extension:   AttrMap{Sources: []string{"phones[work].value"}, Transforms: []Transform{{Kind: TransformDigits}}},
		Active:      ActiveMap{Attribute: "active", Mode: ActiveBool},
	}
}

// SecretField implements SecretDriver.
func (Google) SecretField() string { return "credential" }

// googleKey is the part of a service-account key file Hello uses. The key's
// own token_uri is ignored: the assertion only ever goes to Google.
type googleKey struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
}

// googleOAuthSecret is the sealed credential of oauth_admin.
type googleOAuthSecret struct {
	ClientSecret string `json:"clientSecret"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

func parseGoogleKey(secret string) (googleKey, error) {
	var k googleKey
	if err := json.Unmarshal([]byte(secret), &k); err != nil {
		return k, errors.New("must be the service account's JSON key file")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return k, errors.New("must be a service account JSON key (type service_account with client_email and private_key)")
	}
	if _, err := parseRSAKey(k.PrivateKey); err != nil {
		return k, errors.New("the key file's private_key is not an RSA key")
	}
	return k, nil
}

// parseGoogleOAuth reads an oauth_admin credential: a JSON object, or the
// bare client secret.
func parseGoogleOAuth(secret string) (googleOAuthSecret, error) {
	var s googleOAuthSecret
	t := strings.TrimSpace(secret)
	if strings.HasPrefix(t, "{") {
		if err := json.Unmarshal([]byte(t), &s); err != nil {
			return s, errors.New(`must be the client secret, or {"clientSecret": ...}`)
		}
	} else {
		s.ClientSecret = t
	}
	if s.ClientSecret == "" || len(s.ClientSecret) > 512 || strings.ContainsAny(s.ClientSecret, " \t\r\n") {
		return s, errors.New("the client secret is missing or malformed")
	}
	return s, nil
}

// CheckSecret implements SecretDriver.
func (Google) CheckSecret(raw json.RawMessage, secret string) error {
	var c GoogleConfig
	_ = json.Unmarshal(raw, &c)
	if len(secret) > 64<<10 {
		return errors.New("too long")
	}
	if c.AuthMode == GoogleOAuthAdmin {
		_, err := parseGoogleOAuth(secret)
		return err
	}
	_, err := parseGoogleKey(secret)
	return err
}

// GoogleClientEmail is the service account a key file belongs to, for display
// (it is not a secret); empty when the credential is not a key file.
func GoogleClientEmail(secret string) string {
	k, err := parseGoogleKey(secret)
	if err != nil {
		return ""
	}
	return k.ClientEmail
}

// scopes are the scopes a fetch asks for: the user scope, and the group
// scopes only when group rules need them (a delegated client must have been
// authorised for exactly what it asks).
func googleScopesFor(groups bool) []string {
	if groups {
		return GoogleScopes
	}
	return []string{ScopeGoogleUsers}
}

// token obtains an access token for cfg with the sealed secret.
func (g Google) token(ctx context.Context, cc *cloudClient, cfg GoogleConfig, secret string, scopes []string, now time.Time) error {
	for _, s := range scopes {
		if !googleScopeAllowed(s) {
			return &Error{Code: CodeInternal, Detail: "refusing a scope outside the read-only allow-list"}
		}
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	switch cfg.AuthMode {
	case GoogleOAuthAdmin:
		s, err := parseGoogleOAuth(secret)
		if err != nil {
			return &Error{Code: CodeAuthFailed, Detail: "the stored OAuth credential is unreadable; enter the client secret again"}
		}
		if s.RefreshToken == "" {
			return &Error{Code: CodeConsentRequired, Detail: "an administrator has not granted consent yet; use Grant consent"}
		}
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}, "client_id": {cfg.ClientID}, "client_secret": {s.ClientSecret}}
		if err := cc.postForm(ctx, g.tokenURL(), form, &tok); err != nil {
			return err
		}
	default:
		k, err := parseGoogleKey(secret)
		if err != nil {
			return &Error{Code: CodeAuthFailed, Detail: "the stored key file is unreadable; upload it again"}
		}
		key, _ := parseRSAKey(k.PrivateKey)
		if now.IsZero() {
			now = time.Now()
		}
		claims := map[string]any{"iss": k.ClientEmail, "scope": strings.Join(scopes, " "), "aud": googleTokenURL,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
		if cfg.AuthMode == GoogleDelegation {
			claims["sub"] = cfg.Subject
		}
		header := map[string]any{}
		if k.PrivateKeyID != "" {
			header["kid"] = k.PrivateKeyID
		}
		assertion, err := signRS256(key, header, claims)
		if err != nil {
			return &Error{Code: CodeAuthFailed, Detail: "could not sign the assertion with the key file"}
		}
		form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
		if err := cc.postForm(ctx, g.tokenURL(), form, &tok); err != nil {
			return err
		}
	}
	if tok.AccessToken == "" {
		return &Error{Code: CodeAuthFailed, Detail: "the token endpoint returned no access token"}
	}
	cc.token = tok.AccessToken
	return nil
}

// googleUserPage is one users.list page.
type googleUserPage struct {
	Users         []map[string]any `json:"users"`
	NextPageToken string           `json:"nextPageToken"`
}

// googleUser turns a Directory API user into the source model: id is the
// stable link (it equals the OIDC sub of the same person), the JSON is
// flattened into the attribute bag, and active is false when the user is
// suspended or archived.
func googleUser(m map[string]any) (SourceUser, bool) {
	id, _ := m["id"].(string)
	if id == "" {
		return SourceUser{}, false
	}
	u := SourceUser{ExternalID: id, Attrs: map[string][]string{}}
	flatten("", m, u.Attrs)
	suspended, _ := m["suspended"].(bool)
	archived, _ := m["archived"].(bool)
	u.Attrs["active"] = []string{boolString(!suspended && !archived)}
	return u, true
}

func (g Google) usersURL(cfg GoogleConfig, max int, page string) string {
	q := url.Values{"projection": {"full"}, "viewType": {"admin_view"}, "maxResults": {strconv.Itoa(max)}}
	if cfg.Domain != "" {
		q.Set("domain", cfg.Domain)
	} else {
		q.Set("customer", cfg.Customer)
	}
	if cfg.Query != "" {
		q.Set("query", cfg.Query)
	}
	if page != "" {
		q.Set("pageToken", page)
	}
	return g.apiBase() + "/admin/directory/v1/users?" + q.Encode()
}

func googleCfg(raw json.RawMessage) (GoogleConfig, error) {
	var c GoogleConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, &Error{Code: CodeInternal, Detail: "the stored config is unreadable"}
	}
	return c, nil
}

// Fetch implements Driver (I-20).
func (g Google) Fetch(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*Result, error) {
	cfg, err := googleCfg(raw)
	if err != nil {
		return nil, err
	}
	cc := newCloudClient(o)
	if err := g.token(ctx, cc, cfg, secret, googleScopesFor(len(o.NeedGroups) > 0), o.Now); err != nil {
		return nil, err
	}
	var users []SourceUser
	page := ""
	for {
		var p googleUserPage
		if err := cc.getJSON(ctx, g.usersURL(cfg, 500, page), &p); err != nil {
			return nil, err
		}
		for _, m := range p.Users {
			if u, ok := googleUser(m); ok {
				users = append(users, u)
			}
		}
		if o.Limit > 0 && len(users) > o.Limit {
			return nil, &Error{Code: CodeUserCap, Detail: "the source returned more users than the cap of " + strconv.Itoa(o.Limit)}
		}
		if p.NextPageToken == "" {
			break
		}
		page = p.NextPageToken
	}
	res := &Result{Users: users}
	if o.Since != nil {
		res.FullReason = FullUnsupported
	}
	if len(o.NeedGroups) > 0 {
		groups, member, err := g.groups(ctx, cc, o.NeedGroups)
		if err != nil {
			return nil, err
		}
		res.Groups = groups
		for i := range res.Users {
			res.Users[i].Groups = member[res.Users[i].ExternalID]
		}
	}
	return res, nil
}

var googleIDRe = regexp.MustCompile(`^[0-9A-Za-z._@-]{1,200}$`)

// groups reads the name and the (nested) user members of each group the rules
// use. A group that no longer exists has no members.
func (g Google) groups(ctx context.Context, cc *cloudClient, ids []string) ([]SourceGroup, map[string][]string, error) {
	member := map[string][]string{}
	var out []SourceGroup
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for _, id := range sorted {
		if !googleIDRe.MatchString(id) {
			continue
		}
		var grp struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		err := cc.getJSON(ctx, g.apiBase()+"/admin/directory/v1/groups/"+url.PathEscape(id), &grp)
		var de *Error
		if errors.As(err, &de) && de.Code == CodeBadFilter {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, SourceGroup{ID: id, Name: grp.Name})
		page := ""
		for {
			q := url.Values{"includeDerivedMembership": {"true"}, "maxResults": {"200"}}
			if page != "" {
				q.Set("pageToken", page)
			}
			var p struct {
				Members []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"members"`
				NextPageToken string `json:"nextPageToken"`
			}
			if err := cc.getJSON(ctx, g.apiBase()+"/admin/directory/v1/groups/"+url.PathEscape(id)+"/members?"+q.Encode(), &p); err != nil {
				return nil, nil, err
			}
			for _, m := range p.Members {
				if m.Type == "USER" && m.ID != "" {
					member[m.ID] = append(member[m.ID], id)
				}
			}
			if p.NextPageToken == "" {
				break
			}
			page = p.NextPageToken
		}
	}
	return out, member, nil
}

// Test implements Driver (I-19): a token and one small page of users and
// groups.
func (g Google) Test(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*TestResult, error) {
	cfg, err := googleCfg(raw)
	if err != nil {
		return nil, err
	}
	cc := newCloudClient(o)
	if err := g.token(ctx, cc, cfg, secret, GoogleScopes, o.Now); err != nil {
		// A service account allowed only the user scope still passes a test
		// without groups.
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeAuthFailed || cfg.AuthMode == GoogleOAuthAdmin {
			return nil, err
		}
		if err := g.token(ctx, cc, cfg, secret, googleScopesFor(false), o.Now); err != nil {
			return nil, err
		}
	}
	var p googleUserPage
	if err := cc.getJSON(ctx, g.usersURL(cfg, 20, ""), &p); err != nil {
		return nil, err
	}
	res := &TestResult{OK: true, Sample: []SampleRow{}, Groups: []SourceGroup{}}
	for _, m := range p.Users {
		if u, ok := googleUser(m); ok {
			res.MatchedUsers++
			res.Sample = append(res.Sample, SampleRow{ExternalID: u.ExternalID, Raw: u.Attrs})
		}
	}
	q := url.Values{"maxResults": {"50"}}
	if cfg.Domain != "" {
		q.Set("domain", cfg.Domain)
	} else {
		q.Set("customer", cfg.Customer)
	}
	var gp struct {
		Groups []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"groups"`
	}
	// Listing groups needs the group scope; without it the test still
	// passes and lists none.
	if err := cc.getJSON(ctx, g.apiBase()+"/admin/directory/v1/groups?"+q.Encode(), &gp); err == nil {
		for _, gr := range gp.Groups {
			res.Groups = append(res.Groups, SourceGroup{ID: gr.ID, Name: gr.Name})
		}
	}
	return res, nil
}

// ConsentURL is where an administrator grants an oauth_admin connector its
// read-only access, once: the authorization request with PKCE (S256) and
// offline access, so Google returns a refresh token.
func (g Google) ConsentURL(raw json.RawMessage, redirectURI, state, verifier string) (string, error) {
	cfg, err := googleCfg(raw)
	if err != nil {
		return "", err
	}
	if cfg.AuthMode != GoogleOAuthAdmin || cfg.ClientID == "" {
		return "", fieldErr("config.authMode", "consent applies to oauth_admin connectors with a client id")
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {cfg.ClientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {strings.Join(GoogleScopes, " ")}, "access_type": {"offline"}, "prompt": {"consent"},
		"include_granted_scopes": {"false"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	return g.authURL() + "?" + q.Encode(), nil
}

// Redeem exchanges a consent code for the refresh token and returns the new
// credential to seal. A grant with any scope outside the allow-list is
// refused: Hello never holds more than read access (T-16).
func (g Google) Redeem(ctx context.Context, raw json.RawMessage, secret, code, verifier, redirectURI string, o FetchOptions) (string, error) {
	cfg, err := googleCfg(raw)
	if err != nil {
		return "", err
	}
	s, err := parseGoogleOAuth(secret)
	if err != nil {
		return "", &Error{Code: CodeAuthFailed, Detail: "set the client secret before granting consent"}
	}
	cc := newCloudClient(o)
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {cfg.ClientID}, "client_secret": {s.ClientSecret},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}}
	if err := cc.postForm(ctx, g.tokenURL(), form, &tok); err != nil {
		return "", err
	}
	for _, sc := range strings.Fields(tok.Scope) {
		if !googleScopeAllowed(sc) {
			return "", &Error{Code: CodeAuthFailed, Detail: "Google granted a scope outside the read-only allow-list; the grant was not stored"}
		}
	}
	if !strings.Contains(" "+tok.Scope+" ", " "+ScopeGoogleUsers+" ") {
		return "", &Error{Code: CodeAuthFailed, Detail: "the read-only user scope was not granted"}
	}
	if tok.RefreshToken == "" {
		return "", &Error{Code: CodeAuthFailed, Detail: "Google returned no refresh token; remove Hello's access in the Google account and grant consent again"}
	}
	s.RefreshToken = tok.RefreshToken
	b, err := json.Marshal(s) //nolint:gosec // G117: the credential is sealed by the store, never logged or returned
	if err != nil {
		return "", err
	}
	return string(b), nil
}
