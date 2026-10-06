// Package sip is Hello's real-time SIP core: a UDP listener built on sipgo's
// transport and transaction layers, digest authentication with stateless
// nonces, a registrar writing bindings to the live state, and a B2BUA for
// internal calls. sipgo stays behind this package; callers see only Server.
package sip

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Config is the SIP node's behaviour; see config.SIP for the meaning of each.
type Config struct {
	NodeID string
	// Incarnation is this process's id (cluster.NewIncarnation; generated
	// when empty): the replicated dialogs it owns carry it, so a node
	// restarted in place under the same NodeID recognises the calls of its
	// previous process as orphans and takes them over at once.
	Incarnation    string
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
	// MaxCallDuration ends a connected call with BYE to both legs (4h).
	MaxCallDuration time.Duration
	// PeerRefresh is how often this node announces itself and reloads the
	// peer set (10s); NodeTTL is how long its announcement lives (30s).
	PeerRefresh time.Duration
	NodeTTL     time.Duration

	// Trunks. TrunkLeaseRefresh is how often a node takes or renews each
	// trunk's lease (10s; the lease lives 3x as long). TrunkStatusPoll is
	// how often every node reads the shared trunk status (2s).
	// TrunkAttemptTimeout fails a trunk attempt over when the destination
	// sends nothing beyond 100 Trying (32s, Timer B). TrunkRetryBase and
	// TrunkRetryMax bound the registration backoff (30s, 10m).
	// TrunkOptionsTimeout bounds one OPTIONS (5s, at most the interval).
	TrunkLeaseRefresh   time.Duration
	TrunkStatusPoll     time.Duration
	TrunkAttemptTimeout time.Duration
	TrunkRetryBase      time.Duration
	TrunkRetryMax       time.Duration
	TrunkOptionsTimeout time.Duration
	// TrunkReRegisterMin floors the re-REGISTER interval (30s).
	TrunkReRegisterMin time.Duration
	// PresenceTTL is how long a device's published state lives in Valkey
	// without a refresh (2m; refreshed on REGISTER and every state change).
	PresenceTTL time.Duration
	// TrustedProxies are the edge proxies (Kamailio) whose X-Hello-Client
	// and Path headers are believed (plan contract 7).
	TrustedProxies []netip.Prefix

	// RTPPortMin and RTPPortMax bound the UDP port range anchored media
	// sessions draw their relay sockets from (20000-21000 default,
	// config.SIP).
	RTPPortMin int
	RTPPortMax int
	// MediaForceAnchor anchors every call's media regardless of the
	// per-call triggers (HELLO_MEDIA_FORCE_ANCHOR, spec S-1d).
	MediaForceAnchor bool
	// MediaRecordingNotice plays the recording-notice announcement before
	// a recording starts (default true).
	MediaRecordingNotice bool

	// In-call HA (Phase 7). HADialogHeartbeat is the replication cadence
	// (5s; the record's TTL is 30s). HATakeoverEnabled gates the orphan
	// poller; HATakeoverPoll is its base interval and HATakeoverJitter the
	// random extra delay that keeps survivors out of lockstep.
	HADialogHeartbeat time.Duration
	HATakeoverEnabled bool
	HATakeoverPoll    time.Duration
	HATakeoverJitter  time.Duration
}

