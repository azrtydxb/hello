// Conference-room video (clients-webrtc-mobile-video S-45): last-active-speaker
// forwarding. The room mixes audio as it always did; for video it forwards one
// stream: the active speaker's picture goes to every other participant, and the
// speaker sees the speaker before. Nothing is decoded, composed or mixed, so a
// room costs the node what its relays cost: one packet in, one out per viewer.
// Each participant has its own outbound stream (own SSRC, payload type and a
// sequence and timestamp that continue across a switch of speaker, which is the
// translator of relayvideo.go), RTCP is ended and regenerated per participant,
// and a switch holds the new speaker's picture back until its keyframe, which it
// asks for with a PLI.
//
// Why not an SFU or a composed (mixed) picture. A composed picture needs every
// stream decoded and a new one encoded: a CPU cost per room that grows with its
// size and that pure Go cannot pay without cgo codecs. An SFU (LiveKit, Janus)
// is the right answer for a gallery of many pictures with simulcast; it is a
// second media engine beside the one that anchors, records and detects DTMF
// (media-anchoring.md). Hello's rooms are meetings of a few to a dozen people on
// a LAN where one picture is what a speaker-view needs, so forwarding is the
// smaller thing that works. A room's participants must share one codec (the
// first video participant's: H.264 Constrained Baseline, else VP8); one that
// cannot gets the audio only.
package media

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
)

const (
	// speakerDwell is the least time a speaker keeps the picture.
	speakerDwell = 1500 * time.Millisecond
	// speakerGate is the mean-square level (RMS about 100) below which nobody is
	// taken for a speaker.
	speakerGate = 100 * 100
	// speakerLead is how much louder a challenger must be than the speaker.
	speakerLead = 1.5
	// keyGateTimeout is how long a new speaker's pictures are held back for a
	// keyframe before they are let through anyway (a sender that ignores PLI).
	keyGateTimeout = 2 * time.Second
	// viewerAskEvery is how often a viewer that waits for a keyframe has its
	// source asked for one.
	viewerAskEvery = 250 * time.Millisecond
)

// VideoJoin says how a participant's video is taken into a room.
type VideoJoin struct {
	// Kbps is the ceiling written as b=AS and enforced on the forwarded streams.
	Kbps int
	// Codec is the room's codec ("H264" or "VP8"), "" while no participant has
	// video yet: the participant's own preference decides.
	Codec string
}

// partyVideo is a participant's video: the negotiated codec, the stream it
// sends the room, the stream the room sends it, and the queue between.
type partyVideo struct {
	p    *Party
	vid  videoLeg
	vq   chan relayMsg
	pv   *plainVideo // a plain RTP participant's sockets and SDP; nil for a browser
	done chan struct{}

	dropped atomic.Uint64
	// The participant as a viewer: whose stream it is shown and whether it is
	// still waiting for a keyframe of it (a new source's pictures are held back
	// until one arrives, so nobody sees a picture that cannot be decoded).
	gateMu  sync.Mutex
	src     *Party
	waiting bool
	waitAt  time.Time
	askedAt time.Time
	codec   string
}

// VideoCodec is the codec the participant's video carries ("H264" or "VP8"),
// "" for a participant without video.
func (p *Party) VideoCodec() string {
	if p.vid == nil {
		return ""
	}
	return p.vid.codec
}

// HasVideo reports whether the participant carries video.
func (p *Party) HasVideo() bool { return p.vid != nil }

