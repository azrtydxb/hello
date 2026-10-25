package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// User passwords are hashed with argon2id (OWASP's 19 MiB, t=2, p=1
// profile) and stored in the PHC string format. Hashes written before
// argon2id was adopted are bcrypt; CheckUserPassword still accepts them and
// NeedsRehash tells the login to upgrade them. Voicemail PINs stay bcrypt
// (HashPassword): they are verified on the call path, where a memory-hard
// hash per attempt is the wrong trade.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16

	// MinPasswordLen and MaxPasswordLen bound a user password.
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

// ErrPasswordPolicy is wrapped by ValidatePassword failures.
var ErrPasswordPolicy = errors.New("password does not meet the policy")

// ValidatePassword checks a new user password: 10 to 128 bytes.
func ValidatePassword(p string) error {
	if len(p) < MinPasswordLen || len(p) > MaxPasswordLen {
		return fmt.Errorf("%w: must be %d-%d characters", ErrPasswordPolicy, MinPasswordLen, MaxPasswordLen)
	}
	return nil
}

// HashUserPassword returns the argon2id PHC hash of password.
func HashUserPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckUserPassword reports whether password matches hash, an argon2id PHC
// string or a legacy bcrypt hash. An empty or unknown hash never matches.
func CheckUserPassword(hash, password string) bool {
	if !strings.HasPrefix(hash, "$argon2id$") {
		return CheckPassword(hash, password)
	}
	var (
		version         int
		mem, t          uint32
		threads         uint8
		saltB64, keyB64 string
	)
	parts := strings.Split(hash, "$") // "", argon2id, v=19, m=..,t=..,p=.., salt, key
	if len(parts) != 6 {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &threads); err != nil {
		return false
	}
	saltB64, keyB64 = parts[4], parts[5]
	salt, err1 := base64.RawStdEncoding.DecodeString(saltB64)
	want, err2 := base64.RawStdEncoding.DecodeString(keyB64)
	// Bound the cost a stored hash can demand of a login.
	if err1 != nil || err2 != nil || len(want) == 0 || mem > 1<<20 || t > 10 || threads == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash reports whether hash is not an argon2id hash of the current
// parameters, so a login that just verified it should store a fresh one.
func NeedsRehash(hash string) bool {
	return !strings.HasPrefix(hash, fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argonMemoryKiB, argonTime, argonThreads))
}

// dummyUserHash is checked when a login names an unknown or disabled user.
var dummyUserHash, _ = HashUserPassword("hello-unknown-user")

// SpendUserPasswordCheck burns the time of one user password check.
func SpendUserPasswordCheck(password string) {
	_ = CheckUserPassword(dummyUserHash, password)
}
