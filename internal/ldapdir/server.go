package ldapdir

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	ber "github.com/go-asn1-ber/asn1-ber"
)

// Limits (spec S-25).
const (
	MaxSize      = 200
	MaxPageSize  = 100
	SearchTime   = 10 * time.Second
	handshakeTTL = 10 * time.Second
	idleTTL      = 2 * time.Minute
	writeTTL     = 30 * time.Second
	maxConns     = 1024
)

// Config is the server's settings.
type Config struct {
	// Base is the base DN (HELLO_LDAP_BASE).
	Base string
	// Anon allows an anonymous bind (HELLO_LDAP_ANON).
	Anon bool
	// TLS is the server certificate. Its MinVersion is raised to 1.2.
	TLS     *tls.Config
	Backend Backend
	// Cache holds rendered sets; nil renders every time.
	Cache prov.BookCache
	// Limiter counts failed binds; nil counts in memory.
	Limiter Limiter
	Log     *slog.Logger
}

// Server is the LDAP directory. It serves LDAPS with ServeLDAPS and, when
// asked to, StartTLS with ServeStartTLS; it never serves plain LDAP.
type Server struct {
	cfg  Config
	base string // normalised
	dir  *directory
	sem  chan struct{}

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	lns    []net.Listener
	closed bool
	wg     sync.WaitGroup
}

// New returns a server.
func New(cfg Config) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewMemoryLimiter()
	}
	cfg.TLS = cfg.TLS.Clone()
	if cfg.TLS.MinVersion < tls.VersionTLS12 {
		cfg.TLS.MinVersion = tls.VersionTLS12
	}
	base := NormDN(cfg.Base)
	return &Server{cfg: cfg, base: base, dir: &directory{b: cfg.Backend, cache: cfg.Cache, base: base, log: cfg.Log},
		sem: make(chan struct{}, maxConns), conns: map[net.Conn]struct{}{}}
}

// ServeLDAPS serves LDAP over TLS on ln until it is closed.
func (s *Server) ServeLDAPS(ln net.Listener) error { return s.serve(ln, true) }

// ServeStartTLS serves LDAP on ln in the clear, refusing every operation
// but StartTLS and the Root DSE read until the client has upgraded.
func (s *Server) ServeStartTLS(ln net.Listener) error { return s.serve(ln, false) }

