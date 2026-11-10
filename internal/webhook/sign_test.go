package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSignKnownAnswer fails if the signature differs from the one documented
// for receivers: HMAC-SHA256 over "<t>.<body>" in hex. The expected value was
// computed independently with openssl.
func TestSignKnownAnswer(t *testing.T) {
	got := Sign("whsec_test_secret", 1700000000, []byte(`{"id":"evt_1"}`))
	const want = "248a374f50f943a28b0f6ab50faf9a7e7e29b710fa26df9fb1618b9bf8ea9c9a"
	if got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

// TestVerify fails if a valid signature is refused, or a stale timestamp, a
// tampered body, a wrong secret or a malformed header is accepted, or if a
// rotation window (two v1 values) does not verify under either secret.
func TestVerify(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"id":"evt_1","type":"call.ended"}`)
	one := SignatureHeader([]string{"new"}, now, body)
	both := SignatureHeader([]string{"new", "old"}, now, body)
	if strings.Count(both, "v1=") != 2 || !strings.HasPrefix(both, "t=1700000000,") {
		t.Fatalf("header = %s", both)
	}
	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		secrets []string
		at      time.Time
		want    error
	}{
		{"valid", one, body, []string{"new"}, now, nil},
		{"within tolerance", one, body, []string{"new"}, now.Add(4 * time.Minute), nil},
		{"early within tolerance", one, body, []string{"new"}, now.Add(-4 * time.Minute), nil},
		{"rotation, new secret", both, body, []string{"new"}, now, nil},
		{"rotation, old secret", both, body, []string{"old"}, now, nil},
		{"stale", one, body, []string{"new"}, now.Add(6 * time.Minute), ErrSignatureStale},
		{"from the future", one, body, []string{"new"}, now.Add(-6 * time.Minute), ErrSignatureStale},
		{"tampered body", one, []byte(`{"id":"evt_2"}`), []string{"new"}, now, ErrSignatureMismatch},
		{"wrong secret", one, body, []string{"other"}, now, ErrSignatureMismatch},
		{"no secret", one, body, nil, now, ErrSignatureMismatch},
		{"no timestamp", "v1=00", body, []string{"new"}, now, ErrSignatureFormat},
		{"no signature", "t=1700000000", body, []string{"new"}, now, ErrSignatureFormat},
		{"not hex", "t=1700000000,v1=zz", body, []string{"new"}, now, ErrSignatureFormat},
		{"empty", "", body, []string{"new"}, now, ErrSignatureFormat},
	} {
		if got := Verify(tc.header, tc.body, tc.secrets, tc.at, 0); !errors.Is(got, tc.want) {
			t.Errorf("%s: Verify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRetryDelay fails if the retry schedule is not 10 s, 30 s, 2 min,
// 10 min, 1 h, 4 h and 12 h (plus or minus 20 percent of jitter), if the
// series does not end after the seventh retry, or if the delivery does not
// get eight attempts in about 17 hours.
func TestRetryDelay(t *testing.T) {
	want := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 4 * time.Hour, 12 * time.Hour}
	var total time.Duration
	for i, w := range want {
		d, ok := RetryDelay(i+1, func() float64 { return 0.5 })
		if !ok || d != w {
			t.Fatalf("RetryDelay(%d) = %v, %v; want %v", i+1, d, ok, w)
		}
		lo, _ := RetryDelay(i+1, func() float64 { return 0 })
		hi, _ := RetryDelay(i+1, func() float64 { return 0.999999 })
		if lo != time.Duration(float64(w)*0.8) || hi < time.Duration(float64(w)*1.19) || hi > time.Duration(float64(w)*1.2) {
			t.Fatalf("jitter of %v spans %v to %v", w, lo, hi)
		}
		total += w
	}
	if _, ok := RetryDelay(len(want)+1, nil); ok {
		t.Fatal("the series does not end")
	}
	if _, ok := RetryDelay(0, nil); ok {
		t.Fatal("attempt 0 has a retry delay")
	}
	if MaxAttempts != 8 || total < 17*time.Hour || total > 18*time.Hour {
		t.Fatalf("attempts %d, total %v", MaxAttempts, total)
	}
}

// TestClassify fails if a status is classified against the spec: 2xx
// succeeds; 5xx, 408, 429 and network errors retry; 410 is gone; every
// other status, a redirect included, is a permanent failure.
func TestClassify(t *testing.T) {
	for status, want := range map[int]Outcome{
		200: OutcomeSuccess, 201: OutcomeSuccess, 204: OutcomeSuccess,
		500: OutcomeRetry, 502: OutcomeRetry, 503: OutcomeRetry, 408: OutcomeRetry, 429: OutcomeRetry,
		410: OutcomeGone,
		400: OutcomeDead, 401: OutcomeDead, 403: OutcomeDead, 404: OutcomeDead, 422: OutcomeDead,
		301: OutcomeDead, 302: OutcomeDead, 307: OutcomeDead,
	} {
		if got := Classify(status, nil); got != want {
			t.Errorf("Classify(%d) = %v, want %v", status, got, want)
		}
	}
	if Classify(0, errors.New("connection refused")) != OutcomeRetry {
		t.Error("a network error must be retried")
	}
}

// TestParseRetryAfter fails if seconds and HTTP dates are not read, a past
// date is not zero, or a long wait is not capped at one hour.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", 0}, {"30", 30 * time.Second}, {"-5", 0}, {"junk", 0}, {"99999", time.Hour},
		{now.Add(2 * time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT"), 2 * time.Minute},
		{now.Add(-2 * time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT"), 0},
	} {
		if got := ParseRetryAfter(tc.in, now); got != tc.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestErrorTextHidesTheURL fails if a stored error carries the request URL,
// which may hold a token.
func TestErrorTextHidesTheURL(t *testing.T) {
	err := errors.New(`Post "https://hooks.example.com/x?token=abc": dial tcp: lookup failed`)
	if got := ErrorText(0, err); strings.Contains(got, "token") || strings.Contains(got, "example") {
		t.Fatalf("ErrorText = %q", got)
	}
}
