// The relay (plan contract 3): one anchored session's bidirectional RTP
// bridge. Two legs, each with its own UDP socket from the node's RTP port
// range, its own receive goroutine feeding a bounded queue, and its own
// pump goroutine that rewrites sequence, timestamp and SSRC per direction
// and sends to the leg's latched peer address. Audio flows leg to leg; no
// transcoding: payload types pass through untouched, so whatever the two
// ends negotiated between themselves (G.722, Opus on a dynamic payload type,
// PCMU, PCMA) flows through the relay as it is; the relay only needs to know
// the negotiated codecs (SetCodecs) to count audio packets, pace its own
// announcement streams and let the recording tap decode.
//
// Queues are bounded with drop-oldest, counted (spec S-3). Every relay
// goroutine recovers its own panics: one panicking session falls back to
// direct media (spec failure mode "relay goroutine panic") without taking
// the node down.
package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// relayQueue is one leg's bounded inbound queue, about 1.3s of 20 ms
// frames: a pump that starts a beat late loses nothing, and beyond that
// audio is dropped rather than buffered unboundedly.
const relayQueue = 64

// RelayStats is one anchored session's counters, as returned by
// Relay.Metrics and handed to ObserveFunc callbacks.
type RelayStats struct {
	// Directions maps "srcLeg>dstLeg" to that direction's counters.
	Directions map[string]DirectionStats
}

// DirectionStats is one direction's counters: the packets relayed from one
// leg into the other.
type DirectionStats struct {
	Packets uint64
	Octets  uint64
	// Lost counts sequence gaps (packets that never arrived).
	Lost uint64
	// Dropped counts packets discarded because the destination leg's
	// bounded queue was full (drop-oldest, spec S-3).
	Dropped uint64
	// Rejected counts packets refused because their source address did
	// not match the leg's peer (see relayLeg.sourceOK).
	Rejected uint64
	// JitterMs is the RFC 3550 interarrival jitter estimate in ms.
	JitterMs float64
}

// ObserveFunc receives a stats snapshot once per statsInterval and once at
// Close; it must not block the relay's goroutines.
type ObserveFunc func(RelayStats)

// statsInterval is how often the relay hands a stats snapshot to the
// ObserveFunc callback.
const statsInterval = time.Second

// Relay is one anchored session's media bridge (contract 3). Create it
// with NewRelay, hand out ports with AddLeg, aim each leg with SetTarget
// (the remote SDP's address until inbound packets latch the peer), and
// Close it when the call ends.
type Relay struct {
	minPort, maxPort int

	mu      sync.Mutex
	legs    []*relayLeg
	used    map[int]bool // ports already bound, no two legs share a port
	closed  bool
	observe ObserveFunc
	// onFail counts a failed session (panic recovery); nil is fine.
	onFail func(what string)
	// onDTMF and onPacket are the tap hooks (nil disables).
	onDTMF   func(leg string, digit byte)
	onPacket func(leg string, pkt *RTPPacket)

	started bool

	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
}

// relayLeg is one side of the bridge: its socket, latched peer address,
// outbound rewrite state and bounded queue.
type relayLeg struct {
	name string
	r    *Relay
	conn *net.UDPConn
	port int

	remoteMu sync.Mutex
	remote   *net.UDPAddr // latched peer address

	// Source policy (guarded by remoteMu). sdpIP is the address the remote
	// SDP names, extraIPs are further addresses the signalling vouches for
	// (the INVITE or response source, which is where a NATed phone's media
	// comes from), and pinnedIP is the address of the first accepted
	// packet. See Relay.accept.
	sdpIP    net.IP
	extraIPs []net.IP
	pinnedIP net.IP
	rejected atomic.Uint64

	out rewriteState // rewrite applied to packets sent INTO this leg

	// q carries packets received on the peer leg, bound for this leg.
	q       chan relayMsg
	dropped atomic.Uint64

	// filter dedupes this leg's inbound RFC 2833 telephone events; it runs
	// only in the recv goroutine, so it needs no lock.
	filter  DTMFFilter
	dtmfPT  uint8 // telephone-event payload type, 0 when none offered
	audioPT uint8 // first negotiated audio payload type, 0 when anything goes

	// codecs are the leg's negotiated audio codecs (first = preferred);
	// accept is the set of their payload types, empty when unknown. Both
	// are written at setup and at answer time, before media flows in
	// earnest, and read from the leg's goroutines; codecMu guards them.
	codecMu sync.RWMutex
	codecs  []Codec
	accept  [128]bool
	anyPT   bool

	// stats is the leg's inbound-stream counters (locked).
	stats legStats

	// muted drops what this leg receives instead of forwarding it to the
	// peer (a holder's stream while music plays to the held party).
	muted atomic.Bool
}

