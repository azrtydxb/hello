package directory

// The Microsoft Entra ID connector (spec I-22): Microsoft Graph v1.0 with the
// OAuth 2.0 client-credentials grant, by client secret or by certificate (a
// signed client assertion whose private key is the sealed credential). A full
// run lists /users; an incremental run follows the delta link of the last
// one. The tenant is pinned (common, organizations and consumers are
// refused), paging links must stay on Graph, and 429 with Retry-After is
// honoured within the run's budget.

import (
	"context"
	"crypto/sha1" //nolint:gosec // G505: x5t is the SHA-1 thumbprint Entra's assertion format names, not a security hash
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entra credential kinds.
const (
	EntraSecret      = "secret"
	EntraCertificate = "certificate"
)

// Entra's endpoints and the only scope Hello asks for: the application
// permissions granted to the app (User.Read.All, GroupMember.Read.All).
const (
	entraLoginBase = "https://login.microsoftonline.com"
	entraGraphBase = "https://graph.microsoft.com"
	EntraScope     = "https://graph.microsoft.com/.default"
)

// entraSelect are the user properties always read; mappings add theirs.
var entraSelect = []string{"id", "userPrincipalName", "mail", "displayName", "givenName", "surname", "accountEnabled",
	"businessPhones", "mobilePhone", "employeeId", "userType", "jobTitle", "department", "onPremisesSamAccountName"}

// EntraConfig is the stored config of an Entra connector.
type EntraConfig struct {
	// TenantID is the directory (tenant) id, a GUID, or a verified domain.
	TenantID string `json:"tenantId"`
	ClientID string `json:"clientId"`
	// AuthMethod is secret (default) or certificate.
	AuthMethod string `json:"authMethod"`
	// CertificatePEM is the public certificate uploaded to the app
	// registration (certificate only; not a secret).
	CertificatePEM string `json:"certificatePem"`
	// IncludeGuests lists guest (B2B) accounts too; off by default.
	IncludeGuests  bool `json:"includeGuests"`
	TimeoutSeconds int  `json:"timeoutSeconds"`
}

// Entra is the Entra driver; the endpoint fields are for tests.
type Entra struct {
	LoginBase string
	GraphBase string
}

func (e Entra) loginBase() string { return strings.TrimRight(or(e.LoginBase, entraLoginBase), "/") }
func (e Entra) graphBase() string { return strings.TrimRight(or(e.GraphBase, entraGraphBase), "/") }

var (
	guidRe = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// propRe is a Graph property name a mapping may select.
	propRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

// IsMultiTenant reports a tenant segment that is not one tenant.
func IsMultiTenant(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "common", "organizations", "consumers":
		return true
	}
	return false
}

// Validate implements Driver.
func (Entra) Validate(raw json.RawMessage, _ ValidateOptions) (json.RawMessage, error) {
	var c EntraConfig
	if err := strictJSON(raw, &c); err != nil {
		return nil, fieldErr("config", "not a valid Entra config: unknown or mistyped field")
	}
	var v ValidationError
	add := func(path, msg string) { v.Fields = append(v.Fields, FieldError{"config." + path, msg}) }
	c.TenantID = strings.ToLower(strings.TrimSpace(c.TenantID))
	switch {
	case IsMultiTenant(c.TenantID):
		add("tenantId", "must be your own tenant; common, organizations and consumers are refused")
	case !guidRe.MatchString(c.TenantID) && !domainRe.MatchString(c.TenantID):
		add("tenantId", "required: the directory (tenant) id or a verified domain")
	}
	c.ClientID = strings.ToLower(strings.TrimSpace(c.ClientID))
	if !guidRe.MatchString(c.ClientID) {
		add("clientId", "required: the application (client) id, a GUID")
	}
	if c.AuthMethod == "" {
		c.AuthMethod = EntraSecret
	}
	switch c.AuthMethod {
	case EntraSecret:
		if c.CertificatePEM != "" {
			add("certificatePem", "only applies to certificate authentication")
		}
	case EntraCertificate:
		if _, err := parseCert(c.CertificatePEM); err != nil {
			add("certificatePem", "required: the PEM certificate uploaded to the app registration")
		}
	default:
		add("authMethod", "must be secret or certificate")
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

func parseCert(p string) (*x509.Certificate, error) {
	if len(p) > 64<<10 {
		return nil, errors.New("too long")
	}
	block, _ := pem.Decode([]byte(p))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// Identity implements Driver.
func (Entra) Identity(raw json.RawMessage) string {
	var c EntraConfig
	_ = json.Unmarshal(raw, &c)
	return "entra|" + strings.ToLower(c.TenantID) + "|" + strings.ToLower(c.ClientID) + "|" + c.AuthMethod
}

// DefaultMappings implements Driver.
func (Entra) DefaultMappings() Mappings {
	return Mappings{
		Username:    AttrMap{Sources: []string{"userPrincipalName"}},
		Email:       AttrMap{Sources: []string{"mail", "userPrincipalName"}},
		DisplayName: AttrMap{Sources: []string{"displayName"}},
		Extension:   AttrMap{Sources: []string{"businessPhones"}, Transforms: []Transform{{Kind: TransformDigits}}},
		Active:      ActiveMap{Attribute: "accountEnabled", Mode: ActiveBool},
	}
}

// SecretField implements SecretDriver.
func (Entra) SecretField() string { return "credential" }

// CheckSecret implements SecretDriver: a client secret, or the certificate's
// RSA private key, which must belong to the configured certificate.
func (Entra) CheckSecret(raw json.RawMessage, secret string) error {
	var c EntraConfig
	_ = json.Unmarshal(raw, &c)
	if len(secret) > 64<<10 {
		return errors.New("too long")
	}
	if c.AuthMethod != EntraCertificate {
		if strings.ContainsAny(secret, "\r\n") || len(secret) > 512 {
			return errors.New("must be the client secret's value (one line)")
		}
		return nil
	}
	key, err := parseRSAKey(secret)
	if err != nil {
		return errors.New("must be the certificate's PEM RSA private key")
	}
	cert, err := parseCert(c.CertificatePEM)
	if err != nil {
		return errors.New("set the certificate first")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return errors.New("the private key does not belong to the configured certificate")
	}
	return nil
}

func entraCfg(raw json.RawMessage) (EntraConfig, error) {
	var c EntraConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, &Error{Code: CodeInternal, Detail: "the stored config is unreadable"}
	}
	return c, nil
}

// tokenURL is the pinned token endpoint of the tenant.
func (e Entra) tokenURL(cfg EntraConfig) string {
	return e.loginBase() + "/" + url.PathEscape(cfg.TenantID) + "/oauth2/v2.0/token"
}

// token runs the client-credentials grant.
func (e Entra) token(ctx context.Context, cc *cloudClient, cfg EntraConfig, secret string, now time.Time) error {
	if IsMultiTenant(cfg.TenantID) {
		return &Error{Code: CodeAuthFailed, Detail: "a multi-tenant authority is refused"}
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {cfg.ClientID}, "scope": {EntraScope}}
	if cfg.AuthMethod == EntraCertificate {
		assertion, err := entraAssertion(cfg, secret, e.tokenURL(cfg), now)
		if err != nil {
			return err
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", assertion)
	} else {
		if secret == "" {
			return &Error{Code: CodeAuthFailed, Detail: "no client secret is set"}
		}
		form.Set("client_secret", secret)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := cc.postForm(ctx, e.tokenURL(cfg), form, &tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return &Error{Code: CodeAuthFailed, Detail: "the token endpoint returned no access token"}
	}
	cc.token = tok.AccessToken
	return nil
}

// entraAssertion is the certificate credential: a JWT signed with the
// private key, naming the certificate by its SHA-1 (x5t) and SHA-256
// (x5t#S256) thumbprints, for the token endpoint audience.
func entraAssertion(cfg EntraConfig, secret, aud string, now time.Time) (string, error) {
	key, err := parseRSAKey(secret)
	if err != nil {
		return "", &Error{Code: CodeAuthFailed, Detail: "the stored private key is unreadable; upload it again"}
	}
	cert, err := parseCert(cfg.CertificatePEM)
	if err != nil {
		return "", &Error{Code: CodeAuthFailed, Detail: "the configured certificate is unreadable"}
	}
	if now.IsZero() {
		now = time.Now()
	}
	s1 := sha1.Sum(cert.Raw) //nolint:gosec // G401: the x5t header is defined as a SHA-1 thumbprint
	s256 := sha256.Sum256(cert.Raw)
	enc := base64.RawURLEncoding
	header := map[string]any{"x5t": enc.EncodeToString(s1[:]), "x5t#S256": enc.EncodeToString(s256[:])}
	claims := map[string]any{"aud": aud, "iss": cfg.ClientID, "sub": cfg.ClientID, "jti": randomID(),
		"nbf": now.Unix(), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	a, err := signRS256(key, header, claims)
	if err != nil {
		return "", &Error{Code: CodeAuthFailed, Detail: "could not sign the client assertion"}
	}
	return a, nil
}

// selectList is the $select of a user read: the defaults and the top-level
// property of every mapped attribute.
func selectList(attrs []string) string {
	seen := map[string]bool{}
	var out []string
	for _, a := range entraSelect {
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	for _, a := range attrs {
		top := a
		if i := strings.IndexAny(top, ".["); i >= 0 {
			top = top[:i]
		}
		if propRe.MatchString(top) && !seen[strings.ToLower(top)] {
			seen[strings.ToLower(top)] = true
			out = append(out, top)
		}
	}
	return strings.Join(out, ",")
}

// graphPage is a Graph collection page.
type graphPage struct {
	Value     []map[string]any `json:"value"`
	NextLink  string           `json:"@odata.nextLink"`
	DeltaLink string           `json:"@odata.deltaLink"`
}

// entraUser turns a Graph user into the source model; ok is false for one
// without an id and, unless guests are included, for a guest.
func entraUser(m map[string]any, guests bool) (SourceUser, bool) {
	id, _ := m["id"].(string)
	if id == "" {
		return SourceUser{}, false
	}
	if t, _ := m["userType"].(string); !guests && strings.EqualFold(t, "Guest") {
		return SourceUser{}, false
	}
	u := SourceUser{ExternalID: strings.ToLower(id), Attrs: map[string][]string{}}
	flatten("", m, u.Attrs)
	return u, true
}

// pages reads a collection from first, following nextLinks on Graph only,
// and returns the delta link the last page carried.
func (e Entra) pages(ctx context.Context, cc *cloudClient, first string, each func(map[string]any) error) (string, error) {
	next := first
	for next != "" {
		if !sameOrigin(e.graphBase(), next) {
			return "", &Error{Code: CodeUnreachable, Detail: "Graph returned a paging link to another host; refused"}
		}
		var p graphPage
		if err := cc.getJSON(ctx, next, &p); err != nil {
			return "", err
		}
		for _, m := range p.Value {
			if err := each(m); err != nil {
				return "", err
			}
		}
		if p.DeltaLink != "" {
			return p.DeltaLink, nil
		}
		next = p.NextLink
	}
	return "", nil
}

// Delta mark method.
const markDelta = "delta"

// maxDeltaChanges is the largest incremental change set re-read user by
// user; more and a full read is cheaper.
const maxDeltaChanges = 1000

// Fetch implements Driver (I-22).
func (e Entra) Fetch(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*Result, error) {
	cfg, err := entraCfg(raw)
	if err != nil {
		return nil, err
	}
	cc := newCloudClient(o)
	if err := e.token(ctx, cc, cfg, secret, o.Now); err != nil {
		return nil, err
	}
	var (
		groups  []SourceGroup
		member  map[string][]string
		grpHash string
	)
	if len(o.NeedGroups) > 0 {
		if groups, member, err = e.groups(ctx, cc, o.NeedGroups); err != nil {
			return nil, err
		}
		grpHash = membershipHash(member)
	}
	attach := func(us []SourceUser) {
		for i := range us {
			us[i].Groups = member[us[i].ExternalID]
		}
	}
	reason := ""
	if o.Since != nil {
		res, why, err := e.incremental(ctx, cc, cfg, o, grpHash)
		if err != nil {
			return nil, err
		}
		if res != nil {
			attach(res.Users)
			res.Groups = groups
			return res, nil
		}
		reason = why
	}
	// The mark is taken before the read, so a change made during it is in
	// the next delta.
	mark, err := e.latestDelta(ctx, cc)
	if err != nil {
		return nil, err
	}
	var users []SourceUser
	q := url.Values{"$select": {selectList(o.Attributes)}, "$top": {"999"}}
	_, err = e.pages(ctx, cc, e.graphBase()+"/v1.0/users?"+q.Encode(), func(m map[string]any) error {
		if u, ok := entraUser(m, cfg.IncludeGuests); ok {
			users = append(users, u)
			if o.Limit > 0 && len(users) > o.Limit {
				return &Error{Code: CodeUserCap, Detail: "the source returned more users than the cap of " + strconv.Itoa(o.Limit)}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	attach(users)
	res := &Result{Users: users, Groups: groups, FullReason: reason}
	if mark != "" {
		res.Mark = &SyncMark{Method: markDelta, Delta: mark, Groups: grpHash}
	}
	return res, nil
}

// latestDelta asks Graph for a delta link that starts now
// ($deltatoken=latest), without enumerating the users.
func (e Entra) latestDelta(ctx context.Context, cc *cloudClient) (string, error) {
	q := url.Values{"$select": {"id"}, "$deltatoken": {"latest"}}
	link, err := e.pages(ctx, cc, e.graphBase()+"/v1.0/users/delta?"+q.Encode(), func(map[string]any) error { return nil })
	if err != nil {
		var de *Error
		// A tenant or a Graph version without the shortcut: the run is full
		// every time, which is correct, only slower.
		if errors.As(err, &de) && de.Code == CodeBadFilter {
			return "", nil
		}
		return "", err
	}
	return link, nil
}

// incremental follows the last delta link. It returns nil and the reason when
// the run must read everything: the link expired (410 or a sync-state error),
// a user was removed (absence is only judged by a full read), a group the
// rules use changed, or too much changed.
func (e Entra) incremental(ctx context.Context, cc *cloudClient, cfg EntraConfig, o FetchOptions, grpHash string) (*Result, string, error) {
	since := o.Since
	if since.Method != markDelta || since.Delta == "" {
		return nil, FullMethodChanged, nil
	}
	if !sameOrigin(e.graphBase(), since.Delta) {
		return nil, FullMethodChanged, nil
	}
	changed := map[string]bool{}
	removed := false
	link, err := e.pages(ctx, cc, since.Delta, func(m map[string]any) error {
		id, _ := m["id"].(string)
		if _, gone := m["@removed"]; gone {
			removed = true
			return nil
		}
		if guidRe.MatchString(id) {
			changed[strings.ToLower(id)] = true
		}
		return nil
	})
	var de *Error
	switch {
	case errors.Is(err, errGone), errors.As(err, &de) && de.Code == CodeBadFilter:
		return nil, FullDeltaExpired, nil
	case err != nil:
		return nil, "", err
	case link == "":
		return nil, FullDeltaExpired, nil
	case removed:
		return nil, FullDeltaRemoved, nil
	case len(changed) > maxDeltaChanges:
		return nil, FullDeltaLarge, nil
	case len(o.NeedGroups) > 0 && grpHash != since.Groups:
		return nil, FullGroupsChanged, nil
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var users []SourceUser
	// Delta pages carry only the changed properties: read each changed user
	// whole, 15 ids per request (Graph's limit for "in").
	for i := 0; i < len(ids); i += 15 {
		chunk := ids[i:min(i+15, len(ids))]
		quoted := make([]string, len(chunk))
		for j, id := range chunk {
			quoted[j] = "'" + id + "'" // GUIDs only (checked above): nothing to escape
		}
		q := url.Values{"$select": {selectList(o.Attributes)}, "$filter": {"id in (" + strings.Join(quoted, ",") + ")"}}
		if _, err := e.pages(ctx, cc, e.graphBase()+"/v1.0/users?"+q.Encode(), func(m map[string]any) error {
			if u, ok := entraUser(m, cfg.IncludeGuests); ok {
				users = append(users, u)
			}
			return nil
		}); err != nil {
			return nil, "", err
		}
	}
	return &Result{Users: users, Incremental: true, Mark: &SyncMark{Method: markDelta, Delta: link, Groups: grpHash}}, "", nil
}

// groups reads the name and the transitive user members of each group the
// rules use. A group that no longer exists has no members.
func (e Entra) groups(ctx context.Context, cc *cloudClient, ids []string) ([]SourceGroup, map[string][]string, error) {
	member := map[string][]string{}
	var out []SourceGroup
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for _, id := range sorted {
		if !guidRe.MatchString(id) {
			continue
		}
		id = strings.ToLower(id)
		var g struct {
			DisplayName string `json:"displayName"`
		}
		err := cc.getJSON(ctx, e.graphBase()+"/v1.0/groups/"+id+"?$select=id,displayName", &g)
		var de *Error
		if errors.As(err, &de) && de.Code == CodeBadFilter {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, SourceGroup{ID: id, Name: g.DisplayName})
		q := url.Values{"$select": {"id"}, "$top": {"999"}}
		if _, err := e.pages(ctx, cc, e.graphBase()+"/v1.0/groups/"+id+"/transitiveMembers/microsoft.graph.user?"+q.Encode(), func(m map[string]any) error {
			if uid, _ := m["id"].(string); uid != "" {
				member[strings.ToLower(uid)] = append(member[strings.ToLower(uid)], id)
			}
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}
	return out, member, nil
}

// membershipHash summarises who is in which group.
func membershipHash(member map[string][]string) string {
	keys := make([]string, 0, len(member))
	for k := range member {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		gs := append([]string(nil), member[k]...)
		sort.Strings(gs)
		_, _ = h.Write([]byte(k + "=" + strings.Join(gs, ",") + ";"))
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// Test implements Driver (I-19).
func (e Entra) Test(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*TestResult, error) {
	cfg, err := entraCfg(raw)
	if err != nil {
		return nil, err
	}
	cc := newCloudClient(o)
	if err := e.token(ctx, cc, cfg, secret, o.Now); err != nil {
		return nil, err
	}
	var p graphPage
	q := url.Values{"$select": {selectList(o.Attributes)}, "$top": {"20"}}
	if err := cc.getJSON(ctx, e.graphBase()+"/v1.0/users?"+q.Encode(), &p); err != nil {
		return nil, err
	}
	res := &TestResult{OK: true, Sample: []SampleRow{}, Groups: []SourceGroup{}}
	for _, m := range p.Value {
		if u, ok := entraUser(m, cfg.IncludeGuests); ok {
			res.MatchedUsers++
			res.Sample = append(res.Sample, SampleRow{ExternalID: u.ExternalID, Raw: u.Attrs})
		}
	}
	// Listing groups needs a group permission; without it the test still
	// passes and lists none.
	var gp graphPage
	if err := cc.getJSON(ctx, e.graphBase()+"/v1.0/groups?$select=id,displayName&$top=50", &gp); err == nil {
		for _, g := range gp.Value {
			id, _ := g["id"].(string)
			name, _ := g["displayName"].(string)
			if id != "" {
				res.Groups = append(res.Groups, SourceGroup{ID: strings.ToLower(id), Name: name})
			}
		}
	}
	return res, nil
}