func (s *Server) serve(ln net.Listener, implicitTLS bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	s.lns = append(s.lns, ln)
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case s.sem <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		if implicitTLS {
			c = tls.Server(c, s.cfg.TLS)
		}
		if !s.track(c) {
			_ = c.Close()
			<-s.sem
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			s.handle(c, implicitTLS)
		}()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// Close stops accepting, closes every connection and waits for the handlers.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for _, ln := range s.lns {
		_ = ln.Close()
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// session is one client connection.
type session struct {
	s      *Server
	conn   net.Conn
	br     *bufio.Reader
	secure bool
	ip     string
	bound  bool
	anon   bool
	phone  int64
}

func (s *Server) handle(c net.Conn, secure bool) {
	defer s.untrack(c)
	ss := &session{s: s, conn: c, secure: secure, ip: remoteIP(c)}
	defer func() { _ = ss.conn.Close() }()
	if secure {
		if tc, ok := c.(*tls.Conn); ok {
			ctx, cancel := context.WithTimeout(context.Background(), handshakeTTL)
			err := tc.HandshakeContext(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
	ss.br = bufio.NewReader(ss.conn)
	for {
		_ = ss.conn.SetReadDeadline(time.Now().Add(idleTTL))
		p, err := ber.ReadPacket(ss.br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isTimeout(err) && !errors.Is(err, net.ErrClosed) {
				ss.notice(ResultProtocolError, "malformed message")
			}
			return
		}
		m, err := parseMessage(p)
		if err != nil {
			ss.notice(ResultProtocolError, "malformed message")
			return
		}
		if !ss.dispatch(m) {
			return
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func remoteIP(c net.Conn) string {
	h, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return h
}

func (ss *session) send(b []byte) bool {
	_ = ss.conn.SetWriteDeadline(time.Now().Add(writeTTL))
	_, err := ss.conn.Write(b)
	return err == nil
}

// notice is the notice of disconnection, sent before the server closes.
func (ss *session) notice(code int, diag string) {
	op := result(opExtResponse, code, diag)
	op.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, extResponseNameTag, oidNoticeOfDisconnection, "responseName"))
	ss.send(envelope(0, op, nil))
}

// respTag is the response tag of a request.
func respTag(req ber.Tag) (ber.Tag, bool) {
	switch req {
	case opBindRequest:
		return opBindResponse, true
	case opSearchRequest:
		return opSearchDone, true
	case opModifyRequest:
		return opModifyResponse, true
	case opAddRequest:
		return opAddResponse, true
	case opDelRequest:
		return opDelResponse, true
	case opModDNRequest:
		return opModDNResponse, true
	case opCompareRequest:
		return opCompareResponse, true
	case opExtRequest:
		return opExtResponse, true
	}
	return 0, false
}

// dispatch handles one message; false closes the connection.
func (ss *session) dispatch(m message) bool {
	tag := m.op.Tag
	switch tag {
	case opUnbindRequest:
		return false
	case opAbandonRequest:
		return true
	}
	rt, ok := respTag(tag)
	if !ok {
		ss.notice(ResultProtocolError, "unknown operation")
		return false
	}
	if !ss.secure && !ss.allowedInClear(m) {
		return ss.send(envelope(m.id, result(rt, ResultUnwillingToPerform, "TLS is required: plain LDAP is not served"), nil))
	}
	switch tag {
	case opBindRequest:
		return ss.bind(m)
	case opSearchRequest:
		return ss.search(m)
	case opExtRequest:
		return ss.extended(m)
	default:
		// Add, modify, delete, modifyDN and compare: the directory is read-only.
		return ss.send(envelope(m.id, result(rt, ResultUnwillingToPerform, "the directory is read-only"), nil))
	}
}

// allowedInClear is StartTLS and the Root DSE read, the only operations
// before TLS.
func (ss *session) allowedInClear(m message) bool {
	switch m.op.Tag {
	case opExtRequest:
		return len(m.op.Children) > 0 && str(m.op.Children[0]) == oidStartTLS
	case opSearchRequest:
		if len(m.op.Children) < 2 {
			return false
		}
		scope, _ := intOf(m.op.Children[1])
		return str(m.op.Children[0]) == "" && scope == scopeBase
	}
	return false
}

func (ss *session) extended(m message) bool {
	if len(m.op.Children) == 0 || m.op.Children[0].ClassType != ber.ClassContext || m.op.Children[0].Tag != extRequestNameTag {
		return ss.send(envelope(m.id, result(opExtResponse, ResultProtocolError, "malformed extended request"), nil))
	}
	if str(m.op.Children[0]) != oidStartTLS {
		return ss.send(envelope(m.id, result(opExtResponse, ResultUnwillingToPerform, "unsupported extended operation"), nil))
	}
	if ss.secure {
		return ss.send(envelope(m.id, result(opExtResponse, ResultOperationsError, "TLS is already active"), nil))
	}
	// Anything the client sent behind the request would be read as TLS
	// bytes: refuse a pipelined client (plaintext injection).
	if ss.br.Buffered() > 0 {
		ss.notice(ResultProtocolError, "data after StartTLS")
		return false
	}
	op := result(opExtResponse, ResultSuccess, "")
	op.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, extResponseNameTag, oidStartTLS, "responseName"))
	if !ss.send(envelope(m.id, op, nil)) {
		return false
	}
	tc := tls.Server(ss.conn, ss.s.cfg.TLS)
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTTL)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		return false
	}
	ss.s.mu.Lock()
	delete(ss.s.conns, ss.conn)
	ss.s.conns[tc] = struct{}{}
	ss.s.mu.Unlock()
	ss.conn, ss.br, ss.secure = tc, bufio.NewReader(tc), true
	ss.bound, ss.anon, ss.phone = false, false, 0
	return true
}

// NormDN folds a DN for comparison: lower case, no space around commas and
// equals signs. A DN with escapes is only trimmed and folded, so it never
// equals a DN the server builds.
func NormDN(dn string) string {
	parts := strings.Split(dn, ",")
	for i, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if ok {
			p = strings.TrimSpace(k) + "=" + strings.TrimSpace(v)
		} else {
			p = strings.TrimSpace(p)
		}
		parts[i] = strings.ToLower(p)
	}
	return strings.Join(parts, ",")
}