// Deps are the Server's collaborators.
type Deps struct {
	Snapshots Snapshots
	State     State
	Throttle  Throttle
	CDRs      CDRSink
	Metrics   *Metrics
	Log       *slog.Logger
	// Presence lists the cluster's SIP nodes for the edge proxy; nil means
	// this node is alone (it never relays for another node).
	Presence Presence
	// Trunks is the shared trunk state; nil disables trunk calls,
	// registration and health checks.
	Trunks TrunkState
	// Lifecycle is the node's state machine; nil means always READY.
	Lifecycle Lifecycle
	// Voicemails is the voicemail store (PostgreSQL via hello-sip's
	// long-lived pool); nil disables voicemail. Its methods run only in the
	// voicemail media goroutine, never on the SIP transaction path.
	Voicemails VoicemailStore
	// Objects is the voicemail audio store (MinIO); nil disables voicemail.
	Objects ObjectStore
	// Settings applies feature-code mutations (DND, forwarding) by queueing
	// them to PostgreSQL and bumping the configuration revision; nil makes
	// the mutating feature codes answer 503.
	Settings SettingsSink
	// Media holds the anchored-media metrics; nil disables them (tests).
	Media *media.Metrics
	// Recordings is the recordings metadata store (PostgreSQL); nil
	// disables recording. Its methods run only in a media goroutine, never
	// on the SIP transaction path.
	Recordings RecordingStore
	// HAState is the dialog-replication store (Phase 7); nil disables
	// in-call HA (no replication, no takeover).
	HAState HAState
	// Membership reports the cluster's nodes (Phase 3); nil disables
	// takeover.
	Membership Membership
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
	// bgMu guards bgClosed: once shutdown waits for bg, a late call event
	// (a call ending as the node stops) must not start more work on it.
	bgMu     sync.Mutex
	bgClosed bool
	mu       sync.Mutex
	dialogs  map[string]dialogRef // Call-ID -> call side
	calls    map[*call]struct{}
	done     chan struct{} // closed when Serve returns
	laddr    sip.Addr      // the listening socket, for requests we originate
	peers    peers         // the cluster's SIP nodes, for the edge proxy
	serving  atomic.Bool   // the listener is up
	trunks   *trunkManager
	// registrations is the last count of bindings this node registered.
	registrations atomic.Int64
	// answerHook, when set (tests only), runs as a call's winning fork is
	// connected, before the call is marked connected.
	answerHook atomic.Pointer[func()]
	// abortHook, when set (tests only), runs inside abort after it has
	// taken the setup's ownership before it signals the setup goroutine, so
	// a test can inject the fork failure that races the abort.
	abortHook atomic.Pointer[func(*call)]

	// haOffline tracks OFFLINE nodes for the zombie reaper (guarded by
	// haOfflineMu).
	haOfflineMu sync.Mutex
	haOffline   map[string]*haOfflineNode
	// haLastActive is each live SIP node's last published call count
	// (guarded by haOfflineMu).
	haLastActive map[string]int
	// haIncSeen is each SIP node's last seen incarnation and until when
	// its dialogs are scanned for a dead incarnation's (guarded by
	// haOfflineMu).
	haIncSeen map[string]haIncWatch
	// haSent is the highest raw (outside sipgo's dialog sessions) CSeq
	// this node sent per dialog Call-ID, so replicated CSeqs continue past
	// it; entries go with the dialog's binding.
	haSent sync.Map
	// haGone holds the dialog Call-IDs of calls this node yielded to a
	// taker (the value is the taker): their in-dialog requests get 503 so
	// the edge retries them on a survivor.
	haGone sync.Map
	// handoff is set while the node drains with in-call HA: its calls are
	// being handed to survivors.
	handoff atomic.Bool

	// Presence (S-10) and feature-code (S-11) state. subs holds the live
	// dialog subscriptions by Call-ID, byExt the per-extension index used
	// for NOTIFY fan-out; rrPos is the round-robin rotation per group; lastEnd
	// tracks the last call end per extension for longest-idle; digits holds
	// an in-dialog DTMF collection per call. anchor binds voicemail media
	// (nil unless SetMediaAnchor ran); vmBytes is this node's running total
	// of stored voicemail audio.
	subs     map[string]*subscription
	byExt    map[string]map[string]bool
	subsMu   sync.Mutex
	rrPos    map[int64]*atomic.Uint64
	rrMu     sync.Mutex
	lastEnd  map[string]time.Time
	lastMu   sync.Mutex
	digits   map[*call]*digitBuffer
	digitsMu sync.Mutex
	anchor   atomic.Pointer[media.Anchor]
	vmBytes  atomic.Int64
}

// SetMediaAnchor wires the voicemail media anchor; without it voicemail
// calls answer without recording (and store nothing). Call it before Serve;
// the atomic keeps the write ordered against every later reader either way.
func (s *Server) SetMediaAnchor(a *media.Anchor) { s.anchor.Store(a) }

// digitBuffer collects in-dialog DTMF digits for one call's feature-code
// dispatch (S-11). '#' hands the collected digits on; a fresh code restarts
// the buffer, so a mistyped code never dispatches late.
type digitBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// push appends digits and reports the whole buffer.
func (d *digitBuffer) push(digits []byte) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, digits...)
	return d.buf
}

