// Video on a relay (clients-webrtc-mobile-video S-36 to S-41): the anchor
// relays the RTP of a video stream between two webrtc legs without decoding,
// repacketizing or transcoding it, next to the audio. It is a translator in
// the RFC 7667 sense: the payload is untouched, each leg gets its own SSRC and
// a constant sequence and timestamp offset (so a retransmission request on one
// leg maps to a sequence number on the other by subtraction), and RTCP is
// terminated and regenerated per leg: reception and sender reports from the
// leg's own statistics, with picture loss, full intra requests, NACK and the
// receiver's bandwidth estimate (REMB) carried across.
//
// Audio and video share one transport (BUNDLE): the leg's SRTP context
// protects both and the payload type tells them apart. Nothing here knows
// about SDP; the sip package decides what is negotiated and tells the relay
// with SetVideo. The seam for recording (6b), conference video (11) and video
// phones (10) is VideoLeg and the two-leg pairing: a plain-RTP video leg would
// be one more leg configuration, a recorder one more consumer of forwardVideo.
package media

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
)

// relayVideoQueue is a leg's video queue: about half a second of a 1.5 Mbit/s
// stream, enough for a keyframe burst.
const relayVideoQueue = 512

// videoRTCPInterval is how often reports and the bandwidth cap go out.
const videoRTCPInterval = time.Second

// keyframeSpacing is the shortest gap between two keyframe requests to one
// sender (RFC 4585 leaves the pacing to the implementation).
const keyframeSpacing = 400 * time.Millisecond

// videoClock is the RTP clock of every video codec Hello relays.
const videoClock = 90000

// VideoFeedback is the RTCP feedback a leg negotiated.
type VideoFeedback struct {
	NACK, PLI, FIR, REMB bool
}

// VideoLeg is one leg's negotiated video: the payload type in this leg's
// numbering, the codec name (H264 or VP8) and the feedback it takes.
type VideoLeg struct {
	PT    uint8
	Codec string
	FB    VideoFeedback
}

// VideoStats are a relay's video counters.
type VideoStats struct {
	Packets, Octets, DroppedCap, DroppedNoPeer, KeyframeRequests, NACKs uint64
}

// relayVideo is the session-level video state of a Relay.
type relayVideo struct {
	maxKbps                                                    atomic.Int32
	packets, octets, droppedCap, droppedNoPeer, keyReqs, nacks atomic.Uint64
	started                                                    bool
}

// SetVideoCap sets the per-direction ceiling in kbit/s (0 for none): REMB
// toward each sender is held at it and packets above it are dropped and
// counted.
func (r *Relay) SetVideoCap(kbps int) {
	if kbps < 0 {
		kbps = 0
	}
	r.video.maxKbps.Store(int32(kbps)) //nolint:gosec // bounded by the caller's configuration
}

// VideoStats snapshots the session's video counters.
func (r *Relay) VideoStats() VideoStats {
	v := &r.video
	return VideoStats{Packets: v.packets.Load(), Octets: v.octets.Load(), DroppedCap: v.droppedCap.Load(),
		DroppedNoPeer: v.droppedNoPeer.Load(), KeyframeRequests: v.keyReqs.Load(), NACKs: v.nacks.Load()}
}

// SetVideo turns video on for leg (a nil v turns it off): from now on packets
// of the payload type are relayed to the other leg, if that one carries video
// too. Setting a leg starts it with a new outbound stream (new SSRC and
// offsets); a renegotiation that leaves the payload type alone keeps the
// stream.
func (r *Relay) SetVideo(leg string, v *VideoLeg) {
	l := r.findLeg(leg)
	if l == nil {
		return
	}
	l.vid.set(v)
}

// VideoOn reports whether both legs carry video.
func (r *Relay) VideoOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.legs) == 2 && r.legs[0].vid.on() && r.legs[1].vid.on()
}

// LegVideo returns leg's video configuration, ok false when it has none.
func (r *Relay) LegVideo(leg string) (VideoLeg, bool) {
	if l := r.findLeg(leg); l != nil {
		return l.vid.config()
	}
	return VideoLeg{}, false
}

// RequestKeyframe asks the sender on leg (a PLI, or a FIR where that is all it
// takes) for a new keyframe; the SIP layer calls it when a stream resumes
// after hold or a leg is re-homed. It is paced and does nothing until the
// sender's stream is known.
func (r *Relay) RequestKeyframe(leg string) {
	if l := r.findLeg(leg); l != nil {
		r.requestKeyframe(l)
	}
}