// legStats is one leg's inbound counters; the direction label comes from
// the leg pair at Metrics time.
type legStats struct {
	mu          sync.Mutex
	packets     uint64
	octets      uint64
	lost        uint64
	jitter      float64
	haveSeq     bool
	lastSeq     uint16
	lastTS      uint32
	lastArrival time.Time
}

type relayMsg struct {
	data []byte // the full RTP packet, copied before queueing
}

// rewriteState tracks one direction's rewrite: our outbound stream (fresh
// SSRC, sequence and timestamp space) and the inbound stream it mirrors.
type rewriteState struct {
	mu      sync.Mutex
	ssrc    uint32
	seq     uint16
	ts      uint32
	started bool
	// The outbound deltas mirror the inbound stream's deltas, so loss and
	// pacing survive the rewrite while the two legs never share sequence
	// spaces or SSRCs.
	inSSRC uint32
	inSeq  uint16
	inTS   uint32
	haveIn bool
}

// NewRelay validates the range for the two relay sockets (contract 3). It
// fails when the range is not a port range or holds fewer than two ports.
func NewRelay(minPort, maxPort int) (*Relay, error) {
	if minPort < 1 || maxPort > 65535 || minPort > maxPort {
		return nil, fmt.Errorf("media: invalid RTP port range %d-%d", minPort, maxPort)
	}
	if maxPort-minPort+1 < 2 {
		return nil, fmt.Errorf("media: RTP port range %d-%d holds fewer than two ports", minPort, maxPort)
	}
	r := &Relay{minPort: minPort, maxPort: maxPort, used: map[int]bool{}}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r, nil
}

// AddLeg binds the next leg's socket from the range and returns its local
// port for the SDP. A relay takes two legs: the caller's first, the
// callee's second. It fails after Close or when nothing in the range can
// be bound (port exhaustion, spec failure mode "anchor RTP bind
// failure"); the error carries a metric through Relay.onFail.
func (r *Relay) AddLeg(name string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.legs) >= 2 {
		return 0, errors.New("media: relay takes exactly two legs")
	}
	var (
		conn    *net.UDPConn
		port    int
		lastErr error
	)
	for p := r.minPort; p <= r.maxPort; p++ {
		if r.used[p] {
			continue
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p})
		if err != nil {
			lastErr = err
			continue
		}
		conn, port = c, p
		break
	}
	if conn == nil {
		return 0, fmt.Errorf("media: bind leg %q RTP socket: %w", name, lastErr)
	}
	l := &relayLeg{
		name: name, r: r, conn: conn, port: port,
		q: make(chan relayMsg, relayQueue),
	}
	l.remoteMu.Lock()
	l.remote = nil
	l.remoteMu.Unlock()
	r.legs = append(r.legs, l)
	r.used[port] = true
	return port, nil
}

// SetTarget aims leg at addr: the address from the remote SDP until the
// first inbound packet latches the peer's real source (symmetric RTP, so a
// phone behind NAT reaches the leg from a different port than its SDP
// names). Unknown leg names are ignored (a closed relay has no legs).
func (r *Relay) SetTarget(name string, addr *net.UDPAddr) {
	l := r.findLeg(name)
	if l == nil {
		return
	}
	l.remoteMu.Lock()
	l.remote = addr
	// A new target is a new negotiation (first INVITE, answer, re-INVITE,
	// takeover): the source policy starts over from what this SDP names.
	l.pinnedIP = nil
	l.sdpIP = nil
	if addr != nil {
		l.sdpIP = append(net.IP(nil), addr.IP...)
	}
	l.remoteMu.Unlock()
}

