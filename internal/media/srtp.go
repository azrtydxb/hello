// SRTP for the anchored media path (phone-feature-parity S-15): SDES key
// exchange in SDP (RFC 4568) and SRTP/SRTCP protection (RFC 3711) per relay
// leg, with pion/srtp doing the cryptography.
//
// Every leg of an anchored call has its own keys. A leg's local key is the
// one Hello sends with (it goes into the SDP Hello writes for that leg), its
// remote key is the one the far end sends with (it comes out of the far
// end's SDP). The relay decrypts a packet with the receiving leg's remote
// key, rewrites it as plain RTP, and encrypts it with the sending leg's
// local key, so the two legs never share a key.
//
// Keys are secrets: the Crypto type redacts itself in every formatting verb
// and in slog, and nothing here logs one.
package media

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// CryptoSuite is an SDES crypto-suite name (RFC 4568 6.2, RFC 6188).
type CryptoSuite string

// The suites Hello speaks. AES_CM_128_HMAC_SHA1_80 is the one every SDES
// endpoint supports; the _32 variant (32-bit RTP tag) is offered second;
// AES_256_CM_HMAC_SHA1_80 is accepted when a far end asks for it but never
// offered.
const (
	SuiteAES128SHA1_80 CryptoSuite = "AES_CM_128_HMAC_SHA1_80"
	SuiteAES128SHA1_32 CryptoSuite = "AES_CM_128_HMAC_SHA1_32"
	SuiteAES256SHA1_80 CryptoSuite = "AES_256_CM_HMAC_SHA1_80"
	// SuiteAEADGCM128 is AEAD_AES_128_GCM (RFC 7714). It is only ever the
	// result of a DTLS-SRTP handshake (S-16); SDES neither offers nor accepts
	// it.
	SuiteAEADGCM128 CryptoSuite = "AEAD_AES_128_GCM"
)

// OfferedSuites is what Hello offers, in preference order.
var OfferedSuites = []CryptoSuite{SuiteAES128SHA1_80, SuiteAES128SHA1_32}

// profile maps the suite to pion's protection profile and the master key and
// salt lengths in bytes.
func (s CryptoSuite) profile() (p srtp.ProtectionProfile, keyLen, saltLen int, ok bool) {
	switch s {
	case SuiteAES128SHA1_80:
		return srtp.ProtectionProfileAes128CmHmacSha1_80, 16, 14, true
	case SuiteAES128SHA1_32:
		return srtp.ProtectionProfileAes128CmHmacSha1_32, 16, 14, true
	case SuiteAES256SHA1_80:
		return srtp.ProtectionProfileAes256CmHmacSha1_80, 32, 14, true
	case SuiteAEADGCM128:
		return srtp.ProtectionProfileAeadAes128Gcm, 16, 12, true
	}
	return 0, 0, 0, false
}

// Crypto is one a=crypto attribute: a tag, a suite and the master key and
// salt. The key is unexported and never printed.
type Crypto struct {
	Tag   int
	Suite CryptoSuite
	key   []byte // master key followed by master salt
}

// String redacts the key.
func (c Crypto) String() string {
	return fmt.Sprintf("crypto{tag=%d suite=%s key=<redacted>}", c.Tag, c.Suite)
}

// GoString redacts the key for %#v.
func (c Crypto) GoString() string { return c.String() }

// LogValue redacts the key in slog output.
func (c Crypto) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("tag", c.Tag), slog.String("suite", string(c.Suite)))
}

// Valid reports whether the crypto carries a usable suite and key.
func (c Crypto) Valid() bool {
	_, kl, sl, ok := c.Suite.profile()
	return ok && len(c.key) == kl+sl
}

// sameKey compares two keys in constant time.
func (c Crypto) sameKey(o Crypto) bool {
	return c.Suite == o.Suite && subtle.ConstantTimeCompare(c.key, o.key) == 1
}

// Attribute is the SDP attribute value without the "a=": the form
// "crypto:<tag> <suite> inline:<base64>".
func (c Crypto) Attribute() string {
	return "crypto:" + strconv.Itoa(c.Tag) + " " + string(c.Suite) + " inline:" + base64.StdEncoding.EncodeToString(c.key)
}