// PhoneDN is the bind DN of a phone.
func PhoneDN(base string, phoneID int64) string { return prov.LDAPBindDN(base, phoneID) }

// phoneOf extracts the phone id from a normalised bind DN.
func (s *Server) phoneOf(dn string) (int64, bool) {
	rest, ok := strings.CutPrefix(dn, "uid=")
	if !ok {
		return 0, false
	}
	id, rest, ok := strings.Cut(rest, ",")
	if !ok || rest != "ou=phones,"+s.base {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return 0, false
	}
	return n, true
}

func (ss *session) bind(m message) bool {
	reply := func(code int, diag string) bool {
		return ss.send(envelope(m.id, result(opBindResponse, code, diag), nil))
	}
	c := m.op.Children
	ss.bound, ss.anon, ss.phone = false, false, 0
	if len(c) != 3 {
		return reply(ResultProtocolError, "malformed bind request")
	}
	if v, ok := intOf(c[0]); !ok || v != bindVersion {
		return reply(ResultProtocolError, "only LDAP version 3 is supported")
	}
	if c[2].ClassType != ber.ClassContext || c[2].Tag != bindAuthSimple {
		return reply(ResultAuthMethodNotSupported, "only simple bind is supported")
	}
	name, pw := NormDN(str(c[1])), string(c[2].Data.Bytes())
	switch {
	case name == "" && pw == "":
		if !ss.s.cfg.Anon {
			return reply(ResultUnwillingToPerform, "anonymous bind is not allowed")
		}
		ss.bound, ss.anon = true, true
		return reply(ResultSuccess, "")
	case pw == "":
		return reply(ResultUnwillingToPerform, "unauthenticated bind is not allowed")
	}
	sum := sha256.Sum256([]byte(name))
	dnKey, ipKey := "dn:"+hex.EncodeToString(sum[:16]), "ip:"+ss.ip
	ctx, cancel := context.WithTimeout(context.Background(), SearchTime)
	defer cancel()
	lim := ss.s.cfg.Limiter
	if lim.Blocked(ctx, dnKey) || lim.Blocked(ctx, ipKey) {
		ss.s.cfg.Log.Warn("ldap: bind refused", "result", ResultBusy)
		return reply(ResultBusy, "too many failed binds, try again later")
	}
	fail := func() bool {
		lim.Fail(ctx, dnKey)
		lim.Fail(ctx, ipKey)
		ss.s.cfg.Log.Info("ldap: bind failed", "result", ResultInvalidCredentials)
		return reply(ResultInvalidCredentials, "invalid credentials")
	}
	id, ok := ss.s.phoneOf(name)
	if !ok {
		return fail()
	}
	good, err := ss.s.cfg.Backend.Authenticate(ctx, id, pw)
	if err != nil {
		ss.s.cfg.Log.Error("ldap: bind unavailable", "error", err)
		return reply(ResultUnavailable, "directory unavailable")
	}
	if !good {
		return fail()
	}
	ss.bound, ss.phone = true, id
	return reply(ResultSuccess, "")
}

// searchReq is a parsed search request.
type searchReq struct {
	base      string
	scope     int64
	sizeLimit int64
	timeLimit int64
	typesOnly bool
	filter    *filter
	filterRaw []byte
	attrs     []string
}

func parseSearch(m message) (searchReq, error) {
	c := m.op.Children
	var r searchReq
	if len(c) != 8 {
		return r, errProtocol
	}
	var ok1, ok2, ok3 bool
	r.base = NormDN(str(c[0]))
	r.scope, ok1 = intOf(c[1])
	r.sizeLimit, ok2 = intOf(c[3])
	r.timeLimit, ok3 = intOf(c[4])
	if !ok1 || !ok2 || !ok3 || r.scope < 0 || r.scope > 2 {
		return r, errProtocol
	}
	r.typesOnly = len(c[5].Data.Bytes()) == 1 && c[5].Data.Bytes()[0] != 0
	f, err := parseFilter(c[6])
	if err != nil {
		return r, err
	}
	r.filter, r.filterRaw = f, c[6].Bytes()
	for _, a := range c[7].Children {
		r.attrs = append(r.attrs, str(a))
	}
	return r, nil
}

