package directory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// LDAPConfig is the stored config of an LDAP or Active Directory connector
// (spec I-15). The bind password is not part of it: it is sealed separately.
type LDAPConfig struct {
	// URL is ldaps://host[:636], or ldap://host[:389] with StartTLS.
	URL string `json:"url"`
	// StartTLS is on for ldap:// and cannot be turned off outside the lab
	// switch (I-16).
	StartTLS      *bool  `json:"startTls,omitempty"`
	BindDN        string `json:"bindDn"`
	BaseDN        string `json:"baseDn"`
	Scope         string `json:"scope"`
	UserFilter    string `json:"userFilter"`
	GroupBaseDN   string `json:"groupBaseDn"`
	GroupFilter   string `json:"groupFilter"`
	CACertPEM     string `json:"caCertPem"`
	ServerName    string `json:"serverName"`
	PageSize      int    `json:"pageSize"`
	TimeoutSecond int    `json:"timeoutSeconds"`
	// NestedGroups resolves nested membership with the AD in-chain matching
	// rule; slow on a large forest.
	NestedGroups bool `json:"nestedGroups"`
	// LDAPBindLogin is the switch for signing in by an LDAP bind (a later
	// phase); it is stored and not acted on here.
	LDAPBindLogin bool `json:"ldapBindLogin"`
}

// Defaults of the LDAP connector.
const (
	DefaultUserFilter  = "(&(objectCategory=person)(objectClass=user))"
	DefaultGroupFilter = "(objectClass=group)"
	DefaultPageSize    = 500
	DefaultLDAPTimeout = 30
	matchingRuleChain  = "1.2.840.113556.1.4.1941"
	maxFilterLen       = 2048
	maxGroups          = 5000
)

// LDAP is the LDAP/LDAPS/StartTLS driver with paged search.
type LDAP struct{}

func (c LDAPConfig) startTLS() bool { return c.StartTLS == nil || *c.StartTLS }

func (c LDAPConfig) timeout() time.Duration {
	if c.TimeoutSecond <= 0 {
		return DefaultLDAPTimeout * time.Second
	}
	return time.Duration(c.TimeoutSecond) * time.Second
}

// pageSize is the paging size as the wire wants it; Validate keeps it 10 to
// 5000, and a stored value outside that is clamped.
func (c LDAPConfig) pageSize() uint32 {
	n := c.PageSize
	if n < 10 || n > 5000 {
		n = DefaultPageSize
	}
	return uint32(n) //nolint:gosec // G115: clamped to 10..5000 above
}

func (c LDAPConfig) scope() int {
	switch c.Scope {
	case "base":
		return ldap.ScopeBaseObject
	case "onelevel":
		return ldap.ScopeSingleLevel
	}
	return ldap.ScopeWholeSubtree
}

// hostPort returns the dial address and the name to verify.
func (c LDAPConfig) hostPort() (addr, host string, ldaps bool, err error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" {
		return "", "", false, errors.New("not a URL")
	}
	port := u.Port()
	switch u.Scheme {
	case "ldaps":
		ldaps = true
		if port == "" {
			port = "636"
		}
	case "ldap":
		if port == "" {
			port = "389"
		}
	default:
		return "", "", false, errors.New("scheme must be ldaps or ldap")
	}
	return net.JoinHostPort(u.Hostname(), port), u.Hostname(), ldaps, nil
}

