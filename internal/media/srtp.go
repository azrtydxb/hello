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
	"strconv"
	"strings"
	"sync"

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
	if !ok {
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
	savp   bool
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

// LocalCrypto returns the keys Hello offers on leg, generating them on first
// use. Once a key is active the active one is returned alone, so a re-INVITE
// answer repeats it.
func (r *Relay) LocalCrypto(leg string) ([]Crypto, error) {
	l := r.findLeg(leg)
	if l == nil {
		return nil, errors.New("media: unknown relay leg " + leg)
	}
	l.sec.mu.Lock()
	defer l.sec.mu.Unlock()
	if l.sec.keys != nil {
		return []Crypto{l.sec.keys.local}, nil
	}
	if len(l.sec.offered) == 0 {
		for i, s := range OfferedSuites {
			c, err := NewCrypto(i+1, s)
			if err != nil {
				return nil, err
			}
			l.sec.offered = append(l.sec.offered, c)
		}
	}
	return append([]Crypto(nil), l.sec.offered...), nil
}

// AcceptCrypto settles leg on one of the far end's offered keys (Hello is
// the answerer): the first line, in the offerer's order, whose suite Hello
// speaks. It returns the line to put in the answer, which carries the same
// tag and a fresh local key; after it the leg is encrypted. It fails when
// none of the lines is usable.
func (r *Relay) AcceptCrypto(leg string, offers []Crypto, savp bool) (Crypto, error) {
	l := r.findLeg(leg)
	if l == nil {
		return Crypto{}, errors.New("media: unknown relay leg " + leg)
	}
	l.sec.mu.Lock()
	defer l.sec.mu.Unlock()
	for _, o := range offers {
		if !o.Valid() {
			continue
		}
		if k := l.sec.keys; k != nil && k.remote.sameKey(o) {
			return k.local, nil // a re-INVITE repeating the key
		}
		local := Crypto{}
		if k := l.sec.keys; k != nil && k.suite == o.Suite {
			local = k.local
		} else {
			c, err := NewCrypto(o.Tag, o.Suite)
			if err != nil {
				return Crypto{}, err
			}
			local = c
		}
		local.Tag = o.Tag
		keys, err := newLegKeys(local, o, l.sec.keys)
		if err != nil {
			return Crypto{}, err
		}
		keys.savp = savp
		l.sec.keys = keys
		l.sec.offered = nil
		l.secActive.Store(keys)
		return local, nil
	}
	return Crypto{}, errors.New("media: the offer has no SRTP suite Hello supports")
}

// ApplyAnswerCrypto settles leg on the far end's answer to Hello's offer: the
// answer's line must match the tag and suite of one Hello offered. It
// returns false (and leaves the leg plain) when the answer carries no usable
// line.
func (r *Relay) ApplyAnswerCrypto(leg string, answers []Crypto, savp bool) bool {
	l := r.findLeg(leg)
	if l == nil {
		return false
	}
	l.sec.mu.Lock()
	defer l.sec.mu.Unlock()
	if len(answers) == 0 {
		return false
	}
	cands := l.sec.offered
	if l.sec.keys != nil {
		cands = []Crypto{l.sec.keys.local}
	}
	for _, a := range answers {
		if !a.Valid() {
			continue
		}
		for _, o := range cands {
			if o.Tag != a.Tag || o.Suite != a.Suite {
				continue
			}
			if k := l.sec.keys; k != nil && k.remote.sameKey(a) {
				return true
			}
			keys, err := newLegKeys(o, a, l.sec.keys)
			if err != nil {
				return false
			}
			keys.savp = savp
			l.sec.keys = keys
			l.sec.offered = nil
			l.secActive.Store(keys)
			return true
		}
	}
	return false
}

// Secure reports whether leg's media is SRTP-protected and with which suite.
func (r *Relay) Secure(leg string) (CryptoSuite, bool) {
	l := r.findLeg(leg)
	if l == nil {
		return "", false
	}
	if k := l.secActive.Load(); k != nil {
		return k.suite, true
	}
	return "", false
}

// SecureProfile reports whether leg is encrypted and whether it negotiated
// RTP/SAVP (true) or RTP/AVP with crypto lines (false).
func (r *Relay) SecureProfile(leg string) (savp, on bool) {
	l := r.findLeg(leg)
	if l == nil {
		return false, false
	}
	if k := l.secActive.Load(); k != nil {
		return k.savp, true
	}
	return false, false
}

// SecureLegs counts the legs that are SRTP-protected (0 to 2).
func (r *Relay) SecureLegs() int {
	r.mu.Lock()
	legs := append([]*relayLeg(nil), r.legs...)
	r.mu.Unlock()
	n := 0
	for _, l := range legs {
		if l.secActive.Load() != nil {
			n++
		}
	}
	return n
}

// UpdateRemoteCrypto applies the far end's a=crypto lines from a re-INVITE or
// a re-INVITE answer to an encrypted leg: a line with the active suite and the
// same key changes nothing (the replay window and rollover counter stay), a
// new key replaces the receive key and keeps the send key. It reports whether
// the leg now uses one of the lines; a leg that is plain is never switched on
// by it.
func (r *Relay) UpdateRemoteCrypto(leg string, lines []Crypto) bool {
	l := r.findLeg(leg)
	if l == nil {
		return false
	}
	l.sec.mu.Lock()
	defer l.sec.mu.Unlock()
	cur := l.sec.keys
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
		l.sec.keys = keys
		l.secActive.Store(keys)
		return true
	}
	return false
}

// LearnCrypto applies an SDP's a=crypto lines from the far end of leg: the
// answer to a key Hello offered activates the leg, a repeat or a rekey on an
// encrypted leg updates it. It reports whether the leg is encrypted after it.
// A leg Hello never offered a key on stays plain whatever the lines say.
func (r *Relay) LearnCrypto(leg string, lines []Crypto, savp bool) bool {
	l := r.findLeg(leg)
	if l == nil {
		return false
	}
	if l.secActive.Load() != nil {
		return r.UpdateRemoteCrypto(leg, lines)
	}
	l.sec.mu.Lock()
	pending := len(l.sec.offered) > 0
	l.sec.mu.Unlock()
	if !pending {
		return false
	}
	return r.ApplyAnswerCrypto(leg, lines, savp)
}

// ResetCrypto drops a leg's SRTP state, for when another endpoint takes the
// leg over (a transfer target, a retrieved parked call, a pickup): the keys
// belonged to the previous endpoint. The leg is plain RTP until it is
// negotiated again.
func (r *Relay) ResetCrypto(leg string) {
	l := r.findLeg(leg)
	if l == nil {
		return
	}
	l.sec.mu.Lock()
	l.sec.keys = nil
	l.sec.offered = nil
	l.secActive.Store(nil)
	l.sec.mu.Unlock()
}
