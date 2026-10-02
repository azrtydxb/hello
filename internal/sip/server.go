// Package sip is Hello's real-time SIP core: a UDP listener built on sipgo's
// transport and transaction layers, digest authentication with stateless
// nonces, a registrar writing bindings to the live state, and a B2BUA for
// internal calls. sipgo stays behind this package; callers see only Server.
package sip

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Config is the SIP node's behaviour; see config.SIP for the meaning of each.
type Config struct {
	NodeID         string
	Domain         string // digest realm and AOR host
	AdvertisedAddr string // host:port written into Via and Contact
	NonceSecret    []byte
	MinExpires     time.Duration
	MaxExpires     time.Duration
	RingTimeout    time.Duration
	AuthFailLimit  int
	StateTimeout   time.Duration
	// CallHeartbeat and CallTTL govern the live call record (10s / 30s).
	CallHeartbeat time.Duration
	CallTTL       time.Duration
	// RetryAfter is sent with 503 when the live state is unreachable (5s).
	RetryAfter time.Duration
	// RecountInterval is how often hello_sip_registrations is refreshed (15s).
	RecountInterval time.Duration
}

// Deps are the Server's collaborators.
type Deps struct {
	Snapshots Snapshots
	State     State
	Throttle  Throttle
	CDRs      CDRSink
	Metrics   *Metrics
	Log       *slog.Logger
}

// Server is one SIP node.
type Server struct {
	cfg     Config
	deps    Deps
	log     *slog.Logger
	m       *Metrics
	digest  *Digest
	contact sip.ContactHeader
	advHost string
	advPort int
	recount chan struct{}
	ua      *sipgo.UserAgent
	client  *sipgo.Client
	uas     *sipgo.DialogUA // A legs: callers
	uac     *sipgo.DialogUA // B legs: forks
	bg      sync.WaitGroup
	mu      sync.Mutex
	dialogs map[string]dialogRef // Call-ID -> call side
	calls   map[*call]struct{}
	done    chan struct{} // closed when Serve returns
	laddr   sip.Addr      // the listening socket, for requests we originate
}

// dialogRef is one leg of a call, found by its Call-ID.
type dialogRef struct {
	c   *call
	leg *leg // nil for the caller's leg
}

// New validates cfg and returns a Server; Serve starts it.
func New(cfg Config, deps Deps) (*Server, error) {
	switch {
	case cfg.Domain == "":
		return nil, errors.New("sip: domain required")
	case len(cfg.NonceSecret) < 32:
		return nil, errors.New("sip: nonce secret must be at least 32 bytes")
	case deps.Snapshots == nil || deps.State == nil || deps.Throttle == nil || deps.CDRs == nil || deps.Metrics == nil:
		return nil, errors.New("sip: missing dependency")
	}
	host, port, err := sip.ParseAddr(cfg.AdvertisedAddr)
	if err != nil {
		return nil, fmt.Errorf("sip: advertised address: %w", err)
	}
	if deps.Log == nil {
		deps.Log = slog.New(slog.DiscardHandler)
	}
	setDefault(&cfg.CallHeartbeat, 10*time.Second)
	setDefault(&cfg.CallTTL, 30*time.Second)
	setDefault(&cfg.RetryAfter, 5*time.Second)
	setDefault(&cfg.RecountInterval, 15*time.Second)
	setDefault(&cfg.StateTimeout, 200*time.Millisecond)
	setDefault(&cfg.RingTimeout, 30*time.Second)
	if cfg.AuthFailLimit <= 0 {
		cfg.AuthFailLimit = 10
	}
	s := &Server{
		cfg: cfg, deps: deps, m: deps.Metrics,
		log:     slog.New(NewRedactingHandler(deps.Log.Handler())),
		digest:  &Digest{Realm: cfg.Domain, Secret: cfg.NonceSecret},
		advHost: host, advPort: port,
		recount: make(chan struct{}, 1),
		dialogs: map[string]dialogRef{},
		calls:   map[*call]struct{}{},
		done:    make(chan struct{}),
	}
	s.contact = sip.ContactHeader{Address: sip.Uri{Scheme: "sip", Host: host, Port: port, UriParams: sip.HeaderParams{{K: "transport", V: "udp"}}}}
	return s, nil
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

// Serve handles SIP on conn until ctx is cancelled or conn fails; it closes
// conn and the transaction layer before returning.
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) error {
	ua, err := sipgo.NewUA(
		sipgo.WithUserAgent("Hello"),
		sipgo.WithUserAgentHostname(s.cfg.Domain),
		sipgo.WithUserAgentTransactionLayerOptions(
			sip.WithTransactionLayerLogger(s.log),
			sip.WithTransactionLayerUnhandledResponseHandler(func(r *sip.Response) {
				s.log.Debug("unmatched SIP response", "code", r.StatusCode)
			}),
		),
		sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerLogger(s.log)),
	)
	if err != nil {
		return err
	}
	srv, err := sipgo.NewServer(ua, sipgo.WithServerLogger(s.log))
	if err != nil {
		return err
	}
	client, err := sipgo.NewClient(ua,
		sipgo.WithClientAddr(s.cfg.AdvertisedAddr),
		// Send fresh requests from the listening socket, so replies and
		// NAT pinholes use the SIP port.
		sipgo.WithClientConnectionAddr(conn.LocalAddr().String()),
		sipgo.WithClientLogger(s.log),
	)
	if err != nil {
		return err
	}
	s.ua, s.client = ua, client
	if host, port, err := sip.ParseAddr(conn.LocalAddr().String()); err == nil {
		s.laddr = sip.Addr{IP: net.ParseIP(host), Port: port, Hostname: host}
	}
	s.uas = &sipgo.DialogUA{Client: client, ContactHDR: s.contact, RewriteContact: true}
	s.uac = &sipgo.DialogUA{Client: client, ContactHDR: s.contact, RewriteContact: true}

	s.handle(srv, sip.OPTIONS, s.handleOptions)
	s.handle(srv, sip.REGISTER, s.handleRegister)
	s.handle(srv, sip.INVITE, s.handleInvite)
	s.handle(srv, sip.ACK, s.handleAck)
	s.handle(srv, sip.BYE, s.handleBye)
	s.handle(srv, sip.CANCEL, s.handleStrayCancel)
	s.handle(srv, sip.UPDATE, s.handleInDialog)
	srv.OnNoRoute(s.wrap(s.handleNotAllowed))

	rctx, stop := context.WithCancel(ctx)
	defer stop()
	s.bg.Go(func() { s.recountLoop(rctx) })

	errc := make(chan error, 1)
	go func() { errc <- srv.ServeUDP(conn) }()
	select {
	case <-ctx.Done():
		_ = conn.Close()
		<-errc
		err = nil
	case err = <-errc:
		_ = conn.Close()
	}
	stop()
	close(s.done)
	_ = ua.Close()
	s.bg.Wait()
	return err
}