func (ss *session) search(m message) bool {
	start := time.Now()
	done := func(code int, diag string, ctl *ber.Packet) bool {
		ss.s.cfg.Log.Debug("ldap: search", "result", code, "ms", time.Since(start).Milliseconds())
		return ss.send(envelope(m.id, result(opSearchDone, code, diag), ctl))
	}
	req, err := parseSearch(m)
	if err != nil {
		if errors.Is(err, errFilterTooComplex) {
			return done(ResultAdminLimitExceeded, "filter too complex", nil)
		}
		return done(ResultProtocolError, "malformed search request", nil)
	}
	var page *pageReq
	for _, c := range m.controls {
		if c.oid == oidPagedResults {
			size, cookie, err := parsePaged(c.value)
			if err != nil {
				return done(ResultProtocolError, "malformed paged results control", nil)
			}
			page = &pageReq{size: size, cookie: cookie}
		} else if c.critical {
			return done(ResultUnavailableCriticalExt, "unsupported critical control", nil)
		}
	}
	if req.base == "" {
		if req.scope != scopeBase {
			return done(ResultNoSuchObject, "", nil)
		}
		return ss.rootDSE(m, req, done)
	}
	if !ss.bound {
		return done(ResultInsufficientAccess, "bind required", nil)
	}
	return ss.searchDirectory(m, req, page, done)
}

type pageReq struct {
	size   int64
	cookie []byte
}

// containers are the two entries above the contacts.
func (s *Server) containers() []*entry {
	first, _, _ := strings.Cut(s.base, ",")
	typ, val, _ := strings.Cut(first, "=")
	b := &entry{dn: s.base, norm: map[string][]string{}}
	b.add("objectClass", "top", "domain")
	b.add(typ, val)
	p := &entry{dn: "ou=phonebook," + s.base, norm: map[string][]string{}}
	p.add("objectClass", "top", "organizationalUnit")
	p.add("ou", "phonebook")
	return []*entry{b, p}
}

func (s *Server) rootEntry() *entry {
	e := &entry{dn: "", norm: map[string][]string{}}
	e.add("objectClass", "top")
	e.attrs = append(e.attrs,
		attribute{"namingContexts", []string{s.cfg.Base}},
		attribute{"supportedLDAPVersion", []string{"3"}},
		attribute{"supportedControl", []string{oidPagedResults}},
		attribute{"supportedExtension", []string{oidStartTLS}},
		attribute{"vendorName", []string{"Hello"}})
	return e
}

func (ss *session) rootDSE(m message, req searchReq, done func(int, string, *ber.Packet) bool) bool {
	e := ss.s.rootEntry()
	if req.filter.eval(e) == triTrue {
		if !ss.send(envelope(m.id, searchEntry(e, req), nil)) {
			return false
		}
	}
	return done(ResultSuccess, "", nil)
}

func searchEntry(e *entry, req searchReq) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, opSearchEntry, nil, "searchResultEntry")
	op.AppendChild(octets(e.dn, "objectName"))
	attrs := ber.NewSequence("attributes")
	for _, a := range e.project(req.attrs, req.typesOnly) {
		pa := ber.NewSequence("attribute")
		pa.AppendChild(octets(a.name, "type"))
		vs := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "vals")
		for _, v := range a.vals {
			vs.AppendChild(octets(v, "value"))
		}
		pa.AppendChild(vs)
		attrs.AppendChild(pa)
	}
	op.AppendChild(attrs)
	return op
}

// inScope reports whether the entry dn (whose parent is parent) is in the
// scope of a search from base.
func inScope(base string, scope int64, dn, parent string) bool {
	switch scope {
	case scopeBase:
		return dn == base
	case scopeSingle:
		return parent == base
	default:
		return dn == base || strings.HasSuffix(dn, ","+base)
	}
}

