// Package auth holds hello-control's credential handling: bcrypt passwords,
// session and API-token generation and hashing, device SIP secrets and their
// digest HA1 values, the bootstrap admin, and the middleware that
// authenticates /api/v1 requests.
package auth

import (
	"crypto/md5" //nolint:gosec // RFC 3261 digest HA1 is defined over MD5.
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns the bcrypt hash of password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches the bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is compared against when a login names an unknown user, so the
// response time does not reveal whether the user exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("hello-unknown-user"), bcrypt.DefaultCost)

// SpendPasswordCheck burns the time of one bcrypt comparison.
func SpendPasswordCheck(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}

// NewToken returns a new random credential (32 bytes, base64url without
// padding) and its SHA-256 hash, the only form that is stored. It serves for
// both session cookies and API tokens.
func NewToken() (plain string, hash []byte) {
	plain = randomString(32)
	return plain, HashToken(plain)
}

// HashToken is the stored form of a session or API token.
func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

// NewDeviceSecret returns a new device SIP secret: 24 random bytes,
// base64url without padding.
func NewDeviceSecret() string { return randomString(24) }

// HA1 returns hex(MD5) and hex(SHA-256) of "username:realm:secret", the
// digest HA1 values hello-sip verifies against (RFC 3261, RFC 8760).
func HA1(username, realm, secret string) (md5Hex, sha256Hex string) {
	in := []byte(username + ":" + realm + ":" + secret)
	m := md5.Sum(in) //nolint:gosec // RFC 3261 digest HA1 is defined over MD5.
	s := sha256.Sum256(in)
	return hex.EncodeToString(m[:]), hex.EncodeToString(s[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ErrNoCredentials is returned by a Lookup when a credential is unknown,
// expired, or revoked.
var ErrNoCredentials = errors.New("auth: invalid credentials")