// Validate implements Driver (I-15, I-16, I-18).
func (LDAP) Validate(raw json.RawMessage, o ValidateOptions) (json.RawMessage, error) {
	var c LDAPConfig
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fieldErr("config", "not a valid LDAP config: unknown or mistyped field")
	}
	var v ValidationError
	add := func(path, msg string) { v.Fields = append(v.Fields, FieldError{"config." + path, msg}) }
	_, _, ldaps, herr := c.hostPort()
	switch {
	case c.URL == "":
		add("url", "required (ldaps://host:636 or ldap://host:389 with StartTLS)")
	case herr != nil:
		add("url", herr.Error())
	case len(c.URL) > 512:
		add("url", "too long")
	}
	if herr == nil && c.URL != "" {
		if u, _ := url.Parse(c.URL); u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			add("url", "must be scheme://host[:port] only")
		}
	}
	if herr == nil && c.URL != "" && !ldaps && !c.startTLS() && !o.AllowPlaintext {
		return nil, &Error{Code: CodeTLSRequired, Detail: "ldap:// must use StartTLS; plaintext LDAP is refused"}
	}
	if herr == nil && c.URL != "" && ldaps {
		c.StartTLS = nil
	}
	if strings.TrimSpace(c.BindDN) == "" {
		add("bindDn", "required: an anonymous bind is refused")
	}
	if strings.TrimSpace(c.BaseDN) == "" {
		add("baseDn", "required")
	}
	for name, val := range map[string]string{"bindDn": c.BindDN, "baseDn": c.BaseDN, "groupBaseDn": c.GroupBaseDN, "serverName": c.ServerName} {
		if len(val) > 1024 || strings.ContainsRune(val, 0) {
			add(name, "too long or contains a NUL")
		}
	}
	switch c.Scope {
	case "":
		c.Scope = "subtree"
	case "subtree", "onelevel", "base":
	default:
		add("scope", "must be subtree, onelevel or base")
	}
	if c.UserFilter == "" {
		c.UserFilter = DefaultUserFilter
	}
	if err := checkFilter(c.UserFilter); err != nil {
		add("userFilter", err.Error())
	}
	if c.GroupFilter == "" {
		c.GroupFilter = DefaultGroupFilter
	}
	if err := checkFilter(c.GroupFilter); err != nil {
		add("groupFilter", err.Error())
	}
	if o.GroupRules && strings.TrimSpace(c.GroupBaseDN) == "" {
		add("groupBaseDn", "required while group rules are set")
	}
	if c.CACertPEM != "" {
		if len(c.CACertPEM) > 64<<10 || !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CACertPEM)) {
			add("caCertPem", "not a PEM certificate bundle")
		}
	}
	switch {
	case c.PageSize == 0:
		c.PageSize = DefaultPageSize
	case c.PageSize < 10 || c.PageSize > 5000:
		add("pageSize", "must be 10 to 5000")
	}
	switch {
	case c.TimeoutSecond == 0:
		c.TimeoutSecond = DefaultLDAPTimeout
	case c.TimeoutSecond < 1 || c.TimeoutSecond > 300:
		add("timeoutSeconds", "must be 1 to 300")
	}
	if len(v.Fields) > 0 {
		return nil, &v
	}
	return json.Marshal(c)
}

// checkFilter parses a filter the way the server will (I-18).
func checkFilter(f string) error {
	if len(f) > maxFilterLen || strings.ContainsRune(f, 0) {
		return errors.New("too long or contains a NUL")
	}
	if _, err := ldap.CompileFilter(f); err != nil {
		return errors.New("not a valid LDAP filter")
	}
	return nil
}

// Identity implements Driver: the host and the bind DN.
func (LDAP) Identity(raw json.RawMessage) string {
	var c LDAPConfig
	_ = json.Unmarshal(raw, &c)
	addr, _, _, err := c.hostPort()
	if err != nil {
		addr = strings.ToLower(c.URL)
	}
	return strings.ToLower(addr) + "|" + strings.ToLower(strings.TrimSpace(c.BindDN))
}

// DefaultMappings implements Driver: the Active Directory suggestions (I-3).
func (LDAP) DefaultMappings() Mappings {
	return Mappings{
		Username:    AttrMap{Sources: []string{"userPrincipalName", "sAMAccountName"}},
		Email:       AttrMap{Sources: []string{"mail"}},
		DisplayName: AttrMap{Sources: []string{"displayName", "cn"}},
		Extension:   AttrMap{Sources: []string{"ipPhone", "telephoneNumber"}, Transforms: []Transform{{Kind: TransformDigits}}},
		Active:      ActiveMap{Attribute: "userAccountControl", Mode: ActiveUAC},
	}
}