// NewCrypto draws a fresh random key for suite.
func NewCrypto(tag int, suite CryptoSuite) (Crypto, error) {
	_, kl, sl, ok := suite.profile()
	if !ok {
		return Crypto{}, fmt.Errorf("media: unsupported SRTP suite %q", suite)
	}
	key := make([]byte, kl+sl)
	if _, err := rand.Read(key); err != nil {
		return Crypto{}, fmt.Errorf("media: generate SRTP key: %w", err)
	}
	return Crypto{Tag: tag, Suite: suite, key: key}, nil
}

// ParseCrypto parses the value of an a=crypto line (after "a=crypto:" or
// with it). Lines Hello cannot honour are errors: an MKI, a key-derivation
// rate other than 0, and session parameters it does not implement
// (UNENCRYPTED_SRTP, FEC, ...). A lifetime is accepted and ignored: a call
// does not send 2^31 packets.
func ParseCrypto(attr string) (Crypto, error) {
	attr = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(attr), "crypto:"))
	fields := strings.Fields(attr)
	if len(fields) < 3 {
		return Crypto{}, errors.New("media: a=crypto needs a tag, a suite and a key")
	}
	tag, err := strconv.Atoi(fields[0])
	if err != nil || tag < 0 || tag > 999999999 {
		return Crypto{}, errors.New("media: a=crypto tag is not a number")
	}
	suite := CryptoSuite(fields[1])
	_, kl, sl, ok := suite.profile()
	if !ok || suite == SuiteAEADGCM128 {
		return Crypto{}, fmt.Errorf("media: unsupported SRTP suite %q", suite)
	}
	method, ok := strings.CutPrefix(fields[2], "inline:")
	if !ok {
		return Crypto{}, errors.New("media: a=crypto key method is not inline")
	}
	keyB64, params, _ := strings.Cut(method, "|")
	if params != "" {
		// key|lifetime|MKI:length; a third part is an MKI.
		parts := strings.Split(params, "|")
		if len(parts) > 2 || (len(parts) == 2 && strings.Contains(parts[1], ":")) ||
			(len(parts) == 1 && strings.Contains(parts[0], ":")) {
			return Crypto{}, errors.New("media: a=crypto MKI is not supported")
		}
	}
	for _, p := range fields[3:] {
		if kdr, ok := strings.CutPrefix(strings.ToUpper(p), "KDR="); ok && kdr == "0" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(p), "WSH=") {
			continue
		}
		return Crypto{}, errors.New("media: a=crypto session parameter is not supported")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		if key, err = base64.RawStdEncoding.DecodeString(keyB64); err != nil {
			return Crypto{}, errors.New("media: a=crypto key is not base64")
		}
	}
	if len(key) != kl+sl {
		return Crypto{}, errors.New("media: a=crypto key has the wrong length for its suite")
	}
	return Crypto{Tag: tag, Suite: suite, key: key}, nil
}

// txState is the send half of a leg's SRTP state. It outlives a change of
// the far end's key (a re-INVITE with a new a=crypto): the send key, and
// with it the rollover counter, stays.
type txState struct {
	mu  sync.Mutex // pion's context is not safe for concurrent use
	ctx *srtp.Context
}

// legKeys is a leg's active SRTP state. tx protects what Hello sends into
// the leg, rx unprotects what the leg's far end sends.
type legKeys struct {
	// savp is the media profile the leg negotiated: RTP/SAVP (true) or
	// RTP/AVP with crypto lines. Later SDPs for the leg keep it, since a
	// transport profile must not change inside a dialog.
	savp bool
	// dtls is true when the keys came from a DTLS-SRTP handshake (S-16)
	// and not from SDES.
	dtls   bool
	suite  CryptoSuite
	local  Crypto
	remote Crypto

	tx *txState

	// rx is used by the leg's receive goroutine only.
	rx *srtp.Context

	// scratch and hdr are the receive goroutine's decryption buffers.
	scratch []byte
	hdr     rtp.Header
	rtcpHdr rtcp.Header
}

