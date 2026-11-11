package auth

import (
	"testing"
	"time"
)

func TestSplitDerived(t *testing.T) {
	exp := time.Unix(1893456000, 0)
	u := DerivedUsername(DerivedBase(KindWeb, 12), exp)
	if u != "w12.1893456000" {
		t.Fatalf("username = %q", u)
	}
	base, got, ok := SplitDerived(u)
	if !ok || base != "w12" || !got.Equal(exp) {
		t.Fatalf("SplitDerived(%q) = %q %v %v", u, base, got, ok)
	}
	for _, bad := range []string{"", "w12", "w.5", "x12.5", "w12.", "w12.0", "w1a.5", "w12.5x", "desk.5", "w12.99999999999999999999"} {
		if _, _, ok := SplitDerived(bad); ok {
			t.Errorf("SplitDerived(%q) accepted", bad)
		}
	}
}

// TestDerivedSecretVector pins the secret to a known HMAC-SHA256 value, so a
// change of the construction (which would lock every enrolled client out)
// fails here.
func TestDerivedSecretVector(t *testing.T) {
	got := DerivedSecret([]byte("0123456789abcdef0123456789abcdef"), "w12.1893456000", "hello.test")
	// Computed independently (python hmac, hashlib.sha256).
	const want = "2wCcMfpJHVHjgWmuL1oUiMEFEdLt7yuO_tsI-BYUGNU"
	if got != want {
		t.Fatalf("secret = %q, want %q", got, want)
	}
	if other := DerivedSecret([]byte("0123456789abcdef0123456789abcdeg"), "w12.1893456000", "hello.test"); other == got {
		t.Fatal("secret does not depend on the key")
	}
	if other := DerivedSecret([]byte("0123456789abcdef0123456789abcdef"), "w12.1893456001", "hello.test"); other == got {
		t.Fatal("secret does not depend on the expiry")
	}
	if other := DerivedSecret([]byte("0123456789abcdef0123456789abcdef"), "w12.1893456000", "other"); other == got {
		t.Fatal("secret does not depend on the realm")
	}
}
