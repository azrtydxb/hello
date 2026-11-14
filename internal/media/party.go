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

// partyCodec converts between a participant's RTP payload and samples at
// the mixing rate (MixRate, 8 kHz). G.711 is direct. A wideband codec (G.722,
// Opus) decodes at 16 kHz and a streaming half-band filter brings it down to
// the mixing rate; the mix goes up the same way before it is encoded. The
// codec's state lives here, so one partyCodec serves one participant.
type partyCodec struct {
	codec Codec
	enc   *Encoder
	dec   *Decoder
	down  *Down2 // wideband decode path, nil for G.711
	up    *Up2   // wideband encode path, nil for G.711
}

func newPartyCodec(c Codec) (*partyCodec, error) {
	enc, err := NewEncoder(c)
	if err != nil {
		return nil, err
	}
	dec, err := NewDecoder(c)
	if err != nil {
		enc.Close()
		return nil, err
	}
	pc := &partyCodec{codec: c, enc: enc, dec: dec}
	if c.SampleRate() != MixRate {
		pc.down, pc.up = NewDown2(), NewUp2()
	}
	return pc, nil
}

func (pc *partyCodec) close() {
	pc.enc.Close()
	pc.dec.Close()
}

// decode turns one RTP payload into samples at MixRate.
func (pc *partyCodec) decode(payload []byte) []int16 {
	s := PCMToSamples(pc.dec.Decode(payload))
	if pc.down != nil {
		return pc.down.Process(s)
	}
	return s
}

// encode turns one MixFrame of samples at MixRate into an RTP payload.
func (pc *partyCodec) encode(frame []int16) []byte {
	if pc.up != nil {
		wide := pc.up.Process(frame)
		// The converter's output length can differ from the codec's frame
		// by a sample at a stream's start; the codecs need whole frames.
		want := pc.codec.FrameSamples()
		if len(wide) > want {
			wide = wide[:want]
		}
		for len(wide) < want {
			wide = append(wide, 0)
		}
		return pc.enc.Encode(SamplesToPCM(wide))
	}
	return pc.enc.Encode(SamplesToPCM(frame))
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
	conn  LegConn
	codec *partyCodec
	offer AudioSDP // the negotiated single codec and its telephone-event

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

	// sec is the participant's SRTP state (plain RTP until SDES settles).
	sec *Secure

	closeOnce sync.Once
	done      chan struct{}

	// vid is the participant's video (roomvideo.go), nil without; the room the
	// participant is in is set by Room.Add.
	vid    *partyVideo
	vwg    sync.WaitGroup
	roomMu sync.Mutex
	room   *Room
	roomID string

	// Counters, for the room's metrics.
	rx, tx, dropped atomic.Uint64
	lastRx          atomic.Int64 // unix nanos of the last inbound audio packet
}

// NewParty binds a socket for the participant's media from the port range
// [minPort, maxPort] (0 for both lets the kernel choose, as the tests do),
// negotiates the codec from offer the way every call Hello terminates does
// (Opus when offered and built in, else G.722, else G.711)
// and returns the answer SDP advertising host.
func NewParty(host string, minPort, maxPort int, offer []byte) ([]byte, *Party, error) {
	return NewPartySecure(host, minPort, maxPort, offer, SecurePolicy{})
}

// NewPartySecure is NewParty for a participant whose leg may carry SRTP
// (SDES): with pol.Allow the party accepts the participant's a=crypto offer,
// the answer carries Hello's key, and the mixer's audio is encrypted per
// participant; a policy that requires SRTP and an offer that cannot deliver
// it fails with ErrSRTPRequired and binds nothing.
func NewPartySecure(host string, minPort, maxPort int, offer []byte, pol SecurePolicy) ([]byte, *Party, error) {
	return NewPartySecureVideo(host, minPort, maxPort, offer, pol, nil)
}