// AllowSource vouches for ip as a source of the leg's media: the address
// the signalling for this leg came from (or went to). A phone behind NAT
// names a private address in its SDP, but its media arrives from the same
// public address as its signalling. Calling it again adds another address;
// SetTarget keeps them.
func (r *Relay) AllowSource(name string, ip net.IP) {
	l := r.findLeg(name)
	if l == nil || len(ip) == 0 || ip.IsUnspecified() {
		return
	}
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	for _, have := range l.extraIPs {
		if have.Equal(ip) {
			return
		}
	}
	l.extraIPs = append(l.extraIPs, append(net.IP(nil), ip...))
}

// findLeg returns the named leg.
func (r *Relay) findLeg(name string) *relayLeg {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.legs {
		if l.name == name {
			return l
		}
	}
	return nil
}

// LegTarget is the leg's current target (nil when unset): the address the
// leg's SDP names until inbound packets latch the peer. In-call HA
// replicates it as the leg's LatchedAddr, so a taker starts with the last
// known media addresses before its re-INVITEs answer.
func (r *Relay) LegTarget(name string) *net.UDPAddr {
	l := r.findLeg(name)
	if l == nil {
		return nil
	}
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	if l.remote == nil {
		return nil
	}
	out := *l.remote
	return &out
}

// SetPayloadTypes tells the relay which payload types this leg negotiates,
// so telephone-event packets can be deduped for DTMF hooks and the
// recording tap can note the codec.
func (r *Relay) SetPayloadTypes(name string, audio, dtmf uint8) {
	l := r.findLeg(name)
	if l == nil {
		return
	}
	var codecs []Codec
	if c, ok := StaticCodec(audio); ok {
		codecs = []Codec{c}
	}
	l.setCodecs(codecs, audio, dtmf)
}

// SetCodecs tells the relay the audio codecs a leg negotiated (dynamic
// payload types included) and its telephone-event payload type. The first
// codec is the one the relay speaks itself when it plays an announcement
// into the leg.
func (r *Relay) SetCodecs(name string, codecs []Codec, dtmf uint8) {
	l := r.findLeg(name)
	if l == nil {
		return
	}
	var first uint8
	if len(codecs) > 0 {
		first = codecs[0].PT
	}
	l.setCodecs(codecs, first, dtmf)
}

func (l *relayLeg) setCodecs(codecs []Codec, first, dtmf uint8) {
	l.codecMu.Lock()
	defer l.codecMu.Unlock()
	l.codecs = append([]Codec(nil), codecs...)
	l.accept = [128]bool{}
	for _, c := range codecs {
		l.accept[c.PT&0x7f] = true
	}
	l.anyPT = len(codecs) == 0
	l.audioPT = first
	l.dtmfPT = dtmf
}

// eventType is the leg's telephone-event payload type, 0 when none.
func (l *relayLeg) eventType() uint8 {
	l.codecMu.RLock()
	defer l.codecMu.RUnlock()
	return l.dtmfPT
}

// carries reports whether pt is a negotiated audio payload type of the leg.
func (l *relayLeg) carries(pt uint8) bool {
	l.codecMu.RLock()
	defer l.codecMu.RUnlock()
	return l.anyPT || l.accept[pt&0x7f]
}

// clockRate is the RTP clock rate of the leg's preferred codec, 8000 when
// none is known.
func (l *relayLeg) clockRate() float64 {
	l.codecMu.RLock()
	defer l.codecMu.RUnlock()
	if len(l.codecs) > 0 && l.codecs[0].ClockRate > 0 {
		return float64(l.codecs[0].ClockRate)
	}
	return 8000
}

// speaks returns the codec the relay itself uses toward the leg: the first
// negotiated codec it can encode, PCMU when none is known.
func (l *relayLeg) speaks() Codec {
	l.codecMu.RLock()
	defer l.codecMu.RUnlock()
	for _, c := range l.codecs {
		if c.Supported() {
			return c
		}
	}
	if l.audioPT == 8 {
		c, _ := StaticCodec(8)
		return c
	}
	c, _ := StaticCodec(0)
	return c
}

