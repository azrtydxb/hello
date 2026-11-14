// The WebRTC UDP mux (clients-webrtc-mobile-video S-4): every webrtc leg of a
// node shares one UDP port, so the firewall rules and the TURN
// allowed-peer-ip list name one port per node. The mux tells the legs apart
// the way ICE does. A STUN binding request carries USERNAME "<our ufrag>:<their
// ufrag>", so its first check finds the leg; the source address it came from
// is then bound to that leg, and the DTLS records and SRTP packets that follow
// from that address go to it too (RFC 7983 gives the classification; the
// mux does not look inside, the leg's own ICE-lite agent authenticates).
//
// A leg sees its endpoint as a LegConn, the same surface as the UDP socket a
// plain leg owns, so the relay does not know the difference. Closing an
// endpoint frees its ufrag and addresses; the shared socket stays open.
package media

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/stun/v3"
)

// LegConn is the datagram transport of a relay leg: what a *net.UDPConn
// offers it, and what a MuxEndpoint offers on a shared port.
type LegConn interface {
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
	WriteToUDP(b []byte, to *net.UDPAddr) (int, error)
	LocalAddr() net.Addr
	Close() error
}

var _ LegConn = (*net.UDPConn)(nil)

const (
	// muxQueue is how many datagrams an endpoint buffers; more are dropped
	// (RTP tolerates loss, DTLS retransmits).
	muxQueue = 256
	// muxMaxAddrs bounds the source addresses bound to one endpoint: a
	// browser has a few candidate pairs, an attacker who spams checks with a
	// known ufrag must not grow the table without limit.
	muxMaxAddrs = 8
	// muxMaxEndpoints bounds the endpoints of one mux.
	muxMaxEndpoints = 4096
)

// ErrMuxUfragInUse is returned when a ufrag is already routed to an endpoint.
var ErrMuxUfragInUse = errors.New("media: ICE ufrag already in use on the WebRTC port")

// ErrMuxFull is returned when the mux holds its maximum of endpoints.
var ErrMuxFull = errors.New("media: the WebRTC port holds too many legs")

// MuxStats are the mux's counters.
type MuxStats struct {
	Endpoints   int
	Routed      uint64 // datagrams delivered to an endpoint
	Unrouted    uint64 // datagrams from an address no endpoint owns, or a check for an unknown ufrag
	QueueDrops  uint64 // datagrams dropped because an endpoint's queue was full
	STUNChecks  uint64 // STUN datagrams that named a known ufrag
	AddrRebinds uint64 // an address that moved from one endpoint to another
}

// Mux owns the shared UDP socket and routes its datagrams to endpoints.
type Mux struct {
	conn *net.UDPConn

	mu      sync.RWMutex
	byUfrag map[string]*MuxEndpoint
	byAddr  map[string]*MuxEndpoint
	all     map[*MuxEndpoint]struct{}
	n       int
	closed  bool

	routed, unrouted, drops, checks, rebinds atomic.Uint64
	done                                     chan struct{}
}

// ListenMux binds the shared UDP port (0 picks a free one, for tests) and
// starts routing.
func ListenMux(port int) (*Mux, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, fmt.Errorf("media: bind WebRTC port %d: %w", port, err)
	}
	markRTP(c)
	return NewMux(c), nil
}

// NewMux routes the datagrams read from conn, which the mux then owns.
func NewMux(conn *net.UDPConn) *Mux {
	m := &Mux{
		conn: conn, byUfrag: map[string]*MuxEndpoint{}, byAddr: map[string]*MuxEndpoint{},
		all:  map[*MuxEndpoint]struct{}{},
		done: make(chan struct{}),
	}
	go m.readLoop()
	return m
}

// Port is the shared UDP port.
func (m *Mux) Port() int { return m.conn.LocalAddr().(*net.UDPAddr).Port }

// Stats snapshots the counters.
func (m *Mux) Stats() MuxStats {
	m.mu.RLock()
	n := m.n
	m.mu.RUnlock()
	return MuxStats{
		Endpoints: n, Routed: m.routed.Load(), Unrouted: m.unrouted.Load(),
		QueueDrops: m.drops.Load(), STUNChecks: m.checks.Load(), AddrRebinds: m.rebinds.Load(),
	}
}

// Close closes the shared socket and every endpoint.
func (m *Mux) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	eps := make([]*MuxEndpoint, 0, m.n)
	for e := range m.all {
		eps = append(eps, e)
	}
	m.mu.Unlock()
	err := m.conn.Close()
	<-m.done
	for _, e := range eps {
		e.shut()
	}
	return err
}

type muxDatagram struct {
	data []byte
	from *net.UDPAddr
}

func (m *Mux) readLoop() {
	defer close(m.done)
	buf := make([]byte, rtpMaxPacket)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		m.route(buf[:n], from)
	}
}

// route sends one datagram to its endpoint, or drops it.
func (m *Mux) route(b []byte, from *net.UDPAddr) {
	var ep *MuxEndpoint
	if muxIsSTUN(b) {
		ep = m.checkTarget(b, from)
	} else {
		m.mu.RLock()
		ep = m.byAddr[from.String()]
		m.mu.RUnlock()
	}
	if ep == nil {
		m.unrouted.Add(1)
		return
	}
	d := muxDatagram{data: append([]byte(nil), b...), from: from}
	select {
	case ep.in <- d:
		m.routed.Add(1)
	case <-ep.closed:
	default:
		m.drops.Add(1)
	}
}