// chooseRoomCodec picks the codec a participant's video offer gives the room:
// the room's when it has one, else H.264 Constrained Baseline, else VP8.
func chooseRoomCodec(offer []SDPCodec, roomCodec string) (SDPCodec, bool) {
	cands := SupportedVideo(offer)
	for _, c := range cands {
		if roomCodec == "" || equalFold(c.Name, roomCodec) {
			return c, true
		}
	}
	return SDPCodec{}, false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if 'a' <= x && x <= 'z' {
			x -= 'a' - 'A'
		}
		if 'a' <= y && y <= 'z' {
			y -= 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func newPartyVideo(p *Party, c SDPCodec) *partyVideo {
	v := &partyVideo{p: p, vq: make(chan relayMsg, relayVideoQueue), done: make(chan struct{}), codec: upper(c.Name)}
	cfg := LegFor(c)
	v.vid.set(&cfg)
	return v
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// start launches the participant's video goroutines.
func (v *partyVideo) start() {
	go v.pump()
	go v.reports()
	if v.pv != nil && v.pv.sock != nil {
		p := v.p
		p.vwg.Add(2)
		v.pv.sock.run(&p.vwg, func(*net.UDPAddr) bool { return true }, func(data []byte) {
			if pkt, ok := ParseRTP(data); ok && v.vid.carries(pkt.PayloadType) {
				p.onVideoRTP(pkt, data)
			}
		}, p.onVideoRTCP)
	}
}

func (v *partyVideo) close() {
	select {
	case <-v.done:
	default:
		close(v.done)
	}
	if v.pv != nil {
		v.pv.close()
	}
}

// pump writes the queue to the participant.
func (v *partyVideo) pump() {
	for {
		select {
		case <-v.done:
			return
		case m := <-v.vq:
			v.send(m)
		}
	}
}

func (v *partyVideo) send(m relayMsg) {
	p := v.p
	if v.pv != nil && v.pv.sock != nil {
		if m.rtcp {
			v.pv.sock.writeRTCP(m.data)
		} else if out, ok := v.vid.rewriteOut(m.data, time.Now()); ok {
			v.pv.sock.writeRTP(out)
		}
		return
	}
	dst := p.peer()
	if dst == nil {
		return
	}
	if m.rtcp {
		if enc, ok := p.sec.ProtectRTCP(m.data); ok {
			_, _ = p.conn.WriteToUDP(enc, dst)
		}
		return
	}
	if out, ok := v.vid.rewriteOut(m.data, time.Now()); ok {
		if enc, ok := p.sec.Protect(out); ok {
			_, _ = p.conn.WriteToUDP(enc, dst)
		}
	}
}

func (v *partyVideo) enqueue(m relayMsg) { enqueueDropOldest(v.vq, m, &v.dropped) }

// ---- the room's side --------------------------------------------------------------

// SetVideoCap sets the ceiling, in kbit/s, of the video the room forwards (0 for
// none).
func (r *Room) SetVideoCap(kbps int) { r.vcap.Store(int32(max(kbps, 0))) } //nolint:gosec // a configured ceiling

// VideoCodec is the codec the room's video uses, "" while no participant has
// video.
func (r *Room) VideoCodec() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vcodec
}

// SetVideoCodec fixes the room's codec when its first video participant joins
// and frees it when the last leaves (Remove does that).
func (r *Room) SetVideoCodec(c string) {
	r.mu.Lock()
	if r.vcodec == "" {
		r.vcodec = c
	}
	r.mu.Unlock()
}

// Speaker is the id of the participant whose picture the room forwards, "" when
// nobody's.
func (r *Room) Speaker() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.speaker
}

// VideoParticipants is how many participants carry video.
func (r *Room) VideoParticipants() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rp := range r.parties {
		if rp.p.vid != nil {
			n++
		}
	}
	return n
}

// bind ties a participant to the room it joined (Add).
func (p *Party) bind(r *Room, id string) {
	p.roomMu.Lock()
	p.room, p.roomID = r, id
	p.roomMu.Unlock()
}

func (p *Party) boundRoom() *Room {
	p.roomMu.Lock()
	defer p.roomMu.Unlock()
	return p.room
}

// videoSource returns the party whose stream rp is sent: the speaker, or the one
// before when rp is the speaker. r.mu is held.
func (r *Room) videoSourceLocked(rp *roomParty) *Party {
	if rp.p.vid == nil || rp.isolated {
		return nil
	}
	want := r.speaker
	if want == rp.id {
		want = r.prevSpeaker
	}
	if want == "" || want == rp.id {
		return nil
	}
	src := r.parties[want]
	if src == nil || src.p.vid == nil || src.isolated {
		return nil
	}
	return src.p
}

