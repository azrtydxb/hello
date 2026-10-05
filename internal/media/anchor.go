package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// errSessionClosed is returned by Play and Record once Close has stopped
// the session; it distinguishes "the caller cancelled" from "the session
// is gone".
var errSessionClosed = errors.New("media: session closed")

// Anchor is the media anchor the B2BUA creates per voicemail call. It
// holds the IP that goes into answer SDP so phones behind NAT reach the
// node's RTP socket, and it hands out one Session per answered offer.
type Anchor struct {
	host string
	log  *slog.Logger
}

// NewAnchor builds an anchor that advertises host (an IPv4 literal) in its
// answer SDP. A nil log falls back to slog.Default().
func NewAnchor(host string, log *slog.Logger) *Anchor {
	if log == nil {
		log = slog.Default()
	}
	return &Anchor{host: host, log: log}
}

// AdvertisedHost is the IPv4 the anchor writes into SDP answers.
func (a *Anchor) AdvertisedHost() string { return a.host }

// Answer parses the caller's SDP offer, binds a UDP socket for the
// anchored leg, starts the receive loop and returns the local answer SDP
// plus the Session. The socket binds an explicit port (bindRTP); a bind
// can still fail (exhaustion, a race with a just-closed socket), so the
// bind is retried a few times before Answer gives up.
func (a *Anchor) Answer(offer []byte) ([]byte, Session, error) {
	ip := net.ParseIP(a.host)
	if ip == nil || ip.To4() == nil {
		return nil, nil, fmt.Errorf("media: advertised host %q is not an IPv4 address", a.host)
	}
	off, err := ParseAudioSDP(offer)
	if err != nil {
		return nil, nil, err
	}
	var conn *net.UDPConn
	var bindErr error
	for attempt := 0; attempt < 5; attempt++ {
		c, err := bindRTP()
		if err == nil {
			conn = c
			break
		}
		bindErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		return nil, nil, fmt.Errorf("media: bind RTP socket: %w", bindErr)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port

	target := &net.UDPAddr{IP: net.ParseIP(off.Address), Port: off.Port}
	if target.IP == nil {
		_ = conn.Close() // nothing received yet; the close error is irrelevant
		return nil, nil, fmt.Errorf("media: offer address %q is not an IP", off.Address)
	}
	s := &anchorSession{
		conn:   conn,
		offer:  off,
		target: target,
		log:    a.log,
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if _, err := rand.Read(s.ssrc[:]); err != nil {
		_ = conn.Close() // nothing received yet; the close error is irrelevant
		return nil, nil, fmt.Errorf("media: generate RTP SSRC: %w", err)
	}
	s.audio = make(chan []byte, inboundQueue)
	s.dtmf = make(chan byte, dtmfQueue)
	go s.loop()
	answer := BuildAudioSDP(a.host, port, off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	return answer, s, nil
}

// bindRTP binds a wildcard UDP socket on an explicit port of the dynamic
// range, chosen at random, falling back to a kernel-chosen one. Asking for
// port 0 is not enough on BSD kernels (macOS): a wildcard bind to port 0
// can be handed a port another socket holds on a specific address, and
// datagrams to that address then reach the other socket, never this one
// (the in-process tests' 127.0.0.1 phones). An explicit port is refused
// when any socket holds it, on every kernel.
func bindRTP() (*net.UDPConn, error) {
	const lo, hi = 49152, 65535
	var b [2]byte
	for range 16 {
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		p := lo + int(binary.BigEndian.Uint16(b[:]))%(hi-lo+1)
		if c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p}); err == nil {
			return c, nil
		}
	}
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
}

// anchorSession is the Session implementation over one UDP socket.
type anchorSession struct {
	conn   *net.UDPConn
	offer  AudioSDP     // negotiated codecs; immutable after Answer
	target *net.UDPAddr // where Play sends until the first packet locks the peer
	log    *slog.Logger

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	remoteMu sync.Mutex
	remote   *net.UDPAddr // peer source address, pinned on the first packet

	seqMu sync.Mutex
	seq   uint16 // next outgoing sequence number
	ts    uint32 // next outgoing RTP timestamp
	ssrc  [4]byte

	// audio carries decoded inbound PCM in chunks of at most frameBytes;
	// dtmf carries decoded DTMF presses awaiting the active Record.
	audio chan []byte
	dtmf  chan byte
	// filter runs only in the loop goroutine, so it needs no lock.
	filter DTMFFilter
}

// Queue bounds. inboundQueue holds about 2.5 seconds of 20 ms frames, so a
// Record that starts a beat late loses nothing; dtmfQueue holds a full
// feature-code sequence, and beyond that digits are dropped rather than
// delaying RTP.
const (
	inboundQueue = 128
	dtmfQueue    = 16
	// maxDrainPerTick bounds how much queued audio a single Record tick
	// consumes, so a misbehaving sender cannot balloon one recording.
	maxDrainPerTick = 16
)

// Close stops the receive loop and closes the socket. sync.Once makes the
// second call a no-op, so a caller closing on both the hangup path and a
// deferred cleanup cannot double-close.
func (s *anchorSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		// A close error on teardown (already closed, ECONNRESET) changes
		// nothing for the caller; Close keeps its error-free signature.
		_ = s.conn.Close()
	})
	return nil
}

