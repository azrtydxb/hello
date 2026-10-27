package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/secret"
)

func totpStore(t *testing.T) *Store {
	t.Helper()
	box, err := secret.New("MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	if err != nil {
		t.Fatal(err)
	}
	return scratchStore(t).WithSecretBox(box)
}

// codeAt is the TOTP code of secret at time at.
func codeAt(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	c, err := auth.TOTPCode(secretB32, auth.TOTPStep(at))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func enrol(t *testing.T, s *Store, id int64, now time.Time) (secretB32 string, recovery []string) {
	t.Helper()
	ctx := context.Background()
	secretB32 = auth.NewTOTPSecret()
	if err := s.BeginTOTPSetup(ctx, id, secretB32); err != nil {
		t.Fatal(err)
	}
	rc, err := s.EnableTOTP(ctx, id, codeAt(t, secretB32, now), now)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	return secretB32, rc
}

// TestTOTPStore fails if enrolment stores the secret in clear or activates
// it without a valid code, a code can be replayed, a recovery code works
// twice, five wrong codes do not end a challenge, a challenge works twice or
// after it expires, or the 2FA policy lets an admin lock every admin out.
func TestTOTPStore(t *testing.T) {
	ctx := context.Background()
	s := totpStore(t)
	now := time.Now()
	root := newAccount(t, s, "root", auth.RoleAdmin)
	other := newAccount(t, s, "other", auth.RoleAdmin)
	alice := newAccount(t, s, "alice", auth.RoleUser)

	t.Run("setup is sealed and inactive until a valid code", func(t *testing.T) {
		sec := auth.NewTOTPSecret()
		if err := s.BeginTOTPSetup(ctx, alice.ID, sec); err != nil {
			t.Fatal(err)
		}
		var enc []byte
		if err := s.db.QueryRowContext(ctx, `SELECT totp_secret_enc FROM users WHERE id = $1`, alice.ID).Scan(&enc); err != nil {
			t.Fatal(err)
		}
		if len(enc) == 0 || string(enc) == sec || bytes.Contains(enc, []byte(sec)) {
			t.Fatal("secret is not sealed")
		}
		if st, _ := s.TwoFactorStatus(ctx, alice.ID); st.Enabled || !st.Pending {
			t.Fatalf("status = %+v", st)
		}
		if _, err := s.EnableTOTP(ctx, alice.ID, "000000", now); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("wrong code = %v", err)
		}
		rc, err := s.EnableTOTP(ctx, alice.ID, codeAt(t, sec, now), now)
		if err != nil || len(rc) != auth.RecoveryCodeCount {
			t.Fatalf("enable = %d codes, %v", len(rc), err)
		}
		if st, _ := s.TwoFactorStatus(ctx, alice.ID); !st.Enabled || st.RecoveryLeft != 10 {
			t.Fatalf("status = %+v", st)
		}
		if err := s.BeginTOTPSetup(ctx, alice.ID, auth.NewTOTPSecret()); !errors.Is(err, ErrTOTPEnrolled) {
			t.Fatalf("setup while active = %v", err)
		}
	})

	t.Run("a copied secret does not open under another user", func(t *testing.T) {
		var enc []byte
		_ = s.db.QueryRowContext(ctx, `SELECT totp_secret_enc FROM users WHERE id = $1`, alice.ID).Scan(&enc)
		if _, err := s.box.Open(enc, secret.TOTPAAD(root.ID)); err == nil {
			t.Fatal("opened under the wrong AAD")
		}
	})

	t.Run("challenge: replay, expiry, single use and attempt limit", func(t *testing.T) {
		var sec string
		sec, rc := enrolFresh(t, s, "bob", now)
		bob := userByName(t, s, "bob")
		mk := func(ttl time.Duration) []byte {
			_, h := auth.NewToken()
			if err := s.CreateMFAChallenge(ctx, bob.ID, h, now.Add(ttl)); err != nil {
				t.Fatal(err)
			}
			return h
		}
		step := now.Add(2 * time.Minute) // a later step than the enrolment code
		h := mk(time.Hour)
		if _, err := s.CompleteMFAChallenge(ctx, h, "123456", step); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("wrong code = %v", err)
		}
		code := codeAt(t, sec, step)
		if r, err := s.CompleteMFAChallenge(ctx, h, code, step); err != nil || r.UserID != bob.ID {
			t.Fatalf("right code = %+v, %v", r, err)
		}
		if _, err := s.CompleteMFAChallenge(ctx, h, code, step); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("spent challenge = %v", err)
		}
		// The same step on a fresh challenge is a replay.
		if _, err := s.CompleteMFAChallenge(ctx, mk(time.Hour), code, step); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("replayed code = %v", err)
		}
		// An expired challenge refuses even a right code.
		later := now.Add(4 * time.Minute)
		if _, err := s.CompleteMFAChallenge(ctx, mk(time.Second), codeAt(t, sec, later), later); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("expired challenge = %v", err)
		}
		// Five wrong codes end the challenge: the sixth is refused with a right one.
		later = now.Add(6 * time.Minute)
		h = mk(time.Hour)
		for i := 0; i < 5; i++ {
			if _, err := s.CompleteMFAChallenge(ctx, h, "000000", later); !errors.Is(err, ErrMFAInvalid) {
				t.Fatalf("wrong code %d = %v", i, err)
			}
		}
		if _, err := s.CompleteMFAChallenge(ctx, h, codeAt(t, sec, later), later); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("challenge survived five wrong codes: %v", err)
		}
		// A recovery code works once.
		h = mk(time.Hour)
		r, err := s.CompleteMFAChallenge(ctx, h, rc[0], later)
		if err != nil || !r.Recovery || r.RecoveryLeft != 9 {
			t.Fatalf("recovery = %+v, %v", r, err)
		}
		if _, err := s.CompleteMFAChallenge(ctx, mk(time.Hour), rc[0], later); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("recovery code reused = %v", err)
		}
	})

	t.Run("ten wrong codes in ten minutes refuse the user", func(t *testing.T) {
		sec, _ := enrolFresh(t, s, "erin", now)
		erin := userByName(t, s, "erin")
		at := now.Add(10 * time.Minute)
		for i := 0; i < 2; i++ {
			_, h := auth.NewToken()
			_ = s.CreateMFAChallenge(ctx, erin.ID, h, at.Add(time.Hour))
			for j := 0; j < 5; j++ {
				_, _ = s.CompleteMFAChallenge(ctx, h, "000000", at)
			}
		}
		_, h := auth.NewToken()
		_ = s.CreateMFAChallenge(ctx, erin.ID, h, at.Add(time.Hour))
		if _, err := s.CompleteMFAChallenge(ctx, h, codeAt(t, sec, at), at); !errors.Is(err, ErrMFAInvalid) {
			t.Fatalf("user limit not applied: %v", err)
		}
	})

	t.Run("policy: admins cannot be required by a setter without 2FA", func(t *testing.T) {
		if err := s.SetRequire2FARoles(ctx, "user:root", root.ID, []string{"admin"}); !errors.Is(err, ErrTwoFactorRequired) {
			t.Fatalf("require admin without 2FA = %v", err)
		}
		tr := true
		if _, err := s.UpdateAccount(ctx, "user:root", other.ID, AccountChange{TOTPRequired: &tr, SetterID: root.ID}); !errors.Is(err, ErrTwoFactorRequired) {
			t.Fatalf("totp_required on an admin without 2FA = %v", err)
		}
		if err := s.SetRequire2FARoles(ctx, "user:root", root.ID, []string{"viewer"}); err != nil {
			t.Fatalf("require viewer: %v", err)
		}
		enrol(t, s, root.ID, now.Add(20*time.Minute))
		if err := s.SetRequire2FARoles(ctx, "user:root", root.ID, []string{"admin"}); err != nil {
			t.Fatalf("require admin with 2FA = %v", err)
		}
		p, _ := s.GetAuthSettings(ctx)
		if len(p.Require2FARoles) != 1 || p.Require2FARoles[0] != "admin" {
			t.Fatalf("policy = %+v", p)
		}
	})

	t.Run("an unenrolled required user is flagged on the session", func(t *testing.T) {
		_, h := auth.NewToken()
		if err := s.CreateSession(ctx, other.ID, h, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		a, err := s.SessionActor(ctx, h)
		if err != nil || !a.MustEnrollMFA {
			t.Fatalf("other (admin, required, not enrolled) = %+v, %v", a, err)
		}
		_, h = auth.NewToken()
		_ = s.CreateSession(ctx, root.ID, h, time.Now().Add(time.Hour))
		if a, _ := s.SessionActor(ctx, h); a.MustEnrollMFA {
			t.Fatal("an enrolled admin is limited")
		}
	})

	t.Run("reset: the last enrolled admin is refused over the API, not by the CLI", func(t *testing.T) {
		if err := s.ResetTOTP(ctx, "user:other", root.ID, true); !errors.Is(err, ErrLastEnrolledAdmin) {
			t.Fatalf("reset last enrolled admin = %v", err)
		}
		if err := s.ResetTOTP(ctx, "user:root", alice.ID, true); err != nil {
			t.Fatalf("reset alice: %v", err)
		}
		if st, _ := s.TwoFactorStatus(ctx, alice.ID); st.Enabled || st.Pending || st.RecoveryLeft != 0 {
			t.Fatalf("alice after reset = %+v", st)
		}
		if err := s.ResetTOTPByName(ctx, "cli", "root"); err != nil {
			t.Fatalf("break-glass reset: %v", err)
		}
	})
}

func enrolFresh(t *testing.T, s *Store, name string, now time.Time) (string, []string) {
	t.Helper()
	u := newAccount(t, s, name, auth.RoleUser)
	return enrol(t, s, u.ID, now)
}

func userByName(t *testing.T, s *Store, name string) User {
	t.Helper()
	var id int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT id FROM users WHERE username = $1`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUser(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