// pickSpeaker runs every audio tick with the parties' frames: it keeps each
// participant's level and moves the picture to a new speaker who has been
// clearly louder for long enough. r.mu is held.
func (r *Room) pickSpeakerLocked(in map[string][]int16, now time.Time) {
	var best *roomParty
	for id, rp := range r.parties {
		if rp.p.vid == nil || rp.isolated || rp.muted {
			rp.level = 0
			continue
		}
		e := energy(in[id])
		rp.level += (e - rp.level) * 0.1
		if best == nil || rp.level > best.level {
			best = rp
		}
	}
	if best == nil {
		if r.speaker != "" {
			r.setSpeakerLocked("", now)
		}
		return
	}
	cur := r.parties[r.speaker]
	switch {
	case cur == nil || cur.p.vid == nil || cur.isolated:
		// Nobody has the picture: the first participant with video does until
		// someone speaks.
		first := r.firstVideoLocked()
		if best.level >= speakerGate {
			first = best
		}
		if first != nil {
			r.setSpeakerLocked(first.id, now)
		}
	case best != cur && best.level >= speakerGate && best.level > cur.level*speakerLead && now.Sub(r.lastSwitch) >= speakerDwell:
		r.setSpeakerLocked(best.id, now)
	}
}

func (r *Room) firstVideoLocked() *roomParty {
	for _, id := range r.order {
		if rp := r.parties[id]; rp != nil && rp.p.vid != nil && !rp.isolated {
			return rp
		}
	}
	return nil
}

// setSpeakerLocked moves the picture. The new speaker and the one before are
// each a source for someone new: their pictures are held back until a keyframe,
// which they are asked for.
func (r *Room) setSpeakerLocked(id string, now time.Time) {
	if id == r.speaker {
		return
	}
	r.prevSpeaker, r.speaker, r.lastSwitch = r.speaker, id, now
	r.switches.Add(1)
}

// Switches is how many times the picture moved to another speaker.
func (r *Room) Switches() uint64 { return r.switches.Load() }

// admit decides whether a packet of src may go to this viewer. A viewer whose
// source just changed (it joined, or the speaker moved) waits for a keyframe of
// the new source, which the caller asks for; after keyGateTimeout the pictures
// pass anyway, for a sender that does not answer a PLI.
func (v *partyVideo) admit(src *Party, payload []byte, now time.Time) (pass, askKey bool) {
	v.gateMu.Lock()
	defer v.gateMu.Unlock()
	if v.src != src {
		v.src, v.waiting, v.waitAt, v.askedAt = src, true, now, time.Time{}
	}
	if !v.waiting {
		return true, false
	}
	if isKeyStart(src.vid.codec, payload) || now.Sub(v.waitAt) > keyGateTimeout {
		v.waiting = false
		return true, false
	}
	// Still waiting: ask again every so often, since the sender may have been
	// asked just before the viewer began to wait.
	if now.Sub(v.askedAt) >= viewerAskEvery {
		v.askedAt = now
		askKey = true
	}
	return false, askKey
}

// isKeyStart reports whether an RTP payload begins a keyframe: for H.264 an SPS
// or an IDR slice (alone, in a STAP-A or as the start of a FU-A), for VP8 the
// first packet of a frame whose P bit is 0.
func isKeyStart(codec string, p []byte) bool {
	if len(p) == 0 {
		return false
	}
	switch codec {
	case "H264":
		switch t := p[0] & 0x1f; {
		case t == 5 || t == 7:
			return true
		case t == 28 && len(p) > 1:
			return p[1]&0x80 != 0 && p[1]&0x1f == 5
		case t == 24:
			rest := p[1:]
			for len(rest) >= 3 {
				n := int(rest[0])<<8 | int(rest[1])
				if t := rest[2] & 0x1f; t == 5 || t == 7 {
					return true
				}
				if n+2 > len(rest) {
					break
				}
				rest = rest[n+2:]
			}
		}
	case "VP8":
		x, start := p[0]&0x80 != 0, p[0]&0x10 != 0
		if !start || p[0]&0x07 != 0 {
			return false
		}
		i := 1
		if x && len(p) > 1 {
			ext := p[1]
			i = 2
			if ext&0x80 != 0 && i < len(p) {
				if p[i]&0x80 != 0 {
					i++
				}
				i++
			}
			if ext&0x40 != 0 {
				i++
			}
			if ext&0x30 != 0 {
				i++
			}
		}
		return i < len(p) && p[i]&1 == 0
	}
	return false
}

