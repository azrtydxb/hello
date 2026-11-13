// Plain RTP video next to a leg's audio (clients-webrtc-mobile-video S-44):
// a desk phone or a door station that offers H.264 on a video m-line of its
// own, RTP/AVP, with RTP and RTCP on a port pair apart from the audio's. The
// leg gets a socket pair for it, and what arrives there goes through the same
// translator as a browser's video (relayvideo.go): constant sequence and
// timestamp offsets, one SSRC per leg, a payload type per leg, RTCP ended and
// regenerated per leg, PLI, FIR, NACK and REMB carried across, the rate
// ceiling. So a phone, a door station and a browser in any pairing are one
// more VideoLeg configuration, never a special case.
package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// videoSock is a plain video transport: an RTP socket on an even port and an
// RTCP socket on the next one (the a=rtcp line says so; rtcp-mux is never
// answered).
type videoSock struct {
	rtp, rtcp *net.UDPConn
	port      int

	mu            sync.Mutex
	rtpTo, rtcpTo *net.UDPAddr
	closed        bool
}

// bindVideoSock binds an RTP and an RTCP port from [minPort, maxPort]; with no
// range (tests) the kernel's high ports are used. taken reports ports the
// caller already holds.
func bindVideoSock(minPort, maxPort int, taken func(port int) bool) (*videoSock, error) {
	try := func(p int) *videoSock {
		if p%2 != 0 || p+1 > 65535 || (taken != nil && (taken(p) || taken(p+1))) {
			return nil
		}
		rc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p})
		if err != nil {
			return nil
		}
		cc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p + 1})
		if err != nil {
			_ = rc.Close()
			return nil
		}
		markRTP(rc)
		markRTP(cc)
		return &videoSock{rtp: rc, rtcp: cc, port: p}
	}
	if minPort <= 0 || maxPort < minPort {
		var b [2]byte
		for range 64 {
			if _, err := rand.Read(b[:]); err != nil {
				break
			}
			p := 49152 + int(binary.BigEndian.Uint16(b[:]))%(65534-49152)
			if s := try(p &^ 1); s != nil {
				return s, nil
			}
		}
		return nil, errors.New("media: no free video port pair")
	}
	for p := minPort + minPort%2; p+1 <= maxPort; p += 2 {
		if s := try(p); s != nil {
			return s, nil
		}
	}
	return nil, fmt.Errorf("media: no free video port pair in %d-%d", minPort, maxPort)
}

// setTarget aims the sockets at the addresses of a remote SDP until the
// phone's own packets latch the real sources. A nil RTCP address is the RTP
// port plus one.
func (v *videoSock) setTarget(rtp, rtcp *net.UDPAddr) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rtpTo = rtp
	if rtcp == nil && rtp != nil {
		rtcp = &net.UDPAddr{IP: rtp.IP, Port: rtp.Port + 1}
	}
	v.rtcpTo = rtcp
}

func (v *videoSock) writeRTP(b []byte) {
	v.mu.Lock()
	to := v.rtpTo
	v.mu.Unlock()
	if to != nil {
		_, _ = v.rtp.WriteToUDP(b, to)
	}
}

func (v *videoSock) writeRTCP(b []byte) {
	v.mu.Lock()
	to := v.rtcpTo
	v.mu.Unlock()
	if to != nil {
		_, _ = v.rtcp.WriteToUDP(b, to)
	}
}

func latchAddr(dst **net.UDPAddr, from *net.UDPAddr) {
	if *dst != nil && (*dst).IP.Equal(from.IP) && (*dst).Port == from.Port {
		return
	}
	*dst = &net.UDPAddr{IP: append(net.IP(nil), from.IP...), Port: from.Port, Zone: from.Zone}
}

// run reads both sockets until they close (the caller has added two to wg). A datagram is handed on only from a
// source ok accepts, and latches where the replies go (symmetric RTP).
func (v *videoSock) run(wg *sync.WaitGroup, ok func(*net.UDPAddr) bool, onRTP, onRTCP func(data []byte)) {
	read := func(c *net.UDPConn, rtcp bool, h func([]byte)) {
		defer wg.Done()
		defer func() { _ = recover() }() // a bad packet never takes the call down
		buf := make([]byte, rtpMaxPacket)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if ok != nil && !ok(from) {
				continue
			}
			v.mu.Lock()
			if rtcp {
				latchAddr(&v.rtcpTo, from)
			} else {
				latchAddr(&v.rtpTo, from)
			}
			v.mu.Unlock()
			h(buf[:n])
		}
	}
	go read(v.rtp, false, onRTP)
	go read(v.rtcp, true, onRTCP)
}

func (v *videoSock) close() {
	v.mu.Lock()
	already := v.closed
	v.closed = true
	v.mu.Unlock()
	if already {
		return
	}
	_ = v.rtp.Close()
	_ = v.rtcp.Close()
}