// take empties the buffer and returns what it held.
func (d *digitBuffer) take() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.buf
	d.buf = nil
	return out
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
	if cfg.Incarnation == "" {
		cfg.Incarnation = cluster.NewIncarnation()
	}
	setDefault(&cfg.CallHeartbeat, 10*time.Second)
	setDefault(&cfg.CallTTL, 30*time.Second)
	setDefault(&cfg.RetryAfter, 5*time.Second)
	setDefault(&cfg.RecountInterval, 15*time.Second)
	setDefault(&cfg.StateTimeout, 200*time.Millisecond)
	setDefault(&cfg.RingTimeout, 30*time.Second)
	setDefault(&cfg.MaxCallDuration, 4*time.Hour)
	setDefault(&cfg.PeerRefresh, 10*time.Second)
	setDefault(&cfg.NodeTTL, 30*time.Second)
	setDefault(&cfg.TrunkLeaseRefresh, 10*time.Second)
	setDefault(&cfg.TrunkStatusPoll, 2*time.Second)
	setDefault(&cfg.TrunkAttemptTimeout, 32*time.Second)
	setDefault(&cfg.TrunkRetryBase, 30*time.Second)
	setDefault(&cfg.TrunkRetryMax, 10*time.Minute)
	setDefault(&cfg.TrunkOptionsTimeout, 5*time.Second)
	setDefault(&cfg.TrunkReRegisterMin, 30*time.Second)
	setDefault(&cfg.PresenceTTL, 2*time.Minute)
	if cfg.AuthFailLimit <= 0 {
		cfg.AuthFailLimit = 10
	}
	if cfg.RTPPortMin <= 0 || cfg.RTPPortMax <= cfg.RTPPortMin {
		cfg.RTPPortMin, cfg.RTPPortMax = 20000, 21000
	}
	s := &Server{
		cfg: cfg, deps: deps, m: deps.Metrics,
		log:     slog.New(NewRedactingHandler(deps.Log.Handler())),
		digest:  &Digest{Realm: cfg.Domain, Secret: cfg.NonceSecret},
		advHost: host, advPort: port,
		recount:   make(chan struct{}, 1),
		dialogs:   map[string]dialogRef{},
		calls:     map[*call]struct{}{},
		done:      make(chan struct{}),
		subs:      map[string]*subscription{},
		byExt:     map[string]map[string]bool{},
		rrPos:     map[int64]*atomic.Uint64{},
		lastEnd:   map[string]time.Time{},
		digits:    map[*call]*digitBuffer{},
		haOffline: map[string]*haOfflineNode{}, haLastActive: map[string]int{}, haIncSeen: map[string]haIncWatch{},
	}
	setDefault(&cfg.HADialogHeartbeat, 5*time.Second)
	setDefault(&cfg.HATakeoverPoll, time.Second)
	setDefault(&cfg.HATakeoverJitter, 2*time.Second)
	s.trunks = newTrunkManager(s)
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
		// rport (RFC 3581) on our Via: replies come back to the socket we
		// sent from, even when the far side sees us through NAT.
		sipgo.WithClientNAT(),
		sipgo.WithClientLogger(s.log),
	)
	if err != nil {
		return err
	}
	s.ua, s.client = ua, client
	if host, port, err := sip.ParseAddr(conn.LocalAddr().String()); err == nil {
		s.laddr = sip.Addr{IP: net.ParseIP(host), Port: port, Hostname: host}
	}
	s.refreshPeers(ctx) // before serving, so the edge knows its peers
	s.uas = &sipgo.DialogUA{Client: client, ContactHDR: s.contact, RewriteContact: true}
	s.uac = &sipgo.DialogUA{Client: client, ContactHDR: s.contact, RewriteContact: true}

	s.handle(srv, sip.OPTIONS, s.handleOptions)
	s.handle(srv, sip.REGISTER, s.handleRegister)
	s.handle(srv, sip.INVITE, s.handleInvite)
	s.handle(srv, sip.ACK, s.handleAck)
	s.handle(srv, sip.BYE, s.handleBye)
	s.handle(srv, sip.CANCEL, s.handleStrayCancel)
	s.handle(srv, sip.UPDATE, s.handleInDialog)
	s.handle(srv, sip.REFER, s.handleRefer)
	s.handle(srv, sip.SUBSCRIBE, s.handleSubscribe)
	s.handle(srv, sip.INFO, s.handleInfo)
	s.handle(srv, sip.MESSAGE, s.handleMessage)
	srv.OnNoRoute(s.wrap(s.handleNotAllowed))

	rctx, stop := context.WithCancel(ctx)
	defer stop()
	s.bg.Go(func() { s.recountLoop(rctx) })
	s.bg.Go(func() { s.peerLoop(rctx) })
	s.bg.Go(func() { s.subExpireLoop(rctx) })
	if s.deps.HAState != nil && s.deps.Membership != nil && s.cfg.HATakeoverEnabled {
		// A takeover re-INVITEs from the listening socket, which sipgo
		// only knows once ServeUDP registered it: a restarted node's first
		// pass reclaims its own calls at once, and must not send before.
		laddr := conn.LocalAddr().String()
		s.bg.Go(func() {
			if waitListening(rctx, ua, laddr) {
				s.takeoverLoop(rctx)
			}
		})
	}

	// Trunk holders stop before the socket closes, so they can unregister
	// and release their leases for another node.
	tctx, stopTrunks := context.WithCancel(context.Background())
	trunksDone := make(chan struct{})
	defer func() { stopTrunks(); <-trunksDone }()

	errc := make(chan error, 1)
	s.serving.Store(true)
	go func() { errc <- srv.ServeUDP(conn) }()
	go func() {
		defer close(trunksDone)
		// Requests we originate leave from the listening socket, which
		// sipgo only knows once ServeUDP has registered it: sending
		// earlier fails (a second bind on the port), so the first OPTIONS
		// would mark a healthy carrier down.
		if waitListening(tctx, ua, conn.LocalAddr().String()) {
			s.trunks.run(tctx)
		}
	}()
	select {
	case <-ctx.Done():
		stopTrunks()
		<-trunksDone
		_ = conn.Close()
		<-errc
		err = nil
	case err = <-errc:
		_ = conn.Close()
	}
	s.serving.Store(false)
	stop()
	close(s.done)
	_ = ua.Close()
	s.bgMu.Lock()
	s.bgClosed = true
	s.bgMu.Unlock()
	s.bg.Wait()
	return err
}