// newLegKeys builds the leg state for local and remote keys, reusing the send
// half of prev when the local key did not change.
func newLegKeys(local, remote Crypto, prev *legKeys) (*legKeys, error) {
	if local.Suite != remote.Suite {
		return nil, errors.New("media: SRTP suites of the two directions differ")
	}
	prof, kl, _, ok := local.Suite.profile()
	if !ok || !local.Valid() || !remote.Valid() {
		return nil, errors.New("media: invalid SRTP key")
	}
	var tx *txState
	if prev != nil && prev.local.sameKey(local) {
		tx = prev.tx
	} else {
		ctx, err := srtp.CreateContext(local.key[:kl], local.key[kl:], prof)
		if err != nil {
			return nil, fmt.Errorf("media: SRTP send context: %w", err)
		}
		tx = &txState{ctx: ctx}
	}
	rx, err := srtp.CreateContext(remote.key[:kl], remote.key[kl:], prof,
		srtp.SRTPReplayProtection(128), srtp.SRTCPReplayProtection(128))
	if err != nil {
		return nil, fmt.Errorf("media: SRTP receive context: %w", err)
	}
	return &legKeys{suite: local.Suite, local: local, remote: remote, tx: tx, rx: rx,
		scratch: make([]byte, 0, rtpMaxPacket+32)}, nil
}

// protect encrypts one plain RTP packet for sending into the leg.
func (k *legKeys) protect(pkt []byte) ([]byte, error) {
	k.tx.mu.Lock()
	defer k.tx.mu.Unlock()
	return k.tx.ctx.EncryptRTP(nil, pkt, nil)
}

// protectRTCP encrypts one plain RTCP compound packet.
func (k *legKeys) protectRTCP(pkt []byte) ([]byte, error) {
	k.tx.mu.Lock()
	defer k.tx.mu.Unlock()
	return k.tx.ctx.EncryptRTCP(nil, pkt, nil)
}

// unprotect authenticates and decrypts an RTP packet from the leg's far
// end. The result aliases the leg's receive scratch buffer.
func (k *legKeys) unprotect(pkt []byte) ([]byte, error) {
	out, err := k.rx.DecryptRTP(k.scratch[:0], pkt, &k.hdr)
	return out, err
}

// unprotectRTCP authenticates and decrypts an SRTCP packet.
func (k *legKeys) unprotectRTCP(pkt []byte) ([]byte, error) {
	return k.rx.DecryptRTCP(k.scratch[:0], pkt, &k.rtcpHdr)
}

// isRTCP reports whether a datagram on an RTP port (rtcp-mux, RFC 5761) is
// RTCP: payload types 72 to 76 are reserved for it.
func isRTCP(pkt []byte) bool {
	if len(pkt) < 8 || pkt[0]>>6 != rtpVersion {
		return false
	}
	pt := pkt[1] & 0x7f
	return pt >= 72 && pt <= 76 || pkt[1] >= 192 && pkt[1] <= 223
}

// Protector is the standalone SRTP/SRTCP protect side of a key, for tests
// and tools that speak to the anchor as a phone would.
type Protector struct{ k *legKeys }

// NewEndpoint builds the two directions of one SRTP endpoint: send is the
// key it sends with, recv the key it receives with.
func NewEndpoint(send, recv Crypto) (*Protector, error) {
	k, err := newLegKeys(send, recv, nil)
	if err != nil {
		return nil, err
	}
	return &Protector{k: k}, nil
}

// Protect encrypts an RTP packet.
func (p *Protector) Protect(pkt []byte) ([]byte, error) { return p.k.protect(pkt) }

// ProtectRTCP encrypts an RTCP packet.
func (p *Protector) ProtectRTCP(pkt []byte) ([]byte, error) { return p.k.protectRTCP(pkt) }

// Unprotect decrypts an RTP packet; the result is a copy.
func (p *Protector) Unprotect(pkt []byte) ([]byte, error) {
	out, err := p.k.unprotect(pkt)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), out...), nil
}

// UnprotectRTCP decrypts an RTCP packet; the result is a copy.
func (p *Protector) UnprotectRTCP(pkt []byte) ([]byte, error) {
	out, err := p.k.unprotectRTCP(pkt)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), out...), nil
}

// legSecurity is a relay leg's SDES negotiation state. offered holds the
// local keys written into an offer (or the one answer key) until the far end
// settles on one; keys is the active state once it has.
type legSecurity struct {
	mu      sync.Mutex
	offered []Crypto
	// keys is the active state once the far end settled on a key; a
	// re-INVITE that repeats the far end's key keeps it, replay window and
	// rollover counter included.
	keys *legKeys
}