// ---- a relay leg's plain video ----------------------------------------------

// plainVideo is a plain leg's video: the sockets once bound and what the leg's
// SDPs say about video.
type plainVideo struct {
	mu   sync.Mutex
	sock *videoSock
	// offered: the remote's offer had a video section, so every answer has one
	// (port 0 when the video is not carried). firstPT is that section's first
	// format, kept for the rejected line.
	offered bool
	firstPT string
	// vOffer is what Hello offers (offerer legs), vSel the negotiated section;
	// with neither an answer refuses and an offer has no video.
	vOffer    []SDPCodec
	vSel      *SDPCodec
	vDir      string
	kbps      int
	sentVideo bool
	running   bool
}

// pvState returns the leg's plain video state, making it when asked.
func (l *relayLeg) pvState(create bool) *plainVideo {
	l.pvMu.Lock()
	defer l.pvMu.Unlock()
	if l.pv == nil && create {
		l.pv = &plainVideo{}
	}
	return l.pv
}

// NotePlainVideoOffer records whether the remote's latest offer on leg had a
// video section (and its first format), so every SDP Hello writes for the leg
// has a video section too, refused unless the video is carried. An offer
// without one clears it: the m= lines of the dialog follow the offer's.
func (r *Relay) NotePlainVideoOffer(leg string, has bool, firstPT string) {
	l := r.findLeg(leg)
	if l == nil || l.web != nil {
		return
	}
	pv := l.pvState(has)
	if pv == nil {
		return
	}
	pv.mu.Lock()
	pv.offered, pv.firstPT = has, firstPT
	if !has {
		pv.sentVideo = false
		pv.vSel = nil
	}
	pv.mu.Unlock()
}

// PlainVideoSection is the video section of leg's SDP for connection address
// ip ("" when the leg has none or is a browser's, whose SDP the relay writes
// whole).
func (r *Relay) PlainVideoSection(leg, ip string) string {
	l := r.findLeg(leg)
	if l == nil || l.web != nil {
		return ""
	}
	if pv := l.pvState(false); pv != nil {
		return pv.section(ip)
	}
	return ""
}

// BindPlainVideo gives a plain leg its video socket pair and returns the RTP
// port for the SDP. It is idempotent and starts the receive loops when the
// relay already runs.
func (r *Relay) BindPlainVideo(leg string) (int, error) {
	l := r.findLeg(leg)
	if l == nil || l.web != nil {
		return 0, errors.New("media: " + leg + " is not a plain leg")
	}
	pv := l.pvState(true)
	pv.mu.Lock()
	if pv.sock != nil {
		p := pv.sock.port
		pv.mu.Unlock()
		return p, nil
	}
	r.mu.Lock()
	closed, started := r.closed, r.started
	r.mu.Unlock()
	if closed {
		pv.mu.Unlock()
		return 0, errors.New("media: relay closed")
	}
	sock, err := bindVideoSock(r.minPort, r.maxPort, func(p int) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.used[p]
	})
	if err != nil {
		pv.mu.Unlock()
		return 0, err
	}
	r.mu.Lock()
	r.used[sock.port], r.used[sock.port+1] = true, true
	r.mu.Unlock()
	pv.sock = sock
	pv.mu.Unlock()
	if started {
		r.startPlainVideo(l)
	}
	return sock.port, nil
}

// startPlainVideo launches the receive loops of a bound plain video leg once.
func (r *Relay) startPlainVideo(l *relayLeg) {
	pv := l.pvState(false)
	if pv == nil {
		return
	}
	pv.mu.Lock()
	sock := pv.sock
	if sock == nil || pv.running {
		pv.mu.Unlock()
		return
	}
	pv.running = true
	pv.mu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.wg.Add(2)
	r.mu.Unlock()
	sock.run(&r.wg, l.sourceOK, func(data []byte) {
		pkt, ok := ParseRTP(data)
		if !ok || !l.vid.carries(pkt.PayloadType) {
			return
		}
		r.forwardVideo(l, pkt, data)
	}, func(data []byte) { r.handleRTCP(l, data) })
}

// SetPlainVideoTarget aims leg's video at the addresses of its remote SDP.
func (r *Relay) SetPlainVideoTarget(leg string, rtp, rtcp *net.UDPAddr) {
	if l := r.findLeg(leg); l != nil {
		if pv := l.pvState(false); pv != nil {
			pv.mu.Lock()
			s := pv.sock
			pv.mu.Unlock()
			if s != nil {
				s.setTarget(rtp, rtcp)
			}
		}
	}
}

// HasPlainVideo reports whether leg has a video socket pair.
func (r *Relay) HasPlainVideo(leg string) bool {
	l := r.findLeg(leg)
	if l == nil {
		return false
	}
	pv := l.pvState(false)
	if pv == nil {
		return false
	}
	pv.mu.Lock()
	defer pv.mu.Unlock()
	return pv.sock != nil
}

