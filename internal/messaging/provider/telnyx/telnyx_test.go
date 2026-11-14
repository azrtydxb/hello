package telnyx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// Built at run time: a literal key looks like a real one to secret scanners.
var (
	testKey  = "KEY" + strings.Repeat("0123456789", 3) + "-do-not-log"
	testMsg  = "40385f64-5717-4562-b3fc-2c963f66afa6"
	testProf = "4001932a-6b2d-4a0c-8f7e-9d5a1c2b3e4f"
)

func client(t *testing.T, h http.Handler, mod ...func(*provider.Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := provider.Config{APIKey: testKey}
	for _, m := range mod {
		m(&cfg)
	}
	c, err := New(cfg, WithBaseURL(srv.URL+"/v2"), WithHTTPClient(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsBadCredentials(t *testing.T) {
	if _, err := New(provider.Config{}); err == nil {
		t.Error("an empty key was accepted")
	}
	if _, err := New(provider.Config{APIKey: "k", MessagingProfileID: "../x"}); err == nil {
		t.Error("a malformed messaging profile was accepted")
	}
}

func TestSendGolden(t *testing.T) {
	var got struct {
		method, path, auth, ctype string
		body                      map[string]any
	}
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.auth, got.ctype = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		_, _ = io.WriteString(w, `{"data":{"record_type":"message","id":"`+testMsg+`","to":[{"phone_number":"+32475000001","status":"queued"}]}}`)
	}), func(c *provider.Config) { c.MessagingProfileID = testProf })
	s, err := c.Send(context.Background(), provider.Outbound{From: "+3270123456", To: "+32475000001", Body: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ProviderID != testMsg || s.Status != provider.StatusQueued {
		t.Errorf("sent = %+v", s)
	}
	if got.method != "POST" || got.path != "/v2/messages" || got.auth != "Bearer "+testKey || got.ctype != "application/json" {
		t.Errorf("request = %+v", got)
	}
	want := map[string]any{"from": "+3270123456", "to": "+32475000001", "text": "hello there", "type": "SMS", "messaging_profile_id": testProf}
	if len(got.body) != len(want) {
		t.Errorf("body = %v", got.body)
	}
	for k, v := range want {
		if got.body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, got.body[k], v)
		}
	}
}

func TestSendWithoutProfileOmitsIt(t *testing.T) {
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "messaging_profile_id") {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"data":{"id":"`+testMsg+`","to":[{"status":"queued"}]}}`)
	}))
	if _, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestSendErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"opted out", 403, `{"errors":[{"code":"40300","title":"Blocked due to STOP message"}]}`,
			func(e error) bool {
				var r *provider.Rejected
				return errors.As(e, &r) && r.Code == provider.CodeOptedOut
			}},
		{"bad number", 422, `{"errors":[{"code":"40310","title":"Invalid To address"}]}`,
			func(e error) bool { var r *provider.Rejected; return errors.As(e, &r) && r.Code == "40310" }},
		{"no code", 400, `<html>`,
			func(e error) bool { var r *provider.Rejected; return errors.As(e, &r) && r.Code == "http_400" }},
		{"unauthorized", 401, `{"errors":[{"code":"10009"}]}`, func(e error) bool { return errors.Is(e, provider.ErrAuth) }},
		{"forbidden key", 403, `{"errors":[{"code":"10010"}]}`, func(e error) bool { return errors.Is(e, provider.ErrAuth) }},
		{"throttled", 429, `{}`,
			func(e error) bool {
				var r *provider.Rejected
				return errors.As(e, &r) && r.Code == provider.CodeRateLimited
			}},
		{"server error", 503, `oops`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
		{"accepted but unreadable", 200, `not json`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
		{"accepted without id", 200, `{"data":{}}`, func(e error) bool { return errors.Is(e, provider.ErrAmbiguous) }},
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
			for _, leak := range []string{testKey, "secret customer text", "Blocked due"} {
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
	srv.Close()
	c, _ := New(provider.Config{APIKey: testKey}, WithBaseURL(base), WithHTTPClient(&http.Client{}))
	_, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "x"})
	if !errors.Is(err, provider.ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error leaks: %v", err)
	}
}

func TestSendNeverFollowsRedirect(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	if _, err := c.Send(context.Background(), provider.Outbound{From: "+1", To: "+2", Body: "x"}); err == nil {
		t.Error("a redirect was treated as success")
	}
	if hit {
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
	for telnyx, want := range map[string]string{
		"queued": provider.StatusQueued, "sending": provider.StatusQueued, "sent": provider.StatusSent,
		"delivery_unconfirmed": provider.StatusSent, "delivered": provider.StatusDelivered,
		"webhook_delivered": provider.StatusDelivered, "sending_failed": provider.StatusFailed,
		"delivery_failed": provider.StatusFailed, "something-new": provider.StatusQueued,
	} {
		c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v2/messages/"+testMsg {
				t.Errorf("path = %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"data":{"id":"` + testMsg + `","to":[{"status":"` + telnyx + `"}],"errors":[{"code":"40008"}]}}`))
		}))
		st, err := c.Status(context.Background(), testMsg)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != want {
			t.Errorf("%s -> %s, want %s", telnyx, st.Status, want)
		}
		if (want == provider.StatusFailed) != (st.ErrorCode == "40008") {
			t.Errorf("%s error code = %q", telnyx, st.ErrorCode)
		}
	}
}