// Codecs returns the audio codecs the leg negotiated, nil when none were
// set.
func (r *Relay) Codecs(name string) []Codec {
	l := r.findLeg(name)
	if l == nil {
		return nil
	}
	l.codecMu.RLock()
	defer l.codecMu.RUnlock()
	return append([]Codec(nil), l.codecs...)
}

// OnDTMF delivers one deduplicated DTMF digit per press on leg, ending
// # included; it runs on the leg's receive goroutine, so it must not
// block.
func (r *Relay) OnDTMF(h func(leg string, digit byte)) {
	r.mu.Lock()
	r.onDTMF = h
	r.mu.Unlock()
}

// OnPacket taps every parsed inbound RTP packet on leg; it runs on the
// leg's receive goroutine, so it must not block. The packet aliases the
// receive buffer, so the hook must copy what it keeps.
func (r *Relay) OnPacket(h func(leg string, pkt *RTPPacket)) {
	r.mu.Lock()
	r.onPacket = h
	r.mu.Unlock()
}

// Start launches the session goroutines: one receiver and one pump per
// leg, plus the stats ticker. It must run after both legs exist. Panics
// from any goroutine are recovered here (spec failure mode), reported
// through onFail, and the relay closes.
func (r *Relay) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || len(r.legs) < 2 || r.closed {
		return
	}
	r.started = true
	r.wg.Add(len(r.legs))
	for _, l := range r.legs {
		go r.runLeg(l)
	}
	r.wg.Add(1)
	go r.observeLoop()
}

// runLeg guards one leg's receive and pump goroutines: a panic in either
// is recovered, reported through onFail, and closes the whole relay so the
// call falls back to direct media instead of hanging (spec failure mode).
func (r *Relay) runLeg(l *relayLeg) {
	defer r.wg.Done()
	defer func() {
		if rec := recover(); rec != nil {
			r.failWith("relay panic")
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if rec := recover(); rec != nil {
				r.failWith("relay pump panic")
			}
		}()
		r.pump(l)
	}()
	r.recv(l)
	<-done
}

// failWith reports a failed session once and closes the relay.
func (r *Relay) failWith(what string) {
	r.mu.Lock()
	if r.onFail != nil && !r.closed {
		r.onFail(what)
	}
	r.mu.Unlock()
	r.Close()
}

// recv reads RTP from leg l's socket until Close. Each datagram is
// latched, counted, tapped, and forwarded to the peer leg's queue.
func (r *Relay) recv(l *relayLeg) {
	buf := make([]byte, rtpMaxPacket)
	for {
		n, from, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed by Close
		}
		if !l.sourceOK(from) {
			l.rejected.Add(1)
			continue // not the leg's peer: never counted, tapped or relayed
		}
		r.latch(l, from)
		pkt, ok := ParseRTP(buf[:n])
		if !ok {
			continue // a malformed datagram is a dropped packet, never fatal
		}
		dtmfPT := l.eventType()
		if l.carries(pkt.PayloadType) && pkt.PayloadType != dtmfPT {
			r.noteAudio(l, pkt)
		}
		if dtmfPT != 0 && pkt.PayloadType == dtmfPT {
			if ev, ok := ParseTelephoneEvent(pkt.Payload); ok {
				ev.Sequence = pkt.Sequence
				if code, deliver := l.filter.Observe(ev, pkt.Marker); deliver && r.onDTMF != nil {
					r.mu.Lock()
					h := r.onDTMF
					r.mu.Unlock()
					if h != nil {
						h(l.name, code)
					}
				}
			}
		}
		if r.onPacket != nil {
			r.mu.Lock()
			h := r.onPacket
			r.mu.Unlock()
			if h != nil {
				h(l.name, pkt)
			}
		}
		if l.muted.Load() {
			continue
		}
		r.forward(l, buf[:n])
	}
}