// videoLeg is one leg's video state: what it negotiated, the inbound stream it
// receives (for reports and feedback) and the outbound stream it sends.
type videoLeg struct {
	pt atomic.Int32 // payload type + 1, 0 when the leg carries no video

	mu  sync.Mutex
	cfg VideoLeg

	// Inbound: the stream the leg's endpoint sends us.
	haveIn            bool
	inSSRC            uint32
	baseSeq, maxSeq   uint16
	cycles            uint32
	received          uint32
	expectedPrior     uint32
	receivedPrior     uint32
	jitter            float64
	lastTransit       int64
	haveTransit       bool
	lastSRMid         uint32 // middle 32 bits of the endpoint's last sender report NTP time
	lastSRAt          time.Time
	inPackets, inOcts uint32

	// Outbound: the stream we send into the leg, a constant-offset image of
	// the other leg's inbound stream.
	outSSRC    uint32
	mapped     bool
	mapSSRC    uint32 // the other leg's inbound SSRC this stream mirrors
	seqOff     uint16
	tsOff      uint32
	lastOutSeq uint16
	lastOutTS  uint32
	lastOutAt  time.Time
	outPackets uint32
	outOctets  uint32
	remoteEst  float64 // the endpoint's own bandwidth estimate (REMB) for our stream, bit/s; 0 unknown
	lastKeyReq time.Time
	bucket     float64 // rate-cap token bucket, bytes
	bucketAt   time.Time
	firSeq     uint8
}

func (v *videoLeg) set(c *VideoLeg) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c == nil {
		v.pt.Store(0)
		v.cfg = VideoLeg{}
		return
	}
	// The same payload type again (a re-INVITE that kept video) keeps the
	// stream; anything else starts it over.
	if int32(c.PT)+1 == v.pt.Load() && v.cfg.Codec == c.Codec {
		v.cfg = *c
		return
	}
	v.cfg = *c
	v.haveIn, v.mapped, v.haveTransit = false, false, false
	v.outPackets, v.outOctets, v.inPackets, v.inOcts = 0, 0, 0, 0
	var b [4]byte
	_, _ = rand.Read(b[:])
	v.outSSRC = binary.BigEndian.Uint32(b[:]) | 1 // never 0
	v.pt.Store(int32(c.PT) + 1)
}

func (v *videoLeg) on() bool { return v.pt.Load() != 0 }

func (v *videoLeg) config() (VideoLeg, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cfg, v.pt.Load() != 0
}

// carries reports whether a packet of payload type pt is this leg's video.
func (v *videoLeg) carries(pt uint8) bool { return v.pt.Load() == int32(pt)+1 }

// noteIn tracks one inbound packet (RFC 3550 A.1 and A.8) and reports whether
// it starts a new stream.
func (v *videoLeg) noteIn(seq uint16, ts, ssrc uint32, size int, now time.Time) (fresh bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.haveIn || ssrc != v.inSSRC {
		v.haveIn, v.inSSRC = true, ssrc
		v.baseSeq, v.maxSeq, v.cycles = seq, seq, 0
		v.received, v.expectedPrior, v.receivedPrior = 0, 0, 0
		v.jitter, v.haveTransit = 0, false
		fresh = true
	} else if d := seq - v.maxSeq; d < 0x8000 && d != 0 {
		if seq < v.maxSeq {
			v.cycles += 1 << 16
		}
		v.maxSeq = seq
	}
	v.received++
	v.inPackets++
	v.inOcts += uint32(size) //nolint:gosec // a datagram
	arrival := now.UnixNano() * videoClock / int64(time.Second)
	transit := arrival - int64(ts)
	if v.haveTransit {
		d := transit - v.lastTransit
		if d < 0 {
			d = -d
		}
		v.jitter += (float64(d) - v.jitter) / 16
	}
	v.lastTransit, v.haveTransit = transit, true
	return fresh
}

// report is the reception report for the inbound stream, nil before any
// packet arrived.
func (v *videoLeg) report(now time.Time) *rtcp.ReceptionReport {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.haveIn {
		return nil
	}
	ext := v.cycles + uint32(v.maxSeq)
	expected := ext - uint32(v.baseSeq) + 1
	lost := int64(expected) - int64(v.received)
	if lost < 0 {
		lost = 0
	}
	expInt := expected - v.expectedPrior
	recInt := v.received - v.receivedPrior
	v.expectedPrior, v.receivedPrior = expected, v.received
	var fraction uint8
	if expInt > recInt && expInt > 0 {
		fraction = uint8((uint64(expInt-recInt) << 8) / uint64(expInt)) //nolint:gosec // the quotient is below 256
	}
	var dlsr uint32
	if v.lastSRMid != 0 {
		dlsr = uint32(now.Sub(v.lastSRAt).Seconds() * 65536) //nolint:gosec // seconds of delay, small
	}
	return &rtcp.ReceptionReport{
		SSRC: v.inSSRC, FractionLost: fraction, TotalLost: uint32(lost), //nolint:gosec // bounded above
		LastSequenceNumber: ext, Jitter: uint32(v.jitter), LastSenderReport: v.lastSRMid, Delay: dlsr,
	}
}

