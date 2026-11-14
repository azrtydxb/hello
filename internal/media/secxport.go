// The ICE-lite and DTLS-SRTP side of Secure (phone-feature-parity S-16): what
// an endpoint's socket carries besides RTP, and the negotiation of a=ice-* and
// a=fingerprint/a=setup that goes with it.
//
// A host binds its socket to the endpoint once (Bind) and hands every
// datagram it reads to UnprotectFrom, which sends STUN to the ICE-lite agent
// and DTLS to the handshake and returns only media. Everything else, the
// SDP, the policy and the call, stays with the caller.
package media

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Features switch DTLS-SRTP and ICE-lite on a node (HELLO_MEDIA_DTLS and
// HELLO_MEDIA_ICE); both are on by default. With DTLS off, an offer that
// carries only DTLS is refused (488); with ICE off, ICE attributes are
// ignored as before.
var (
	dtlsOff atomic.Bool
	iceOff  atomic.Bool
)

// SetFeatures turns DTLS-SRTP and ICE-lite on or off for this process.
func SetFeatures(dtlsOn, iceOn bool) {
	dtlsOff.Store(!dtlsOn)
	iceOff.Store(!iceOn)
}

// DTLSEnabled reports whether this node terminates DTLS-SRTP.
func DTLSEnabled() bool { return !dtlsOff.Load() }

// ICEEnabled reports whether this node answers as ICE-lite.
func ICEEnabled() bool { return !iceOff.Load() }

var (
	// ErrNoTransport is returned when DTLS or ICE is negotiated on an
	// endpoint whose socket was never bound.
	ErrNoTransport = errors.New("media: the endpoint has no DTLS/ICE transport")
	// ErrDTLSOff is returned for a DTLS-only offer on a node with DTLS off.
	ErrDTLSOff = errors.New("media: DTLS-SRTP is disabled on this node")
)

// xport is an endpoint's ICE-lite agent and DTLS session on its socket.
type xport struct {
	send  func(b []byte, to *net.UDPAddr)
	onICE func(from *net.UDPAddr, nominated bool)
	local net.Addr

	mu       sync.Mutex
	peer     *net.UDPAddr
	ice      *iceLite
	verified bool
	ds       *dtlsSession
	client   bool
	mux      bool
	onFail   func(reason string)
	onReady  func()
}

// muxed reports whether RTCP shares the RTP port.
func (x *xport) muxed() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.mux
}

// Bind attaches the endpoint to its socket: send writes a datagram from it,
// local is its address, and onICE is told about every authenticated ICE check
// (the address it came from and whether it nominated the pair) so the host
// can accept media from that address. A host that does not bind cannot
// negotiate ICE or DTLS on the endpoint.
func (s *Secure) Bind(local net.Addr, send func(b []byte, to *net.UDPAddr), onICE func(from *net.UDPAddr, nominated bool)) {
	s.xp.Store(&xport{send: send, onICE: onICE, local: local})
}

// OnFail registers what runs when the endpoint's DTLS handshake fails or
// times out: reason is a word (timeout, fingerprint, failed). The endpoint
// then carries no media; the host ends the call.
func (s *Secure) OnFail(fn func(reason string)) {
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		x.onFail = fn
		x.mu.Unlock()
	}
}

// OnReady registers what runs when the endpoint's DTLS handshake has finished
// and its keys are in, so the host can record the encryption.
func (s *Secure) OnReady(fn func()) {
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		x.onReady = fn
		x.mu.Unlock()
	}
}

// SetPeer tells the endpoint where its far end's media is expected (the
// address in the far end's SDP, or a moved one): DTLS and ICE answers go
// there until a datagram or an ICE check reveals the real source.
func (s *Secure) SetPeer(addr *net.UDPAddr) {
	x := s.xp.Load()
	if x == nil || addr == nil {
		return
	}
	x.mu.Lock()
	x.peer = addr
	x.readyLocked()
	x.mu.Unlock()
}

