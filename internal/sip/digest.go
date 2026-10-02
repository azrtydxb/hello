package sip

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // RFC 3261 digest requires MD5; SHA-256 is offered first.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// NonceValidity is how long a challenge's nonce is accepted.
const NonceValidity = 5 * time.Minute

// nonceSkew tolerates clock differences between nodes for nonces dated in
// the future.
const nonceSkew = 30 * time.Second

// Algorithms, in the order they are offered (RFC 8760 §2.4: preferred first).
const (
	AlgSHA256 = "SHA-256"
	AlgMD5    = "MD5"
)

// Verdict is the outcome of checking digest credentials.
type Verdict int

// Verdicts.
const (
	// Bad: wrong response, unknown algorithm, forged or malformed nonce.
	Bad Verdict = iota
	// Stale: the response is correct but the nonce has expired; the client
	// should retry with the fresh nonce (401 stale=true).
	Stale
	// OK: authenticated.
	OK
)

// Digest issues and verifies stateless nonces: base64url(ts || HMAC-SHA256(
// secret, ts || realm || source IP)), where ts is the issue time in Unix
// seconds. Any node with the same secret and realm accepts another node's
// nonce, but only from the source IP it was issued to, which narrows the
// replay window of a captured response without per-nonce state.
type Digest struct {
	Realm  string
	Secret []byte
	Now    func() time.Time // defaults to time.Now
}

func (d *Digest) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Digest) mac(ts []byte, ip string) []byte {
	m := hmac.New(sha256.New, d.Secret)
	m.Write(ts)
	m.Write([]byte(d.Realm))
	m.Write([]byte{0})
	m.Write([]byte(ip))
	return m.Sum(nil)
}

// Nonce returns a fresh nonce for a client at source IP ip.
func (d *Digest) Nonce(ip string) string {
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(d.now().Unix())) //nolint:gosec // Unix time is positive.
	return base64.RawURLEncoding.EncodeToString(append(ts, d.mac(ts, ip)...))
}

// checkNonce reports whether n is authentic for ip and still fresh.
func (d *Digest) checkNonce(n, ip string) (authentic, fresh bool) {
	raw, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil || len(raw) != 8+sha256.Size {
		return false, false
	}
	ts, sig := raw[:8], raw[8:]
	if !hmac.Equal(sig, d.mac(ts, ip)) {
		return false, false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(ts)), 0) //nolint:gosec // authenticated value we wrote.
	now := d.now()
	if issued.After(now.Add(nonceSkew)) {
		return false, false
	}
	return true, now.Sub(issued) <= NonceValidity
}

// Challenges returns the WWW-Authenticate values to send to a client at
// source IP ip, SHA-256 first.
func (d *Digest) Challenges(stale bool, ip string) []string {
	nonce := d.Nonce(ip)
	out := make([]string, 0, 2)
	for _, alg := range []string{AlgSHA256, AlgMD5} {
		c := digest.Challenge{Realm: d.Realm, Nonce: nonce, Algorithm: alg, QOP: []string{"auth"}, Stale: stale}
		out = append(out, c.String())
	}
	return out
}

// ParseCredentials parses an Authorization header value.
func ParseCredentials(v string) (*digest.Credentials, error) {
	return digest.ParseCredentials(strings.TrimSpace(v))
}

// Verify checks credentials for a request (method and Request-URI) from
// source IP ip against the device's HA1 values. The credentials' uri must
// name the Request-URI (RFC 3261 §22.4), so a response captured for one
// request cannot authorize another.
func (d *Digest) Verify(c *digest.Credentials, method string, ruri sip.Uri, ip, ha1MD5, ha1SHA256 string) Verdict {
	var (
		h   hash.Hash
		ha1 string
	)
	switch strings.ToUpper(c.Algorithm) {
	case "", AlgMD5:
		h, ha1 = md5.New(), ha1MD5 //nolint:gosec // RFC 3261 digest.
	case AlgSHA256:
		h, ha1 = sha256.New(), ha1SHA256
	default:
		return Bad
	}
	if c.Realm != d.Realm || c.QOP != "auth" || c.Cnonce == "" || c.Nc <= 0 || ha1 == "" || !sameURI(c.URI, ruri) {
		return Bad
	}
	ha2 := hexHash(h, method+":"+c.URI)
	want := hexHash(h, fmt.Sprintf("%s:%s:%08x:%s:%s:%s", strings.ToLower(ha1), c.Nonce, c.Nc, c.Cnonce, c.QOP, ha2))
	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(c.Response))) != 1 {
		return Bad
	}
	authentic, fresh := d.checkNonce(c.Nonce, ip)
	switch {
	case !authentic:
		return Bad
	case !fresh:
		return Stale
	default:
		return OK
	}
}

func hexHash(h hash.Hash, s string) string {
	h.Reset()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// sameURI compares a digest uri with the Request-URI after normalising
// scheme and host case and the default port; URI parameters are ignored.
func sameURI(digestURI string, ruri sip.Uri) bool {
	var u sip.Uri
	if err := sip.ParseUri(digestURI, &u); err != nil {
		return false
	}
	port := func(x sip.Uri) int {
		if x.Port == 0 {
			return sip.DefaultPort("udp")
		}
		return x.Port
	}
	return strings.EqualFold(u.Scheme, ruri.Scheme) && u.User == ruri.User &&
		strings.EqualFold(u.Host, ruri.Host) && port(u) == port(ruri)
}