// NewPartySecureVideo is NewPartySecure for a participant that may bring video
// (a phone or a door station with H.264 on RTP/AVP): with vj the answer carries
// a video section when the offer holds H.264 Constrained Baseline in the room's
// codec, on a socket pair of the participant's own, and refuses it in place
// (port 0) otherwise. The video goes through the room's forwarder.
func NewPartySecureVideo(host string, minPort, maxPort int, offer []byte, pol SecurePolicy, vj *VideoJoin) ([]byte, *Party, error) {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil, nil, fmt.Errorf("media: advertised host %q is not an IPv4 address", host)
	}
	parsed, err := ParseAudioSDP(offer)
	if err != nil {
		return nil, nil, err
	}
	off, ok := parsed.Terminated()
	if !ok {
		return nil, nil, errors.New("media: the offer has no codec a conference can mix")
	}
	codec, err := newPartyCodec(off.Codec)
	if err != nil {
		return nil, nil, fmt.Errorf("media: conference codec %s: %w", off.Codec.Name, err)
	}
	target := &net.UDPAddr{IP: net.ParseIP(off.Address), Port: off.Port}
	if target.IP == nil {
		codec.close()
		return nil, nil, fmt.Errorf("media: offer address %q is not an IP", off.Address)
	}
	conn, err := bindPartySocket(minPort, maxPort)
	if err != nil {
		codec.close()
		return nil, nil, err
	}
	// The socket carries STUN and DTLS as well as RTP when the participant
	// negotiates ICE or DTLS-SRTP; p exists before the first datagram is
	// read.
	var p *Party
	sec := &Secure{}
	sec.Bind(conn.LocalAddr(), func(b []byte, to *net.UDPAddr) { _, _ = conn.WriteToUDP(b, to) },
		func(from *net.UDPAddr, _ bool) {
			if p != nil {
				p.latch(from)
			}
		})
	ansSDP, err := sec.AnswerSDP(parsed, off, pol)
	if err != nil {
		_ = conn.Close() // nothing received yet; the close error is irrelevant
		sec.Close()
		codec.close()
		return nil, nil, err
	}
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = conn.Close() // nothing received yet; the close error is irrelevant
		sec.Close()
		codec.close()
		return nil, nil, fmt.Errorf("media: party RTP identifiers: %w", err)
	}
	p = &Party{
		conn: conn, codec: codec, offer: off, target: target,
		seq: startSeq(b[0:2]), ts: binary.BigEndian.Uint32(b[2:6]), ssrc: binary.BigEndian.Uint32(b[6:10]),
		marker: true, sec: sec,
		dtmf: make(chan byte, partyDTMFQueue), done: make(chan struct{}),
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // always a UDPAddr
	answer := ansSDP.Build(host, port, "sendrecv")
	if vj != nil {
		if pv, c, sock := plainPartyVideo(sec, offer, minPort, maxPort, vj); pv != nil {
			answer = append(answer[:len(answer):len(answer)], pv.section(host)...)
			if sock != nil {
				p.vid = newPartyVideo(p, *c)
				p.vid.pv = pv
			}
		}
	} else if first, has := PlainVideoLine(offer); has {
		// A video line in the offer is answered, refused in place (RFC 3264).
		answer = append(answer[:len(answer):len(answer)], (&plainVideo{offered: true, firstPT: first}).section(host)...)
	}
	go p.recv()
	if p.vid != nil {
		p.vid.start()
	}
	return answer, p, nil
}

// plainPartyVideo decides the video of a plain RTP participant: its SDP state,
// the codec taken and the sockets, the last two nil when the video is refused. A
// participant whose audio is SRTP gets no plain video beside it.
func plainPartyVideo(sec *Secure, offer []byte, minPort, maxPort int, vj *VideoJoin) (*plainVideo, *SDPCodec, *videoSock) {
	first, has := PlainVideoLine(offer)
	if !has {
		return nil, nil, nil
	}
	pv := &plainVideo{offered: true, firstPT: first}
	vo, rtp, rtcp, ok := PlainVideoOffer(offer)
	if !ok {
		return pv, nil, nil
	}
	if _, enc := sec.Profile(); enc {
		return pv, nil, nil
	}
	c, ok := chooseRoomCodec(vo.Codecs, vj.Codec)
	if !ok || !equalFold(c.Name, "H264") {
		return pv, nil, nil
	}
	sock, err := bindVideoSock(minPort, maxPort, nil)
	if err != nil {
		return pv, nil, nil
	}
	sock.setTarget(rtp, rtcp)
	pv.sock = sock
	pv.setSection(&c, "sendrecv", vj.Kbps)
	return pv, &c, sock
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
	p.sec.SetPeer(&net.UDPAddr{IP: ip, Port: port})
}

