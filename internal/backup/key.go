// Package backup is Hello's backup and restore (spec pbx-gaps S-2 to S-4):
// a scheduled, encrypted archive of the PostgreSQL database plus a
// content-addressed copy of the MinIO objects, verified after every run, and
// the restore that reads it back.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// keyInfo is the HKDF info string that binds the derived key to backups.
const keyInfo = "hello-backup-v1"

// Key is the archive encryption key derived from HELLO_SECRET_KEY. It is
// never written into an archive; only its fingerprint is.
type Key struct {
	aead cipher.AEAD
	fp   [8]byte
}

// ErrKey is returned for a HELLO_SECRET_KEY that is not 32 bytes of base64.
var ErrKey = errors.New("backup: key must be 32 bytes, base64-encoded")

// DeriveKey derives the backup key from the base64 HELLO_SECRET_KEY with
// HKDF-SHA256 and the info string "hello-backup-v1".
func DeriveKey(secretB64 string) (Key, error) {
	raw, err := base64.StdEncoding.DecodeString(secretB64)
	if err != nil || len(raw) != 32 {
		return Key{}, ErrKey
	}
	k, err := hkdf.Key(sha256.New, raw, nil, keyInfo, 32)
	if err != nil {
		return Key{}, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return Key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Key{}, err
	}
	sum := sha256.Sum256(k)
	out := Key{aead: aead}
	copy(out.fp[:], sum[:8])
	return out, nil
}

// Fingerprint is the first 8 bytes of SHA-256 over the derived key, hex.
func (k Key) Fingerprint() string { return hex.EncodeToString(k.fp[:]) }
