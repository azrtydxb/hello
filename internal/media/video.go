package media

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Door station video (spec phone-feature-parity S-49). A door station may
// offer an H.264 video stream next to its audio. Hello does not decode it:
// the anchor relays the RTP and RTCP of the video port pairs unchanged
// between the door and the phone that answered, and the SDP carries a video
// m-line on each side with Hello's own ports.

// VideoCodec is one H.264 payload type of a video m-line.
type VideoCodec struct {
	PT        uint8
	ClockRate int
	Fmtp      string
	// FB are the a=rtcp-fb feedback types of the payload type (nack, pli,
	// ccm fir, ...), without the payload type.
	FB []string
}

// VideoSDP is the video section of an SDP offer or answer: where to send
// video and the H.264 payload types it lists.
type VideoSDP struct {
	Address string // the connection address of the section
	Port    int    // m=video transport port
	// RTCPPort is the a=rtcp port, 0 when the SDP names none (RTP port + 1).
	RTCPPort int
	Proto    string // RTP/AVP or RTP/AVPF
	Codecs   []VideoCodec
	// Direction is the section's own direction attribute, "sendrecv" when
	// it has none.
	Direction string
}

// RTCPAddr is where the section's RTCP goes.
func (v VideoSDP) RTCPAddr() *net.UDPAddr {
	ip := net.ParseIP(v.Address)
	if ip == nil {
		return nil
	}
	port := v.RTCPPort
	if port == 0 {
		port = v.Port + 1
	}
	return &net.UDPAddr{IP: ip, Port: port}
}

// RTPAddr is where the section's RTP goes.
func (v VideoSDP) RTPAddr() *net.UDPAddr {
	ip := net.ParseIP(v.Address)
	if ip == nil {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: v.Port}
}

// ParseVideoSDP extracts the first accepted video section that offers H.264
// over plain RTP: a section with port 0 (refused), over SRTP, or without an
// H.264 payload type reports ok=false. Other payload types of the section
// are dropped, so what Hello offers on is H.264 only. Session-level c= lines
// apply to the section, a media-level one overrides them.
func ParseVideoSDP(body []byte) (VideoSDP, bool) {
	var (
		out         VideoSDP
		sessionConn string
		mediaConn   string
		inVideo     bool
		have        bool
		pts         []uint8
		maps        = map[uint8]int{}
		fmtps       = map[uint8]string{}
		fbs         = map[uint8][]string{}
		allFB       []string
	)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "m="):
			if have {
				inVideo = false
				continue
			}
			f := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(f) < 4 || f[0] != "video" {
				inVideo = false
				continue
			}
			port, err := strconv.Atoi(f[1])
			if err != nil || port <= 0 || port > 65535 || (f[2] != "RTP/AVP" && f[2] != "RTP/AVPF") {
				inVideo = false
				continue
			}
			inVideo, have = true, true
			out.Port, out.Proto = port, f[2]
			for _, p := range f[3:] {
				if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < 128 {
					pts = append(pts, uint8(n))
				}
			}
		case strings.HasPrefix(line, "c="):
			f := strings.Fields(strings.TrimPrefix(line, "c="))
			if len(f) == 3 && f[0] == "IN" && f[1] == "IP4" {
				if inVideo {
					mediaConn = f[2]
				} else if !have {
					sessionConn = f[2]
				}
			}
		case inVideo && strings.HasPrefix(line, "a=rtpmap:"):
			pt, rest, _ := strings.Cut(strings.TrimPrefix(line, "a=rtpmap:"), " ")
			n, err := strconv.Atoi(pt)
			enc := strings.Split(rest, "/")
			if err != nil || len(enc) < 2 || !strings.EqualFold(enc[0], "H264") {
				continue
			}
			rate, err := strconv.Atoi(enc[1])
			if err != nil || rate <= 0 {
				continue
			}
			maps[uint8(n)] = rate //nolint:gosec // n came from a payload type list capped at 127
		case inVideo && strings.HasPrefix(line, "a=fmtp:"):
			pt, rest, _ := strings.Cut(strings.TrimPrefix(line, "a=fmtp:"), " ")
			if n, err := strconv.Atoi(pt); err == nil {
				fmtps[uint8(n)] = strings.TrimSpace(rest) //nolint:gosec // as above
			}
		case inVideo && strings.HasPrefix(line, "a=rtcp-fb:"):
			pt, rest, _ := strings.Cut(strings.TrimPrefix(line, "a=rtcp-fb:"), " ")
			rest = strings.TrimSpace(rest)
			switch n, err := strconv.Atoi(pt); {
			case pt == "*":
				allFB = append(allFB, rest)
			case err == nil:
				fbs[uint8(n)] = append(fbs[uint8(n)], rest) //nolint:gosec // as above
			}
		case inVideo && strings.HasPrefix(line, "a=rtcp:"):
			f := strings.Fields(strings.TrimPrefix(line, "a=rtcp:"))
			if len(f) > 0 {
				if p, err := strconv.Atoi(f[0]); err == nil && p > 0 && p <= 65535 {
					out.RTCPPort = p
				}
			}
		case inVideo && (line == "a=sendonly" || line == "a=recvonly" || line == "a=inactive" || line == "a=sendrecv"):
			out.Direction = strings.TrimPrefix(line, "a=")
		}
	}
	if !have {
		return VideoSDP{}, false
	}
	for _, pt := range pts {
		rate, ok := maps[pt]
		if !ok {
			continue
		}
		out.Codecs = append(out.Codecs, VideoCodec{PT: pt, ClockRate: rate, Fmtp: fmtps[pt], FB: append(append([]string(nil), allFB...), fbs[pt]...)})
	}
	out.Address = mediaConn
	if out.Address == "" {
		out.Address = sessionConn
	}
	if len(out.Codecs) == 0 || net.ParseIP(out.Address) == nil {
		return VideoSDP{}, false
	}
	if out.Direction == "" {
		out.Direction = "sendrecv"
	}
	return out, true
}

