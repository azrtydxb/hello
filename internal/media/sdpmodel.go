// The multi-section SDP model (clients-webrtc-mobile-video S-2): a session
// with any number of audio and video sections, BUNDLE groups, rtcp-mux,
// rtcp-fb, extmap, fmtp, ICE (ufrag, pwd, candidates, ice-lite,
// end-of-candidates), DTLS (fingerprint, setup) and SDES (crypto), parsed and
// written with pion/sdp.
//
// The model keeps every attribute in its original order, known or not, so a
// parse followed by Build loses nothing: the round-trip property the browser
// legs rely on. ParseAudioSDP and BuildAudioSDP stay the tolerant, audio-only
// reader and writer for Hello-terminated media (voicemail, announcements);
// AudioSection bridges a section of this model to them.
package media

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	pionsdp "github.com/pion/sdp/v3"
)

// SDPSession is a parsed SDP body. The zero value is not usable; start from
// ParseSDPSession or NewSDPSession.
type SDPSession struct {
	sd pionsdp.SessionDescription
}

// SDPMedia is one m= section of an SDPSession.
type SDPMedia struct {
	md *pionsdp.MediaDescription
}

// ParseSDPSession parses an SDP body (CRLF or bare LF). It is stricter than
// ParseAudioSDP: the session must have v=, o=, s= and t= lines, as every
// browser and SIP stack writes them.
func ParseSDPSession(body []byte) (*SDPSession, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("media: SDP: empty body")
	}
	s := &SDPSession{}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	text = strings.ReplaceAll(strings.TrimRight(text, "\n")+"\n", "\n", "\r\n")
	if err := s.sd.UnmarshalString(text); err != nil {
		return nil, fmt.Errorf("media: SDP: %w", err)
	}
	return s, nil
}

// NewSDPSession starts a session for an answer or offer: ip is the o= and c=
// address, id the o= session id and version.
func NewSDPSession(ip string, id uint64) *SDPSession {
	s := &SDPSession{}
	s.sd = pionsdp.SessionDescription{
		Version:     0,
		Origin:      pionsdp.Origin{Username: "-", SessionID: id, SessionVersion: id, NetworkType: "IN", AddressType: "IP4", UnicastAddress: ip},
		SessionName: "-",
		ConnectionInformation: &pionsdp.ConnectionInformation{
			NetworkType: "IN", AddressType: "IP4", Address: &pionsdp.Address{Address: ip},
		},
		TimeDescriptions: []pionsdp.TimeDescription{{}},
	}
	return s
}

// Build writes the session as an SDP body with CRLF line ends.
func (s *SDPSession) Build() ([]byte, error) {
	b, err := s.sd.Marshal()
	if err != nil {
		return nil, fmt.Errorf("media: SDP: %w", err)
	}
	return b, nil
}

// Address is the session-level c= address, "" when there is none.
func (s *SDPSession) Address() string {
	if c := s.sd.ConnectionInformation; c != nil && c.Address != nil {
		return c.Address.Address
	}
	return ""
}

// Media returns the sections in order.
func (s *SDPSession) Media() []*SDPMedia {
	out := make([]*SDPMedia, len(s.sd.MediaDescriptions))
	for i, md := range s.sd.MediaDescriptions {
		out[i] = &SDPMedia{md: md}
	}
	return out
}

// AddMedia appends a section: kind is audio or video, proto the m= transport,
// formats the payload type numbers.
func (s *SDPSession) AddMedia(kind string, port int, proto string, formats ...string) *SDPMedia {
	md := &pionsdp.MediaDescription{MediaName: pionsdp.MediaName{
		Media: kind, Port: pionsdp.RangedPort{Value: port}, Protos: strings.Split(proto, "/"), Formats: formats,
	}}
	s.sd.MediaDescriptions = append(s.sd.MediaDescriptions, md)
	return &SDPMedia{md: md}
}

