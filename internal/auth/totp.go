package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 authenticator apps use HMAC-SHA1 (spec pbx-gaps D9).
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (spec pbx-gaps D9): RFC 6238, HMAC-SHA1, 6 digits, 30 s
// steps, one step of tolerance either way.
const (
	TOTPDigits   = 6
	TOTPPeriod   = 30
	totpWindow   = 1
	totpSecretLn = 20
	// RecoveryCodeCount is how many single-use recovery codes enrolment issues.
	RecoveryCodeCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 without padding.
func NewTOTPSecret() string {
	b := make([]byte, totpSecretLn)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand: " + err.Error())
	}
	return b32.EncodeToString(b)
}

// TOTPURI is the otpauth:// URI an authenticator app enrols from.
func TOTPURI(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(TOTPDigits))
	v.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + v.Encode()
}

// hotp is RFC 4226 over HMAC-SHA1 with the given digit count.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// TOTPCode is the code for a step of the base32 secret; authenticator apps
// compute it themselves, so only tests and the enrolment check need it.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(step), TOTPDigits), nil //nolint:gosec // G115: steps are positive.
}

// TOTPStep is the 30 s step number of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// VerifyTOTP checks code against the base32 secret at time now. It accepts
// the current step and one either side, but only a step after lastStep, so an
// accepted code cannot be used again. It returns the step that matched.
// Every candidate step is compared in constant time.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if err != nil || len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for s := cur - totpWindow; s <= cur+totpWindow; s++ {
		want := hotp(key, uint64(s), TOTPDigits) //nolint:gosec // G115: steps are positive.
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && s > lastStep && s > step {
			step, ok = s, true
		}
	}
	return step, ok
}

// NewRecoveryCodes returns n fresh recovery codes (80 random bits each,
// shown as four groups of four base32 characters) and their stored hashes.
func NewRecoveryCodes(n int) (plain []string, hashes [][]byte) {
	for i := 0; i < n; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			panic("auth: crypto/rand: " + err.Error())
		}
		s := b32.EncodeToString(b)
		c := s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
		plain = append(plain, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return plain, hashes
}

// NormalizeRecoveryCode folds case and drops separators, so a code typed
// without its dashes still matches.
func NormalizeRecoveryCode(c string) string {
	c = strings.ToUpper(c)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, c)
}

// LooksLikeRecoveryCode reports whether c has the shape of a recovery code
// rather than a 6-digit TOTP code.
func LooksLikeRecoveryCode(c string) bool { return len(NormalizeRecoveryCode(c)) == 16 }

// HashRecoveryCode is the stored form of a recovery code.
func HashRecoveryCode(c string) []byte {
	h := sha256.Sum256([]byte(NormalizeRecoveryCode(c)))
	return h[:]
}