// forwardVideo takes one video packet a participant sent and passes it to the
// participants that are shown it. Who is shown whom, the keyframe wait and the
// queueing happen under the room's lock together, so a switch of speaker never
// lets a late packet of the old speaker land among the new one's.
func (r *Room) forwardVideo(from *Party, pkt *RTPPacket, data []byte) {
	now := time.Now()
	if from.vid.vid.noteIn(pkt.Sequence, pkt.Timestamp, pkt.SSRC, len(data), now) {
		go from.requestKeyframe()
	}
	capKbps := r.vcap.Load()
	asked := false
	r.mu.Lock()
	var mine *roomParty
	for _, rp := range r.parties {
		if rp.p == from {
			mine = rp
		}
	}
	if mine == nil || mine.isolated {
		r.mu.Unlock()
		return
	}
	var cp []byte
	for _, rp := range r.parties {
		if rp.p == from || r.videoSourceLocked(rp) != from {
			continue
		}
		pass, askKey := rp.p.vid.admit(from, pkt.Payload, now)
		if askKey && !asked {
			asked = true
			go from.requestKeyframeGap(viewerAskEvery)
		}
		if !pass {
			continue
		}
		if !rp.p.vid.vid.allow(len(data), capKbps, now) {
			noteVideoDrop("cap")
			continue
		}
		if cp == nil {
			cp = append([]byte(nil), data...)
		}
		rp.p.vid.enqueue(relayMsg{data: cp})
	}
	r.mu.Unlock()
}

// sourceOf is the party p is shown (nil for none).
func (r *Room) sourceOf(p *Party) *Party {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rp := range r.parties {
		if rp.p == p {
			return r.videoSourceLocked(rp)
		}
	}
	return nil
}

// viewersOf are the participants shown p's stream.
func (r *Room) viewersOf(p *Party) []*Party {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Party
	for _, rp := range r.parties {
		if rp.p != p && r.videoSourceLocked(rp) == p {
			out = append(out, rp.p)
		}
	}
	return out
}

// ---- the participant's side ---------------------------------------------------------

// onVideoRTP is a video packet from the participant.
func (p *Party) onVideoRTP(pkt *RTPPacket, data []byte) {
	if r := p.boundRoom(); r != nil && p.vid != nil {
		r.forwardVideo(p, pkt, data)
	}
}

// onVideoRTCP is an RTCP compound packet from the participant.
func (p *Party) onVideoRTCP(data []byte) {
	if p.vid == nil {
		return
	}
	pkts, err := rtcp.Unmarshal(data)
	if err != nil {
		return
	}
	r := p.boundRoom()
	if r == nil {
		return
	}
	for _, x := range pkts {
		switch x := x.(type) {
		case *rtcp.SenderReport:
			v := &p.vid.vid
			v.mu.Lock()
			if v.haveIn && x.SSRC == v.inSSRC {
				v.lastSRMid = uint32(x.NTPTime >> 16) //nolint:gosec // the middle 32 bits are the point
				v.lastSRAt = time.Now()
			}
			v.mu.Unlock()
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			if src := r.sourceOf(p); src != nil && src.vid != nil {
				src.requestKeyframe()
			}
		case *rtcp.TransportLayerNack:
			if src := r.sourceOf(p); src != nil && src.vid != nil {
				r.forwardNACKTo(p, src, x)
			}
		case *rtcp.ReceiverEstimatedMaximumBitrate:
			p.vid.vid.mu.Lock()
			p.vid.vid.remoteEst = float64(x.Bitrate)
			p.vid.vid.mu.Unlock()
		}
	}
}