// ldapConn is an open, bound connection.
type ldapConn struct {
	*ldap.Conn
	cfg   LDAPConfig
	state *tls.ConnectionState
}

// ErrBlocked is dialling an address the guard refuses.
func isBlocked(err error) bool { return errors.Is(err, safehttp.ErrBlockedAddress) }

// connect opens, secures and binds a connection. An empty password is refused
// before any network call: an empty-password simple bind is an anonymous bind
// and succeeds on many servers (I-18).
func connect(ctx context.Context, cfg LDAPConfig, secret string, o FetchOptions) (*ldapConn, error) {
	if secret == "" || strings.TrimSpace(cfg.BindDN) == "" {
		return nil, &Error{Code: CodeAuthFailed, Detail: "a bind DN and password are required"}
	}
	addr, host, ldaps, err := cfg.hostPort()
	if err != nil {
		return nil, &Error{Code: CodeUnreachable, Detail: "the server URL is not valid"}
	}
	timeout := cfg.timeout()
	dial := o.Dial
	if dial == nil {
		dial = safehttp.NewTransport(safehttp.Options{Timeout: timeout, Allow: o.Allow}).DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	nc, err := dial(dctx, "tcp", addr)
	if err != nil {
		return nil, classifyLDAP(err)
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if cfg.ServerName != "" {
		tc.ServerName = cfg.ServerName
	}
	if cfg.CACertPEM != "" {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(cfg.CACertPEM))
		tc.RootCAs = pool
	}
	_ = nc.SetDeadline(time.Now().Add(timeout))
	var (
		conn  *ldap.Conn
		state *tls.ConnectionState
	)
	switch {
	case ldaps:
		tlsConn := tls.Client(nc, tc)
		if err := tlsConn.HandshakeContext(dctx); err != nil {
			_ = nc.Close()
			return nil, classifyLDAP(err)
		}
		st := tlsConn.ConnectionState()
		state = &st
		_ = nc.SetDeadline(time.Time{})
		conn = ldap.NewConn(tlsConn, true)
		conn.Start()
	case cfg.startTLS():
		_ = nc.SetDeadline(time.Time{})
		conn = ldap.NewConn(nc, false)
		conn.Start()
		conn.SetTimeout(timeout)
		if err := conn.StartTLS(tc); err != nil {
			_ = conn.Close()
			return nil, classifyLDAP(err)
		}
		if st, ok := conn.TLSConnectionState(); ok {
			state = &st
		}
	default:
		if !o.AllowPlaintext {
			_ = nc.Close()
			return nil, &Error{Code: CodeTLSRequired, Detail: "plaintext LDAP is refused"}
		}
		slog.Warn("directory: connecting without TLS (HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP is set)")
		_ = nc.SetDeadline(time.Time{})
		conn = ldap.NewConn(nc, false)
		conn.Start()
	}
	conn.SetTimeout(timeout)
	if err := conn.Bind(cfg.BindDN, secret); err != nil {
		_ = conn.Close()
		return nil, classifyLDAP(err)
	}
	return &ldapConn{Conn: conn, cfg: cfg, state: state}, nil
}

// classifyLDAP turns a transport or protocol failure into a classified error
// whose detail never carries the server's text or any credential (I-14).
func classifyLDAP(err error) *Error {
	var (
		unknownCA x509.UnknownAuthorityError
		invalid   x509.CertificateInvalidError
		hostErr   x509.HostnameError
		ne        net.Error
	)
	switch {
	case isBlocked(err):
		return &Error{Code: CodeUnreachable, Detail: "the address is not allowed; private ranges need HELLO_INTEGRATIONS_ALLOWED_CIDRS"}
	case errors.As(err, &unknownCA), errors.As(err, &invalid), errors.As(err, &hostErr):
		return &Error{Code: CodeTLSUntrusted, Detail: "the server certificate is not trusted or does not match the name; supply the CA certificate or the server name"}
	case ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials), ldap.IsErrorWithCode(err, ldap.LDAPResultInappropriateAuthentication),
		ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights), ldap.IsErrorWithCode(err, ldap.LDAPResultUnwillingToPerform):
		return &Error{Code: CodeAuthFailed, Detail: "the server refused the bind or the search"}
	case ldap.IsErrorWithCode(err, ldap.LDAPResultFilterError), ldap.IsErrorWithCode(err, ldap.LDAPResultProtocolError),
		ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidDNSyntax), ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject):
		return &Error{Code: CodeBadFilter, Detail: "the server rejected the base DN or the filter"}
	case ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded), ldap.IsErrorWithCode(err, ldap.LDAPResultAdminLimitExceeded):
		return &Error{Code: CodeQuota, Detail: "the server's size or administrative limit was reached; lower the page size"}
	case ldap.IsErrorWithCode(err, ldap.LDAPResultTimeLimitExceeded), errors.Is(err, context.DeadlineExceeded),
		(errors.As(err, &ne) && ne.Timeout()):
		return &Error{Code: CodeTimeout, Detail: "the server did not answer in time"}
	}
	return &Error{Code: CodeUnreachable, Detail: "the server could not be reached or closed the connection"}
}