// Secure is the SDES negotiation and SRTP state of one media endpoint: a
// relay leg, a voicemail session, a conference participant, a page caller or
// a page member. The zero value is a plain RTP endpoint. offered holds the
// local keys written into an offer (or the one answer key) until the far end
// settles on one; the active state is read by the packet path without a lock.
type Secure struct {
	sec    legSecurity
	active atomic.Pointer[legKeys]
	// pending is true while a DTLS-SRTP endpoint has negotiated but has no
	// keys yet (handshake running or failed): nothing is sent or accepted,
	// since the endpoint must never fall back to plain RTP.
	pending atomic.Bool
	// xp is the endpoint's ICE-lite and DTLS transport (secxport.go); nil
	// until the host binds the endpoint's socket with Bind.
	xp atomic.Pointer[xport]
}

// Local returns the keys Hello offers, generating them on first use. Once a
// key is active the active one is returned alone, so a re-INVITE answer
// repeats it.
func (s *Secure) Local() ([]Crypto, error) {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if s.sec.keys != nil {
		return []Crypto{s.sec.keys.local}, nil
	}
	if len(s.sec.offered) == 0 {
		for i, suite := range OfferedSuites {
			c, err := NewCrypto(i+1, suite)
			if err != nil {
				return nil, err
			}
			s.sec.offered = append(s.sec.offered, c)
		}
	}
	return append([]Crypto(nil), s.sec.offered...), nil
}

// Accept settles the endpoint on one of the far end's offered keys (Hello is
// the answerer): the first line, in the offerer's order, whose suite Hello
// speaks. It returns the line to put in the answer, which carries the same
// tag and a fresh local key; after it the endpoint is encrypted. It fails
// when none of the lines is usable.
func (s *Secure) Accept(offers []Crypto, savp bool) (Crypto, error) {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	for _, o := range offers {
		if !o.Valid() {
			continue
		}
		if k := s.sec.keys; k != nil && k.remote.sameKey(o) {
			return k.local, nil // a re-INVITE repeating the key
		}
		local := Crypto{}
		if k := s.sec.keys; k != nil && k.suite == o.Suite {
			local = k.local
		} else {
			c, err := NewCrypto(o.Tag, o.Suite)
			if err != nil {
				return Crypto{}, err
			}
			local = c
		}
		local.Tag = o.Tag
		keys, err := newLegKeys(local, o, s.sec.keys)
		if err != nil {
			return Crypto{}, err
		}
		keys.savp = savp
		s.sec.keys = keys
		s.sec.offered = nil
		s.active.Store(keys)
		return local, nil
	}
	return Crypto{}, errors.New("media: the offer has no SRTP suite Hello supports")
}

// ApplyAnswer settles the endpoint on the far end's answer to Hello's offer:
// the answer's line must match the tag and suite of one Hello offered. It
// returns false (and leaves the endpoint plain) when the answer carries no
// usable line.
func (s *Secure) ApplyAnswer(answers []Crypto, savp bool) bool {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if len(answers) == 0 {
		return false
	}
	cands := s.sec.offered
	if s.sec.keys != nil {
		cands = []Crypto{s.sec.keys.local}
	}
	for _, a := range answers {
		if !a.Valid() {
			continue
		}
		for _, o := range cands {
			if o.Tag != a.Tag || o.Suite != a.Suite {
				continue
			}
			if k := s.sec.keys; k != nil && k.remote.sameKey(a) {
				return true
			}
			keys, err := newLegKeys(o, a, s.sec.keys)
			if err != nil {
				return false
			}
			keys.savp = savp
			s.sec.keys = keys
			s.sec.offered = nil
			s.active.Store(keys)
			return true
		}
	}
	return false
}

// Suite reports whether the endpoint's media is SRTP-protected and with
// which suite.
func (s *Secure) Suite() (CryptoSuite, bool) {
	if k := s.active.Load(); k != nil {
		return k.suite, true
	}
	return "", false
}

// Profile reports whether the endpoint is encrypted and whether it
// negotiated RTP/SAVP (true) or RTP/AVP with crypto lines (false).
func (s *Secure) Profile() (savp, on bool) {
	if k := s.active.Load(); k != nil {
		return k.savp, true
	}
	if s.pending.Load() {
		return true, true // DTLS-SRTP negotiated, handshake not finished
	}
	return false, false
}