// goBG runs f as a background task that shutdown waits for; once shutdown
// has begun, f is dropped (a WaitGroup must not grow while Wait runs).
func (s *Server) goBG(f func()) {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.bgClosed {
		return
	}
	s.bg.Go(f)
}

// waitListening waits until sipgo serves the listener at addr.
func waitListening(ctx context.Context, ua *sipgo.UserAgent, addr string) bool {
	for {
		if c, _ := ua.TransportLayer().GetConnection("udp", addr); c != nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

const allow = "INVITE, ACK, CANCEL, BYE, OPTIONS, REGISTER, UPDATE, REFER, SUBSCRIBE, INFO, MESSAGE"

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
		// A top Route naming this node with a flow token makes it an edge
		// proxy for the request, except for REGISTER and OPTIONS, which
		// are always ours. proxy declines (false) a request it must not
		// relay, which is then handled here like any other; handlers do not
		// use the Route header, so a self Route needs no stripping.
		if token, ok := s.edgeToken(req); ok && req.Method != sip.REGISTER && req.Method != sip.OPTIONS {
			if s.proxy(req, tx, token) {
				return
			}
		}
		if s.handedOff(req, tx) || s.haOnDemand(req, tx) {
			return
		}
		h(req, tx)
	}
}

func (s *Server) respond(tx sip.ServerTransaction, req *sip.Request, code int, reason string, hdrs ...sip.Header) {
	res := symmetric(req, sip.NewResponseFromRequest(req, code, reason, nil))
	for _, h := range hdrs {
		res.AppendHeader(h)
	}
	s.send(tx, res)
}

