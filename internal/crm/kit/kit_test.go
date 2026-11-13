package kit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit/kittest"
)

func TestSamePhoneAcrossFormats(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"+32 475 12 34 56", "+32475123456", true},
		{"0475/12.34.56", "+32475123456", true},
		{"(555) 123-4567", "+15551234567", true},
		{"+32475123456", "+32475123457", false},
		{"123456", "+32123456", false}, // too short to trust
		{"", "+32475123456", false},
	} {
		if got := Same(c.a, c.b); got != c.want {
			t.Errorf("Same(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if Last4("+32 475 12 34 56") != "3456" {
		t.Fatal("Last4")
	}
}

func TestHostAndLabelValidation(t *testing.T) {
	for _, ok := range []string{"acme.halopsa.com", "api-na.myconnectwise.net"} {
		if !Host(ok) {
			t.Errorf("%q should be a host", ok)
		}
	}
	for _, bad := range []string{"https://x.com", "x.com/path", "x.com:8443", "localhost", "10.0.0.1", "a b.com", "-x.com"} {
		if Host(bad) {
			t.Errorf("%q must not be a host", bad)
		}
	}
	if !Label("acme-2") || Label("ac.me") || Label("-a") || Label("") {
		t.Fatal("Label")
	}
}

func TestDoMapsFailures(t *testing.T) {
	srv := kittest.New(t)
	srv.JSON("GET /ok", 200, `{"a":1}`)
	srv.JSON("GET /401", 401, `{"error":"bad token"}`)
	srv.Handle("GET /429", func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"description":"slow  down\nnow"}`))
	})
	srv.JSON("GET /500", 500, `oops`)
	srv.JSON("GET /422", 422, `{"errors":[{"message":"name: blank"},{"message":"x"}]}`)
	srv.JSON("GET /bad", 200, `<html>`)
	do := func(path string, authed bool, out any) error {
		return Do(context.Background(), srv.Client, Request{Method: "GET", URL: srv.URL + path, Authed: authed}, out)
	}
	var out struct{ A int }
	if err := do("/ok", true, &out); err != nil || out.A != 1 {
		t.Fatalf("ok: %v %+v", err, out)
	}
	if err := do("/401", true, nil); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("401 authed = %v", err)
	}
	var ae *crm.APIError
	if err := do("/401", false, nil); !errors.As(err, &ae) || ae.Status != 401 || ae.Retry {
		t.Fatalf("401 unauthed = %v", err)
	}
	if err := do("/429", true, nil); !errors.As(err, &ae) || !ae.Retry || ae.After != 7*time.Second || ae.Msg != "slow down now" {
		t.Fatalf("429 = %+v", ae)
	}
	if err := do("/500", true, nil); !errors.As(err, &ae) || !ae.Retry || ae.Msg != "Internal Server Error" {
		t.Fatalf("500 = %+v", ae)
	}
	if err := do("/422", true, nil); !errors.As(err, &ae) || ae.Retry || ae.Msg != "name: blank; x" {
		t.Fatalf("422 = %+v", ae)
	}
	if err := do("/bad", true, &out); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("bad json = %v", err)
	}
	// The URL never reaches an error.
	if err := Do(context.Background(), srv.Client, Request{Method: "GET", URL: "http://127.0.0.1:1/secret-path?token=abc"}, nil); err == nil || strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "abc") {
		t.Fatalf("network error leaks: %v", err)
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := RetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d < 89*time.Second || d > 91*time.Second {
		t.Fatalf("date = %v", d)
	}
	if RetryAfter("", now) != 0 || RetryAfter("-3", now) != 0 {
		t.Fatal("empty or negative")
	}
}

func TestCallTextAndEscaping(t *testing.T) {
	l := crm.CallLog{
		CallID: "c1", Direction: "inbound", Number: "+32475123456", From: "+32475123456", To: "+3211223344", Extension: "201",
		StartedAt: time.Date(2026, 11, 12, 14, 3, 5, 0, time.UTC), Duration: 192 * time.Second, Outcome: crm.OutcomeAnswered,
		RecordingURL: `https://h.example/r/a"b<.wav`, Contact: &crm.Contact{Name: "Ann <script>"},
	}
	txt := Text(l)
	for _, want := range []string{"Inbound call: +32475123456 to +3211223344", "Extension: 201", "talk time 3m 12s", "2026-11-12 14:03:05 UTC", "Recording: https://h.example/r/", "call c1"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	h := HTML(l)
	if strings.Contains(h, "<script>") || strings.Contains(h, `a"b<`) || !strings.Contains(h, `<a href="https://h.example/r/a&quot;b&lt;.wav">`) {
		t.Fatalf("html not escaped: %s", h)
	}
	if Title(l) != "Call from Ann <script>" {
		t.Fatalf("title = %q", Title(l))
	}
	l.Missed, l.Outcome, l.RecordingURL = true, crm.OutcomeNoAnswer, ""
	if !strings.HasPrefix(Title(l), "Missed call from") || strings.Contains(Text(l), "Recording:") || !strings.Contains(Text(l), "Missed inbound call") {
		t.Fatalf("missed text = %q", Text(l))
	}
	l.Missed, l.Direction = false, "outbound"
	if !strings.HasPrefix(Title(l), "Call to ") {
		t.Fatal("outbound title")
	}
	if Clock(3723*time.Second) != "1h 2m 3s" || Clock(9*time.Second) != "9s" {
		t.Fatal("Clock")
	}
}