// rewriteOut maps a packet of the other leg's stream into this leg's outbound
// stream: our SSRC, this leg's payload type, and sequence and timestamp at a
// constant offset from the source's, so retransmissions and reordering land
// where they belong. A new source stream (a re-INVITE that restarted it) is
// joined seamlessly: the offsets are re-drawn so the numbers continue.
func (v *videoLeg) rewriteOut(data []byte, now time.Time) ([]byte, bool) {
	if len(data) < 12 {
		return nil, false
	}
	seq := binary.BigEndian.Uint16(data[2:4])
	ts := binary.BigEndian.Uint32(data[4:8])
	ssrc := binary.BigEndian.Uint32(data[8:12])
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pt.Load() == 0 {
		return nil, false
	}
	if !v.mapped || ssrc != v.mapSSRC {
		if !v.mapped {
			var b [6]byte
			_, _ = rand.Read(b[:])
			v.seqOff = binary.BigEndian.Uint16(b[0:2])
			v.tsOff = binary.BigEndian.Uint32(b[2:6])
		} else {
			v.seqOff = v.lastOutSeq + 1 - seq
			v.tsOff = v.lastOutTS + videoClock/30 - ts
		}
		v.mapSSRC, v.mapped = ssrc, true
	}
	outSeq := seq + v.seqOff
	outTS := ts + v.tsOff
	if d := outSeq - v.lastOutSeq; v.outPackets == 0 || d < 0x8000 {
		v.lastOutSeq, v.lastOutTS, v.lastOutAt = outSeq, outTS, now
	}
	out := append([]byte(nil), data...)
	binary.BigEndian.PutUint16(out[2:4], outSeq)
	binary.BigEndian.PutUint32(out[4:8], outTS)
	binary.BigEndian.PutUint32(out[8:12], v.outSSRC)
	out[1] = out[1]&0x80 | v.cfg.PT&0x7f
	v.outPackets++
	v.outOctets += uint32(len(data) - 12) //nolint:gosec // a datagram
	return out, true
}