// SessionAttr returns the first session-level attribute named key.
func (s *SDPSession) SessionAttr(key string) (string, bool) { return s.sd.Attribute(key) }

// SetSessionAttr adds a session-level attribute; a flag has no value.
func (s *SDPSession) SetSessionAttr(key, value string) {
	s.sd.Attributes = append(s.sd.Attributes, mkAttr(key, value))
}

func mkAttr(key, value string) pionsdp.Attribute {
	if value == "" {
		return pionsdp.NewPropertyAttribute(key)
	}
	return pionsdp.NewAttribute(key, value)
}

// IceLite reports a=ice-lite at the session level (RFC 8445 section 3.3).
func (s *SDPSession) IceLite() bool {
	_, ok := s.sd.Attribute("ice-lite")
	return ok
}

// Bundle returns the mids of the a=group:BUNDLE line (RFC 9143), nil when
// the session has none.
func (s *SDPSession) Bundle() []string {
	for _, a := range s.sd.Attributes {
		if a.Key != "group" {
			continue
		}
		f := strings.Fields(a.Value)
		if len(f) > 0 && f[0] == "BUNDLE" {
			return f[1:]
		}
	}
	return nil
}

// SetBundle writes the a=group:BUNDLE line for mids (replacing an old one).
func (s *SDPSession) SetBundle(mids ...string) {
	kept := s.sd.Attributes[:0]
	for _, a := range s.sd.Attributes {
		if a.Key == "group" && strings.HasPrefix(a.Value, "BUNDLE") {
			continue
		}
		kept = append(kept, a)
	}
	s.sd.Attributes = kept
	if len(mids) > 0 {
		s.SetSessionAttr("group", "BUNDLE "+strings.Join(mids, " "))
	}
}

// ---------------------------------------------------------------------------
// Sections

// Kind is the media type: audio, video or application.
func (m *SDPMedia) Kind() string { return m.md.MediaName.Media }

// Port is the m= port; 0 rejects the section.
func (m *SDPMedia) Port() int { return m.md.MediaName.Port.Value }

// SetPort changes the m= port (0 rejects the section).
func (m *SDPMedia) SetPort(p int) { m.md.MediaName.Port.Value = p }

// Proto is the m= transport, for example UDP/TLS/RTP/SAVPF.
func (m *SDPMedia) Proto() string { return strings.Join(m.md.MediaName.Protos, "/") }

// Formats are the payload type numbers listed in the m= line.
func (m *SDPMedia) Formats() []string { return m.md.MediaName.Formats }

// Address is the section's c= address, falling back to the session's when the
// section has none.
func (m *SDPMedia) Address(s *SDPSession) string {
	if c := m.md.ConnectionInformation; c != nil && c.Address != nil {
		return c.Address.Address
	}
	return s.Address()
}

// SetAddress writes a section-level c= line.
func (m *SDPMedia) SetAddress(ip string) {
	m.md.ConnectionInformation = &pionsdp.ConnectionInformation{
		NetworkType: "IN", AddressType: "IP4", Address: &pionsdp.Address{Address: ip},
	}
}

// Attr returns the first attribute named key.
func (m *SDPMedia) Attr(key string) (string, bool) { return m.md.Attribute(key) }

// Attrs returns every value of the attributes named key, in order.
func (m *SDPMedia) Attrs(key string) []string {
	var out []string
	for _, a := range m.md.Attributes {
		if a.Key == key {
			out = append(out, a.Value)
		}
	}
	return out
}

// Has reports whether the section carries the flag or attribute key.
func (m *SDPMedia) Has(key string) bool { _, ok := m.md.Attribute(key); return ok }

// Add appends an attribute; a flag has no value.
func (m *SDPMedia) Add(key, value string) {
	m.md.Attributes = append(m.md.Attributes, mkAttr(key, value))
}

// Remove drops every attribute named key.
func (m *SDPMedia) Remove(key string) {
	kept := m.md.Attributes[:0]
	for _, a := range m.md.Attributes {
		if a.Key != key {
			kept = append(kept, a)
		}
	}
	m.md.Attributes = kept
}

