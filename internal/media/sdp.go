package media

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AudioSDP is the slice of an SDP offer or answer the media package acts on:
// where to send audio, which codecs were listed, and how the remote receives
// DTMF.
type AudioSDP struct {
	Address string // IP from the c= line (IN IP4 only)
	Port    int    // m=audio transport port

	// Codecs are the audio codecs of the m= line in the sender's order,
	// each with its rtpmap and fmtp (static payload types without an
	// rtpmap are filled in from RFC 3551). telephone-event is not one of
	// them. A codec Hello does not know (G.729, ...) is kept when the SDP
	// named it with an rtpmap, so a pass-through answer can echo it.
	Codecs []Codec
	// Events are the offered telephone-event codecs in line order.
	Events []Codec

	// PayloadType and Codec are the first supported codec (G.722, Opus,
	// PCMU or PCMA) in Codecs; zero values when there is none. After
	// Pick, Terminated or Narrow they are the single negotiated codec.
	PayloadType uint8
	Codec       Codec
	// DTMFPayloadType is the payload type of the telephone-event codec
	// that goes with Codec (the one whose clock rate matches the codec's
	// RTP clock, else the first offered), 0 when the SDP has none.
	// DTMFRate is its clock rate, 8000 when the SDP omits it.
	DTMFPayloadType uint8
	DTMFRate        int
	// Ptime is the a=ptime packetisation time in milliseconds, 0 when the
	// SDP does not say.
	Ptime int

	// SRTPOnly is true when the m= line is RTP/SAVP: the sender speaks SRTP
	// only. Crypto holds the sender's usable a=crypto lines (RFC 4568), in
	// order; CryptoSkipped counts lines it sent that Hello cannot honour
	// (unknown suite, MKI, unsupported session parameters). An RTP/AVP
	// offer with crypto lines offers SRTP optionally. Neither is ever
	// written by Build: Build writes only what WithCrypto set.
	SRTPOnly      bool
	Crypto        []Crypto
	CryptoSkipped int

	// Proto is the m= transport as the sender wrote it: RTP/AVP, RTP/SAVP,
	// UDP/TLS/RTP/SAVP or UDP/TLS/RTP/SAVPF (S-16). The last two are
	// DTLS-SRTP and count as SRTPOnly.
	Proto string
	// DTLS holds the sender's a=fingerprint and a=setup (RFC 8122, RFC
	// 4145), ICE its credentials and candidates (RFC 8839), RTCPMux its
	// a=rtcp-mux (RFC 5761). Like Crypto, none of them is written by Build:
	// Build writes only what WithDTLS, WithICE and WithRTCPMux set.
	DTLS    *DTLSInfo
	ICE     *ICEInfo
	RTCPMux bool

	// local is what Build writes for crypto: the profile and the a=crypto
	// lines of the Hello side (WithCrypto), or the DTLS and ICE attributes.
	localSRTP   bool
	localCrypto []Crypto
	localDTLS   *DTLSInfo
	localProto  string
	localICE    *ICEInfo
	localMux    bool
}

// DTLSInfo is the DTLS-SRTP part of an SDP: the certificate fingerprints and
// the DTLS role the sender takes.
type DTLSInfo struct {
	Fingerprints []Fingerprint
	Setup        string // active, passive, actpass or "" when absent
}

// protoDTLS reports a DTLS-SRTP transport profile (RFC 5764).
func protoDTLS(proto string) bool {
	return proto == "UDP/TLS/RTP/SAVP" || proto == "UDP/TLS/RTP/SAVPF"
}

// OffersDTLS reports whether the sender offers DTLS-SRTP: a DTLS transport
// profile with at least one usable fingerprint.
func (a AudioSDP) OffersDTLS() bool {
	return protoDTLS(a.Proto) && a.DTLS != nil && len(a.DTLS.Fingerprints) > 0
}