// Play sends audio to the remote party as G.711 RTP, one 20 ms frame at a
// time, paced in real time so a phone plays it at speech speed. It returns
// ctx.Err() when ctx is cancelled mid-way and errSessionClosed after
// Close.
func (s *anchorSession) Play(ctx context.Context, audio []byte) error {
	if len(audio)%2 != 0 {
		return errors.New("media: PCM length is odd (16-bit samples expected)")
	}
	encode := EncodePCMU
	if s.offer.PayloadType == 8 {
		encode = EncodePCMA
	}
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	for off := 0; off < len(audio); off += frameBytes {
		end := min(off+frameBytes, len(audio))
		if err := s.sendAudio(encode, audio[off:end]); err != nil {
			if s.ctx.Err() != nil {
				return errSessionClosed
			}
			return fmt.Errorf("media: send RTP frame: %w", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			return errSessionClosed
		}
	}
	return nil
}

// sendAudio encodes one frame of PCM and puts it on the wire with the next
// sequence number and a timestamp advanced by one frame.
func (s *anchorSession) sendAudio(encode func([]byte) []byte, pcm []byte) error {
	payload := encode(pcm)
	s.seqMu.Lock()
	seq := s.seq
	s.seq++
	ts := s.ts
	s.ts += frameSamples
	s.seqMu.Unlock()
	packet := BuildRTP(s.offer.PayloadType, seq, ts, binary.BigEndian.Uint32(s.ssrc[:]), false, payload)
	return s.send(packet)
}

// send writes one datagram to the current peer address.
func (s *anchorSession) send(packet []byte) error {
	_, err := s.conn.WriteToUDP(packet, s.currentRemote())
	if err != nil {
		return fmt.Errorf("media: write RTP: %w", err)
	}
	return nil
}

// currentRemote returns where to send: the address the offer named until
// the first inbound packet reveals the peer's real source (symmetric RTP,
// the NAT-safe default most UAs rely on), then that pinned address.
func (s *anchorSession) currentRemote() *net.UDPAddr {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remote != nil {
		return s.remote
	}
	return s.target
}

// loop receives RTP until the socket closes. This single goroutine owns
// the filter and feeds the two queues, so the hot path never takes a lock
// beyond the one-off remote pin.
func (s *anchorSession) loop() {
	buf := make([]byte, rtpMaxPacket)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed by Close
		}
		s.lockRemote(from)
		pkt, ok := ParseRTP(buf[:n])
		if !ok {
			continue // a malformed datagram is dropped, never fatal
		}
		switch {
		case pkt.PayloadType == s.offer.PayloadType:
			s.pushAudio(decodePayload(pkt.PayloadType, pkt.Payload))
		case s.offer.DTMFPayloadType != 0 && pkt.PayloadType == s.offer.DTMFPayloadType:
			ev, ok := ParseTelephoneEvent(pkt.Payload)
			if !ok {
				continue
			}
			ev.Sequence = pkt.Sequence
			if code, deliver := s.filter.Observe(ev, pkt.Marker); deliver {
				select {
				case s.dtmf <- code:
				default:
					// No reader and a full queue: drop the digit rather
					// than stall the receive loop behind it.
				}
			}
		}
	}
}