func TestStatusOptOutError(t *testing.T) {
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"to":[{"status":"delivery_failed"}],"errors":[{"code":"40300"}]}}`))
	}))
	st, err := c.Status(context.Background(), testMsg)
	if err != nil || st.ErrorCode != provider.CodeOptedOut {
		t.Fatalf("state = %+v, %v", st, err)
	}
}

func TestStatusNotFoundAndBadID(t *testing.T) {
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	if _, err := c.Status(context.Background(), testMsg); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Status(context.Background(), "../balance"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("a path-shaped id reached the carrier: %v", err)
	}
}

func TestValidateAndNumbers(t *testing.T) {
	pages := 0
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v2/phone_numbers/messaging" {
			w.WriteHeader(404)
			return
		}
		pages++
		if r.URL.Query().Get("page[number]") == "1" {
			_, _ = io.WriteString(w, `{"data":[{"id":"1","phone_number":"+3270123456","type":"longcode","features":{"sms":{"domestic_two_way":true},"mms":{"domestic_two_way":false}}}],"meta":{"total_pages":2}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"2","phone_number":"+18005550100","type":"toll-free","features":{"sms":{"domestic_two_way":true},"mms":{"domestic_two_way":true}}},{"id":"3","phone_number":"+3299","features":{}}],"meta":{"total_pages":2}}`)
	}))
	if err := c.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	pages = 0
	ns, err := c.Numbers(context.Background())
	if err != nil || len(ns) != 3 || pages != 2 {
		t.Fatalf("numbers = %+v, %v (pages %d)", ns, err, pages)
	}
	if !ns[0].SMS || ns[0].MMS || ns[0].ID != "1" || !ns[1].MMS || ns[2].SMS {
		t.Errorf("capabilities = %+v", ns)
	}
	unauth := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	if err := unauth.Validate(context.Background()); !errors.Is(err, provider.ErrAuth) {
		t.Errorf("bad credentials: %v", err)
	}
}

func TestRegistered(t *testing.T) {
	if !provider.Known(Kind) {
		t.Fatal("telnyx is not registered")
	}
}

func TestRegistrationGolden(t *testing.T) {
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/10dlc/brand":
			_, _ = io.WriteString(w, `{"records":[{"brandId":"B1","displayName":"Acme","entityType":"PRIVATE_PROFIT","status":"OK","identityStatus":"VERIFIED"}]}`)
		case "/v2/10dlc/campaign":
			_, _ = io.WriteString(w, `{"records":[{"campaignId":"C1","brandId":"B1","usecase":"CUSTOMER_CARE","status":"ACTIVE"},{"campaignId":"C2","brandId":"B1","usecase":"MARKETING","status":"ACTIVE","submissionStatus":"PENDING"}]}`)
		case "/v2/10dlc/phone_number_campaigns":
			_, _ = io.WriteString(w, `{"records":[{"phoneNumber":"+12125550100","campaignId":"C1","assignmentStatus":"ASSIGNED"},{"phoneNumber":"+12125550101","campaignId":"C2","assignmentStatus":"ASSIGNED"},{"phoneNumber":"+12125550102","campaignId":"C1","assignmentStatus":"FAILED_ASSIGNMENT"}]}`)
		case "/v2/messaging_tollfree/verification/requests":
			_, _ = io.WriteString(w, `{"records":[{"phoneNumbers":[{"phoneNumber":"+18005550100"}],"verificationStatus":"Verified"},{"phoneNumbers":[{"phoneNumber":"+18335550100"}],"verificationStatus":"Rejected","reason":"missing opt-in"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	nums := []provider.Number{{E164: "+12125550100"}, {E164: "+12125550101"}, {E164: "+12125550102"}, {E164: "+12125550103"},
		{E164: "+18005550100"}, {E164: "+18335550100"}, {E164: "+18445550100"}, {E164: "+3270123456"}}
	reg, err := c.Registration(context.Background(), nums)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Brands) != 1 || reg.Brands[0].State != provider.RegApproved || reg.Brands[0].Name != "Acme" {
		t.Errorf("brands = %+v", reg.Brands)
	}
	if len(reg.Campaigns) != 2 || reg.Campaigns[0].State != provider.RegApproved || reg.Campaigns[1].State != provider.RegPending {
		t.Errorf("campaigns = %+v", reg.Campaigns)
	}
	want := []string{provider.RegApproved, provider.RegPending, provider.RegRejected, provider.RegUnregistered,
		provider.RegApproved, provider.RegRejected, provider.RegUnregistered, provider.RegUnknown}
	kinds := []string{"10dlc", "10dlc", "10dlc", "10dlc", "toll_free", "toll_free", "toll_free", "other"}
	for i, n := range reg.Numbers {
		if n.State != want[i] || n.Kind != kinds[i] {
			t.Errorf("number %s = %+v, want %s %s", nums[i].E164, n, kinds[i], want[i])
		}
	}
	if reg.Numbers[5].Detail != "missing opt-in" || len(reg.Unavailable) != 0 {
		t.Errorf("detail %q unavailable %v", reg.Numbers[5].Detail, reg.Unavailable)
	}
}

func TestRegistrationDegrades(t *testing.T) {
	// A key without the 10DLC permission still reads toll-free, and the
	// 10DLC numbers are unknown rather than unregistered.
	c := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/10dlc/") {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"errors":[{"code":"10010"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"records":[]}`)
	}))
	reg, err := c.Registration(context.Background(), []provider.Number{{E164: "+12125550100"}, {E164: "+18005550100"}})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Numbers[0].State != provider.RegUnknown || reg.Numbers[1].State != provider.RegUnregistered {
		t.Errorf("numbers = %+v", reg.Numbers)
	}
	if strings.Join(reg.Unavailable, ",") != "brands,campaigns" {
		t.Errorf("unavailable = %v", reg.Unavailable)
	}
	// Nothing readable at all is the carrier's error.
	none := client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	if _, err := none.Registration(context.Background(), nil); !errors.Is(err, provider.ErrAuth) {
		t.Errorf("err = %v", err)
	}
}