// Mid is the a=mid identification tag (RFC 5888), "" when absent.
func (m *SDPMedia) Mid() string { v, _ := m.md.Attribute("mid"); return v }

// Direction is sendrecv, sendonly, recvonly or inactive; sendrecv when the
// section does not say (RFC 4566 section 6).
func (m *SDPMedia) Direction() string {
	for _, a := range m.md.Attributes {
		switch a.Key {
		case "sendrecv", "sendonly", "recvonly", "inactive":
			return a.Key
		}
	}
	return "sendrecv"
}

// RTCPMux reports a=rtcp-mux (RFC 5761).
func (m *SDPMedia) RTCPMux() bool { return m.Has("rtcp-mux") }

// RTCPRsize reports a=rtcp-rsize (RFC 5506).
func (m *SDPMedia) RTCPRsize() bool { return m.Has("rtcp-rsize") }

// SDPCodec is one payload type of a section with what the SDP says about it.
type SDPCodec struct {
	PT       uint8
	Name     string // encoding name from a=rtpmap, "" for a static type without one
	Rate     int
	Channels int
	Fmtp     string
	// Feedback are the a=rtcp-fb values that apply to the type (its own and
	// the wildcard "*"), for example "nack pli" or "transport-cc".
	Feedback []string
}

// Codecs returns the section's payload types in m= line order. Static types
// without an rtpmap get their RFC 3551 name for the G.711 and G.722 types.
func (m *SDPMedia) Codecs() []SDPCodec {
	maps := map[string]SDPCodec{}
	for _, v := range m.Attrs("rtpmap") {
		pt, rest, ok := strings.Cut(v, " ")
		if !ok {
			continue
		}
		f := strings.Split(strings.TrimSpace(rest), "/")
		c := SDPCodec{Name: f[0]}
		if len(f) > 1 {
			c.Rate, _ = strconv.Atoi(f[1])
		}
		c.Channels = 1
		if len(f) > 2 {
			if n, err := strconv.Atoi(f[2]); err == nil {
				c.Channels = n
			}
		}
		maps[pt] = c
	}
	fmtps := map[string]string{}
	for _, v := range m.Attrs("fmtp") {
		if pt, rest, ok := strings.Cut(v, " "); ok {
			fmtps[pt] = strings.TrimSpace(rest)
		}
	}
	fbs := map[string][]string{}
	for _, v := range m.Attrs("rtcp-fb") {
		if pt, rest, ok := strings.Cut(v, " "); ok {
			fbs[pt] = append(fbs[pt], strings.TrimSpace(rest))
		}
	}
	var out []SDPCodec
	for _, f := range m.Formats() {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > 127 {
			continue
		}
		c, ok := maps[f]
		if !ok {
			c = staticCodec(n)
		}
		c.PT = uint8(n)
		c.Fmtp = fmtps[f]
		c.Feedback = append(append([]string(nil), fbs[f]...), fbs["*"]...)
		out = append(out, c)
	}
	return out
}

func staticCodec(pt int) SDPCodec {
	switch pt {
	case 0:
		return SDPCodec{Name: "PCMU", Rate: 8000, Channels: 1}
	case 8:
		return SDPCodec{Name: "PCMA", Rate: 8000, Channels: 1}
	case 9:
		return SDPCodec{Name: "G722", Rate: 8000, Channels: 1}
	}
	return SDPCodec{}
}

// SDPExtmap is one a=extmap line (RFC 8285).
type SDPExtmap struct {
	ID        int
	Direction string
	URI       string
}

// Extmaps returns the header extensions the section declares.
func (m *SDPMedia) Extmaps() []SDPExtmap {
	var out []SDPExtmap
	for _, v := range m.Attrs("extmap") {
		f := strings.Fields(v)
		if len(f) < 2 {
			continue
		}
		idStr, dir, _ := strings.Cut(f[0], "/")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		out = append(out, SDPExtmap{ID: id, Direction: dir, URI: f[1]})
	}
	return out
}