// Fetch implements Driver (I-17).
func (LDAP) Fetch(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*Result, error) {
	var cfg LDAPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: "the stored config is unreadable"}
	}
	c, err := connect(ctx, cfg, secret, o)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	// The mark is taken before anything is read, so a change made while the
	// read runs is picked up by the next incremental run.
	mark := c.readMark(ctx, o.Now)
	since, reason := chooseSince(o.Since, mark)
	if since != nil && len(o.NeedGroups) > 0 && cfg.GroupBaseDN != "" {
		// A user's groups come from the group objects (and, on AD, memberOf,
		// which does not touch the user's own USN): when a group the rules use
		// changed, read every user, so no membership change is missed.
		changed, err := c.groupsChanged(ctx, since, o)
		if err != nil {
			return nil, err
		}
		if changed {
			since, reason = nil, FullGroupsChanged
		}
	}
	userFilter := cfg.UserFilter
	if since != nil {
		userFilter = "(&" + cfg.UserFilter + since.changedClause() + ")"
	}
	var groups []SourceGroup
	dnToGroup := map[string]string{}
	groupDN := map[string]string{}
	if len(o.NeedGroups) > 0 && cfg.GroupBaseDN != "" {
		gs, err := c.groups(ctx, o)
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			groups = append(groups, g.SourceGroup)
			dnToGroup[strings.ToLower(g.dn)] = g.ID
			groupDN[g.ID] = g.dn
		}
	}
	users, err := c.users(ctx, o, userFilter, len(o.NeedGroups) > 0 && !cfg.NestedGroups)
	if err != nil {
		return nil, err
	}
	need := map[string]bool{}
	for _, id := range o.NeedGroups {
		need[id] = true
	}
	if len(o.NeedGroups) > 0 {
		index := map[string]*SourceUser{}
		for i := range users {
			index[users[i].ExternalID] = &users[i]
		}
		if cfg.NestedGroups {
			ids := make([]string, 0, len(o.NeedGroups))
			for _, id := range o.NeedGroups {
				if _, ok := groupDN[id]; ok {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			for _, id := range ids {
				members, err := c.chainMembers(ctx, groupDN[id], o)
				if err != nil {
					return nil, err
				}
				for _, ext := range members {
					if u := index[ext]; u != nil {
						u.Groups = append(u.Groups, id)
					}
				}
			}
		} else {
			for i := range users {
				u := &users[i]
				for _, dn := range u.Attrs["memberof"] {
					if id, ok := dnToGroup[strings.ToLower(dn)]; ok && need[id] {
						u.Groups = append(u.Groups, id)
					}
				}
			}
		}
	}
	res := &Result{Users: users, Groups: groups, Mark: mark, Incremental: since != nil}
	if since == nil && o.Since != nil {
		res.FullReason = reason
	}
	return res, nil
}

// markOverlap is subtracted from a time-based mark, so clock skew between
// Hello and the directory cannot hide a change.
const markOverlap = 5 * time.Minute

// Mark methods.
const (
	markUSN             = "usn"
	markWhenChanged     = "whenChanged"
	markModifyTimestamp = "modifyTimestamp"
)

// adCapability is the rootDSE supportedCapabilities value of Active Directory.
const adCapability = "1.2.840.113556.1.4.800"

func genTime(t time.Time) string { return t.UTC().Format("20060102150405") + ".0Z" }

// changedClause is the filter clause selecting what changed since the mark.
func (m *SyncMark) changedClause() string {
	switch m.Method {
	case markUSN:
		return "(uSNChanged>=" + strconv.FormatInt(m.USN, 10) + ")"
	case markWhenChanged:
		return "(whenChanged>=" + ldap.EscapeFilter(m.Time) + ")"
	}
	return "(modifyTimestamp>=" + ldap.EscapeFilter(m.Time) + ")"
}

// readMark reads the high-water mark from the rootDSE: AD's
// highestCommittedUSN with the invocation ID, else a time (whenChanged on AD,
// modifyTimestamp elsewhere). A server that will not say gets a time mark;
// nil is returned only when no clock is given.
func (c *ldapConn) readMark(ctx context.Context, now time.Time) *SyncMark {
	if now.IsZero() || ctx.Err() != nil {
		return nil
	}
	timeMark := func(method string) *SyncMark {
		return &SyncMark{Method: method, Time: genTime(now.Add(-markOverlap))}
	}
	req := ldap.NewSearchRequest("", ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, int(c.cfg.timeout().Seconds()), false,
		"(objectClass=*)", []string{"highestCommittedUSN", "dsServiceName", "supportedCapabilities"}, nil)
	res, err := c.Search(req)
	if err != nil || len(res.Entries) == 0 {
		return timeMark(markModifyTimestamp)
	}
	e := res.Entries[0]
	if v := strings.TrimSpace(e.GetAttributeValue("highestCommittedUSN")); v != "" {
		if usn, err := strconv.ParseInt(v, 10, 64); err == nil && usn >= 0 {
			if inv := c.invocationID(e.GetAttributeValue("dsServiceName")); inv != "" {
				return &SyncMark{Method: markUSN, USN: usn, Invocation: inv}
			}
		}
		// Without an invocation ID a restore cannot be told from a normal
		// run, so USN is not safe: use the time.
		return timeMark(markWhenChanged)
	}
	for _, cap := range e.GetAttributeValues("supportedCapabilities") {
		if cap == adCapability {
			return timeMark(markWhenChanged)
		}
	}
	return timeMark(markModifyTimestamp)
}

// invocationID reads the DC's invocation ID from its NTDS Settings object.
func (c *ldapConn) invocationID(dn string) string {
	if dn == "" {
		return ""
	}
	req := ldap.NewSearchRequest(dn, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, int(c.cfg.timeout().Seconds()), false,
		"(objectClass=*)", []string{"invocationId"}, nil)
	res, err := c.Search(req)
	if err != nil || len(res.Entries) == 0 {
		return ""
	}
	if raw := res.Entries[0].GetRawAttributeValues("invocationId"); len(raw) > 0 && len(raw[0]) == 16 {
		return guidString(raw[0])
	}
	return ""
}

// chooseSince decides whether the read can be incremental from since, given
// the mark just read from the server, and says why not when it cannot: a
// restore (the invocation ID changed, or the USN went backwards) or a clock
// that went backwards means the mark describes a different history.
func chooseSince(since, now *SyncMark) (*SyncMark, string) {
	switch {
	case since == nil:
		return nil, ""
	case now == nil:
		return nil, FullUnsupported
	case since.Method != now.Method:
		return nil, FullMethodChanged
	case since.Method == markUSN && since.Invocation != now.Invocation:
		return nil, FullInvocation
	case since.Method == markUSN && now.USN < since.USN:
		return nil, FullUSNRegressed
	case since.Method != markUSN && now.Time < since.Time:
		return nil, FullClockRegressed
	}
	return since, ""
}

// groupsChanged reports whether a group the rules depend on changed since the
// mark. With nested groups any group may matter, so any change counts.
func (c *ldapConn) groupsChanged(ctx context.Context, since *SyncMark, o FetchOptions) (bool, error) {
	need := map[string]bool{}
	for _, id := range o.NeedGroups {
		need[id] = true
	}
	changed := false
	filter := "(&" + c.cfg.GroupFilter + since.changedClause() + ")"
	err := c.paged(ctx, c.cfg.GroupBaseDN, ldap.ScopeWholeSubtree, filter, []string{"objectGUID", "entryUUID", "nsUniqueId"}, maxGroups, func(e *ldap.Entry) error {
		if id := stableID(e); c.cfg.NestedGroups || need[id] {
			changed = true
		}
		return nil
	})
	return changed, err
}

type ldapGroup struct {
	SourceGroup
	dn string
}

// stableID is an entry's stable id: AD objectGUID as its canonical string,
// else the RFC 4530 entryUUID or 389-DS nsUniqueId.
func stableID(e *ldap.Entry) string {
	if raw := e.GetRawAttributeValues("objectGUID"); len(raw) > 0 && len(raw[0]) == 16 {
		return guidString(raw[0])
	}
	for _, a := range []string{"entryUUID", "nsUniqueId"} {
		if v := strings.ToLower(strings.TrimSpace(e.GetAttributeValue(a))); v != "" {
			return v
		}
	}
	return ""
}

// guidString writes an AD objectGUID (a mixed-endian structure) as the
// canonical 8-4-4-4-12 string.
func guidString(b []byte) string {
	d1 := binary.LittleEndian.Uint32(b[0:4])
	d2 := binary.LittleEndian.Uint16(b[4:6])
	d3 := binary.LittleEndian.Uint16(b[6:8])
	return fmt.Sprintf("%08x-%04x-%04x-%s-%s", d1, d2, d3, hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

// paged runs a paged search, calling each for every entry; it stops with
// CodeUserCap when more than limit entries come back (limit <= 0: no cap) and
// checks ctx between pages.
func (c *ldapConn) paged(ctx context.Context, base string, scope int, filter string, attrs []string, limit int, each func(*ldap.Entry) error) error {
	pc := ldap.NewControlPaging(c.cfg.pageSize())
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return classifyLDAP(err)
		}
		req := ldap.NewSearchRequest(base, scope, ldap.NeverDerefAliases, 0, int(c.cfg.timeout().Seconds()), false, filter, attrs, []ldap.Control{pc})
		res, err := c.Search(req)
		if err != nil {
			return classifyLDAP(err)
		}
		for _, e := range res.Entries {
			n++
			if limit > 0 && n > limit {
				return &Error{Code: CodeUserCap, Detail: "the source returned more entries than the cap of " + fmt.Sprint(limit)}
			}
			if err := each(e); err != nil {
				return err
			}
		}
		ctl := ldap.FindControl(res.Controls, ldap.ControlTypePaging)
		p, ok := ctl.(*ldap.ControlPaging)
		if !ok || len(p.Cookie) == 0 {
			return nil
		}
		pc.SetCookie(p.Cookie)
	}
}

func (c *ldapConn) users(ctx context.Context, o FetchOptions, filter string, withMemberOf bool) ([]SourceUser, error) {
	attrs := append([]string{"objectGUID", "entryUUID", "nsUniqueId"}, o.Attributes...)
	if withMemberOf {
		attrs = append(attrs, "memberOf")
	}
	var out []SourceUser
	err := c.paged(ctx, c.cfg.BaseDN, c.cfg.scope(), filter, attrs, o.Limit, func(e *ldap.Entry) error {
		id := stableID(e)
		if id == "" {
			return &Error{Code: CodeBadFilter, Detail: "an entry has no stable id (objectGUID, entryUUID or nsUniqueId); the filter matches something that is not a user"}
		}
		u := SourceUser{ExternalID: id, Attrs: map[string][]string{"dn": {e.DN}}}
		for _, a := range e.Attributes {
			k := strings.ToLower(a.Name)
			if k == "objectguid" {
				continue
			}
			u.Attrs[k] = a.Values
		}
		out = append(out, u)
		return nil
	})
	return out, err
}

func (c *ldapConn) groups(ctx context.Context, o FetchOptions) ([]ldapGroup, error) {
	var out []ldapGroup
	err := c.paged(ctx, c.cfg.GroupBaseDN, ldap.ScopeWholeSubtree, c.cfg.GroupFilter, []string{"objectGUID", "entryUUID", "nsUniqueId", "cn"}, maxGroups, func(e *ldap.Entry) error {
		if id := stableID(e); id != "" {
			out = append(out, ldapGroup{SourceGroup{ID: id, Name: e.GetAttributeValue("cn")}, e.DN})
		}
		return nil
	})
	return out, err
}

// chainMembers lists the external ids of the users that are members of the
// group at dn, directly or through nested groups (I-17). The DN goes through
// EscapeFilter, never string-built (I-18).
func (c *ldapConn) chainMembers(ctx context.Context, dn string, o FetchOptions) ([]string, error) {
	filter := "(&" + c.cfg.UserFilter + "(memberOf:" + matchingRuleChain + ":=" + ldap.EscapeFilter(dn) + "))"
	var out []string
	err := c.paged(ctx, c.cfg.BaseDN, c.cfg.scope(), filter, []string{"objectGUID", "entryUUID", "nsUniqueId"}, o.Limit, func(e *ldap.Entry) error {
		if id := stableID(e); id != "" {
			out = append(out, id)
		}
		return nil
	})
	return out, err
}

// Test implements Driver (I-19): connect, verify, bind and one small search.
func (LDAP) Test(ctx context.Context, raw json.RawMessage, secret string, o FetchOptions) (*TestResult, error) {
	var cfg LDAPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: "the stored config is unreadable"}
	}
	c, err := connect(ctx, cfg, secret, o)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	res := &TestResult{OK: true, Sample: []SampleRow{}, Groups: []SourceGroup{}}
	if c.state != nil && len(c.state.PeerCertificates) > 0 {
		cert := c.state.PeerCertificates[0]
		exp := cert.NotAfter
		res.ServerCertSubject, res.ServerCertExpires = cert.Subject.String(), &exp
	}
	attrs := append([]string{"objectGUID", "entryUUID", "nsUniqueId"}, o.Attributes...)
	req := ldap.NewSearchRequest(cfg.BaseDN, cfg.scope(), ldap.NeverDerefAliases, 0, int(cfg.timeout().Seconds()), false, cfg.UserFilter, attrs,
		[]ldap.Control{ldap.NewControlPaging(cfg.pageSize())})
	r, err := c.Search(req)
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, classifyLDAP(err)
	}
	if r != nil {
		res.MatchedUsers = len(r.Entries)
		for _, e := range r.Entries {
			if len(res.Sample) == 20 {
				break
			}
			id := stableID(e)
			raw := map[string][]string{"dn": {e.DN}}
			for _, a := range e.Attributes {
				if k := strings.ToLower(a.Name); k != "objectguid" {
					raw[k] = a.Values
				}
			}
			res.Sample = append(res.Sample, SampleRow{ExternalID: id, Raw: raw})
		}
	}
	if cfg.GroupBaseDN != "" {
		greq := ldap.NewSearchRequest(cfg.GroupBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 50, int(cfg.timeout().Seconds()), false,
			cfg.GroupFilter, []string{"objectGUID", "entryUUID", "nsUniqueId", "cn"}, nil)
		gr, err := c.Search(greq)
		if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
			return nil, classifyLDAP(err)
		}
		if gr != nil {
			for _, e := range gr.Entries {
				if id := stableID(e); id != "" {
					res.Groups = append(res.Groups, SourceGroup{ID: id, Name: e.GetAttributeValue("cn")})
				}
			}
		}
	}
	return res, nil
}
