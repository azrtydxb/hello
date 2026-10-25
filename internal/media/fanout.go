package media

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Fanout is the one-way media bridge of a server page (phonebooks-ptt spec
// P-12, P-16): one socket receives the caller's RTP, and every packet is
// rewritten once into a fresh outbound stream and sent to each member's
// address from a second socket. Nothing flows the other way: whatever a
// member sends back is read and dropped, counted, and the caller never
// hears it. Payload types pass through untouched (G.711 only, the caller's
// negotiated one); telephone events and any other payload type are dropped.
type Fanout struct {
	caller *net.UDPConn
	member *net.UDPConn
	pt     uint8

	mu      sync.Mutex
	targets map[string]*net.UDPAddr
	closed  bool

	out rewriteState

	wg sync.WaitGroup

	packets atomic.Uint64
	inbound atomic.Uint64
}

// NewFanout binds the caller-side and member-side sockets from the port
// range and starts the bridge. pt is the caller's negotiated audio payload
// type (0 or 8); packets of other types are not fanned out.
func NewFanout(minPort, maxPort int, pt uint8) (*Fanout, error) {
	if minPort < 1 || maxPort > 65535 || minPort > maxPort {
		return nil, fmt.Errorf("media: invalid RTP port range %d-%d", minPort, maxPort)
	}
	f := &Fanout{pt: pt, targets: map[string]*net.UDPAddr{}}
	var err error
	if f.caller, err = bindFrom(minPort, maxPort, nil); err != nil {
		return nil, fmt.Errorf("media: bind page caller socket: %w", err)
	}
	if f.member, err = bindFrom(minPort, maxPort, f.caller); err != nil {
		_ = f.caller.Close()
		return nil, fmt.Errorf("media: bind page member socket: %w", err)
	}
	f.wg.Add(2)
	go f.recvCaller()
	go f.recvMember()
	return f, nil
}

// bindFrom binds the first free UDP port of the range, never the port that
// other already holds.
func bindFrom(minPort, maxPort int, other *net.UDPConn) (*net.UDPConn, error) {
	var last error
	skip := 0
	if other != nil {
		skip = other.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // always a UDPAddr
	}
	for p := minPort; p <= maxPort; p++ {
		if p == skip {
			continue
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p})
		if err != nil {
			last = err
			continue
		}
		return c, nil
	}
	if last == nil {
		last = errors.New("no port in range")
	}
	return nil, last
}

// CallerPort is the port the caller sends its audio to.
func (f *Fanout) CallerPort() int { return f.caller.LocalAddr().(*net.UDPAddr).Port } //nolint:forcetypeassert // always a UDPAddr

// MemberPort is the port the members' SDP names as Hello's address.
func (f *Fanout) MemberPort() int { return f.member.LocalAddr().(*net.UDPAddr).Port } //nolint:forcetypeassert // always a UDPAddr

// AddTarget starts sending the caller's audio to addr under id; a second
// call with the same id re-aims it.
func (f *Fanout) AddTarget(id string, addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	f.mu.Lock()
	if !f.closed {
		f.targets[id] = addr
	}
	f.mu.Unlock()
}

// RemoveTarget stops sending to the member id.
func (f *Fanout) RemoveTarget(id string) {
	f.mu.Lock()
	delete(f.targets, id)
	f.mu.Unlock()
}

// Targets is the number of members currently receiving audio.
func (f *Fanout) Targets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.targets)
}

// FanoutStats are the bridge's counters.
type FanoutStats struct {
	// Packets is the caller packets fanned out; Inbound the datagrams the
	// members sent back, all of which were dropped.
	Packets, Inbound uint64
}

// Stats returns the counters so far.
func (f *Fanout) Stats() FanoutStats {
	return FanoutStats{Packets: f.packets.Load(), Inbound: f.inbound.Load()}
}

func (f *Fanout) recvCaller() {
	defer f.wg.Done()
	buf := make([]byte, rtpMaxPacket)
	for {
		n, _, err := f.caller.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		pkt, ok := ParseRTP(buf[:n])
		if !ok || pkt.PayloadType != f.pt {
			continue
		}
		out, ok := f.out.rewritePacket(buf[:n])
		if !ok {
			continue
		}
		f.mu.Lock()
		targets := make([]*net.UDPAddr, 0, len(f.targets))
		for _, a := range f.targets {
			targets = append(targets, a)
		}
		f.mu.Unlock()
		for _, a := range targets {
			// A write error is a transient ICMP or the teardown race.
			_, _ = f.member.WriteToUDP(out, a)
		}
		f.packets.Add(1)
	}
}

// recvMember reads and discards whatever the members send: a page is
// one-way by construction.
func (f *Fanout) recvMember() {
	defer f.wg.Done()
	buf := make([]byte, rtpMaxPacket)
	for {
		if _, _, err := f.member.ReadFromUDP(buf); err != nil {
			return
		}
		f.inbound.Add(1)
	}
}

// Close stops the bridge and closes both sockets; it is safe to call twice.
func (f *Fanout) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.targets = map[string]*net.UDPAddr{}
	f.mu.Unlock()
	_ = f.caller.Close()
	_ = f.member.Close()
	f.wg.Wait()
}
