package twilio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// The SIDs are built at run time: a 34-character literal in the source looks
// like a real Twilio identifier to secret scanners.
var (
	testSID   = "AC" + strings.Repeat("ab", 16)
	testToken = "tok-super-secret-value"
	testMsg   = "SM" + strings.Repeat("cd", 16)
	testSvc   = "MG" + strings.Repeat("ef", 16)
)

func client(t *testing.T, h http.Handler, mod ...func(*provider.Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := provider.Config{AccountSID: testSID, AuthToken: testToken}
	for _, m := range mod {
		m(&cfg)
	}
	c, err := New(cfg, WithBaseURL(srv.URL+"/2010-04-01"), WithHTTPClient(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsBadCredentials(t *testing.T) {
	if _, err := New(provider.Config{AccountSID: "nope", AuthToken: "x"}); err == nil {
		t.Error("a malformed account SID was accepted")
	}
	if _, err := New(provider.Config{AccountSID: testSID}); err == nil {
		t.Error("an empty token was accepted")
	}
}

func TestSendGolden(t *testing.T) {
	var got struct {
		path, user, pass, ctype string
		form                    url.Values
	}
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.user, got.pass, _ = r.BasicAuth()
		got.ctype = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		got.form, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"` + testMsg + `","status":"queued","error_code":null}`))
	}))
	sent, err := c.Send(context.Background(), provider.Outbound{ID: 7, From: "+3270123456", To: "+32475000001", Body: "hello é"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.ProviderID != testMsg || sent.Status != provider.StatusQueued {
		t.Errorf("sent = %+v", sent)
	}
	if got.path != "/2010-04-01/Accounts/"+testSID+"/Messages.json" || got.user != testSID || got.pass != testToken {
		t.Errorf("request = %+v", got)
	}
	if got.ctype != "application/x-www-form-urlencoded" {
		t.Errorf("content type = %q", got.ctype)
	}
	if got.form.Get("From") != "+3270123456" || got.form.Get("To") != "+32475000001" || got.form.Get("Body") != "hello é" {
		t.Errorf("form = %v", got.form)
	}
	if got.form.Has("StatusCallback") || got.form.Has("MediaUrl") {
		t.Errorf("a callback or media was sent: %v", got.form)
	}
}

func TestSendThroughMessagingService(t *testing.T) {
	var form url.Values
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"` + testMsg + `","status":"accepted"}`))
	}), func(c *provider.Config) { c.MessagingServiceSID = testSvc })
	if _, err := c.Send(context.Background(), provider.Outbound{From: "+3270123456", To: "+32475000001", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if form.Get("MessagingServiceSid") == "" || form.Has("From") {
		t.Errorf("form = %v", form)
	}
}

func TestSendErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"opted out", 400, `{"code":21610,"message":"The message From/To pair violates a blacklist rule.","status":400}`,
			func(e error) bool {
				var r *provider.Rejected
				return errors.As(e, &r) && r.Code == provider.CodeOptedOut
			}},
		{"bad number", 400, `{"code":21211,"message":"invalid To"}`,
			func(e error) bool { var r *provider.Rejected; return errors.As(e, &r) && r.Code == "21211" }},
		{"no code", 400, `<html>`,
			func(e error) bool { var r *provider.Rejected; return errors.As(e, &r) && r.Code == "http_400" }},
		{"unauthorized", 401, `{"code":20003}`, func(e error) bool { return errors.Is(e, provider.ErrAuth) }},
		{"throttled", 429, `{}`,
			func(e error) bool {
				var r *provider.Rejected
				return errors.As(e, &r) && r.Code == provider.CodeRateLimited
			}},
		{"server error", 503, `oops`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
		{"created but unreadable", 201, `not json`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
		{"created without sid", 201, `{}`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			_, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "secret customer text"})
			if err == nil || !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			for _, leak := range []string{testToken, testSID, "secret customer text", "violates"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q leaks %q", err, leak)
				}
			}
		})
	}
}

func TestSendTransportFailureIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing listens: the request fails
	c, _ := New(provider.Config{AccountSID: testSID, AuthToken: testToken}, WithBaseURL(base), WithHTTPClient(&http.Client{}))
	_, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "x"})
	if !errors.Is(err, provider.ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error leaks: %v", err)
	}
}

func TestSendNeverFollowsRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		if _, _, ok := r.BasicAuth(); ok {
			t.Error("credentials followed a redirect")
		}
	}))
	defer other.Close()
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	if _, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "x"}); err == nil {
		t.Error("a redirect was treated as success")
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

func TestSendRefusesAnOverlongBody(t *testing.T) {
	c := client(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the carrier was called") }))
	_, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: strings.Repeat("a", 1601)})
	var r *provider.Rejected
	if !errors.As(err, &r) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusMapping(t *testing.T) {
	for twilio, want := range map[string]string{
		"accepted": provider.StatusQueued, "scheduled": provider.StatusQueued, "queued": provider.StatusQueued,
		"sending": provider.StatusQueued, "sent": provider.StatusSent, "delivered": provider.StatusDelivered,
		"read": provider.StatusDelivered, "undelivered": provider.StatusFailed, "failed": provider.StatusFailed,
		"something-new": provider.StatusQueued,
	} {
		c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/2010-04-01/Accounts/"+testSID+"/Messages/"+testMsg+".json" {
				t.Errorf("path = %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"status":"` + twilio + `","error_code":30003}`))
		}))
		st, err := c.Status(context.Background(), testMsg)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != want {
			t.Errorf("%s -> %s, want %s", twilio, st.Status, want)
		}
		if (want == provider.StatusFailed) != (st.ErrorCode == "30003") {
			t.Errorf("%s error code = %q", twilio, st.ErrorCode)
		}
	}
}

func TestStatusNotFoundAndBadID(t *testing.T) {
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	if _, err := c.Status(context.Background(), testMsg); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Status(context.Background(), "../Accounts"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("a path-shaped id reached the carrier: %v", err)
	}
}

func TestValidateAndNumbers(t *testing.T) {
	h := func(account string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "IncomingPhoneNumbers.json"):
				_, _ = w.Write([]byte(`{"incoming_phone_numbers":[{"phone_number":"+3270123456","friendly_name":"Main","capabilities":{"sms":true,"mms":false,"voice":true}}]}`))
			case strings.HasSuffix(r.URL.Path, testSID+".json"):
				_, _ = w.Write([]byte(`{"status":"` + account + `"}`))
			default:
				w.WriteHeader(404)
			}
		})
	}
	c := client(t, h("active"))
	if err := c.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ns, err := c.Numbers(context.Background())
	if err != nil || len(ns) != 1 || ns[0].E164 != "+3270123456" || !ns[0].SMS || ns[0].MMS {
		t.Fatalf("numbers = %+v, %v", ns, err)
	}
	var r *provider.Rejected
	if err := client(t, h("suspended")).Validate(context.Background()); !errors.As(err, &r) || r.Code != "account_suspended" {
		t.Errorf("suspended account: %v", err)
	}
	bad := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	if err := bad.Validate(context.Background()); !errors.Is(err, provider.ErrAuth) {
		t.Errorf("bad credentials: %v", err)
	}
}

func TestRegistered(t *testing.T) {
	if !provider.Known(Kind) {
		t.Fatal("twilio is not registered")
	}
}
