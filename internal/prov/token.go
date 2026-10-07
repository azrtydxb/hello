package prov

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"hash/fnv"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// tokenEncoding is base32 lowercase without padding: 32 bytes become 52
// characters of [a-z2-7].
var tokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// TokenLen is the length of a provisioning token.
const TokenLen = 52

// NewToken returns a new provisioning token (32 random bytes, base32
// lowercase without padding) and its SHA-256, the only form Hello looks it
// up by (spec S-3).
func NewToken() (plain string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (it panics instead)
	plain = tokenEncoding.EncodeToString(b)
	return plain, HashToken(plain)
}

// HashToken is a token's lookup hash, the same SHA-256 auth.HashToken
// stores API tokens by.
func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

// tokenRE is a well-formed token; anything else is unknown without a
// database lookup.
var tokenRE = regexp.MustCompile(`^[a-z2-7]{52}$`)

// redactRE finds token-like runs anywhere in a path, in any case, so a
// token pasted into another position or mistyped in case is redacted too.
var redactRE = regexp.MustCompile(`[A-Za-z2-7]{52,}`)

// RedactPath replaces the token in a provisioning path: /p/<token>/… becomes
// /p/****/…. The second segment under /p/ is redacted whatever it holds,
// unless it is one of the fixed public names, so a near-miss of a real
// token is not recorded either; any other token-like run is redacted too.
// The result is cut to 512 bytes.
func RedactPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/p/"); ok {
		seg, tail, _ := strings.Cut(rest, "/")
		switch seg {
		case "boot", "ca.crt", "ca.der", "":
		default:
			sep := ""
			if strings.Contains(rest, "/") {
				sep = "/"
			}
			p = "/p/****" + sep + tail
		}
	}
	p = redactRE.ReplaceAllString(p, "****")
	if len(p) > 512 {
		p = strings.ToValidUTF8(p[:512], "")
	}
	return p
}

// Tokens is a phone's token columns (spec S-3): the current token's hash
// and sealed value, and the previous token's hash with the end of its
// grace. It never holds a plaintext token. This is the reference for the
// rolling rotation the store implements in SQL.
type Tokens struct {
	Hash        []byte
	Enc         []byte // the current token sealed with PhoneTokenAAD
	PrevHash    []byte // nil when no previous token is in grace
	PrevExpires time.Time
}

// Rotate makes (hash, enc) the current token. The old current token stays
// valid as the previous one until now+grace or the first fetch with the new
// token; immediate revokes it at once (a stolen-token response, re-arm).
func (t Tokens) Rotate(hash, enc []byte, now time.Time, grace time.Duration, immediate bool) Tokens {
	n := Tokens{Hash: hash, Enc: enc}
	if !immediate && grace > 0 {
		n.PrevHash, n.PrevExpires = t.Hash, now.Add(grace)
	}
	return n
}

// Match reports whether hash is the current token, or the previous one
// still in grace at now (viaPrevious).
func (t Tokens) Match(hash []byte, now time.Time) (viaPrevious, ok bool) {
	switch {
	case len(hash) == 0:
		return false, false
	case bytes.Equal(hash, t.Hash):
		return false, true
	case t.PrevHash != nil && bytes.Equal(hash, t.PrevHash) && now.Before(t.PrevExpires):
		return true, true
	}
	return false, false
}

// InGrace reports whether a previous token is still valid at now.
func (t Tokens) InGrace(now time.Time) bool { return t.PrevHash != nil && now.Before(t.PrevExpires) }

// Promote ends the previous token's grace: the phone fetched with the new
// one.
func (t Tokens) Promote() Tokens { return Tokens{Hash: t.Hash, Enc: t.Enc} }

// URLs a phone is given. The plain-HTTP forms (boot path, CA) use the
// public URL's host with the http scheme.

// DeviceURL is a phone's own provisioning URL, https://…/p/<token>/.
func DeviceURL(public *url.URL, token string) string {
	return public.Scheme + "://" + public.Host + "/p/" + token + "/"
}

// FirmwareURL is the URL a phone downloads a firmware file from.
func FirmwareURL(public *url.URL, token, filename string) string {
	return DeviceURL(public, token) + "fw/" + url.PathEscape(filename)
}

// BootURL is the DHCP option 66 value, http://…/p/boot/.
func BootURL(public *url.URL) string { return "http://" + public.Host + "/p/boot/" }

// CAURL is the plain-HTTP CA certificate URL.
func CAURL(public *url.URL) string { return "http://" + public.Host + "/p/ca.crt" }

// ResyncSeconds is a phone's re-check interval: base plus a jitter of up
// to a tenth of it derived from the MAC, so phones booted together do not
// re-check together, and one phone always gets the same value (stable
// ETag).
func ResyncSeconds(mac string, base time.Duration) int {
	s := int(base / time.Second)
	if s < 10 {
		return max(s, 1)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(mac))
	return s + int(h.Sum32())%(s/10)
}