// sourceOK is the leg's source policy, the guard against RTP injection and
// hijack: anyone who can reach a relay port could otherwise send audio into
// the call or, by sending first, redirect the other side's audio to
// themselves. A packet is accepted from:
//
//   - the address the leg's remote SDP names, or one the signalling vouches
//     for (AllowSource), until the leg is pinned;
//   - any address, for the first packet, when the SDP names none usable
//     and nothing vouches for the leg: no address (hold, 0.0.0.0) or a
//     private one (a phone behind NAT, whose public address Hello was not
//     told). That packet pins the leg;
//   - after the pin, the pinned address (any port, so a changed NAT
//     mapping still works) or a vouched one.
//
// Everything else is refused. A moved media address is negotiated: the
// re-INVITE's SetTarget starts the policy over.
func (l *relayLeg) sourceOK(from *net.UDPAddr) bool {
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	ip := from.IP
	vouched := false
	for _, v := range l.extraIPs {
		if v.Equal(ip) {
			vouched = true
			break
		}
	}
	if l.pinnedIP != nil {
		if ip.Equal(l.pinnedIP) {
			return true
		}
		if vouched {
			l.pinnedIP = append(net.IP(nil), ip...)
			return true
		}
		return false
	}
	switch {
	case vouched, len(l.sdpIP) > 0 && ip.Equal(l.sdpIP):
	case len(l.extraIPs) == 0 && !publicAddr(l.sdpIP):
	default:
		return false
	}
	l.pinnedIP = append(net.IP(nil), ip...)
	return true
}

// publicAddr reports whether ip is a globally routable unicast address; nil,
// unspecified, loopback, private, link-local and CGNAT (100.64/10) are not.
func publicAddr(ip net.IP) bool {
	if len(ip) == 0 || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 {
		return false
	}
	return true
}

// latch implements symmetric RTP latching with refresh: the first inbound
// datagram pins where replies go, and a later change of source refreshes
// the pin (spec S-2, S-6), so a phone that re-registers through a new NAT
// mapping keeps its audio.
func (r *Relay) latch(l *relayLeg, from *net.UDPAddr) {
	l.remoteMu.Lock()
	defer l.remoteMu.Unlock()
	if l.remote != nil && l.remote.String() == from.String() {
		return
	}
	l.remote = &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port, Zone: from.Zone}
}

// noteAudio counts one audio packet and its loss and jitter for the
// direction into leg l (spec S-3).
func (r *Relay) noteAudio(l *relayLeg, pkt *RTPPacket) {
	l.stats.mu.Lock()
	defer l.stats.mu.Unlock()
	st := &l.stats
	if st.haveSeq {
		gap := int(pkt.Sequence) - int(st.lastSeq)
		if gap > 1 {
			st.lost += uint64(gap - 1)
		}
		arrivalDelta := time.Since(st.lastArrival)
		tsDelta := float64(int32(pkt.Timestamp-st.lastTS)) / l.clockRate() //nolint:gosec // RTP wraparound
		d := arrivalDelta.Seconds() - tsDelta
		if d < 0 {
			d = -d
		}
		st.jitter += (d - st.jitter) / 16
	}
	st.haveSeq = true
	st.lastSeq = pkt.Sequence
	st.lastTS = pkt.Timestamp
	st.lastArrival = time.Now()
	st.packets++
	st.octets += uint64(len(pkt.Payload))
}

// forward queues leg l's inbound packet for the peer leg, drop-oldest on a
// full queue. Octets are counted here because the payload survives the
// rewrite byte for byte.
func (r *Relay) forward(l *relayLeg, data []byte) {
	peer := r.peerOf(l)
	if peer == nil {
		return
	}
	msg := relayMsg{data: append([]byte(nil), data...)}
	select {
	case peer.q <- msg:
	default:
		select {
		case <-peer.q:
			peer.dropped.Add(1)
		default:
		}
		select {
		case peer.q <- msg:
		default:
			peer.dropped.Add(1)
		}
	}
}