// SDPICE is the ICE part of a section as the attributes carry it. Credentials
// not repeated in the section are taken from the session (RFC 8839 section
// 5.4).
type SDPICE struct {
	Ufrag, Pwd string
	Lite       bool
	Candidates []string // a=candidate values, without the "candidate:" prefix
	EndOfCands bool
	Options    string
}

// ICE returns the section's ICE attributes merged with the session's.
func (m *SDPMedia) ICE(s *SDPSession) SDPICE {
	ice := SDPICE{Candidates: nil}
	ice.Ufrag, _ = m.Attr("ice-ufrag")
	ice.Pwd, _ = m.Attr("ice-pwd")
	ice.Options, _ = m.Attr("ice-options")
	if ice.Ufrag == "" {
		ice.Ufrag, _ = s.SessionAttr("ice-ufrag")
	}
	if ice.Pwd == "" {
		ice.Pwd, _ = s.SessionAttr("ice-pwd")
	}
	if ice.Options == "" {
		ice.Options, _ = s.SessionAttr("ice-options")
	}
	ice.Lite = s.IceLite() || m.Has("ice-lite")
	ice.Candidates = m.Attrs("candidate")
	ice.EndOfCands = m.Has("end-of-candidates")
	return ice
}

// SDPDTLS is the DTLS-SRTP part of a section (RFC 8122, RFC 4145): the
// fingerprint values as written ("sha-256 AB:CD:...") and the setup role.
type SDPDTLS struct {
	Fingerprints []string
	Setup        string
}

// DTLS returns the section's DTLS attributes, falling back to the session's.
func (m *SDPMedia) DTLS(s *SDPSession) SDPDTLS {
	d := SDPDTLS{Fingerprints: m.Attrs("fingerprint")}
	d.Setup, _ = m.Attr("setup")
	if len(d.Fingerprints) == 0 {
		for _, a := range s.sd.Attributes {
			if a.Key == "fingerprint" {
				d.Fingerprints = append(d.Fingerprints, a.Value)
			}
		}
	}
	if d.Setup == "" {
		d.Setup, _ = s.SessionAttr("setup")
	}
	d.Setup = strings.ToLower(d.Setup)
	return d
}

// Crypto returns the section's usable SDES a=crypto lines; unusable ones
// (unknown suite, MKI) are counted in skipped.
func (m *SDPMedia) Crypto() (keys []Crypto, skipped int) {
	for _, v := range m.Attrs("crypto") {
		c, err := ParseCrypto("crypto:" + v)
		if err != nil {
			skipped++
			continue
		}
		keys = append(keys, c)
	}
	return keys, skipped
}

// AudioSection bridges an audio section to the AudioSDP the call code and the
// codec rules (Filter, Pick, AnswerFor) work on: the section is rewritten as a
// plain RTP/AVP audio section with its codecs, fmtp lines and direction.
// Everything about the transport (ICE, DTLS, SDES, mux) stays in the model.
func (m *SDPMedia) AudioSection(s *SDPSession) (AudioSDP, error) {
	if m.Kind() != "audio" {
		return AudioSDP{}, errors.New("media: the section is not audio")
	}
	var b strings.Builder
	b.WriteString("v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=-\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\nt=0 0\r\n", m.Address(s))
	fmt.Fprintf(&b, "m=audio %d RTP/AVP %s\r\n", m.Port(), strings.Join(m.Formats(), " "))
	for _, k := range []string{"rtpmap", "fmtp", "ptime"} {
		for _, v := range m.Attrs(k) {
			fmt.Fprintf(&b, "a=%s:%s\r\n", k, v)
		}
	}
	fmt.Fprintf(&b, "a=%s\r\n", m.Direction())
	return ParseAudioSDP([]byte(b.String()))
}
