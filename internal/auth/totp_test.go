package auth

import (
	"strings"
	"testing"
	"time"
)

// TestTOTP covers RFC 6238 Appendix B (SHA-1), the tolerance window, replay
// refusal and recovery codes.
func TestTOTP(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		code string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := hotp(key, uint64(v.unix/30), 8); got != v.code {
			t.Errorf("T=%d: got %s want %s", v.unix, got, v.code)
		}
	}

	secret := b32.EncodeToString(key)
	now := time.Unix(1111111109, 0)
	step := TOTPStep(now)
	code := hotp(key, uint64(step), TOTPDigits)

	t.Run("accepts the current step and returns it", func(t *testing.T) {
		if s, ok := VerifyTOTP(secret, code, now, 0); !ok || s != step {
			t.Fatalf("got %d %v", s, ok)
		}
	})
	t.Run("replay of an accepted step is refused", func(t *testing.T) {
		if _, ok := VerifyTOTP(secret, code, now, step); ok {
			t.Fatal("replayed code accepted")
		}
	})
	t.Run("one step either side is tolerated", func(t *testing.T) {
		for _, d := range []int64{-1, 1} {
			c := hotp(key, uint64(step+d), TOTPDigits)
			if _, ok := VerifyTOTP(secret, c, now, 0); !ok {
				t.Errorf("step %+d refused", d)
			}
		}
	})
	t.Run("90 s of clock drift is refused", func(t *testing.T) {
		for _, d := range []int64{-3, 3} {
			c := hotp(key, uint64(step+d), TOTPDigits)
			if _, ok := VerifyTOTP(secret, c, now, 0); ok {
				t.Errorf("step %+d accepted", d)
			}
		}
	})
	t.Run("malformed input is refused", func(t *testing.T) {
		for _, c := range []string{"", "12345", "1234567", "abcdef", code + "0"} {
			if _, ok := VerifyTOTP(secret, c, now, 0); ok {
				t.Errorf("%q accepted", c)
			}
		}
		if _, ok := VerifyTOTP("not base32 !!", code, now, 0); ok {
			t.Error("bad secret accepted")
		}
	})
	t.Run("a spaced code is accepted", func(t *testing.T) {
		if _, ok := VerifyTOTP(secret, code[:3]+" "+code[3:], now, 0); !ok {
			t.Fatal("refused")
		}
	})
}

func TestRecoveryCodes(t *testing.T) {
	plain, hashes := NewRecoveryCodes(RecoveryCodeCount)
	if len(plain) != 10 || len(hashes) != 10 {
		t.Fatalf("got %d codes", len(plain))
	}
	seen := map[string]bool{}
	for i, c := range plain {
		if seen[c] || !LooksLikeRecoveryCode(c) {
			t.Fatalf("code %q duplicate or malformed", c)
		}
		seen[c] = true
		if string(HashRecoveryCode(strings.ToLower(strings.ReplaceAll(c, "-", "")))) != string(hashes[i]) {
			t.Errorf("normalisation changes the hash of %q", c)
		}
	}
	if LooksLikeRecoveryCode("123456") {
		t.Error("a TOTP code looks like a recovery code")
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("Hello PBX", "alice", "ABC")
	for _, w := range []string{"otpauth://totp/Hello%20PBX:alice?", "secret=ABC", "digits=6", "period=30"} {
		if !strings.Contains(u, w) {
			t.Errorf("%q missing from %q", w, u)
		}
	}
}
