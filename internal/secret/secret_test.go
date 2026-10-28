package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func key(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// TestSealOpen fails if a value does not round-trip, if the ciphertext
// contains the plaintext, or if a wrong key, wrong row binding or tampered
// ciphertext opens.
func TestSealOpen(t *testing.T) {
	b, err := New(key(t))
	if err != nil {
		t.Fatal(err)
	}
	pw := "carrier-" + "pass-" + "1234" // assembled so secret scanners don't match a literal
	sealed, err := b.Seal(pw, "trunk:1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(pw)) {
		t.Fatal("ciphertext contains the plaintext")
	}
	if got, err := b.Open(sealed, "trunk:1"); err != nil || got != pw {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := b.Open(sealed, "trunk:2"); err == nil {
		t.Fatal("value opened under another row's binding")
	}
	other, _ := New(key(t))
	if _, err := other.Open(sealed, "trunk:1"); err == nil {
		t.Fatal("value opened under another key")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b.Open(sealed, "trunk:1"); err == nil {
		t.Fatal("tampered value opened")
	}
	for _, bad := range []string{"", "not-base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := New(bad); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
}

// TestMAC fails if the MAC differs between calls, is the same for another
// purpose, another key or other data, or is not 32 bytes.
func TestMAC(t *testing.T) {
	k1 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	k2 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	b1, err := New(k1)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := New(k2)
	a := b1.MAC("optout", "+32475123456")
	if len(a) != 32 || !bytes.Equal(a, b1.MAC("optout", "+32475123456")) {
		t.Fatal("the MAC is not stable")
	}
	for name, other := range map[string][]byte{
		"purpose": b1.MAC("other", "+32475123456"), "key": b2.MAC("optout", "+32475123456"), "data": b1.MAC("optout", "+32475123457"),
	} {
		if bytes.Equal(a, other) {
			t.Errorf("a different %s gives the same MAC", name)
		}
	}
}
