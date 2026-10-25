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
// hears it. Payload types pass through untouched (the caller's negotiated
// codec, G.711 or G.722); telephone events and any other payload type are
// dropped. When the caller speaks G.722, members that only do G.711 are
// served a G.711 stream decoded and downsampled from it (AddFallbackTarget),
// and so is a multicast group, which is always G.711.
type Fanout struct {
	caller *net.UDPConn
	member *net.UDPConn
	codec  Codec
	pt     uint8

	mu       sync.Mutex
	targets  map[string]*net.UDPAddr
	fallback map[string]*net.UDPAddr // G.711 (PCMU) members of a G.722 page
	closed   bool

	out   rewriteState
	fbOut rewriteState

	// g722 and down are the G.722 to 8 kHz G.711 conversion; they run only
	// in the caller receive goroutine.
	g722 *G722Decoder
	down *Down2

	wg sync.WaitGroup

	packets atomic.Uint64
	inbound atomic.Uint64

	// callerAddr is where the caller's RTP came from, once seen: the tone
	// of PlayCaller follows it through NAT.
	callerAddr atomic.Pointer[net.UDPAddr]

	// mc, once set by EnableMulticast, is where the fanned-out stream is
	// also sent.
	mc atomic.Pointer[multicastOut]
}

// multicastOut is a page's multicast destination and its G.711 payload type.
type multicastOut struct {
	addr *net.UDPAddr
	pt   uint8
}

// NewFanout binds the caller-side and member-side sockets from the port
// range and starts the bridge. pt is the caller's negotiated static audio
// payload type (0, 8 or 9); packets of other types are not fanned out.
func NewFanout(minPort, maxPort int, pt uint8) (*Fanout, error) {
	c, ok := StaticCodec(pt)
	if !ok {
		return nil, fmt.Errorf("media: page payload type %d is not G.711 or G.722", pt)
	}
	return NewFanoutCodec(minPort, maxPort, c)
}

// NewFanoutCodec is NewFanout for a negotiated codec: G.711 or G.722.
func NewFanoutCodec(minPort, maxPort int, c Codec) (*Fanout, error) {
	if minPort < 1 || maxPort > 65535 || minPort > maxPort {
		return nil, fmt.Errorf("media: invalid RTP port range %d-%d", minPort, maxPort)
	}
	if !c.IsG711() && c.Kind() != nameG722 {
		return nil, fmt.Errorf("media: page codec %s is not G.711 or G.722", c.Name)
	}
	f := &Fanout{
		codec: c, pt: c.PT,
		targets: map[string]*net.UDPAddr{}, fallback: map[string]*net.UDPAddr{},
	}
	if c.Kind() == nameG722 {
		f.g722, f.down = NewG722Decoder(), NewDown2()
	}
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
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p})
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

// AddFallbackTarget starts sending a G.711 (PCMU) rendition of the caller's
// G.722 audio to addr under id. It is how a member that answered the page
// with PCMU is served; it does nothing when the caller speaks G.711.
func (f *Fanout) AddFallbackTarget(id string, addr *net.UDPAddr) {
	if addr == nil || f.g722 == nil {
		return
	}
	f.mu.Lock()
	if !f.closed {
		f.fallback[id] = addr
	}
	f.mu.Unlock()
}

// RemoveTarget stops sending to the member id.
func (f *Fanout) RemoveTarget(id string) {
	f.mu.Lock()
	delete(f.targets, id)
	delete(f.fallback, id)
	f.mu.Unlock()
}

// Targets is the number of members currently receiving audio.
func (f *Fanout) Targets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.targets) + len(f.fallback)
}

// Codec is the caller's negotiated codec.
func (f *Fanout) Codec() Codec { return f.codec }

// tonePCM brings 8 kHz tone PCM to the codec's sample rate.
func (f *Fanout) tonePCM(pcm []byte) []byte {
	if rate := f.codec.SampleRate(); rate != sampleRateHz {
		if r, err := Resample(pcm, sampleRateHz, rate); err == nil {
			return r
		}
	}
	return pcm
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
		n, from, err := f.caller.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		pkt, ok := ParseRTP(buf[:n])
		if !ok || pkt.PayloadType != f.pt {
			continue
		}
		if cur := f.callerAddr.Load(); cur == nil || !cur.IP.Equal(from.IP) || cur.Port != from.Port {
			f.callerAddr.Store(from)
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
		fallback := make([]*net.UDPAddr, 0, len(f.fallback))
		for _, a := range f.fallback {
			fallback = append(fallback, a)
		}
		f.mu.Unlock()
		for _, a := range targets {
			// A write error is a transient ICMP or the teardown race.
			_, _ = f.member.WriteToUDP(out, a)
		}
		mc := f.mc.Load()
		if f.g722 != nil && (len(fallback) > 0 || mc != nil) {
			// The G.711 rendition is decoded once and shared by the
			// fallback members and the multicast group.
			pcm8 := Int16ToPCM(f.down.Process(f.g722.Decode(pkt.Payload)))
			if len(fallback) > 0 {
				g711 := BuildRTP(0, pkt.Sequence, pkt.Timestamp, pkt.SSRC, pkt.Marker, EncodePCMU(pcm8))
				if o, ok := f.fbOut.rewritePacket(g711); ok {
					for _, a := range fallback {
						_, _ = f.member.WriteToUDP(o, a)
					}
				}
			}
			if mc != nil {
				payload := EncodePCMU(pcm8)
				if mc.pt == 8 {
					payload = EncodePCMA(pcm8)
				}
				// Sequence and timestamp come from the native stream.
				if o, ok := ParseRTP(out); ok {
					_, _ = f.member.WriteToUDP(BuildRTP(mc.pt, o.Sequence, o.Timestamp, o.SSRC, o.Marker, payload), mc.addr)
				}
			}
		} else if mc != nil {
			_, _ = f.member.WriteToUDP(f.multicastPacket(out, mc.pt), mc.addr)
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
	f.fallback = map[string]*net.UDPAddr{}
	f.mu.Unlock()
	_ = f.caller.Close()
	_ = f.member.Close()
	f.wg.Wait()
}