// UpdateRemote applies the far end's a=crypto lines from a re-INVITE or a
// re-INVITE answer to an encrypted endpoint: a line with the active suite
// and the same key changes nothing (the replay window and rollover counter
// stay), a new key replaces the receive key and keeps the send key. It
// reports whether the endpoint now uses one of the lines; a plain endpoint
// is never switched on by it.
func (s *Secure) UpdateRemote(lines []Crypto) bool {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	cur := s.sec.keys
	if cur == nil {
		return false
	}
	for _, c := range lines {
		if !c.Valid() || c.Suite != cur.suite {
			continue
		}
		if cur.remote.sameKey(c) {
			return true
		}
		keys, err := newLegKeys(cur.local, c, cur)
		if err != nil {
			return false
		}
		keys.savp = cur.savp
		s.sec.keys = keys
		s.active.Store(keys)
		return true
	}
	return false
}

// Learn applies an SDP's a=crypto lines from the far end: the answer to a
// key Hello offered activates the endpoint, a repeat or a rekey on an
// encrypted one updates it. It reports whether the endpoint is encrypted
// after it. An endpoint Hello never offered a key on stays plain whatever
// the lines say.
func (s *Secure) Learn(lines []Crypto, savp bool) bool {
	if s.active.Load() != nil {
		return s.UpdateRemote(lines)
	}
	s.sec.mu.Lock()
	pending := len(s.sec.offered) > 0
	s.sec.mu.Unlock()
	if !pending {
		return false
	}
	return s.ApplyAnswer(lines, savp)
}

// Reset drops the endpoint's SRTP state, for when another endpoint takes it
// over: the keys belonged to the previous one. It is plain RTP until it is
// negotiated again.
func (s *Secure) Reset() {
	s.sec.mu.Lock()
	s.sec.keys = nil
	s.sec.offered = nil
	s.active.Store(nil)
	s.pending.Store(false)
	s.resetTransport()
	s.sec.mu.Unlock()
}

// Protect encrypts a plain RTP packet for sending when the endpoint is
// encrypted. ok is false only when it is encrypted and the packet cannot be
// protected: the caller drops it, never sends it in the clear.
func (s *Secure) Protect(pkt []byte) (out []byte, ok bool) {
	k := s.active.Load()
	if k == nil {
		if s.pending.Load() {
			return nil, false
		}
		return pkt, true
	}
	enc, err := k.protect(pkt)
	if err != nil {
		return nil, false
	}
	return enc, true
}

// Unprotect checks and decrypts a datagram received by an encrypted
// endpoint. ok is false for a packet that fails authentication or replay,
// and for RTCP on the RTP port (rtcp-mux), which is checked and consumed
// (an RTCP packet that fails authentication counts as a failure too). A
// plain endpoint passes the datagram through. authFailed reports a packet
// that did not authenticate. The result may alias an internal buffer: use it
// before the next call, from one goroutine only.
func (s *Secure) Unprotect(data []byte) (plain []byte, ok, authFailed bool) {
	return s.UnprotectFrom(data, nil)
}

// UnprotectFrom is Unprotect for an endpoint whose socket may also carry
// STUN and DTLS (RFC 7983): the datagram's first byte tells them from RTP.
// STUN goes to the ICE-lite agent and DTLS to the handshake, both answered
// from here and never returned as media (ok is false). from is where the
// datagram came from; the host has already vetted it as a source. An endpoint
// that negotiated DTLS-SRTP and has no keys yet drops all media.
func (s *Secure) UnprotectFrom(data []byte, from *net.UDPAddr) (plain []byte, ok, authFailed bool) {
	plain, rtcp, ok, authFailed := s.UnprotectAny(data, from)
	if rtcp {
		return nil, false, authFailed
	}
	return plain, ok, authFailed
}