// OffersSDES reports whether the sender offered at least one usable SDES key.
func (a AudioSDP) OffersSDES() bool { return len(a.Crypto) > 0 }

// WithDTLS returns the SDP with Hello's DTLS-SRTP attributes for its Build:
// the m= line is proto (UDP/TLS/RTP/SAVP, or SAVPF when answering an SAVPF
// offer), followed by Hello's a=fingerprint and a=setup. SDES keys are
// dropped: one stream does one key exchange.
func (a AudioSDP) WithDTLS(proto string, local DTLSInfo) AudioSDP {
	a.localSRTP, a.localCrypto = false, nil
	if !protoDTLS(proto) {
		proto = "UDP/TLS/RTP/SAVP"
	}
	a.localProto = proto
	a.localDTLS = &local
	return a
}

// WithICE returns the SDP with Hello's ICE-lite attributes for its Build:
// a=ice-lite, the credentials and the one host candidate (the address and
// port Build is given).
func (a AudioSDP) WithICE(ufrag, pwd string) AudioSDP {
	a.localICE = &ICEInfo{Ufrag: ufrag, Pwd: pwd, Lite: true}
	return a
}

// WithRTCPMux returns the SDP with a=rtcp-mux for its Build: RTP and RTCP
// share the media port (RFC 5761).
func (a AudioSDP) WithRTCPMux() AudioSDP {
	a.localMux = true
	return a
}

// OffersCrypto reports whether the sender offered a key exchange Hello can
// act on: at least one usable SDES key, or DTLS-SRTP.
func (a AudioSDP) OffersCrypto() bool { return len(a.Crypto) > 0 || a.OffersDTLS() }

// WithCrypto returns the SDP with Hello's keys for its Build: the m= line is
// RTP/SAVP when srtpOnly, RTP/AVP (SRTP optional) otherwise, followed by one
// a=crypto line per key. No keys leaves the SDP plain RTP/AVP.
func (a AudioSDP) WithCrypto(srtpOnly bool, keys ...Crypto) AudioSDP {
	a.localSRTP = srtpOnly && len(keys) > 0
	a.localCrypto = append([]Crypto(nil), keys...)
	return a
}

// PlainOffer is the SDP with its SRTP stripped: what a leg that cannot do
// SRTP (a UDP leg) is offered or answered.
func (a AudioSDP) PlainOffer() AudioSDP {
	a.SRTPOnly, a.Crypto, a.CryptoSkipped = false, nil, 0
	a.localSRTP, a.localCrypto = false, nil
	a.Proto, a.DTLS, a.ICE, a.RTCPMux = "", nil, nil, false
	a.localDTLS, a.localProto, a.localICE, a.localMux = nil, "", nil, false
	return a
}

