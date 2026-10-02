// Package secret seals values Hello must store but later use in clear —
// trunk passwords, which answer carrier digest challenges and so cannot be
// hashed. It uses AES-256-GCM under HELLO_SECRET_KEY; a sealed value is
// nonce || ciphertext.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Box seals and opens values under one key.
type Box struct{ aead cipher.AEAD }

// ErrKey is returned for a key that is not 32 bytes of base64.
var ErrKey = errors.New("secret: key must be 32 bytes, base64-encoded")

// New builds a Box from a base64-encoded 32-byte key.
func New(keyB64 string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext with a fresh random nonce. The additional data
// binds the ciphertext to its use (e.g. "trunk:42"), so a sealed password
// copied to another row does not open there.
func (b *Box) Seal(plaintext, aad string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad)), nil
}

// Open decrypts a value sealed with the same key and additional data.
func (b *Box) Open(sealed []byte, aad string) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("secret: sealed value too short")
	}
	pt, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(aad))
	if err != nil {
		// Wrong key, wrong row, or tampering; never include the input.
		return "", fmt.Errorf("secret: cannot open value for %s", aad)
	}
	return string(pt), nil
}
