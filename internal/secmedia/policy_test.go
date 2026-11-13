package secmedia

import "testing"

// TestPolicy fails if the three policy names do not parse in any case, if
// anything else is accepted, or if an unknown stored value does not fall back
// to the caller's default.
func TestPolicy(t *testing.T) {
	for in, want := range map[string]Policy{"off": Off, "Optional": Optional, " REQUIRED ": Required} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "on", "maybe", "forced"} {
		if Valid(bad) {
			t.Errorf("%q is valid", bad)
		}
	}
	if got := Normalize("", Optional); got != Optional {
		t.Errorf("Normalize empty = %q", got)
	}
	if got := Normalize("off", Optional); got != Off {
		t.Errorf("Normalize off = %q", got)
	}
}

// TestKeyExchange fails if a key exchange is not parsed case-insensitively, or
// an unknown one is accepted, or the default is not SDES.
func TestKeyExchange(t *testing.T) {
	for in, want := range map[string]string{"sdes": KeyExSDES, " DTLS ": KeyExDTLS, "Dtls": KeyExDTLS} {
		if got, ok := ParseKeyEx(in); !ok || got != want {
			t.Errorf("ParseKeyEx(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "zrtp", "dtls-srtp"} {
		if _, ok := ParseKeyEx(in); ok {
			t.Errorf("ParseKeyEx(%q) accepted", in)
		}
		if got := NormalizeKeyEx(in); got != KeyExSDES {
			t.Errorf("NormalizeKeyEx(%q) = %q, want sdes", in, got)
		}
	}
}