// readyLocked starts a waiting DTLS client when its first flight can be sent.
func (x *xport) readyLocked() {
	if x.ds != nil && x.client && x.peer != nil && (x.ice == nil || x.verified) {
		x.ds.markReady()
	}
}

func (x *xport) sendDTLS(b []byte) error {
	x.mu.Lock()
	peer := x.peer
	x.mu.Unlock()
	if peer == nil {
		return errors.New("media: DTLS peer address unknown")
	}
	x.send(b, peer)
	return nil
}

// stunIn answers one STUN datagram.
func (s *Secure) stunIn(x *xport, data []byte, from *net.UDPAddr) {
	x.mu.Lock()
	ice := x.ice
	x.mu.Unlock()
	if ice == nil {
		return
	}
	resp, res, nominate := ice.handle(data, from)
	switch res {
	case iceIgnored:
		return
	case iceBadCreds:
		noteICE("bad_credentials")
		return
	case iceConflict:
		noteICE("role_conflict")
	case iceChecked:
		noteICE("ok")
	}
	if resp != nil {
		x.send(resp, from)
	}
	if res != iceChecked {
		return
	}
	addr := &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port, Zone: from.Zone}
	x.mu.Lock()
	first := !x.verified
	x.verified = true
	if first || nominate || x.peer == nil {
		x.peer = addr
	}
	x.readyLocked()
	cb := x.onICE
	x.mu.Unlock()
	if cb != nil {
		cb(addr, nominate || first)
	}
}

// dtlsIn hands one DTLS record to the handshake.
func (s *Secure) dtlsIn(x *xport, data []byte, from *net.UDPAddr) {
	x.mu.Lock()
	ds := x.ds
	var pc *dtlsPacketConn
	if ds != nil {
		pc = ds.pc
		if x.ice == nil {
			// Without ICE the datagram's source is the only evidence of
			// where the peer is (a NAT's mapping); the host has vetted it.
			x.peer = &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port, Zone: from.Zone}
		}
	}
	x.mu.Unlock()
	if pc != nil {
		pc.push(data)
	}
}

// resetTransport drops the endpoint's DTLS session and ICE state (the lock of
// Secure is held).
func (s *Secure) resetTransport() {
	x := s.xp.Load()
	if x == nil {
		return
	}
	x.mu.Lock()
	ds := x.ds
	x.ds, x.ice, x.verified, x.mux = nil, nil, false, false
	x.mu.Unlock()
	if ds != nil {
		ds.close()
	}
}

// Close stops the endpoint's DTLS handshake and connection, for when its call
// or session ends. The keys stay readable (the call's record still asks what
// was negotiated); nothing more is sent or accepted once the socket is closed.
func (s *Secure) Close() {
	x := s.xp.Load()
	if x == nil {
		return
	}
	x.mu.Lock()
	ds := x.ds
	x.mu.Unlock()
	if ds != nil {
		ds.close()
	}
}

// peerFrom takes the far end's media address from an SDP as the peer address
// of the endpoint, unless an ICE check has already named the real one.
func (s *Secure) peerFrom(a AudioSDP) {
	x := s.xp.Load()
	if x == nil || a.Address == "" || a.Port <= 0 {
		return
	}
	ip := net.ParseIP(a.Address)
	if ip == nil || ip.IsUnspecified() {
		return
	}
	x.mu.Lock()
	if !x.verified {
		x.peer = &net.UDPAddr{IP: ip, Port: a.Port}
	}
	x.readyLocked()
	x.mu.Unlock()
}

