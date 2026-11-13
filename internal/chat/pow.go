package chat

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/bits"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The proof-of-work challenge (spec M-29a.1): stateless, so no table and no
// third party. The server signs a nonce that names the site, the difficulty,
// an expiry and the client's address; the client finds a counter so that
// SHA-256(nonce ":" counter) starts with that many zero bits. A nonce works
// once (this pod remembers the ones it saw until they expire) and only for
// the address it was issued to.

// Challenge limits.
const (
	powTTL     = 3 * time.Minute
	MinPowBits = 14
	MaxPowBits = 22
	maxSeen    = 200000
)

var (
	errChallenge = errors.New("chat: the challenge is not valid")
)

// Pow issues and checks proof-of-work challenges under a key every replica
// shares.
type Pow struct {
	key []byte
	now func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewPow returns a Pow signing with key (derived from HELLO_SECRET_KEY, so
// replicas agree).
func NewPow(key []byte, now func() time.Time) *Pow {
	if now == nil {
		now = time.Now
	}
	return &Pow{key: key, now: now, seen: map[string]time.Time{}}
}

func (p *Pow) mac(parts ...string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(strings.Join(parts, "|")))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

func (p *Pow) clientTag(ip string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte("client|" + ip))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:6])
}

// Issue returns a nonce for a site and client address at the given
// difficulty (clamped to 14..22 bits).
func (p *Pow) Issue(siteID int64, ip string, diff int) (nonce string, bitsUsed int) {
	diff = min(max(diff, MinPowBits), MaxPowBits)
	r := make([]byte, 12)
	_, _ = rand.Read(r)
	body := strings.Join([]string{
		"1", strconv.FormatInt(siteID, 10), strconv.Itoa(diff),
		strconv.FormatInt(p.now().Add(powTTL).Unix(), 10), base64.RawURLEncoding.EncodeToString(r), p.clientTag(ip),
	}, ".")
	return body + "." + p.mac("pow", body), diff
}

// Verify checks a solved challenge for a site and client address: the
// signature, the expiry, the address, that it is not a repeat, that the
// difficulty is at least minBits, and the work itself.
func (p *Pow) Verify(nonce, solution string, siteID int64, ip string, minBits int) error {
	if len(nonce) > 200 || len(solution) > 24 || solution == "" {
		return errChallenge
	}
	i := strings.LastIndexByte(nonce, '.')
	if i < 0 {
		return errChallenge
	}
	body, sig := nonce[:i], nonce[i+1:]
	if !hmac.Equal([]byte(sig), []byte(p.mac("pow", body))) {
		return errChallenge
	}
	f := strings.Split(body, ".")
	if len(f) != 6 || f[0] != "1" || f[1] != strconv.FormatInt(siteID, 10) || f[5] != p.clientTag(ip) {
		return errChallenge
	}
	diff, err1 := strconv.Atoi(f[2])
	exp, err2 := strconv.ParseInt(f[3], 10, 64)
	if err1 != nil || err2 != nil || diff < max(minBits, MinPowBits) || diff > MaxPowBits {
		return errChallenge
	}
	now := p.now()
	if now.Unix() > exp {
		return errChallenge
	}
	if leadingZeroBits(sha256.Sum256([]byte(nonce+":"+solution))) < diff {
		return errChallenge
	}
	// A nonce works once on this pod.
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.seen[nonce]; dup {
		return errChallenge
	}
	if len(p.seen) >= maxSeen {
		for k, t := range p.seen {
			if now.After(t) {
				delete(p.seen, k)
			}
		}
		if len(p.seen) >= maxSeen {
			return errChallenge
		}
	}
	p.seen[nonce] = time.Unix(exp, 0)
	return nil
}

func leadingZeroBits(h [32]byte) int {
	n := 0
	for _, b := range h {
		if b == 0 {
			n += 8
			continue
		}
		return n + bits.LeadingZeros8(b)
	}
	return n
}

// Solve finds a solution; the widget does this in the browser, and the tests
// use it.
func Solve(nonce string, diff int) string {
	for c := 0; ; c++ {
		s := strconv.Itoa(c)
		if leadingZeroBits(sha256.Sum256([]byte(nonce+":"+s))) >= diff {
			return s
		}
	}
}

// newToken returns 256 random bits, URL-safe.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