// Section writes the m=video section for Hello's address: the H.264 payload
// types with their rtpmap, fmtp and feedback lines, an explicit a=rtcp for the
// port next to the RTP port, and the direction dir (the section's own when
// empty). Lines use CRLF; append it to an audio SDP.
func (v VideoSDP) Section(ip string, port int, dir string) string {
	if dir == "" {
		dir = v.Direction
	}
	switch dir {
	case "sendonly", "recvonly", "inactive":
	default:
		dir = "sendrecv"
	}
	proto := v.Proto
	if proto == "" {
		proto = "RTP/AVP"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "m=video %d %s", port, proto)
	for _, c := range v.Codecs {
		fmt.Fprintf(&b, " %d", c.PT)
	}
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", ip)
	for _, c := range v.Codecs {
		fmt.Fprintf(&b, "a=rtpmap:%d H264/%d\r\n", c.PT, c.ClockRate)
		if c.Fmtp != "" {
			fmt.Fprintf(&b, "a=fmtp:%d %s\r\n", c.PT, c.Fmtp)
		}
		for _, fb := range c.FB {
			fmt.Fprintf(&b, "a=rtcp-fb:%d %s\r\n", c.PT, fb)
		}
	}
	fmt.Fprintf(&b, "a=rtcp:%d IN IP4 %s\r\n", port+1, ip)
	fmt.Fprintf(&b, "a=%s\r\n", dir)
	return b.String()
}

// RejectedVideoSection answers a video m-line Hello does not carry: the port
// is 0, as RFC 3264 asks for every refused stream.
func RejectedVideoSection(offered VideoSDP) string {
	pt := uint8(96)
	if len(offered.Codecs) > 0 {
		pt = offered.Codecs[0].PT
	}
	return fmt.Sprintf("m=video 0 RTP/AVP %d\r\n", pt)
}