func (ss *session) searchDirectory(m message, req searchReq, page *pageReq, done func(int, string, *ber.Packet) bool) bool {
	s := ss.s
	pb := "ou=phonebook," + s.base
	leaf := strings.HasSuffix(req.base, ","+pb) && strings.HasPrefix(req.base, "cn=") && strings.Count(strings.TrimSuffix(req.base, ","+pb), ",") == 0
	if req.base != s.base && req.base != pb && !leaf {
		return done(ResultNoSuchObject, "", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), SearchTime)
	defer cancel()
	if req.timeLimit > 0 && time.Duration(req.timeLimit)*time.Second < SearchTime {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(req.timeLimit)*time.Second)
		defer cancel()
	}
	var matches []*entry
	for _, e := range s.containers() {
		parent := ""
		if e.dn != s.base {
			parent = s.base
		}
		if inScope(req.base, req.scope, e.dn, parent) && req.filter.eval(e) == triTrue {
			matches = append(matches, e)
		}
	}
	// The contacts are read only when the search can reach them.
	if req.base != pb || req.scope != scopeBase {
		if req.scope != scopeBase || leaf {
			ents, err := s.dir.entries(ctx, ss.phone)
			switch {
			case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
				return done(ResultTimeLimitExceeded, "", nil)
			case errors.Is(err, ErrNotFound):
				return done(ResultInsufficientAccess, "this phone is not provisioned for LDAP", nil)
			case err != nil:
				s.cfg.Log.Warn("ldap: directory unavailable", "error", err)
				return done(ResultUnavailable, "directory unavailable", nil)
			}
			for i, e := range ents {
				if i&255 == 255 && ctx.Err() != nil {
					return done(ResultTimeLimitExceeded, "", nil)
				}
				if inScope(req.base, req.scope, e.dn, pb) && req.filter.eval(e) == triTrue {
					matches = append(matches, e)
				}
			}
		}
	}
	limit := int64(MaxSize)
	if req.sizeLimit > 0 && req.sizeLimit < limit {
		limit = req.sizeLimit
	}
	if page != nil {
		return ss.pagedResult(m, req, page, matches, limit, done)
	}
	code := ResultSuccess
	if int64(len(matches)) > limit {
		matches, code = matches[:limit], ResultSizeLimitExceeded
	}
	for _, e := range matches {
		if !ss.send(envelope(m.id, searchEntry(e, req), nil)) {
			return false
		}
	}
	s.cfg.Log.Debug("ldap: search entries", "count", len(matches))
	return done(code, "", nil)
}

// pagedResult answers one page of a paged search (RFC 2696). The cookie is
// the offset and a fingerprint of the search, so a cookie from another
// search or another result set is refused.
func (ss *session) pagedResult(m message, req searchReq, page *pageReq, matches []*entry, limit int64, done func(int, string, *ber.Packet) bool) bool {
	size := page.size
	if size > MaxPageSize {
		size = MaxPageSize
	}
	if size > limit {
		size = limit
	}
	fp := fingerprint(req, ss.phone, matches)
	offset := 0
	if len(page.cookie) > 0 {
		o, ok := strings.CutSuffix(string(page.cookie), "."+fp)
		n, err := strconv.Atoi(o)
		if !ok || err != nil || n < 0 || n > len(matches) {
			return done(ResultUnwillingToPerform, "invalid paging cookie", nil)
		}
		offset = n
	}
	if size == 0 {
		return done(ResultSuccess, "", pagedResponseControl(0, nil))
	}
	end := offset + int(size)
	var next []byte
	if end < len(matches) {
		next = []byte(fmt.Sprintf("%d.%s", end, fp))
	} else {
		end = len(matches)
	}
	for _, e := range matches[offset:end] {
		if !ss.send(envelope(m.id, searchEntry(e, req), nil)) {
			return false
		}
	}
	return done(ResultSuccess, "", pagedResponseControl(len(matches), next))
}

func fingerprint(req searchReq, phone int64, matches []*entry) string {
	h := sha256.New()
	var b bytes.Buffer
	b.WriteString(req.base)
	b.WriteByte(byte(req.scope))
	b.Write(req.filterRaw)
	b.WriteString(strconv.FormatInt(phone, 10))
	b.WriteString(strconv.Itoa(len(matches)))
	for _, e := range matches {
		b.WriteString(e.dn)
	}
	h.Write(b.Bytes())
	return hex.EncodeToString(h.Sum(nil)[:8])
}