// SetMux records that RTCP shares the RTP port on this endpoint (a=rtcp-mux
// was negotiated).
func (s *Secure) SetMux(on bool) {
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		x.mux = on
		x.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// ICE negotiation

// iceLocal returns Hello's ICE-lite credentials for the endpoint, starting
// the agent on first use. ok is false when ICE is off or the endpoint has no
// socket.
func (s *Secure) iceLocal() (ufrag, pwd string, ok bool) {
	x := s.xp.Load()
	if x == nil || !ICEEnabled() {
		return "", "", false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ice == nil {
		x.ice = newICELite()
	}
	u, p := x.ice.local()
	return u, p, true
}

// iceRemote applies the far end's ICE credentials; a changed ufrag is an ICE
// restart and the address has to be verified again.
func (s *Secure) iceRemote(info *ICEInfo) {
	x := s.xp.Load()
	if x == nil || !info.usable() {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ice == nil {
		return
	}
	if x.ice.setRemote(info.Ufrag) {
		x.verified = false
	}
}

// ICEActive reports whether the endpoint runs ICE-lite.
func (s *Secure) ICEActive() bool {
	x := s.xp.Load()
	if x == nil {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.ice != nil
}

// ---------------------------------------------------------------------------
// DTLS negotiation

// answerSetup is the role Hello takes when it answers an offer with setup
// offer (RFC 5763 section 5): active (the DTLS client) to actpass or
// passive, passive to active. A missing setup is read as active (RFC 4145).
func answerSetup(offer string) (mine string, client bool) {
	switch offer {
	case SetupActive, "":
		return SetupPassive, false
	}
	return SetupActive, true
}

func (s *Secure) dtlsReady() error {
	if s.xp.Load() == nil {
		return ErrNoTransport
	}
	if !DTLSEnabled() {
		return ErrDTLSOff
	}
	return nil
}

// acceptDTLS is the answerer side: the offer's fingerprint and setup decide
// the role, the handshake starts, and the returned attributes go in the
// answer. A re-INVITE that repeats the fingerprint changes nothing; a new
// fingerprint starts a new handshake. The lock of Secure is held.
func (s *Secure) acceptDTLS(offer *DTLSInfo) (DTLSInfo, error) {
	if err := s.dtlsReady(); err != nil {
		return DTLSInfo{}, err
	}
	x := s.xp.Load()
	x.mu.Lock()
	if ds := x.ds; ds != nil && ds.started && sameFingerprints(ds.remote, offer.Fingerprints) {
		out := DTLSInfo{Fingerprints: []Fingerprint{ds.local}, Setup: ds.setup}
		x.mu.Unlock()
		return out, nil
	}
	x.mu.Unlock()
	cert, err := NodeCertificate()
	if err != nil {
		return DTLSInfo{}, err
	}
	setup, client := answerSetup(offer.Setup)
	ds := newDTLSSession(cert)
	ds.setup = setup
	ds.remote = offer.Fingerprints[0]
	s.replaceSession(x, ds, client, offer.Fingerprints)
	return DTLSInfo{Fingerprints: []Fingerprint{ds.local}, Setup: setup}, nil
}

// offerDTLS is the offerer side: Hello offers actpass with its fingerprint and
// the role is settled by the answer (learnDTLS). The lock of Secure is held.
func (s *Secure) offerDTLS() (DTLSInfo, error) {
	if err := s.dtlsReady(); err != nil {
		return DTLSInfo{}, err
	}
	x := s.xp.Load()
	x.mu.Lock()
	if ds := x.ds; ds != nil {
		out := DTLSInfo{Fingerprints: []Fingerprint{ds.local}, Setup: ds.setup}
		x.mu.Unlock()
		return out, nil
	}
	x.mu.Unlock()
	cert, err := NodeCertificate()
	if err != nil {
		return DTLSInfo{}, err
	}
	ds := newDTLSSession(cert)
	ds.setup = SetupActPass
	x.mu.Lock()
	x.ds = ds
	x.mu.Unlock()
	return DTLSInfo{Fingerprints: []Fingerprint{ds.local}, Setup: SetupActPass}, nil
}

// learnDTLS applies the far end's DTLS attributes to an endpoint Hello
// offered DTLS to (the answer) or that already runs it (a re-INVITE): the
// answer's setup settles the role and starts the handshake. It reports
// whether the endpoint now runs DTLS-SRTP with that far end. The lock of
// Secure is held.
func (s *Secure) learnDTLS(info *DTLSInfo) bool {
	x := s.xp.Load()
	if x == nil || info == nil || len(info.Fingerprints) == 0 {
		return false
	}
	x.mu.Lock()
	ds := x.ds
	x.mu.Unlock()
	if ds == nil {
		return false
	}
	if ds.started {
		if sameFingerprints(ds.remote, info.Fingerprints) {
			return true
		}
		// The far end changed its certificate (a restart, a takeover):
		// handshake again, in the role the connection already had.
		fresh := newDTLSSession(ds.cert)
		fresh.setup = ds.setup
		fresh.remote = info.Fingerprints[0]
		s.replaceSession(x, fresh, ds.setup == SetupActive, info.Fingerprints)
		return true
	}
	// The answer: the far end's setup says which side is the client.
	var client bool
	var mine string
	switch info.Setup {
	case SetupActive:
		mine, client = SetupPassive, false
	case SetupPassive:
		mine, client = SetupActive, true
	default:
		return false // actpass or missing is not an answer
	}
	x.mu.Lock()
	ds.setup = mine
	x.mu.Unlock()
	x.mu.Lock()
	ds.remote = info.Fingerprints[0]
	x.mu.Unlock()
	s.replaceSession(x, ds, client, info.Fingerprints)
	return true
}

// replaceSession installs ds as the endpoint's DTLS session and starts its
// handshake; any keys of an earlier session are dropped and the endpoint
// carries no media until the new handshake ends.
func (s *Secure) replaceSession(x *xport, ds *dtlsSession, client bool, want []Fingerprint) {
	s.active.Store(nil)
	s.sec.keys, s.sec.offered = nil, nil
	s.pending.Store(true)
	x.mu.Lock()
	old := x.ds
	x.ds, x.client = ds, client
	ds.started = true
	ds.wants = want
	var remote net.Addr = &net.UDPAddr{IP: net.IPv4zero}
	if x.peer != nil {
		remote = x.peer
	}
	ds.pc = newDTLSPacketConn(x.local, remote, x.sendDTLS)
	x.readyLocked()
	x.mu.Unlock()
	if old != nil && old != ds {
		old.close()
	}
	go s.runDTLS(x, ds, client)
}

func sameFingerprints(have Fingerprint, want []Fingerprint) bool {
	for _, w := range want {
		if w.Hash == have.Hash && string(w.Sum) == string(have.Sum) {
			return true
		}
	}
	return false
}

// runDTLS is one session's handshake goroutine. A client waits until it can
// reach the peer (and, with ICE, until the peer's address is authenticated);
// a server answers the peer's ClientHello. When it is done the keys go into
// the endpoint and the goroutine keeps the connection read, which DTLS needs
// to answer retransmissions and the close alert.
func (s *Secure) runDTLS(x *xport, ds *dtlsSession, client bool) {
	dtlsActive.Add(1)
	defer dtlsActive.Add(-1)
	if client {
		select {
		case <-ds.ready:
		case <-ds.ctx.Done():
			return
		}
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ds.ctx, dtlsHandshakeTimeout)
	defer cancel()
	conn, err := newDTLSConn(ds.pc, ds.pc.remote, client, ds.cert, ds.wants)
	if err == nil {
		err = conn.HandshakeContext(ctx)
	}
	if err != nil {
		if ds.ctx.Err() != nil {
			return // replaced or closed: not a failure
		}
		reason := "failed"
		switch {
		case errors.Is(err, errFingerprintMismatch):
			reason = "fingerprint"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		}
		noteDTLS(reason, time.Since(start))
		s.dtlsFailed(x, ds, reason)
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	st, ok := conn.ConnectionState()
	prof, ok2 := conn.SelectedSRTPProtectionProfile()
	if !ok || !ok2 {
		noteDTLS("failed", time.Since(start))
		s.dtlsFailed(x, ds, "failed")
		_ = conn.Close()
		return
	}
	local, remote, err := keysFromState(&st, prof, client)
	if err != nil || !s.installDTLS(x, ds, local, remote) {
		if ds.ctx.Err() == nil {
			noteDTLS("failed", time.Since(start))
			s.dtlsFailed(x, ds, "failed")
		}
		_ = conn.Close()
		return
	}
	noteDTLS("ok", time.Since(start))
	x.mu.Lock()
	ready := x.onReady
	x.mu.Unlock()
	if ready != nil {
		ready()
	}
	go func() {
		<-ds.ctx.Done()
		_ = conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

// installDTLS puts the handshake's keys into the endpoint, unless the session
// was replaced meanwhile.
func (s *Secure) installDTLS(x *xport, ds *dtlsSession, local, remote Crypto) bool {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	x.mu.Lock()
	cur := x.ds
	x.mu.Unlock()
	if cur != ds {
		return false
	}
	keys, err := newLegKeys(local, remote, nil)
	if err != nil {
		return false
	}
	keys.savp, keys.dtls = true, true
	s.sec.keys = keys
	s.sec.offered = nil
	s.active.Store(keys)
	s.pending.Store(false)
	return true
}

// dtlsFailed leaves the endpoint carrying nothing and tells the host.
func (s *Secure) dtlsFailed(x *xport, ds *dtlsSession, reason string) {
	x.mu.Lock()
	cur := x.ds
	fn := x.onFail
	x.mu.Unlock()
	if cur != ds {
		return
	}
	if fn != nil {
		fn(reason)
	}
}

// Mode is the endpoint's replicated SDP mode: "" (plain), "avp" (RTP/AVP with
// crypto lines), "savp" (RTP/SAVP with crypto lines) or "dtls" (DTLS-SRTP).
func (s *Secure) Mode() string {
	if k := s.active.Load(); k != nil {
		switch {
		case k.dtls:
			return "dtls"
		case k.savp:
			return "savp"
		}
		return "avp"
	}
	if s.pending.Load() {
		return "dtls"
	}
	return ""
}

// KeyExchange reports how the endpoint's keys were agreed once it is
// encrypted: "sdes" or "dtls"; "" while plain or before a handshake ends.
func (s *Secure) KeyExchange() string {
	if k := s.active.Load(); k != nil {
		if k.dtls {
			return "dtls"
		}
		return "sdes"
	}
	return ""
}

// DTLSNegotiated reports whether the endpoint runs DTLS-SRTP, handshake done
// or not.
func (s *Secure) DTLSNegotiated() bool {
	if k := s.active.Load(); k != nil {
		return k.dtls
	}
	return s.pending.Load()
}

// dtlsLocal returns the attributes of the endpoint's running DTLS session for
// a later SDP of the dialog (the same fingerprint and role).
func (s *Secure) dtlsLocal() (info DTLSInfo, proto string, ok bool) {
	x := s.xp.Load()
	if x == nil {
		return DTLSInfo{}, "", false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ds == nil {
		return DTLSInfo{}, "", false
	}
	return DTLSInfo{Fingerprints: []Fingerprint{x.ds.local}, Setup: x.ds.setup}, x.ds.proto, true
}

// WaitReady blocks while the endpoint has negotiated DTLS-SRTP and its
// handshake has not finished, so a sender that must not lose its first frames
// (a greeting, an announcement) starts when the keys are in. It returns at
// once for an endpoint that is plain or already keyed, and an error when ctx
// ends or the handshake does not finish in time.
func (s *Secure) WaitReady(ctx context.Context) error {
	if !s.pending.Load() {
		return nil
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	limit := time.NewTimer(dtlsHandshakeTimeout + time.Second)
	defer limit.Stop()
	for s.pending.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-limit.C:
			return errors.New("media: the DTLS-SRTP handshake did not finish")
		case <-tick.C:
		}
	}
	return nil
}