// ParseAudioSDP extracts the connection address, the audio port and the
// codecs from a VoIP SDP offer or answer. A port of 0 means the remote
// refused the audio stream, which is an error here and not an answer to
// build on. Session-level c= lines apply to the media section; a
// media-level c= line overrides them. Only the first audio section is
// considered: the B2BUA offers one audio stream per call.
func ParseAudioSDP(offer []byte) (AudioSDP, error) {
	var (
		out         AudioSDP
		sessionConn string
		mediaConn   string
		pts         []uint8
		inAudio     bool
		haveAudio   bool
		maps        = map[uint8]Codec{}
		fmtps       = map[uint8]string{}
		eventOrder  []uint8
		// Session-level ICE and DTLS attributes apply to the media section
		// unless it repeats them (RFC 8839, RFC 8122).
		sess, med secAttrs
	)
	for _, line := range strings.Split(string(offer), "\n") {
		line = strings.TrimRight(line, "\r")
		// A second m= line starts a new media section; we keep only the
		// first audio one, so stop rather than mix sections.
		if haveAudio && strings.HasPrefix(line, "m=") {
			break
		}
		if len(line) < 2 || line[1] != '=' {
			continue // junk between lines is skipped, never fatal
		}
		switch line[0] {
		case 'm':
			fields := strings.Fields(line[2:])
			// m=<media> <port> <transport> <fmt>...
			if len(fields) < 4 || fields[0] != "audio" || !knownProto(fields[2]) {
				continue
			}
			out.Proto = fields[2]
			out.SRTPOnly = fields[2] != "RTP/AVP"
			port, err := strconv.Atoi(fields[1])
			if err != nil {
				return out, fmt.Errorf("media: SDP m=audio port %q is not a number", fields[1])
			}
			if port < 0 || port > 65535 {
				return out, fmt.Errorf("media: SDP m=audio port %d is out of range", port)
			}
			if port == 0 {
				// Port 0 in an offer means the remote refuses this stream.
				return out, errors.New("media: remote refused audio (m=audio port 0)")
			}
			pts = pts[:0]
			for _, f := range fields[3:] {
				n, err := strconv.Atoi(f)
				if err != nil || n < 0 || n > 127 {
					continue // not a payload type; skip the token
				}
				pts = append(pts, uint8(n))
			}
			inAudio, haveAudio = true, true
			out.Port = port
		case 'c':
			// c=<nettype> <addrtype> <address>; we only speak IN IP4.
			fields := strings.Fields(line[2:])
			if len(fields) < 3 || fields[0] != "IN" || fields[1] != "IP4" {
				continue
			}
			if inAudio {
				mediaConn = fields[2]
			} else if !haveAudio {
				sessionConn = fields[2]
			}
		case 'a':
			if !inAudio {
				if !haveAudio {
					sess.parse(line[2:])
				}
				continue
			}
			if med.parse(line[2:]) {
				continue
			}
			if rest, ok := strings.CutPrefix(line[2:], "fmtp:"); ok {
				ptStr, params, _ := strings.Cut(strings.TrimSpace(rest), " ")
				if n, err := strconv.Atoi(ptStr); err == nil && n >= 0 && n <= 127 {
					if _, seen := fmtps[uint8(n)]; !seen {
						fmtps[uint8(n)] = strings.TrimSpace(params)
					}
				}
				continue
			}
			if rest, ok := strings.CutPrefix(line[2:], "crypto:"); ok {
				if c, err := ParseCrypto(rest); err == nil {
					out.Crypto = append(out.Crypto, c)
				} else {
					out.CryptoSkipped++
				}
				continue
			}
			if rest, ok := strings.CutPrefix(line[2:], "ptime:"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && n > 0 && out.Ptime == 0 {
					out.Ptime = n
				}
				continue
			}
			rest, ok := strings.CutPrefix(line[2:], "rtpmap:")
			if !ok {
				continue
			}
			fields := strings.Fields(rest)
			if len(fields) < 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil || n < 0 || n > 127 {
				continue
			}
			pt := uint8(n)
			if _, seen := maps[pt]; seen {
				continue // first rtpmap of a payload type wins
			}
			parts := strings.Split(fields[1], "/")
			name := parts[0]
			c := Codec{PT: pt, Name: name, ClockRate: 8000}
			isEvent := strings.EqualFold(name, nameDTMF)
			if len(parts) > 1 {
				r, err := strconv.Atoi(parts[1])
				if err != nil || r <= 0 {
					if isEvent {
						return out, fmt.Errorf("media: SDP telephone-event rate %q is not a number", parts[1])
					}
					continue // a codec with a nonsense clock is not one we can use
				}
				c.ClockRate = r
			}
			if len(parts) > 2 {
				if ch, err := strconv.Atoi(parts[2]); err == nil {
					c.Channels = ch
				}
			}
			maps[pt] = c
			if isEvent {
				eventOrder = append(eventOrder, pt)
			}
		}
	}
	if !haveAudio {
		return out, errors.New("media: SDP offer has no audio media section")
	}
	addr := mediaConn
	if addr == "" {
		addr = sessionConn
	}
	if addr == "" {
		return out, errors.New("media: SDP offer has no IN IP4 connection address")
	}
	for _, pt := range pts {
		c, ok := maps[pt]
		if !ok {
			c, ok = StaticCodec(pt)
		}
		if !ok {
			continue
		}
		if strings.EqualFold(c.Name, nameDTMF) {
			continue // events are collected below, once
		}
		c.Fmtp = fmtps[pt]
		out.Codecs = append(out.Codecs, c)
	}
	for _, pt := range eventOrder {
		c := maps[pt]
		c.Fmtp = fmtps[pt]
		out.Events = append(out.Events, c)
	}
	if !out.hasSupported() {
		return out, errors.New("media: SDP offer lists no supported audio codec (G.722, Opus, PCMU or PCMA)")
	}
	out.Address = addr
	out.DTLS, out.ICE, out.RTCPMux = med.merge(sess)
	out.finish()
	return out, nil
}