// allow is the rate-cap token bucket for this leg's outbound stream: capKbps
// of sustained rate with one second of burst.
func (v *videoLeg) allow(size int, capKbps int32, now time.Time) bool {
	if capKbps <= 0 {
		return true
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	rate := float64(capKbps) * 125 // bytes per second
	if v.bucketAt.IsZero() {
		v.bucket, v.bucketAt = rate, now
	}
	v.bucket += now.Sub(v.bucketAt).Seconds() * rate
	if v.bucket > rate {
		v.bucket = rate
	}
	v.bucketAt = now
	if v.bucket < float64(size) {
		return false
	}
	v.bucket -= float64(size)
	return true
}

// forwardVideo relays one inbound video packet of leg l to the other leg.
func (r *Relay) forwardVideo(l *relayLeg, pkt *RTPPacket, data []byte) {
	now := time.Now()
	if l.vid.noteIn(pkt.Sequence, pkt.Timestamp, pkt.SSRC, len(data), now) {
		// A new stream: ask its sender for a picture to start from.
		r.requestKeyframe(l)
	}
	peer := r.peerOf(l)
	if peer == nil || !peer.vid.on() || l.muted.Load() {
		r.video.droppedNoPeer.Add(1)
		return
	}
	if !peer.vid.allow(len(data), r.video.maxKbps.Load(), now) {
		r.video.droppedCap.Add(1)
		noteVideoDrop("cap")
		return
	}
	r.video.packets.Add(1)
	r.video.octets.Add(uint64(len(pkt.Payload)))
	msg := relayMsg{data: append([]byte(nil), data...)}
	enqueueDropOldest(peer.vq, msg, &peer.dropped)
}

// enqueueDropOldest queues msg, dropping the oldest entry on a full queue.
func enqueueDropOldest(q chan relayMsg, msg relayMsg, dropped *atomic.Uint64) {
	select {
	case q <- msg:
		return
	default:
	}
	select {
	case <-q:
		dropped.Add(1)
	default:
	}
	select {
	case q <- msg:
	default:
		dropped.Add(1)
	}
}

// sendVideoMsg writes one queued video or RTCP message into leg l.
func (r *Relay) sendVideoMsg(l *relayLeg, m relayMsg) {
	l.remoteMu.Lock()
	dst := l.remote
	l.remoteMu.Unlock()
	if dst == nil {
		return
	}
	if m.rtcp {
		if enc, ok := l.secure.ProtectRTCP(m.data); ok {
			_, _ = l.conn.WriteToUDP(enc, dst)
		}
		return
	}
	if out, ok := l.vid.rewriteOut(m.data, time.Now()); ok {
		l.write(out, dst)
	}
}

// ---- RTCP ---------------------------------------------------------------

// handleRTCP takes one decrypted RTCP compound packet that leg l's endpoint
// sent. Reports are terminated; feedback about the stream the endpoint
// receives is carried to the leg that sends it.
func (r *Relay) handleRTCP(l *relayLeg, data []byte) {
	if !l.vid.on() {
		return
	}
	pkts, err := rtcp.Unmarshal(data)
	if err != nil {
		return
	}
	peer := r.peerOf(l)
	for _, p := range pkts {
		switch p := p.(type) {
		case *rtcp.SenderReport:
			l.vid.mu.Lock()
			if l.vid.haveIn && p.SSRC == l.vid.inSSRC {
				l.vid.lastSRMid = uint32(p.NTPTime >> 16) //nolint:gosec // the middle 32 bits are the point
				l.vid.lastSRAt = time.Now()
			}
			l.vid.mu.Unlock()
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			if peer != nil && peer.vid.on() {
				r.requestKeyframe(peer)
			}
		case *rtcp.TransportLayerNack:
			if peer != nil && peer.vid.on() {
				r.forwardNACK(l, peer, p)
			}
		case *rtcp.ReceiverEstimatedMaximumBitrate:
			l.vid.mu.Lock()
			l.vid.remoteEst = float64(p.Bitrate)
			l.vid.mu.Unlock()
			if peer != nil && peer.vid.on() {
				r.sendREMB(peer, l.vid.estimate())
			}
		}
	}
}

func (v *videoLeg) estimate() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.remoteEst
}

// requestKeyframe sends the sender on leg l a PLI (or FIR) for its inbound
// stream.
func (r *Relay) requestKeyframe(l *relayLeg) {
	now := time.Now()
	l.vid.mu.Lock()
	if !l.vid.haveIn || !l.vid.cfg.FB.PLI && !l.vid.cfg.FB.FIR || now.Sub(l.vid.lastKeyReq) < keyframeSpacing {
		l.vid.mu.Unlock()
		return
	}
	l.vid.lastKeyReq = now
	media := l.vid.inSSRC
	self := l.vid.outSSRC
	pli := l.vid.cfg.FB.PLI
	l.vid.firSeq++
	seq := l.vid.firSeq
	l.vid.mu.Unlock()
	var p rtcp.Packet
	if pli {
		p = &rtcp.PictureLossIndication{SenderSSRC: self, MediaSSRC: media}
	} else {
		p = &rtcp.FullIntraRequest{SenderSSRC: self, MediaSSRC: media, FIR: []rtcp.FIREntry{{SSRC: media, SequenceNumber: seq}}}
	}
	if raw, err := rtcp.Marshal([]rtcp.Packet{p}); err == nil {
		enqueueDropOldest(l.vq, relayMsg{data: raw, rtcp: true}, &l.dropped)
		r.video.keyReqs.Add(1)
		noteVideoKeyframeRequest()
	}
}

// forwardNACK maps a NACK from leg from (about the stream we send it) to the
// sequence numbers of the other leg's inbound stream, by the constant offset.
func (r *Relay) forwardNACK(from, to *relayLeg, n *rtcp.TransportLayerNack) {
	if !to.vid.cfg.FB.NACK {
		return
	}
	from.vid.mu.Lock()
	off, mapped, src := from.vid.seqOff, from.vid.mapped, from.vid.mapSSRC
	from.vid.mu.Unlock()
	if !mapped {
		return
	}
	var seqs []uint16
	for _, pair := range n.Nacks {
		pair.Range(func(s uint16) bool {
			seqs = append(seqs, s-off)
			return true
		})
	}
	if len(seqs) == 0 {
		return
	}
	to.vid.mu.Lock()
	self := to.vid.outSSRC
	to.vid.mu.Unlock()
	out := &rtcp.TransportLayerNack{SenderSSRC: self, MediaSSRC: src, Nacks: nackPairs(seqs)}
	if raw, err := rtcp.Marshal([]rtcp.Packet{out}); err == nil {
		enqueueDropOldest(to.vq, relayMsg{data: raw, rtcp: true}, &to.dropped)
		r.video.nacks.Add(1)
	}
}