// lockRemote pins the peer's source address on the first packet (symmetric
// RTP): the offer's c= address can be wrong behind NAT, but the source of
// the datagrams is where replies must go. Later source changes are
// ignored — a mid-call address change means a re-INVITE, which re-anchors
// the leg instead of silently switching peers.
func (s *anchorSession) lockRemote(from *net.UDPAddr) {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remote == nil {
		s.remote = &net.UDPAddr{
			IP:   append(net.IP(nil), from.IP...),
			Port: from.Port,
			Zone: from.Zone,
		}
	}
}

// pushAudio splits decoded PCM into frame-sized chunks for the queues.
// Chunks are copied so the receive buffer can be reused at once. A full
// queue loses audio rather than blocking RTP reception.
func (s *anchorSession) pushAudio(pcm []byte) {
	if len(pcm)%2 == 1 {
		pcm = pcm[:len(pcm)-1] // a half sample cannot be played; drop it
	}
	for off := 0; off < len(pcm); off += frameBytes {
		end := min(off+frameBytes, len(pcm))
		chunk := make([]byte, end-off)
		copy(chunk, pcm[off:end])
		select {
		case s.audio <- chunk:
		default:
			return
		}
	}
}

// decodePayload converts a G.711 payload to 16-bit little-endian PCM per
// the negotiated payload type.
func decodePayload(pt uint8, payload []byte) []byte {
	if pt == 8 {
		return DecodePCMA(payload)
	}
	return DecodePCMU(payload)
}

// silentFrame is appended by Record for every tick with no inbound media,
// so a silent stretch is PCM of zeros like any recorder produces.
var silentFrame = make([]byte, frameBytes)

// Record captures PCM from the remote party for at most maxDur, stopping
// early on '#' or ctx cancellation, with the guarantees documented on the
// Session interface. Frames the remote sent but Record has not consumed
// yet wait in the queue, so a recording that starts a beat late still
// catches the first audio.
func (s *anchorSession) Record(ctx context.Context, maxDur time.Duration, dtmf chan<- byte) ([]byte, error) {
	if maxDur <= 0 {
		return nil, nil
	}
	frames := int(maxDur / frameDur)
	var (
		out []byte
		rem []byte // decoded audio not yet a whole frame
	)
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()
	for frame := 0; frame < frames; {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-s.ctx.Done():
			return out, errSessionClosed
		case code := <-s.dtmf:
			// Forward every digit, then stop on the terminator. The forward
			// never blocks, per the interface guarantee.
			select {
			case dtmf <- code:
			default:
			}
			if code == '#' {
				return out, nil
			}
			// A digit does not advance the recording clock; the caller was
			// typing, not talking.
		case <-ticker.C:
		drain:
			for i := 0; i < maxDrainPerTick; i++ {
				select {
				case chunk := <-s.audio:
					rem = append(rem, chunk...)
				default:
					break drain
				}
			}
			// Exactly one frame per tick, so the recording's length
			// tracks maxDur whatever the peer's packetisation: media if a
			// complete frame has arrived, silence if not. A partial frame
			// waits in rem and leads the next one.
			if len(rem) >= frameBytes {
				out = append(out, rem[:frameBytes]...)
				rem = rem[frameBytes:]
			} else {
				out = append(out, silentFrame...)
			}
			frame++
		}
	}
	return out, nil
}