// symmetric sends res back to the address its request came from (RFC 3581
// behaviour whether or not the Via asked for rport). Without it a request
// whose Via names another port than its source - an edge proxy's dispatcher
// probe after the edge was replaced, a NATed peer - is answered at the Via
// port, which on kw kept a replaced Kamailio's stale conntrack entries alive
// and left both nodes probed DOWN.
func symmetric(req *sip.Request, res *sip.Response) *sip.Response {
	if src := req.Source(); src != "" {
		res.SetDestination(src)
	}
	return res
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
	if s.isSelfProbe(req) {
		// The balancer's probe: only a READY node is in rotation.
		// The Warning names the state only: the reason holds check errors
		// (internal addresses) that anyone sending OPTIONS would read.
		if st, _ := s.state(); st != cluster.Ready {
			res := symmetric(req, sip.NewResponseFromRequest(req, sip.StatusServiceUnavailable, "Service Unavailable", nil))
			res.AppendHeader(sip.NewHeader("Retry-After", "5"))
			res.AppendHeader(sip.NewHeader("Warning", `399 hello "`+string(st)+`"`))
			s.send(tx, res)
			return
		}
	}
	res := symmetric(req, sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
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

// authenticate runs the digest exchange for req. On success it returns the
// device and the snapshot it was found in, which the caller keeps using so
// one request sees one configuration; otherwise it has already answered
// (401, 403 or 503).
func (s *Server) authenticate(req *sip.Request, tx sip.ServerTransaction) (snapshot.Device, *snapshot.Snapshot, bool) {
	dev, snap := snapshot.Device{}, s.deps.Snapshots.Current()
	if snap == nil {
		s.unavailable(tx, req)
		return dev, nil, false
	}
	dev, ok := s.verify(req, tx, snap)
	return dev, snap, ok
}

func (s *Server) verify(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot) (snapshot.Device, bool) {
	ip := s.clientIP(req)
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
	dev, known := snap.DeviceByUsername(creds.Username)
	if !known {
		// Unknown or disabled: no challenge would help.
		s.authFailed(tx, req, ip, "unknown or disabled device", false)
		return snapshot.Device{}, false
	}
	switch s.digest.Verify(creds, req.Method.String(), req.Recipient, ip, dev.HA1MD5, dev.HA1SHA256) {
	case OK:
		return s.checkReplay(req, tx, creds.Username, creds.Nonce, creds.Cnonce, creds.Nc, dev)
	case Stale:
		s.challenge(tx, req, true)
	default:
		s.authFailed(tx, req, ip, "bad credentials", true)
	}
	return snapshot.Device{}, false
}

// checkReplay accepts verified credentials only if their nonce count is
// higher than any used before with the same nonce and cnonce. qop=auth does
// not cover the message body or Contact, so without this a captured
// REGISTER Authorization could be replayed (from the same IP, within the
// nonce's life) to repoint the binding.
//
// A replay is answered 401 stale=true and is not counted as a failed
// attempt: the response it carries is correct, so the client (or a client
// that reused nc by mistake) is told to retry with a fresh nonce, which
// only the password holder can answer. The state lives in Valkey so every
// node sees it; if Valkey is unreachable the request gets 503, as every
// other auth-state failure on this path does.
func (s *Server) checkReplay(req *sip.Request, tx sip.ServerTransaction, user, nonce, cnonce string, nc int, dev snapshot.Device) (snapshot.Device, bool) {
	if len(cnonce) > 256 {
		s.challenge(tx, req, false)
		return snapshot.Device{}, false
	}
	sum := sha256.Sum256([]byte(user + "\x00" + nonce + "\x00" + cnonce))
	ctx, cancel := s.stateCtx()
	fresh, err := s.deps.Throttle.AdvanceNonceCount(ctx, hex.EncodeToString(sum[:16]), int64(nc), NonceValidity+nonceSkew)
	cancel()
	if err != nil {
		s.log.Warn("live state unavailable", "op", "digest_nc", "error", err)
		s.unavailable(tx, req)
		return snapshot.Device{}, false
	}
	if !fresh {
		s.log.Info("SIP digest replay refused", "method", req.Method.String(), "source", req.Source())
		s.challenge(tx, req, true)
		return snapshot.Device{}, false
	}
	return dev, true
}

func (s *Server) challenge(tx sip.ServerTransaction, req *sip.Request, stale bool) {
	res := sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil)
	for _, c := range s.digest.Challenges(stale, s.clientIP(req)) {
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", c))
	}
	s.send(tx, res)
}

// authFailed counts a failure for ip and answers 401 (rechallenge) or 403.
//
// The 401/403 decision uses the count after this failure, so concurrent bad
// attempts that all passed the precheck cannot all be rechallenged: within
// one window at most AuthFailLimit failed attempts from an IP get a 401,
// every later one 403.
func (s *Server) authFailed(tx sip.ServerTransaction, req *sip.Request, ip, why string, rechallenge bool) {
	ctx, cancel := s.stateCtx()
	n, err := s.deps.Throttle.RecordFailure(ctx, ip)
	cancel()
	if err != nil {
		s.log.Warn("live state unavailable", "op", "authfail_incr", "error", err)
		s.unavailable(tx, req)
		return
	}
	s.log.Info("SIP authentication failed", "method", req.Method.String(), "source", s.clientSource(req), "reason", why)
	if rechallenge && n <= int64(s.cfg.AuthFailLimit) {
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
	s.registrations.Store(int64(n))
}

// Incarnation is this process's incarnation id (Config.Incarnation), which
// the node's membership record must carry.
func (s *Server) Incarnation() string { return s.cfg.Incarnation }

// Serving reports whether the SIP listener is running; it turns false when
// Serve returns, including when the socket fails.
func (s *Server) Serving() bool { return s.serving.Load() }

// SetLifecycle wires the node's state machine (created after the Server,
// since its checks use the Server); call it before Serve.
func (s *Server) SetLifecycle(l Lifecycle) { s.deps.Lifecycle = l }

// Registrations is the number of unexpired bindings whose last REGISTER
// this node handled (as of the last recount).
func (s *Server) Registrations() int { return int(s.registrations.Load()) }

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
		s.haSent.Delete(callID)
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