// knownProto reports the m= transports Hello parses.
func knownProto(p string) bool {
	switch p {
	case "RTP/AVP", "RTP/SAVP", "UDP/TLS/RTP/SAVP", "UDP/TLS/RTP/SAVPF":
		return true
	}
	return false
}

// secAttrs are the ICE and DTLS attributes of one SDP level.
type secAttrs struct {
	fps     []Fingerprint
	setup   string
	ufrag   string
	pwd     string
	lite    bool
	cands   []ICECandidate
	rtcpMux bool
}

// parse takes one attribute (without "a=") and reports whether it was one of
// these.
func (t *secAttrs) parse(attr string) bool {
	name, val, _ := strings.Cut(attr, ":")
	val = strings.TrimSpace(val)
	switch name {
	case "fingerprint":
		if f, err := ParseFingerprint(val); err == nil {
			t.fps = append(t.fps, f)
		}
	case "setup":
		t.setup = strings.ToLower(val)
	case "ice-ufrag":
		t.ufrag = val
	case "ice-pwd":
		t.pwd = val
	case "ice-lite":
		t.lite = true
	case "candidate":
		if c, ok := ParseICECandidate(val); ok {
			t.cands = append(t.cands, c)
		}
	case "rtcp-mux":
		t.rtcpMux = true
	default:
		return false
	}
	return true
}

// merge combines the media level with the session level, the media level
// winning.
func (t secAttrs) merge(sess secAttrs) (*DTLSInfo, *ICEInfo, bool) {
	var d *DTLSInfo
	fps, setup := t.fps, t.setup
	if len(fps) == 0 {
		fps = sess.fps
	}
	if setup == "" {
		setup = sess.setup
	}
	if len(fps) > 0 || setup != "" {
		d = &DTLSInfo{Fingerprints: fps, Setup: setup}
	}
	var ice *ICEInfo
	ufrag, pwd := t.ufrag, t.pwd
	if ufrag == "" {
		ufrag = sess.ufrag
	}
	if pwd == "" {
		pwd = sess.pwd
	}
	if ufrag != "" || pwd != "" || len(t.cands) > 0 {
		ice = &ICEInfo{Ufrag: ufrag, Pwd: pwd, Lite: t.lite || sess.lite, Candidates: t.cands}
	}
	return d, ice, t.rtcpMux
}

// hasSupported reports a codec Hello can use in Codecs.
func (a AudioSDP) hasSupported() bool {
	for _, c := range a.Codecs {
		if c.Supported() {
			return true
		}
	}
	return false
}