// VideoRelay relays the RTP and the RTCP of one video stream between two
// legs, unchanged: an RTP Relay on legs "a" and "b" plus a bridge between the
// RTCP sockets one port above each leg's RTP port. RTCP carries the picture
// loss and keyframe requests a phone sends back to the door.
type VideoRelay struct {
	*Relay
	rtcp [2]*rtcpSide
	wg   sync.WaitGroup
}

type rtcpSide struct {
	conn   *net.UDPConn
	mu     sync.Mutex
	remote *net.UDPAddr
}

// NewVideoRelay binds two video legs, each an even RTP port with the next port
// free for RTCP.
func NewVideoRelay(minPort, maxPort int) (*VideoRelay, error) {
	r, err := NewRelay(minPort, maxPort)
	if err != nil {
		return nil, err
	}
	v := &VideoRelay{Relay: r}
	for i, name := range []string{"a", "b"} {
		if err := v.addLeg(i, name, minPort, maxPort); err != nil {
			v.Close()
			return nil, err
		}
	}
	return v, nil
}

func (v *VideoRelay) addLeg(i int, name string, minPort, maxPort int) error {
	start := minPort + minPort%2
	for p := start; p+1 <= maxPort; p += 2 {
		v.mu.Lock()
		taken := v.used[p] || v.used[p+1]
		v.mu.Unlock()
		if taken {
			continue
		}
		rc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p + 1})
		if err != nil {
			continue
		}
		if _, err := v.addLegAt(name, p); err != nil {
			_ = rc.Close()
			continue
		}
		v.mu.Lock()
		v.used[p+1] = true
		v.mu.Unlock()
		v.rtcp[i] = &rtcpSide{conn: rc}
		return nil
	}
	return fmt.Errorf("media: no free video port pair for leg %q", name)
}

// SetRTCPTarget aims leg "a" or "b" RTCP at addr until the phone's own RTCP
// latches the real source.
func (v *VideoRelay) SetRTCPTarget(name string, addr *net.UDPAddr) {
	if s := v.side(name); s != nil {
		s.mu.Lock()
		s.remote = addr
		s.mu.Unlock()
	}
}

func (v *VideoRelay) side(name string) *rtcpSide {
	switch name {
	case "a":
		return v.rtcp[0]
	case "b":
		return v.rtcp[1]
	}
	return nil
}

// Start starts the RTP relay and the RTCP bridge.
func (v *VideoRelay) Start() {
	v.Relay.Start()
	for i, name := range []string{"a", "b"} {
		v.wg.Go(func() { v.bridge(name, v.rtcp[i], v.rtcp[1-i]) })
	}
}

// bridge forwards RTCP read on one side to the other. A datagram is accepted
// only from a source the matching RTP leg accepts.
func (v *VideoRelay) bridge(name string, from, to *rtcpSide) {
	leg := v.findLeg(name)
	buf := make([]byte, 1500)
	for {
		_ = from.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, src, err := from.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-v.ctx.Done():
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() { //nolint:errorlint // a read deadline, not a wrapped error
				continue
			}
			return
		}
		if leg == nil || !leg.sourceOK(src) {
			continue
		}
		from.mu.Lock()
		from.remote = &net.UDPAddr{IP: append(net.IP(nil), src.IP...), Port: src.Port}
		from.mu.Unlock()
		to.mu.Lock()
		dst := to.remote
		to.mu.Unlock()
		if dst != nil {
			_, _ = to.conn.WriteToUDP(buf[:n], dst)
		}
	}
}

// RTCPPort is the RTCP port of leg "a" or "b".
func (v *VideoRelay) RTCPPort(name string) int {
	if s := v.side(name); s != nil {
		return s.conn.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // always a UDPAddr
	}
	return 0
}

// Close stops the relay and the bridge.
func (v *VideoRelay) Close() {
	v.Relay.Close()
	for _, s := range v.rtcp {
		if s != nil {
			_ = s.conn.Close()
		}
	}
	v.wg.Wait()
}
