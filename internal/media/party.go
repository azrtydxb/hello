package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// PartyCodec converts between a party's RTP payload and mixing-rate samples.
// G.711 is the baseline; a wideband codec (G.722, Opus) implements this
// interface with a transcode to and from MixRate and registers itself with
// RegisterPartyCodec, and the mixer needs no other change.
type PartyCodec interface {
	// PayloadType is the static RTP payload type the SDP negotiates.
	PayloadType() uint8
	// Decode turns one RTP payload into samples at MixRate.
	Decode(payload []byte) []int16
	// Encode turns one MixFrame of samples at MixRate into an RTP payload.
	Encode(frame []int16) []byte
	// TimestampStep is the RTP timestamp advance of one 20 ms frame (160 for
	// 8 kHz G.711, 320 for G.722's 8 kHz-clocked 16 kHz audio).
	TimestampStep() uint32
}

type g711Codec struct{ pt uint8 }

func (c g711Codec) PayloadType() uint8 { return c.pt }

func (c g711Codec) Decode(payload []byte) []int16 {
	if c.pt == 8 {
		return PCMToSamples(DecodePCMA(payload))
	}
	return PCMToSamples(DecodePCMU(payload))
}

func (c g711Codec) Encode(frame []int16) []byte {
	if c.pt == 8 {
		return EncodePCMA(SamplesToPCM(frame))
	}
	return EncodePCMU(SamplesToPCM(frame))
}

func (g711Codec) TimestampStep() uint32 { return frameSamples }

var (
	partyCodecMu sync.RWMutex
	partyCodecs  = map[uint8]PartyCodec{0: g711Codec{0}, 8: g711Codec{8}}
)

// RegisterPartyCodec adds (or replaces) the adapter for a payload type.
func RegisterPartyCodec(c PartyCodec) {
	partyCodecMu.Lock()
	defer partyCodecMu.Unlock()
	partyCodecs[c.PayloadType()] = c
}

// PartyCodecFor returns the adapter for a payload type.
func PartyCodecFor(pt uint8) (PartyCodec, bool) {
	partyCodecMu.RLock()
	defer partyCodecMu.RUnlock()
	c, ok := partyCodecs[pt]
	return c, ok
}

// Queue bounds of a party: its jitter buffer holds at most partyMaxBuffer
// frames of audio (beyond it the oldest is dropped, so a stalled mixer or a
// bursty sender cannot add latency without bound) and trims back to
// partyTrimTo when it overflows.
const (
	partyMaxBuffer = 8 * MixFrame
	partyTrimTo    = 3 * MixFrame
	partyDTMFQueue = 16
)

// Party is one conference participant's media endpoint: a UDP socket that
// receives the participant's RTP into a jitter buffer for the room's mixer
// and sends the mixed audio back. It is created from the participant's SDP
// offer, so the answer carries the node's address and the socket's port.
type Party struct {
	conn  *net.UDPConn
	codec PartyCodec
	offer AudioSDP

	remoteMu sync.Mutex
	target   *net.UDPAddr // the SDP's address, used until the first packet
	remote   *net.UDPAddr // the latched source of the participant's RTP

	bufMu sync.Mutex
	buf   []int16 // decoded audio awaiting the mixer

	sendMu sync.Mutex
	seq    uint16
	ts     uint32
	ssrc   uint32
	marker bool

	dtmf   chan byte
	filter DTMFFilter // recv goroutine only

	closeOnce sync.Once
	done      chan struct{}

	// Counters, for the room's metrics.
	rx, tx, dropped atomic.Uint64
	lastRx          atomic.Int64 // unix nanos of the last inbound audio packet
}

// NewParty binds a socket for the participant's media from the port range
// [minPort, maxPort] (0 for both lets the kernel choose, as the tests do),
// negotiates the codec from offer (G.711 PCMU/PCMA, or any codec registered
// with RegisterPartyCodec) and returns the answer SDP advertising host.
func NewParty(host string, minPort, maxPort int, offer []byte) ([]byte, *Party, error) {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil, nil, fmt.Errorf("media: advertised host %q is not an IPv4 address", host)
	}
	off, err := ParseAudioSDP(offer)
	if err != nil {
		return nil, nil, err
	}
	codec, ok := PartyCodecFor(off.PayloadType)
	if !ok {
		return nil, nil, fmt.Errorf("media: no conference codec for payload type %d", off.PayloadType)
	}
	target := &net.UDPAddr{IP: net.ParseIP(off.Address), Port: off.Port}
	if target.IP == nil {
		return nil, nil, fmt.Errorf("media: offer address %q is not an IP", off.Address)
	}
	conn, err := bindPartySocket(minPort, maxPort)
	if err != nil {
		return nil, nil, err
	}
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = conn.Close() // nothing received yet; the close error is irrelevant
		return nil, nil, fmt.Errorf("media: party RTP identifiers: %w", err)
	}
	p := &Party{
		conn: conn, codec: codec, offer: off, target: target,
		seq: binary.BigEndian.Uint16(b[0:2]), ts: binary.BigEndian.Uint32(b[2:6]), ssrc: binary.BigEndian.Uint32(b[6:10]),
		marker: true,
		dtmf:   make(chan byte, partyDTMFQueue), done: make(chan struct{}),
	}
	go p.recv()
	port := conn.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // always a UDPAddr
	return BuildAudioSDP(host, port, off.PayloadType, off.DTMFPayloadType, off.DTMFRate), p, nil
}