// UnprotectAny is UnprotectFrom for a host that also wants the RTCP of a
// muxed port: a datagram that is RTCP and authenticates (or is plain RTCP on
// a plain endpoint) comes back decrypted with rtcp true, where UnprotectFrom
// consumes it. The result aliases an internal buffer like UnprotectFrom's.
func (s *Secure) UnprotectAny(data []byte, from *net.UDPAddr) (plain []byte, rtcp, ok, authFailed bool) {
	if x := s.xp.Load(); x != nil && from != nil {
		switch {
		case isSTUN(data):
			s.stunIn(x, data, from)
			return nil, false, false, false
		case isDTLS(data):
			s.dtlsIn(x, data, from)
			return nil, false, false, false
		}
	}
	k := s.active.Load()
	if k == nil {
		if s.pending.Load() {
			return nil, false, false, false
		}
		if x := s.xp.Load(); x != nil && x.muxed() && isRTCP(data) {
			return data, true, true, false // plain RTCP on a muxed port
		}
		return data, false, true, false
	}
	if isRTCP(data) {
		p, err := k.unprotectRTCP(data)
		if err != nil {
			return nil, false, false, true
		}
		return p, true, true, false
	}
	p, err := k.unprotect(data)
	if err != nil {
		return nil, false, false, true
	}
	return p, false, true, false
}

// LocalCrypto returns the keys Hello offers on leg, generating them on first
// use. Once a key is active the active one is returned alone, so a re-INVITE
// answer repeats it.
func (r *Relay) LocalCrypto(leg string) ([]Crypto, error) {
	l := r.findLeg(leg)
	if l == nil {
		return nil, errors.New("media: unknown relay leg " + leg)
	}
	return l.secure.Local()
}

// AcceptCrypto settles leg on one of the far end's offered keys (Hello is
// the answerer); see Secure.Accept.
func (r *Relay) AcceptCrypto(leg string, offers []Crypto, savp bool) (Crypto, error) {
	l := r.findLeg(leg)
	if l == nil {
		return Crypto{}, errors.New("media: unknown relay leg " + leg)
	}
	return l.secure.Accept(offers, savp)
}

// ApplyAnswerCrypto settles leg on the far end's answer to Hello's offer; see
// Secure.ApplyAnswer.
func (r *Relay) ApplyAnswerCrypto(leg string, answers []Crypto, savp bool) bool {
	l := r.findLeg(leg)
	return l != nil && l.secure.ApplyAnswer(answers, savp)
}

// Secure reports whether leg's media is SRTP-protected and with which suite.
func (r *Relay) Secure(leg string) (CryptoSuite, bool) {
	l := r.findLeg(leg)
	if l == nil {
		return "", false
	}
	return l.secure.Suite()
}

// SecureProfile reports whether leg is encrypted and whether it negotiated
// RTP/SAVP (true) or RTP/AVP with crypto lines (false).
func (r *Relay) SecureProfile(leg string) (savp, on bool) {
	l := r.findLeg(leg)
	if l == nil {
		return false, false
	}
	return l.secure.Profile()
}

// SecureLegs counts the legs that are SRTP-protected (0 to 2).
func (r *Relay) SecureLegs() int {
	r.mu.Lock()
	legs := append([]*relayLeg(nil), r.legs...)
	r.mu.Unlock()
	n := 0
	for _, l := range legs {
		if l.secure.active.Load() != nil {
			n++
		}
	}
	return n
}

// UpdateRemoteCrypto applies the far end's a=crypto lines from a re-INVITE or
// a re-INVITE answer to an encrypted leg; see Secure.UpdateRemote.
func (r *Relay) UpdateRemoteCrypto(leg string, lines []Crypto) bool {
	l := r.findLeg(leg)
	return l != nil && l.secure.UpdateRemote(lines)
}

// LearnCrypto applies an SDP's a=crypto lines from the far end of leg; see
// Secure.Learn.
func (r *Relay) LearnCrypto(leg string, lines []Crypto, savp bool) bool {
	l := r.findLeg(leg)
	return l != nil && l.secure.Learn(lines, savp)
}

// ResetCrypto drops a leg's SRTP state, for when another endpoint takes the
// leg over (a transfer target, a retrieved parked call, a pickup): the keys
// belonged to the previous endpoint. The leg is plain RTP until it is
// negotiated again.
func (r *Relay) ResetCrypto(leg string) {
	if l := r.findLeg(leg); l != nil {
		l.secure.Reset()
	}
}