// VideoLegOK reports whether leg can carry video: a browser's leg, or a plain
// leg with video sockets.
func (r *Relay) VideoLegOK(leg string) bool { return r.IsWebLeg(leg) || r.HasPlainVideo(leg) }

// LegEncrypted reports whether leg's audio is SRTP (SDES or DTLS): plain video
// is never carried beside it.
func (r *Relay) LegEncrypted(leg string) bool {
	l := r.findLeg(leg)
	if l == nil {
		return false
	}
	_, on := l.secure.Profile()
	return on
}

// plainSetOffer is SetVideoOffer for a plain leg.
func (pv *plainVideo) setOffer(codecs []SDPCodec, dir string, kbps int) {
	pv.mu.Lock()
	pv.vOffer = append([]SDPCodec(nil), codecs...)
	pv.vDir, pv.kbps = dir, kbps
	pv.mu.Unlock()
}

// setSection fixes the negotiated section of a plain leg (nil: none).
func (pv *plainVideo) setSection(c *SDPCodec, dir string, kbps int) {
	pv.mu.Lock()
	pv.vOffer = nil
	if c == nil {
		pv.vSel = nil
	} else {
		cc := *c
		pv.vSel = &cc
	}
	pv.vDir, pv.kbps = dir, kbps
	pv.mu.Unlock()
}

// section writes the leg's video section for the connection address ip, "" when
// the leg says nothing about video.
func (pv *plainVideo) section(ip string) string {
	pv.mu.Lock()
	defer pv.mu.Unlock()
	port := 0
	if pv.sock != nil {
		port = pv.sock.port
	}
	var codecs []SDPCodec
	switch {
	case port != 0 && pv.vSel != nil:
		codecs = []SDPCodec{*pv.vSel}
	case port != 0 && len(pv.vOffer) > 0:
		codecs = pv.vOffer
	}
	if len(codecs) == 0 {
		if !pv.offered && !pv.sentVideo {
			return ""
		}
		first := pv.firstPT
		if first == "" {
			first = "96"
		}
		pv.sentVideo = true
		return "m=video 0 RTP/AVP " + first + "\r\n"
	}
	pv.sentVideo = true
	var b strings.Builder
	fmt.Fprintf(&b, "m=video %d RTP/AVP", port)
	for _, c := range codecs {
		fmt.Fprintf(&b, " %d", c.PT)
	}
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", ip)
	if pv.kbps > 0 {
		fmt.Fprintf(&b, "b=AS:%d\r\n", pv.kbps)
	}
	for _, c := range codecs {
		fmt.Fprintf(&b, "a=rtpmap:%d %s/%d\r\n", c.PT, strings.ToUpper(c.Name), orRate(c.Rate))
		if c.Fmtp != "" {
			fmt.Fprintf(&b, "a=fmtp:%d %s\r\n", c.PT, c.Fmtp)
		}
		for _, fb := range c.Feedback {
			fmt.Fprintf(&b, "a=rtcp-fb:%d %s\r\n", c.PT, fb)
		}
	}
	fmt.Fprintf(&b, "a=rtcp:%d IN IP4 %s\r\n", port+1, ip)
	fmt.Fprintf(&b, "a=%s\r\n", orDir(pv.vDir))
	return b.String()
}

func orRate(r int) int {
	if r <= 0 {
		return videoClock
	}
	return r
}

// close releases the sockets.
func (pv *plainVideo) close() {
	pv.mu.Lock()
	s := pv.sock
	pv.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// PlainVideoOffer reads a plain RTP video section out of an SDP of a phone or a
// door station: its H.264 payload types, where its video RTP and RTCP go and
// the direction. ok is false for no video, a refused section, SRTP video or no
// H.264. The result is shaped like a browser's offer so the negotiation treats
// both alike.
func PlainVideoOffer(body []byte) (VideoOffer, *net.UDPAddr, *net.UDPAddr, bool) {
	v, ok := ParseVideoSDP(body)
	if !ok {
		return VideoOffer{}, nil, nil, false
	}
	out := VideoOffer{Dir: v.Direction}
	for _, c := range v.Codecs {
		out.Codecs = append(out.Codecs, SDPCodec{PT: c.PT, Name: "H264", Rate: c.ClockRate, Fmtp: c.Fmtp, Feedback: lowerAll(c.FB)})
	}
	return out, v.RTPAddr(), v.RTCPAddr(), true
}

// PlainVideoFirstPT is the first format of the first video section of an SDP
// whether it is accepted or not (the rejected line repeats it).
func PlainVideoFirstPT(body []byte) (string, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=video ") {
			f := strings.Fields(line)
			if len(f) >= 4 {
				return f[3], true
			}
			return "", true
		}
	}
	return "", false
}

func lowerAll(in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, strings.ToLower(s))
	}
	return out
}