const allow = "INVITE, ACK, CANCEL, BYE, OPTIONS, REGISTER, UPDATE"

func (s *Server) handle(srv *sipgo.Server, m sip.RequestMethod, h sipgo.RequestHandler) {
	srv.OnRequest(m, s.wrap(h))
}

// wrap counts the request, rejects ones missing the headers every handler
// relies on, and contains panics so one bad message cannot stop the node.
func (s *Server) wrap(h sipgo.RequestHandler) sipgo.RequestHandler {
	return func(req *sip.Request, tx sip.ServerTransaction) {
		s.m.request(req.Method.String())
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("SIP handler panic", "method", req.Method.String(), "panic", fmt.Sprint(r))
				if !req.IsAck() && tx != nil {
					s.respond(tx, req, sip.StatusInternalServerError, "Server Internal Error")
				}
			}
		}()
		if req.CallID() == nil || req.From() == nil || req.To() == nil || req.CSeq() == nil {
			if !req.IsAck() {
				s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
			}
			return
		}
		// A Route with a flow token makes this node an edge proxy for the
		// request; a self Route without one (outbound proxy) is ignored.
		if token, ok := edgeToken(req); ok {
			s.proxy(req, tx, token)
			return
		}
		h(req, tx)
	}
}

func (s *Server) respond(tx sip.ServerTransaction, req *sip.Request, code int, reason string, hdrs ...sip.Header) {
	res := sip.NewResponseFromRequest(req, code, reason, nil)
	for _, h := range hdrs {
		res.AppendHeader(h)
	}
	s.send(tx, res)
}

func (s *Server) send(tx sip.ServerTransaction, res *sip.Response) {
	if err := tx.Respond(res); err != nil {
		s.log.Debug("SIP respond failed", "code", res.StatusCode, "error", err)
		return
	}
	s.m.response(res.StatusCode)
}

func (s *Server) unavailable(tx sip.ServerTransaction, req *sip.Request) {
	s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable",
		sip.NewHeader("Retry-After", strconv.Itoa(int(s.cfg.RetryAfter/time.Second))))
}

func (s *Server) stateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.cfg.StateTimeout)
}

func (s *Server) aor(username string) string { return "sip:" + username + "@" + s.cfg.Domain }

func (s *Server) handleOptions(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	res.AppendHeader(sip.HeaderClone(&s.contact))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	res.AppendHeader(sip.NewHeader("Accept", "application/sdp"))
	s.send(tx, res)
}

func (s *Server) handleNotAllowed(req *sip.Request, tx sip.ServerTransaction) {
	if req.IsAck() {
		return
	}
	s.respond(tx, req, sip.StatusMethodNotAllowed, "Method Not Allowed", sip.NewHeader("Allow", allow))
}

// handleStrayCancel answers a CANCEL matching no pending INVITE; one that
// matches is handled by the transaction layer and the call's OnCancel.
func (s *Server) handleStrayCancel(req *sip.Request, tx sip.ServerTransaction) {
	s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
}