// bindPartySocket takes the first free port of the range; with no range it
// uses the random-port binder the anchor uses.
func bindPartySocket(minPort, maxPort int) (*net.UDPConn, error) {
	if minPort <= 0 || maxPort < minPort {
		c, err := bindRTP()
		if err != nil {
			return nil, fmt.Errorf("media: bind conference RTP socket: %w", err)
		}
		return c, nil
	}
	c, err := bindFrom(minPort, maxPort, nil)
	if err != nil {
		return nil, fmt.Errorf("media: bind conference RTP socket: %w", err)
	}
	return c, nil
}

// Port is the local RTP port.
func (p *Party) Port() int {
	return p.conn.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // always a UDPAddr
}

// Retarget points the party at a new media address (a re-INVITE moved the
// participant) and forgets the latched source, so the next packet latches
// again.
func (p *Party) Retarget(host string, port int) {
	ip := net.ParseIP(host)
	if ip == nil || port <= 0 {
		return
	}
	p.remoteMu.Lock()
	p.target = &net.UDPAddr{IP: ip, Port: port}
	p.remote = nil
	p.remoteMu.Unlock()
}

// Offer is the negotiated audio description (the participant's side).
func (p *Party) Offer() AudioSDP { return p.offer }

// DTMF delivers the participant's key presses (RFC 2833). The channel is
// never closed; a full queue drops digits rather than stall RTP reception.
func (p *Party) DTMF() <-chan byte { return p.dtmf }

// PushDTMF injects a digit from another source (a SIP INFO dtmf-relay).
func (p *Party) PushDTMF(d byte) {
	select {
	case p.dtmf <- d:
	default:
	}
}

// Done is closed once the party's socket is closed.
func (p *Party) Done() <-chan struct{} { return p.done }

// Close stops the receive loop and closes the socket; safe to repeat.
func (p *Party) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.conn.Close() // teardown: nothing depends on the close error
	})
}

// Stats returns the packets received, sent and the samples dropped from the
// jitter buffer.
func (p *Party) Stats() (rx, tx, dropped uint64) { return p.rx.Load(), p.tx.Load(), p.dropped.Load() }

// LastAudio is when the participant's last audio packet arrived (zero if
// none has).
func (p *Party) LastAudio() time.Time {
	n := p.lastRx.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func (p *Party) recv() {
	buf := make([]byte, rtpMaxPacket)
	for {
		n, from, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed
		}
		pkt, ok := ParseRTP(buf[:n])
		if !ok {
			continue
		}
		switch {
		case pkt.PayloadType == p.codec.PayloadType():
			p.latch(from)
			p.rx.Add(1)
			p.lastRx.Store(time.Now().UnixNano())
			p.push(p.codec.Decode(pkt.Payload))
		case p.offer.DTMFPayloadType != 0 && pkt.PayloadType == p.offer.DTMFPayloadType:
			p.latch(from)
			ev, ok := ParseTelephoneEvent(pkt.Payload)
			if !ok {
				continue
			}
			ev.Sequence = pkt.Sequence
			if code, deliver := p.filter.Observe(ev, pkt.Marker); deliver {
				p.PushDTMF(telephoneEventChar(code))
			}
		}
	}
}

// telephoneEventChar maps an RFC 4733 event code to its key.
func telephoneEventChar(code byte) byte {
	switch {
	case code <= 9:
		return '0' + code
	case code == 10:
		return '*'
	case code == 11:
		return '#'
	case code >= 12 && code <= 15:
		return 'A' + (code - 12)
	}
	return code
}

func (p *Party) latch(from *net.UDPAddr) {
	p.remoteMu.Lock()
	defer p.remoteMu.Unlock()
	if p.remote == nil {
		p.remote = &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port, Zone: from.Zone}
	}
}

func (p *Party) peer() *net.UDPAddr {
	p.remoteMu.Lock()
	defer p.remoteMu.Unlock()
	if p.remote != nil {
		return p.remote
	}
	return p.target
}

func (p *Party) push(samples []int16) {
	p.bufMu.Lock()
	defer p.bufMu.Unlock()
	p.buf = append(p.buf, samples...)
	if len(p.buf) > partyMaxBuffer {
		drop := len(p.buf) - partyTrimTo
		p.dropped.Add(uint64(drop))
		p.buf = append(p.buf[:0], p.buf[drop:]...)
	}
}

// pop returns the next mixing frame of the participant's audio, or nil when
// less than a whole frame has arrived (the mixer treats nil as silence).
func (p *Party) pop() []int16 {
	p.bufMu.Lock()
	defer p.bufMu.Unlock()
	if len(p.buf) < MixFrame {
		return nil
	}
	f := make([]int16, MixFrame)
	copy(f, p.buf)
	p.buf = append(p.buf[:0], p.buf[MixFrame:]...)
	return f
}

var errPartyClosed = errors.New("media: party closed")

// send encodes one mixing frame and writes it to the participant.
func (p *Party) send(frame []int16) error {
	select {
	case <-p.done:
		return errPartyClosed
	default:
	}
	payload := p.codec.Encode(frame)
	p.sendMu.Lock()
	pkt := BuildRTP(p.codec.PayloadType(), p.seq, p.ts, p.ssrc, p.marker, payload)
	p.seq++
	p.ts += p.codec.TimestampStep()
	p.marker = false
	p.sendMu.Unlock()
	if _, err := p.conn.WriteToUDP(pkt, p.peer()); err != nil {
		return fmt.Errorf("media: write conference RTP: %w", err)
	}
	p.tx.Add(1)
	return nil
}