// peerOf returns the other leg.
func (r *Relay) peerOf(l *relayLeg) *relayLeg {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.legs) < 2 {
		return nil
	}
	if r.legs[0] == l {
		return r.legs[1]
	}
	return r.legs[0]
}

// pump drains the leg's queue: it rewrites each packet into this leg's
// outbound stream and sends it to the latched peer address.
func (r *Relay) pump(l *relayLeg) {
	for {
		select {
		case <-r.ctx.Done():
			return
		default:
		}
		select {
		case m := <-l.q:
			if out, ok := r.rewrite(l, m.data); ok {
				l.send(out)
			}
		case <-r.ctx.Done():
			return
		}
	}
}

// send writes one datagram to the leg's latched address.
func (l *relayLeg) send(data []byte) {
	l.remoteMu.Lock()
	dst := l.remote
	l.remoteMu.Unlock()
	if dst == nil {
		return // nothing latched and no SDP target yet: drop silently
	}
	// A write error is a transient ICMP or the teardown race; the next
	// packet retries and Close stops the loop either way.
	_, _ = l.conn.WriteToUDP(data, dst)
}

// rewrite maps a packet from the peer leg into leg l's outbound stream:
// sequence, timestamp and SSRC are rewritten (contract 3), the payload is
// untouched (no transcoding).
func (r *Relay) rewrite(l *relayLeg, data []byte) ([]byte, bool) {
	return l.out.rewritePacket(data)
}

// rewritePacket maps one inbound RTP packet into this outbound stream.
func (o *rewriteState) rewritePacket(data []byte) ([]byte, bool) {
	pkt, ok := ParseRTP(data)
	if !ok {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.started {
		if err := o.reset(); err != nil {
			return nil, false
		}
	}
	if o.haveIn && pkt.SSRC != o.inSSRC {
		// A new inbound stream (re-INVITE): start a fresh outbound stream.
		_ = o.reset()
	}
	if !o.haveIn {
		o.inSSRC = pkt.SSRC
		o.inSeq = pkt.Sequence
		o.inTS = pkt.Timestamp
		o.haveIn = true
		o.seq++
		o.ts += frameSamples
	} else {
		dseq := int16(pkt.Sequence - o.inSeq) //nolint:gosec // RTP wraparound is the point
		dts := int32(pkt.Timestamp - o.inTS)  //nolint:gosec // RTP wraparound is the point
		o.seq += uint16(dseq)                 //nolint:gosec // wraparound is the point
		o.ts += uint32(dts)                   //nolint:gosec // wraparound is the point
		o.inSeq = pkt.Sequence
		o.inTS = pkt.Timestamp
	}
	out := append([]byte(nil), data...)
	binary.BigEndian.PutUint16(out[2:4], o.seq)
	binary.BigEndian.PutUint32(out[4:8], o.ts)
	binary.BigEndian.PutUint32(out[8:12], o.ssrc)
	return out, true
}

// reset starts a fresh outbound stream with a random SSRC and sequence and
// timestamp offsets, all drawn from the crypto reader in one go.
func (o *rewriteState) reset() error {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("media: generate rewrite state: %w", err)
	}
	o.ssrc = binary.BigEndian.Uint32(b[0:4])
	o.seq = binary.BigEndian.Uint16(b[4:6])
	o.ts = binary.BigEndian.Uint32(b[6:10])
	o.started = true
	o.haveIn = false
	return nil
}

// Close stops the session goroutines and closes both sockets. It is safe
// to call more than once and waits for the goroutines to finish.
func (r *Relay) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	legs := append([]*relayLeg(nil), r.legs...)
	r.mu.Unlock()
	r.cancel()
	for _, l := range legs {
		_ = l.conn.Close()
	}
	r.wg.Wait()
	r.mu.Lock()
	fn := r.observe
	r.mu.Unlock()
	if fn != nil {
		fn(r.Metrics())
	}
}