func sourceIP(req *sip.Request) string {
	host, _, err := net.SplitHostPort(req.Source())
	if err != nil {
		return req.Source()
	}
	return host
}

// authenticate runs the digest exchange for req. It returns the device on
// success; otherwise it has already answered (401, 403 or 503).
func (s *Server) authenticate(req *sip.Request, tx sip.ServerTransaction) (snapshot.Device, bool) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		s.unavailable(tx, req)
		return snapshot.Device{}, false
	}
	ip := sourceIP(req)
	ctx, cancel := s.stateCtx()
	n, err := s.deps.Throttle.Failures(ctx, ip)
	cancel()
	if err != nil {
		s.log.Warn("live state unavailable", "op", "authfail_get", "error", err)
		s.unavailable(tx, req)
		return snapshot.Device{}, false
	}
	if n >= int64(s.cfg.AuthFailLimit) {
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return snapshot.Device{}, false
	}
	h := req.GetHeader("Authorization")
	if h == nil {
		s.challenge(tx, req, false)
		return snapshot.Device{}, false
	}
	creds, err := ParseCredentials(h.Value())
	if err != nil {
		s.authFailed(tx, req, ip, "malformed credentials", true)
		return snapshot.Device{}, false
	}
	dev, ok := snap.DeviceByUsername(creds.Username)
	if !ok {
		// Unknown or disabled: no challenge would help.
		s.authFailed(tx, req, ip, "unknown or disabled device", false)
		return snapshot.Device{}, false
	}
	switch s.digest.Verify(creds, req.Method.String(), dev.HA1MD5, dev.HA1SHA256) {
	case OK:
		return dev, true
	case Stale:
		s.challenge(tx, req, true)
	default:
		s.authFailed(tx, req, ip, "bad credentials", true)
	}
	return snapshot.Device{}, false
}

func (s *Server) challenge(tx sip.ServerTransaction, req *sip.Request, stale bool) {
	res := sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil)
	for _, c := range s.digest.Challenges(stale) {
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", c))
	}
	s.send(tx, res)
}

// authFailed counts a failure for ip and answers 401 (rechallenge) or 403.
func (s *Server) authFailed(tx sip.ServerTransaction, req *sip.Request, ip, why string, rechallenge bool) {
	ctx, cancel := s.stateCtx()
	err := s.deps.Throttle.RecordFailure(ctx, ip)
	cancel()
	if err != nil {
		s.log.Warn("live state unavailable", "op", "authfail_incr", "error", err)
		s.unavailable(tx, req)
		return
	}
	s.log.Info("SIP authentication failed", "method", req.Method.String(), "source", req.Source(), "reason", why)
	if rechallenge {
		s.challenge(tx, req, false)
		return
	}
	s.respond(tx, req, sip.StatusForbidden, "Forbidden")
}

// SnapshotChanged removes the bindings of devices that are no longer in the
// snapshot (deleted or disabled). Use it as the watcher's OnReload.
func (s *Server) SnapshotChanged(old, cur *snapshot.Snapshot) {
	if old == nil || cur == nil {
		return
	}
	var gone []string
	for _, u := range old.Usernames() {
		if _, ok := cur.DeviceByUsername(u); !ok {
			gone = append(gone, u)
		}
	}
	if len(gone) == 0 {
		return
	}
	go func() {
		for _, u := range gone {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := s.deps.State.DeleteAOR(ctx, s.aor(u)); err != nil {
				s.log.Warn("could not remove bindings of removed device", "device", u, "error", err)
			}
			cancel()
		}
		s.triggerRecount()
	}()
}

func (s *Server) triggerRecount() {
	select {
	case s.recount <- struct{}{}:
	default:
	}
}

func (s *Server) recountLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.RecountInterval)
	defer t.Stop()
	for {
		s.recountRegistrations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.recount:
		}
	}
}

func (s *Server) recountRegistrations(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	bs, err := s.deps.State.AllBindings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Debug("registration recount failed", "error", err)
		}
		return
	}
	n := 0
	for _, b := range bs {
		if b.ReceivedNode == s.cfg.NodeID {
			n++
		}
	}
	s.m.Registrations.Set(float64(n))
}

// ActiveCalls is the number of calls this node owns.
func (s *Server) ActiveCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *Server) lookup(callID string) (dialogRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.dialogs[callID]
	return r, ok
}

func (s *Server) bind(callID string, r dialogRef) {
	s.mu.Lock()
	s.dialogs[callID] = r
	if r.leg == nil {
		s.calls[r.c] = struct{}{}
	}
	s.mu.Unlock()
}

func (s *Server) unbind(callID string, c *call) {
	s.mu.Lock()
	if r, ok := s.dialogs[callID]; ok && r.c == c {
		delete(s.dialogs, callID)
		if r.leg == nil {
			delete(s.calls, c)
		}
	}
	s.mu.Unlock()
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