// muxIsSTUN reports a STUN datagram (RFC 7983: first byte 0 to 3, and the magic
// cookie).
func muxIsSTUN(b []byte) bool {
	return len(b) >= 20 && b[0] < 4 && b[4] == 0x21 && b[5] == 0x12 && b[6] == 0xa4 && b[7] == 0x42
}

// checkTarget finds the endpoint a STUN binding request is for and binds the
// source address to it. Only the ufrag is read: whether the check
// authenticates is the leg's ICE-lite agent's business, and a forged check
// buys the sender nothing but a queue slot on a leg that will drop it.
func (m *Mux) checkTarget(b []byte, from *net.UDPAddr) *MuxEndpoint {
	msg := &stun.Message{Raw: b}
	if err := msg.Decode(); err != nil || msg.Type != stun.BindingRequest {
		return nil
	}
	var u stun.Username
	if err := u.GetFrom(msg); err != nil {
		return nil
	}
	local, _, ok := strings.Cut(string(u), ":")
	if !ok || local == "" {
		return nil
	}
	m.mu.Lock()
	ep := m.byUfrag[local]
	if ep == nil || m.closed {
		m.mu.Unlock()
		return nil
	}
	m.bindLocked(ep, from)
	m.mu.Unlock()
	m.checks.Add(1)
	return ep
}

// bindLocked makes from an address of ep, evicting ep's oldest address when it
// has too many and taking the address from another endpoint that held it.
func (m *Mux) bindLocked(ep *MuxEndpoint, from *net.UDPAddr) {
	key := from.String()
	if old := m.byAddr[key]; old == ep {
		return
	} else if old != nil {
		old.dropAddr(key)
		m.rebinds.Add(1)
	}
	m.byAddr[key] = ep
	ep.addrs = append(ep.addrs, key)
	if len(ep.addrs) > muxMaxAddrs {
		oldest := ep.addrs[0]
		ep.addrs = ep.addrs[1:]
		delete(m.byAddr, oldest)
	}
}

// NewEndpoint makes the transport of one leg. Until SetUfrag names the ICE
// ufrag the leg answers with, nothing is routed to it.
func (m *Mux) NewEndpoint() (*MuxEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, net.ErrClosed
	}
	if m.n >= muxMaxEndpoints {
		return nil, ErrMuxFull
	}
	m.n++
	e := &MuxEndpoint{m: m, in: make(chan muxDatagram, muxQueue), closed: make(chan struct{})}
	m.all[e] = struct{}{}
	return e, nil
}

// MuxEndpoint is a leg's view of the shared port; it is a LegConn.
type MuxEndpoint struct {
	m      *Mux
	in     chan muxDatagram
	closed chan struct{}
	once   sync.Once

	// guarded by m.mu
	ufrag string
	addrs []string
}

var _ LegConn = (*MuxEndpoint)(nil)

// SetUfrag routes STUN checks naming ufrag (the leg's local ICE ufrag) to the
// endpoint, replacing the previous ufrag (an ICE restart). A ufrag another
// endpoint holds is refused.
func (e *MuxEndpoint) SetUfrag(ufrag string) error {
	if ufrag == "" {
		return errors.New("media: empty ICE ufrag")
	}
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	select {
	case <-e.closed:
		return net.ErrClosed
	default:
	}
	if other := e.m.byUfrag[ufrag]; other != nil && other != e {
		return ErrMuxUfragInUse
	}
	if e.ufrag != "" {
		delete(e.m.byUfrag, e.ufrag)
	}
	e.ufrag = ufrag
	e.m.byUfrag[ufrag] = e
	return nil
}

func (e *MuxEndpoint) dropAddr(key string) {
	for i, a := range e.addrs {
		if a == key {
			e.addrs = append(e.addrs[:i], e.addrs[i+1:]...)
			return
		}
	}
}

// ReadFromUDP returns the next datagram routed to the endpoint.
func (e *MuxEndpoint) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	select {
	case <-e.closed:
		return 0, nil, net.ErrClosed
	default:
	}
	select {
	case d := <-e.in:
		return copy(b, d.data), d.from, nil
	case <-e.closed:
		return 0, nil, net.ErrClosed
	}
}

// WriteToUDP sends from the shared port.
func (e *MuxEndpoint) WriteToUDP(b []byte, to *net.UDPAddr) (int, error) {
	select {
	case <-e.closed:
		return 0, net.ErrClosed
	default:
	}
	return e.m.conn.WriteToUDP(b, to)
}

// LocalAddr is the shared port's address.
func (e *MuxEndpoint) LocalAddr() net.Addr { return e.m.conn.LocalAddr() }

// Close frees the endpoint's ufrag and addresses and wakes a blocked reader.
func (e *MuxEndpoint) Close() error {
	e.shut()
	return nil
}

func (e *MuxEndpoint) shut() {
	e.once.Do(func() {
		e.m.mu.Lock()
		if e.ufrag != "" && e.m.byUfrag[e.ufrag] == e {
			delete(e.m.byUfrag, e.ufrag)
		}
		for _, a := range e.addrs {
			if e.m.byAddr[a] == e {
				delete(e.m.byAddr, a)
			}
		}
		e.addrs = nil
		e.m.n--
		delete(e.m.all, e)
		e.m.mu.Unlock()
		close(e.closed)
	})
}