// finish derives the summary fields (PayloadType, Codec, DTMF*) from Codecs
// and Events.
func (a *AudioSDP) finish() {
	a.PayloadType, a.Codec = 0, Codec{}
	a.DTMFPayloadType, a.DTMFRate = 0, 0
	for _, c := range a.Codecs {
		if c.Supported() {
			a.PayloadType, a.Codec = c.PT, c
			break
		}
	}
	if len(a.Events) == 0 {
		return
	}
	ev := a.Events[0]
	for _, e := range a.Events {
		if a.Codec.ClockRate != 0 && e.ClockRate == a.Codec.ClockRate {
			ev = e
			break
		}
	}
	a.DTMFPayloadType, a.DTMFRate = ev.PT, ev.ClockRate
}

// Filter returns the SDP restricted to the supported codecs named in allow
// (case-insensitive: "G722", "opus", "PCMU", "PCMA"), keeping the sender's
// order and each codec's rtpmap and fmtp lines. A nil allow keeps every
// supported codec. telephone-event stays whenever a codec does. The result
// has no Codecs when nothing is allowed.
func (a AudioSDP) Filter(allow []string) AudioSDP {
	out := a
	out.Codecs = nil
	for _, c := range a.Codecs {
		if c.Supported() && codecAllowed(c, allow) {
			out.Codecs = append(out.Codecs, c)
		}
	}
	out.Events = nil
	if len(out.Codecs) > 0 {
		out.Events = append(out.Events, a.Events...)
	}
	out.finish()
	return out
}

func codecAllowed(c Codec, allow []string) bool {
	if allow == nil {
		return true
	}
	for _, n := range allow {
		if strings.EqualFold(n, c.Name) {
			return true
		}
	}
	return false
}

// DefaultTrunkCodecs is what a trunk is offered when it has no codec list:
// G.711 only, the codecs every carrier accepts.
var DefaultTrunkCodecs = []string{"PCMU", "PCMA"}

// PhoneCodecs are the codecs offered to and between phones, in Hello's
// preference order (Filter keeps the sender's own order, so for a filter
// this is the membership list).
var PhoneCodecs = []string{"opus", "G722", "PCMU", "PCMA"}

// Narrow returns the SDP reduced to the one codec with payload type pt, and
// the telephone-event that goes with it. When pt is not listed the SDP's
// own first supported codec is used instead.
func (a AudioSDP) Narrow(pt uint8) AudioSDP {
	out := a
	out.Codecs = nil
	for _, c := range a.Codecs {
		if c.PT == pt && c.Supported() {
			out.Codecs = []Codec{c}
			break
		}
	}
	if len(out.Codecs) == 0 && a.Codec.Supported() {
		out.Codecs = []Codec{a.Codec}
	}
	out.Events = nil
	if len(out.Codecs) > 0 {
		out.Events = a.eventsFor(out.Codecs[0])
	}
	out.finish()
	return out
}

// eventsFor keeps the one telephone-event matching codec's clock (else the
// first offered): an answer names a single event codec.
func (a AudioSDP) eventsFor(c Codec) []Codec {
	if len(a.Events) == 0 {
		return nil
	}
	for _, e := range a.Events {
		if e.ClockRate == c.ClockRate {
			return []Codec{e}
		}
	}
	return []Codec{a.Events[0]}
}

// Pick chooses one codec the way an answerer does: the first preference
// that the SDP lists wins, and within "g711" the SDP's own order decides
// between PCMU and PCMA. prefs are "g722", "opus", "g711" (or the exact
// names pcmu, pcma). It reports false when none matches.
func (a AudioSDP) Pick(prefs ...string) (AudioSDP, bool) {
	for _, p := range prefs {
		for _, c := range a.Codecs {
			k := c.Kind()
			if k == strings.ToLower(p) || (strings.EqualFold(p, "g711") && c.IsG711()) {
				out := a
				out.Codecs = []Codec{c}
				out.Events = a.eventsFor(c)
				out.finish()
				return out, true
			}
		}
	}
	return a, false
}