// forwardNACKTo maps a NACK from viewer (about the stream it is sent) to the
// sequence numbers of the source's own stream, by the constant offset.
func (r *Room) forwardNACKTo(viewer, src *Party, n *rtcp.TransportLayerNack) {
	if !src.vid.vid.cfgFB().NACK {
		return
	}
	viewer.vid.vid.mu.Lock()
	off, mapped, from := viewer.vid.vid.seqOff, viewer.vid.vid.mapped, viewer.vid.vid.mapSSRC
	viewer.vid.vid.mu.Unlock()
	src.vid.vid.mu.Lock()
	inSSRC, have := src.vid.vid.inSSRC, src.vid.vid.haveIn
	self := src.vid.vid.outSSRC
	src.vid.vid.mu.Unlock()
	if !mapped || !have || from != inSSRC {
		return // the viewer's stream is another source's now
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
	out := &rtcp.TransportLayerNack{SenderSSRC: self, MediaSSRC: inSSRC, Nacks: nackPairs(seqs)}
	if raw, err := rtcp.Marshal([]rtcp.Packet{out}); err == nil {
		src.vid.enqueue(relayMsg{data: raw, rtcp: true})
	}
}

func (v *videoLeg) cfgFB() VideoFeedback {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cfg.FB
}

// requestKeyframe asks the participant's sender for a picture (paced).
func (p *Party) requestKeyframe() { p.requestKeyframeGap(keyframeSpacing) }

// requestKeyframeGap is requestKeyframe with the least time since the last one.
func (p *Party) requestKeyframeGap(gap time.Duration) {
	if p.vid == nil {
		return
	}
	v := &p.vid.vid
	now := time.Now()
	v.mu.Lock()
	if !v.haveIn || !v.cfg.FB.PLI && !v.cfg.FB.FIR || now.Sub(v.lastKeyReq) < gap {
		v.mu.Unlock()
		return
	}
	v.lastKeyReq = now
	media, self, pli := v.inSSRC, v.outSSRC, v.cfg.FB.PLI
	v.firSeq++
	seq := v.firSeq
	v.mu.Unlock()
	var x rtcp.Packet
	if pli {
		x = &rtcp.PictureLossIndication{SenderSSRC: self, MediaSSRC: media}
	} else {
		x = &rtcp.FullIntraRequest{SenderSSRC: self, MediaSSRC: media, FIR: []rtcp.FIREntry{{SSRC: media, SequenceNumber: seq}}}
	}
	if raw, err := rtcp.Marshal([]rtcp.Packet{x}); err == nil {
		p.vid.enqueue(relayMsg{data: raw, rtcp: true})
		noteVideoKeyframeRequest()
	}
}

// reports sends the participant its sender and reception reports and the most
// its viewers can take (REMB) once a second.
func (v *partyVideo) reports() {
	t := time.NewTicker(videoRTCPInterval)
	defer t.Stop()
	for {
		select {
		case <-v.done:
			return
		case <-t.C:
			v.sendReports()
		}
	}
}

func (v *partyVideo) sendReports() {
	now := time.Now()
	l := &v.vid
	var reports []rtcp.ReceptionReport
	if rr := l.report(now); rr != nil {
		reports = append(reports, *rr)
	}
	l.mu.Lock()
	self, packets, octets := l.outSSRC, l.outPackets, l.outOctets
	rtpTS := l.lastOutTS + uint32(now.Sub(l.lastOutAt).Seconds()*videoClock) //nolint:gosec // seconds of silence
	l.mu.Unlock()
	var pkts []rtcp.Packet
	if packets > 0 {
		pkts = append(pkts, &rtcp.SenderReport{SSRC: self, NTPTime: ntpTime(now), RTPTime: rtpTS, PacketCount: packets, OctetCount: octets, Reports: reports})
	} else {
		pkts = append(pkts, &rtcp.ReceiverReport{SSRC: self, Reports: reports})
	}
	if raw, err := rtcp.Marshal(pkts); err == nil {
		v.enqueue(relayMsg{data: raw, rtcp: true})
	}
	// What this participant's viewers can take bounds what it should send.
	r := v.p.boundRoom()
	if r == nil || !l.cfgFB().REMB {
		return
	}
	est := float64(0)
	for _, viewer := range r.viewersOf(v.p) {
		if e := viewer.vid.vid.estimate(); e > 0 && (est == 0 || e < est) {
			est = e
		}
	}
	if c := float64(r.vcap.Load()) * 1000; c > 0 && (est == 0 || est > c) {
		est = c
	}
	l.mu.Lock()
	in, ok := l.inSSRC, l.haveIn
	l.mu.Unlock()
	if est > 0 && ok {
		x := &rtcp.ReceiverEstimatedMaximumBitrate{SenderSSRC: self, Bitrate: float32(est), SSRCs: []uint32{in}}
		if raw, err := rtcp.Marshal([]rtcp.Packet{x}); err == nil {
			v.enqueue(relayMsg{data: raw, rtcp: true})
		}
	}
}