// Secure is the participant's SRTP state, for the SDP exchanges of the call.
func (p *Party) Secure() *Secure { return p.sec }

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
		if p.vid != nil {
			p.vid.close()
		}
		p.sec.Close()
		_ = p.conn.Close() // teardown: nothing depends on the close error
		// The receive goroutine owns the decoder and frees it on its way out.
		p.sendMu.Lock()
		p.codec.enc.Close()
		p.sendMu.Unlock()
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
	defer p.codec.dec.Close()
	buf := make([]byte, rtpMaxPacket)
	for {
		n, from, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed
		}
		// An encrypted participant's packets are authenticated first; one
		// that fails is dropped before it is latched, decoded or mixed.
		data, isRTCP, ok, _ := p.sec.UnprotectAny(buf[:n], from)
		if !ok {
			continue
		}
		if isRTCP {
			p.latch(from)
			p.onVideoRTCP(data)
			continue
		}
		pkt, ok := ParseRTP(data)
		if !ok {
			continue
		}
		switch {
		case p.vid != nil && p.vid.vid.carries(pkt.PayloadType):
			p.latch(from)
			p.onVideoRTP(pkt, data)
		case pkt.PayloadType == p.codec.codec.PT:
			p.latch(from)
			p.rx.Add(1)
			p.lastRx.Store(time.Now().UnixNano())
			p.push(p.codec.decode(pkt.Payload))
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
		p.dropped.Add(uint64(drop)) //nolint:gosec // drop is positive here
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

// startSeq draws the first sequence number of an outbound stream from two
// random bytes. SRTP keys the packet index on a rollover counter that a
// receiver assumes is 0 for the first packet it sees (RFC 3711 3.3.1), so a
// stream that begins close to the 16-bit wrap and whose first packets are
// lost, or are not yet being listened to, authenticates nothing after the
// wrap: the receiver's index is one rollover behind the sender's and every
// packet fails. Starting in the lower half leaves a late or lossy receiver
// 32768 packets (over ten minutes of audio) to catch the first one.
func startSeq(b []byte) uint16 { return binary.BigEndian.Uint16(b) & 0x7fff }

var errPartyClosed = errors.New("media: party closed")

// send encodes one mixing frame and writes it to the participant.
func (p *Party) send(frame []int16) error {
	select {
	case <-p.done:
		return errPartyClosed
	default:
	}
	p.sendMu.Lock()
	payload := p.codec.encode(frame)
	pkt := BuildRTP(p.codec.codec.PT, p.seq, p.ts, p.ssrc, p.marker, payload)
	p.seq++
	p.ts += p.codec.codec.TimestampStep()
	p.marker = false
	p.sendMu.Unlock()
	pkt, ok := p.sec.Protect(pkt)
	if !ok {
		return errors.New("media: SRTP protect failed")
	}
	if _, err := p.conn.WriteToUDP(pkt, p.peer()); err != nil {
		return fmt.Errorf("media: write conference RTP: %w", err)
	}
	p.tx.Add(1)
	return nil
}

// NewPartyWebRTC is NewPartySecure for a participant that is a browser: its
// media is an endpoint of the shared WebRTC port (ICE-lite, DTLS-SRTP) instead
// of a socket of its own, and the answer is the browser's SDP, listing only the
// participant's audio: a video or data line in the offer is answered with port 0
// in place (the conference mixes audio; room video comes later).
func NewPartyWebRTC(node *WebRTCNode, offer []byte) ([]byte, *Party, error) {
	return NewPartyWebRTCVideo(node, offer, nil)
}

// NewPartyWebRTCVideo is NewPartyWebRTC for a browser that may bring video: with
// vj the answer carries the video section (H.264 Constrained Baseline or VP8,
// the room's codec once it has one) on the same transport as the audio, and the
// video goes through the room's forwarder; without a codec the room can use the
// section is answered with port 0.
func NewPartyWebRTCVideo(node *WebRTCNode, offer []byte, vj *VideoJoin) ([]byte, *Party, error) {
	if node == nil {
		return nil, nil, errors.New("media: WebRTC is not enabled on this node")
	}
	sess, err := ParseSDPSession(offer)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateWebRTC(sess); err != nil {
		return nil, nil, err
	}
	parsed, err := ParseAudioSDP(offer)
	if err != nil {
		return nil, nil, err
	}
	off, ok := parsed.Terminated()
	if !ok {
		return nil, nil, errors.New("media: the offer has no codec a conference can mix")
	}
	codec, err := newPartyCodec(off.Codec)
	if err != nil {
		return nil, nil, fmt.Errorf("media: conference codec %s: %w", off.Codec.Name, err)
	}
	ep, err := node.Mux().NewEndpoint()
	if err != nil {
		codec.close()
		return nil, nil, err
	}
	var p *Party
	sec := &Secure{}
	sec.Bind(ep.LocalAddr(), func(b []byte, to *net.UDPAddr) { _, _ = ep.WriteToUDP(b, to) },
		func(from *net.UDPAddr, _ bool) {
			if p != nil {
				p.latch(from)
			}
		})
	fail := func(err error) ([]byte, *Party, error) {
		_ = ep.Close()
		sec.Close()
		codec.close()
		return nil, nil, err
	}
	ufrag, _, ok := sec.iceLocal()
	if !ok {
		return fail(errors.New("media: ICE-lite is off on this node"))
	}
	if err := ep.SetUfrag(ufrag); err != nil {
		return fail(err)
	}
	ans, err := sec.AnswerSDP(parsed, off, SecurePolicy{Allow: true, Require: true})
	if err != nil {
		return fail(err)
	}
	w := sectionSpec{audioMid: "0"}
	for _, m := range sess.Media() {
		rs := remoteSection{kind: m.Kind(), mid: m.Mid(), proto: m.Proto(), port0: m.Port() == 0}
		if f := m.Formats(); len(f) > 0 {
			rs.first = f[0]
		}
		w.remote = append(w.remote, rs)
		if m.Kind() == "audio" && !rs.port0 && w.audioMid == "0" {
			w.audioMid = m.Mid()
		}
	}
	var vsel *SDPCodec
	if vj != nil {
		if vo, ok := videoOfferOf(sess); ok {
			if c, ok := chooseRoomCodec(vo.Codecs, vj.Codec); ok {
				vsel = &c
				w.vSel, w.vDir, w.kbps, w.videoMid = &c, "sendrecv", vj.Kbps, vo.Mid
			}
		}
	}
	ans = ans.withWeb(w.audioMid, node.Hosts())
	ans.localWeb.finish = func(sdp []byte) []byte {
		out, _, _, err := renderSections(w, false, sdp)
		if err != nil {
			return sdp
		}
		return out
	}
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fail(fmt.Errorf("media: party RTP identifiers: %w", err))
	}
	p = &Party{
		conn: ep, codec: codec, offer: off, target: nil,
		seq: startSeq(b[0:2]), ts: binary.BigEndian.Uint32(b[2:6]), ssrc: binary.BigEndian.Uint32(b[6:10]),
		marker: true, sec: sec,
		dtmf: make(chan byte, partyDTMFQueue), done: make(chan struct{}),
	}
	if vsel != nil {
		p.vid = newPartyVideo(p, *vsel)
	}
	go p.recv()
	if p.vid != nil {
		p.vid.start()
	}
	return ans.Build(node.Hosts()[0], node.Mux().Port(), "sendrecv"), p, nil
}