// Terminated is the answer for media Hello ends itself (voicemail,
// announcements, IVR, conference): Opus when this build has it and the offer
// lists it, then G.722, then the offer's first G.711. Paging keeps its own
// G.722 preference (internal/sip/page.go).
func (a AudioSDP) Terminated() (AudioSDP, bool) {
	if !OpusAvailable {
		return a.Pick("g722", "g711")
	}
	return a.Pick("opus", "g722", "g711")
}

// AnswerFor builds the SDP body's codec list for the caller out of the
// callee's answer: what the callee chose, restricted to payload types the
// caller offered (the callee answers in the numbering of the offer it got,
// which was the caller's). When the callee's answer shares nothing with the
// caller's offer, which a correct callee never does, the caller's own first
// supported codec is returned so the call still has a coherent answer.
func AnswerFor(callerOffer, calleeAnswer AudioSDP) AudioSDP {
	out := callerOffer
	out.Codecs = nil
	for _, c := range calleeAnswer.Codecs {
		for _, o := range callerOffer.Codecs {
			if o.PT == c.PT && strings.EqualFold(o.Name, c.Name) {
				out.Codecs = append(out.Codecs, o)
				break
			}
		}
	}
	if len(out.Codecs) == 0 {
		if first, ok := callerOffer.Terminated(); ok {
			return first
		}
		return callerOffer
	}
	out.Events = nil
	for _, e := range calleeAnswer.Events {
		for _, o := range callerOffer.Events {
			if o.PT == e.PT {
				out.Events = append(out.Events, o)
				break
			}
		}
	}
	out.finish()
	return out
}

// Build writes the SDP with Hello's address: every codec in Codecs then the
// telephone-event codecs in Events, with their rtpmap and fmtp lines, in dir
// ("sendrecv" when empty or unknown). Lines use CRLF and the session is
// valid forever (t=0 0), the shape every SIP endpoint accepts.
func (a AudioSDP) Build(ip string, port int, dir string) []byte {
	return buildSDP(ip, port, a.Codecs, a.Events, dir, localSec{
		srtpOnly: a.localSRTP, keys: a.localCrypto, proto: a.localProto,
		dtls: a.localDTLS, ice: a.localICE, mux: a.localMux})
}

// BuildSDP writes an SDP body for codecs followed by events.
func BuildSDP(ip string, port int, codecs, events []Codec, dir string) []byte {
	return buildSDP(ip, port, codecs, events, dir, localSec{})
}

// localSec is what an SDP Hello writes says about security: SDES keys or
// DTLS-SRTP attributes, ICE-lite and rtcp-mux.
type localSec struct {
	srtpOnly bool
	keys     []Crypto
	proto    string // overrides the m= transport (DTLS)
	dtls     *DTLSInfo
	ice      *ICEInfo
	mux      bool
}