// AnswerSDP negotiates the leg's security for an offer Hello answers (SDES,
// DTLS-SRTP, ICE-lite); see Secure.AnswerSDP.
func (r *Relay) AnswerSDP(leg string, offer, ans AudioSDP, p SecurePolicy) (AudioSDP, error) {
	l := r.findLeg(leg)
	if l == nil {
		return ans, errors.New("media: unknown relay leg " + leg)
	}
	out, err := l.secure.AnswerSDP(offer, ans, p)
	return l.webAudio(out), err
}

// OfferSDP adds the leg's security to an offer Hello makes; see
// Secure.OfferSDP.
func (r *Relay) OfferSDP(leg string, off AudioSDP, p SecurePolicy) (AudioSDP, error) {
	l := r.findLeg(leg)
	if l == nil {
		return off, errors.New("media: unknown relay leg " + leg)
	}
	if l.web != nil {
		// A browser leg is offered DTLS-SRTP on the browser transport, whatever
		// the caller's policy says.
		out, err := l.secure.OfferSDP(off, secureWebPolicy)
		l.secure.sec.mu.Lock()
		l.secure.setProto(ProtoDTLSFB)
		l.secure.sec.mu.Unlock()
		return l.webAudio(out), err
	}
	return l.secure.OfferSDP(off, p)
}

// KeepSDP repeats the leg's security in a later SDP of the dialog; see
// Secure.KeepSDP.
func (r *Relay) KeepSDP(leg string, a AudioSDP) AudioSDP {
	if l := r.findLeg(leg); l != nil {
		return l.webAudio(l.secure.KeepSDP(a))
	}
	return a
}

// LearnAnswer applies the far end's answer to an offer on leg and reports
// whether the leg is now encrypted; see Secure.LearnAnswer.
func (r *Relay) LearnAnswer(leg string, a AudioSDP, savp bool) bool {
	l := r.findLeg(leg)
	return l != nil && l.secure.LearnAnswer(a, savp)
}

// LearnSDP applies an SDP that arrived from the far end of leg (a re-INVITE
// or the answer to one); see Secure.LearnSDP.
func (r *Relay) LearnSDP(leg string, a AudioSDP) {
	if l := r.findLeg(leg); l != nil {
		l.secure.LearnSDP(a)
	}
}

// SecureMode is the leg's replicated mode: "", "avp", "savp" or "dtls".
func (r *Relay) SecureMode(leg string) string {
	if l := r.findLeg(leg); l != nil {
		return l.secure.Mode()
	}
	return ""
}

// KeyExchange reports how the leg's keys were agreed: "sdes", "dtls" or ""
// while plain or before a handshake ends.
func (r *Relay) KeyExchange(leg string) string {
	if l := r.findLeg(leg); l != nil {
		return l.secure.KeyExchange()
	}
	return ""
}

// ICEActive reports whether leg runs ICE-lite.
func (r *Relay) ICEActive(leg string) bool {
	l := r.findLeg(leg)
	return l != nil && l.secure.ICEActive()
}

// ReOffer rebuilds off with the leg's security for an HA takeover re-INVITE,
// in the replicated mode the leg had ("avp", "savp" or "dtls"), with fresh
// keys; ICE-lite follows when the leg ran it.
func (r *Relay) ReOffer(leg string, off AudioSDP, mode string) (AudioSDP, error) {
	l := r.findLeg(leg)
	if l == nil {
		return off, errors.New("media: unknown relay leg " + leg)
	}
	if mode == "dtls" {
		return l.secure.OfferSDP(off, SecurePolicy{Allow: true, Require: true, DTLS: true, ICE: l.secure.ICEActive()})
	}
	keys, err := l.secure.Local()
	if err != nil {
		return off, err
	}
	return off.WithCrypto(mode == "savp", keys...), nil
}

// ProtectRTCP encrypts a plain RTCP compound packet for sending when the
// endpoint is encrypted; ok is false when it is encrypted and the packet
// cannot be protected (the caller drops it, never sends it in the clear).
func (s *Secure) ProtectRTCP(pkt []byte) (out []byte, ok bool) {
	k := s.active.Load()
	if k == nil {
		if s.pending.Load() {
			return nil, false
		}
		return pkt, true
	}
	enc, err := k.protectRTCP(pkt)
	if err != nil {
		return nil, false
	}
	return enc, true
}