// Metrics snapshots the per-direction counters (contract 3).
func (r *Relay) Metrics() RelayStats {
	r.mu.Lock()
	legs := append([]*relayLeg(nil), r.legs...)
	r.mu.Unlock()
	out := RelayStats{Directions: map[string]DirectionStats{}}
	for _, l := range legs {
		peer := r.peerOf(l)
		if peer == nil {
			continue
		}
		l.stats.mu.Lock()
		st := DirectionStats{
			Packets:  l.stats.packets,
			Octets:   l.stats.octets,
			Lost:     l.stats.lost,
			JitterMs: l.stats.jitter * 1000,
			Dropped:  l.dropped.Load(),
			Rejected: l.rejected.Load(),
		}
		l.stats.mu.Unlock()
		// Audio arriving into leg l is l's party speaking: the direction
		// runs from l's side to the peer's side.
		out.Directions[l.name+">"+peer.name] = st
	}
	return out
}

// Observe registers the stats callback, invoked each statsInterval and at
// Close.
func (r *Relay) Observe(fn ObserveFunc) {
	r.mu.Lock()
	r.observe = fn
	r.mu.Unlock()
}

// OnFail registers the session-failure callback (panic recovery, spec
// failure mode); the callback must not block.
func (r *Relay) OnFail(fn func(what string)) {
	r.mu.Lock()
	r.onFail = fn
	r.mu.Unlock()
}

// observeLoop hands Metrics snapshots to the Observe callback once per
// statsInterval until Close.
func (r *Relay) observeLoop() {
	defer r.wg.Done()
	t := time.NewTicker(statsInterval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.mu.Lock()
			fn := r.observe
			r.mu.Unlock()
			if fn != nil {
				fn(r.Metrics())
			}
		}
	}
}

// LegPort returns the local port of a named leg, 0 without one (the sip
// package reads it for the SDP answers).
func (r *Relay) LegPort(name string) int {
	if l := r.findLeg(name); l != nil {
		return l.port
	}
	return 0
}

// PlayTo plays PCM (16-bit mono, 8 kHz) into one leg in real time, as its
// own announcement stream: a fresh SSRC with its own sequence and
// timestamp space, so the bridged stream's rewrite state is untouched.
// The stream is encoded in the leg's negotiated codec (G.722 and Opus legs
// get the 8 kHz audio resampled up). It returns ctx.Err() when ctx is
// cancelled mid-way.
func (r *Relay) PlayTo(ctx context.Context, legName string, pcm []byte) error {
	return r.PlayPCMTo(ctx, legName, pcm, sampleRateHz)
}

// PlayPCMTo is PlayTo for PCM at an explicit sample rate (WAV assets are 8
// or 16 kHz).
func (r *Relay) PlayPCMTo(ctx context.Context, legName string, pcm []byte, rate int) error {
	l := r.findLeg(legName)
	if l == nil {
		return errors.New("media: unknown relay leg " + legName)
	}
	if len(pcm)%2 != 0 {
		pcm = pcm[:len(pcm)-1]
	}
	codec := l.speaks()
	if want := codec.SampleRate(); want != rate {
		out, err := Resample(pcm, rate, want)
		if err != nil {
			return fmt.Errorf("media: announcement resample: %w", err)
		}
		pcm = out
	}
	enc, err := NewEncoder(codec)
	if err != nil {
		return err
	}
	defer enc.Close()
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("media: announcement stream: %w", err)
	}
	seq := binary.BigEndian.Uint16(b[0:2])
	ts := binary.BigEndian.Uint32(b[2:6])
	ssrc := binary.BigEndian.Uint32(b[6:10])
	frame := 2 * codec.FrameSamples()
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	for off := 0; off < len(pcm); off += frame {
		end := min(off+frame, len(pcm))
		chunk := pcm[off:end]
		if len(chunk) < frame {
			chunk = append(append([]byte(nil), chunk...), make([]byte, frame-len(chunk))...)
		}
		pkt := BuildRTP(codec.PT, seq, ts, ssrc, false, enc.Encode(chunk))
		seq++
		ts += codec.TimestampStep()
		l.remoteMu.Lock()
		dst := l.remote
		l.remoteMu.Unlock()
		if dst != nil {
			_, _ = l.conn.WriteToUDP(pkt, dst)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-r.ctx.Done():
			return errors.New("media: relay closed")
		}
	}
	return nil
}