func buildSDP(ip string, port int, codecs, events []Codec, dir string, sec localSec) []byte {
	switch dir {
	case "sendonly", "recvonly", "inactive":
	default:
		dir = "sendrecv"
	}
	all := make([]Codec, 0, len(codecs)+len(events))
	all = append(all, codecs...)
	for _, e := range events {
		if e.ClockRate <= 0 {
			e.ClockRate = 8000
		}
		all = append(all, e)
	}
	var b strings.Builder
	b.WriteString("v=0\r\n")
	// The o= line needs a session id and version; Unix millis keep answers
	// from different sessions apart without extra state.
	fmt.Fprintf(&b, "o=- %d 1 IN IP4 %s\r\n", time.Now().UnixMilli(), ip)
	b.WriteString("s=-\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", ip)
	b.WriteString("t=0 0\r\n")
	if sec.ice != nil {
		b.WriteString("a=ice-lite\r\n")
	}
	profile := "RTP/AVP"
	switch {
	case sec.proto != "":
		profile = sec.proto
	case sec.srtpOnly:
		profile = "RTP/SAVP"
	}
	fmt.Fprintf(&b, "m=audio %d %s", port, profile)
	for _, c := range all {
		fmt.Fprintf(&b, " %d", c.PT)
	}
	b.WriteString("\r\n")
	for _, c := range all {
		fmt.Fprintf(&b, "a=rtpmap:%s\r\n", c.Rtpmap())
		if c.Fmtp != "" {
			fmt.Fprintf(&b, "a=fmtp:%d %s\r\n", c.PT, c.Fmtp)
		}
	}
	if sec.ice != nil {
		fmt.Fprintf(&b, "a=ice-ufrag:%s\r\na=ice-pwd:%s\r\n", sec.ice.Ufrag, sec.ice.Pwd)
		fmt.Fprintf(&b, "a=candidate:1 1 udp %d %s %d typ host\r\n", hostPriority, ip, port)
	}
	if sec.dtls != nil {
		for _, f := range sec.dtls.Fingerprints {
			fmt.Fprintf(&b, "a=fingerprint:%s\r\n", f)
		}
		fmt.Fprintf(&b, "a=setup:%s\r\n", sec.dtls.Setup)
	}
	if sec.mux {
		b.WriteString("a=rtcp-mux\r\n")
	}
	for _, k := range sec.keys {
		fmt.Fprintf(&b, "a=%s\r\n", k.Attribute())
	}
	fmt.Fprintf(&b, "a=%s\r\n", dir)
	b.WriteString("a=ptime:20\r\n")
	return []byte(b.String())
}

// BuildAudioSDP builds a well-formed SDP with one audio stream: the given
// static payload type (0 PCMU, 8 PCMA, 9 G.722) plus, when dtmfPT is
// non-zero, a telephone-event type at dtmfRate. See AudioSDP.Build for
// dynamic codecs and codec lists.
func BuildAudioSDP(ip string, port int, pt uint8, dtmfPT uint8, dtmfRate int) []byte {
	return BuildAudioSDPDir(ip, port, pt, dtmfPT, dtmfRate, "sendrecv")
}

// BuildAudioSDPDir is BuildAudioSDP with an explicit media direction
// ("sendrecv", "sendonly", "recvonly", "inactive"); anything else is
// sendrecv.
func BuildAudioSDPDir(ip string, port int, pt uint8, dtmfPT uint8, dtmfRate int, dir string) []byte {
	c, ok := StaticCodec(pt)
	if !ok {
		c, _ = StaticCodec(0) // anything that is not a known static type is PCMU, the package default
		c.PT = pt
	}
	var events []Codec
	if dtmfPT != 0 {
		if dtmfRate <= 0 {
			dtmfRate = 8000
		}
		events = []Codec{{PT: dtmfPT, Name: "telephone-event", ClockRate: dtmfRate}}
	}
	return BuildSDP(ip, port, []Codec{c}, events, dir)
}

// SDPDirection reports an SDP body's media direction: the first direction
// attribute in the body, "sendrecv" when none.
func SDPDirection(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "a=sendonly"):
			return "sendonly"
		case strings.HasPrefix(line, "a=recvonly"):
			return "recvonly"
		case strings.HasPrefix(line, "a=inactive"):
			return "inactive"
		case strings.HasPrefix(line, "a=sendrecv"):
			return "sendrecv"
		}
	}
	return "sendrecv"
}

// StripCrypto removes the a=crypto lines of an SDP body, and the far end's
// ICE password. Replicated dialog state carries the far end's SDP so a
// surviving node can rebuild media; the keys in it are secrets that are never
// stored, so the lines are dropped (a taker negotiates fresh keys), and so is
// the ICE password, which authenticates connectivity checks. A fingerprint is
// public and stays.
func StripCrypto(sdp string) string {
	if !strings.Contains(sdp, "a=crypto:") && !strings.Contains(sdp, "a=ice-pwd:") {
		return sdp
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(sdp, "\n") {
		t := strings.TrimLeft(line, " ")
		if strings.HasPrefix(t, "a=crypto:") || strings.HasPrefix(t, "a=ice-pwd:") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