// nackPairs packs sequence numbers into RFC 4585 generic NACK pairs.
func nackPairs(seqs []uint16) []rtcp.NackPair {
	var out []rtcp.NackPair
	for i := 0; i < len(seqs); {
		p := rtcp.NackPair{PacketID: seqs[i]}
		j := i + 1
		for ; j < len(seqs); j++ {
			d := seqs[j] - p.PacketID
			if d == 0 || d > 16 {
				break
			}
			p.LostPackets |= 1 << (d - 1)
		}
		out = append(out, p)
		i = j
	}
	return out
}

// sendREMB tells the sender on leg l the most it should send: the receiver's
// estimate held at the session cap.
func (r *Relay) sendREMB(l *relayLeg, est float64) {
	capKbps := r.video.maxKbps.Load()
	bps := est
	if capKbps > 0 && (bps == 0 || bps > float64(capKbps)*1000) {
		bps = float64(capKbps) * 1000
	}
	l.vid.mu.Lock()
	ok, in, self := l.vid.cfg.FB.REMB && l.vid.haveIn, l.vid.inSSRC, l.vid.outSSRC
	l.vid.mu.Unlock()
	if bps <= 0 || !ok {
		return
	}
	p := &rtcp.ReceiverEstimatedMaximumBitrate{SenderSSRC: self, Bitrate: float32(bps), SSRCs: []uint32{in}}
	if raw, err := rtcp.Marshal([]rtcp.Packet{p}); err == nil {
		enqueueDropOldest(l.vq, relayMsg{data: raw, rtcp: true}, &l.dropped)
	}
}

// startVideoLocked launches the report loop (r.mu is held by Start).
func (r *Relay) startVideoLocked() {
	if r.video.started {
		return
	}
	r.video.started = true
	r.wg.Add(1)
	go r.videoRTCPLoop()
}

// closeVideo is the Close hook; the loop ends with the relay's context.
func (r *Relay) closeVideo() {}

// videoRTCPLoop sends each video leg its sender and reception reports and the
// bandwidth cap once per interval.
func (r *Relay) videoRTCPLoop() {
	defer r.wg.Done()
	t := time.NewTicker(videoRTCPInterval)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.mu.Lock()
			legs := append([]*relayLeg(nil), r.legs...)
			r.mu.Unlock()
			for _, l := range legs {
				if l.vid.on() {
					r.sendReports(l)
				}
			}
		}
	}
}

// ntpTime is the 64-bit NTP timestamp of t.
func ntpTime(t time.Time) uint64 {
	const unixToNTP = 2208988800
	sec := uint64(t.Unix()) + unixToNTP        //nolint:gosec // after 1970
	frac := uint64(t.Nanosecond()) << 32 / 1e9 //nolint:gosec // Nanosecond is 0 to 999999999
	return sec<<32 | frac
}

func (r *Relay) sendReports(l *relayLeg) {
	now := time.Now()
	rr := l.vid.report(now)
	var reports []rtcp.ReceptionReport
	if rr != nil {
		reports = append(reports, *rr)
	}
	l.vid.mu.Lock()
	self, packets, octets := l.vid.outSSRC, l.vid.outPackets, l.vid.outOctets
	rtpTS := l.vid.lastOutTS + uint32(now.Sub(l.vid.lastOutAt).Seconds()*videoClock) //nolint:gosec // seconds of silence
	sending := packets > 0
	remoteEst := l.vid.remoteEst
	l.vid.mu.Unlock()
	var pkts []rtcp.Packet
	if sending {
		pkts = append(pkts, &rtcp.SenderReport{SSRC: self, NTPTime: ntpTime(now), RTPTime: rtpTS,
			PacketCount: packets, OctetCount: octets, Reports: reports})
	} else {
		pkts = append(pkts, &rtcp.ReceiverReport{SSRC: self, Reports: reports})
	}
	if raw, err := rtcp.Marshal(pkts); err == nil {
		enqueueDropOldest(l.vq, relayMsg{data: raw, rtcp: true}, &l.dropped)
	}
	// The estimate that bounds this leg's sender is the other side's.
	if peer := r.peerOf(l); peer != nil {
		r.sendREMB(l, peer.vid.estimate())
	}
	_ = remoteEst
}
