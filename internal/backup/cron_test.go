package backup

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	loc := time.UTC
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, loc) }
	s, err := ParseSchedule("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Prev(at(2026, 10, 26, 10, 15)); !got.Equal(at(2026, 10, 26, 2, 0)) {
		t.Fatalf("prev %v", got)
	}
	if got := s.Prev(at(2026, 10, 26, 1, 59)); !got.Equal(at(2026, 10, 25, 2, 0)) {
		t.Fatalf("prev before %v", got)
	}
	if got := s.Next(at(2026, 10, 26, 2, 0)); !got.Equal(at(2026, 10, 27, 2, 0)) {
		t.Fatalf("next %v", got)
	}
	s, _ = ParseSchedule("*/15 8-10 * * 1,3")
	if got := s.Next(at(2026, 10, 26, 8, 50)); !got.Equal(at(2026, 10, 26, 9, 0)) { // Monday the 26th
		t.Fatalf("list/range/step %v", got)
	}
	if got := s.Next(at(2026, 10, 26, 10, 50)); !got.Equal(at(2026, 10, 28, 8, 0)) { // next Wednesday
		t.Fatalf("weekday list %v", got)
	}
	s, _ = ParseSchedule("30 3 1 * 7")
	if got := s.Next(at(2026, 10, 26, 0, 0)); !got.Equal(at(2026, 11, 1, 3, 30)) { // the 1st, or a Sunday (Nov 1 is both)
		t.Fatalf("either day field %v", got)
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "a * * * *", "*/0 * * * *", "5-1 * * * *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKeyDerivation(t *testing.T) {
	secret := testSecret(t)
	a, err := DeriveKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DeriveKey(secret)
	if a.Fingerprint() != b.Fingerprint() || len(a.Fingerprint()) != 16 {
		t.Fatalf("fingerprint %q", a.Fingerprint())
	}
	if c, _ := DeriveKey(testSecret(t)); c.Fingerprint() == a.Fingerprint() {
		t.Fatal("different secrets share a fingerprint")
	}
	if _, err := DeriveKey("short"); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestSettingsValidate(t *testing.T) {
	ok := SettingsInput{Enabled: true, Cron: "0 2 * * *", Kind: KindMinio, KeepCount: 14}
	if bad := ok.Validate(); len(bad) != 0 {
		t.Fatalf("%v", bad)
	}
	in := SettingsInput{Cron: "x", Kind: KindS3, KeepCount: 0, NotifyEmail: "nope", S3Endpoint: "ftp://x"}
	bad := in.Validate()
	for _, f := range []string{"cron", "keepCount", "notifyEmail", "s3Endpoint", "s3Bucket"} {
		if bad[f] == "" {
			t.Errorf("no error for %s: %v", f, bad)
		}
	}
	if (SettingsInput{Cron: "0 2 * * *", Kind: "ftp", KeepCount: 1}).Validate()["kind"] == "" {
		t.Error("kind accepted")
	}
}
