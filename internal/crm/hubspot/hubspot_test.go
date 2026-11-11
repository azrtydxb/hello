package hubspot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

type call struct {
	Method, Path, Query, Auth, Type string
	Body                            string
}

type mock struct {
	mu    sync.Mutex
	calls []call
	// handler answers a request; nil answers 200 {}.
	handler func(w http.ResponseWriter, r *http.Request, body string)
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.calls = append(m.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
	m.mu.Unlock()
	if m.handler != nil {
		m.handler(w, r, string(b))
		return
	}
	_, _ = w.Write([]byte(`{}`))
}

func (m *mock) last() call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

func setup(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body string)) (*Connector, *mock, crm.Conn) {
	t.Helper()
	m := &mock{handler: h}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.APIBase = srv.URL
	return c, m, crm.Conn{AccessToken: "tok-123", ExternalAcct: "4711", Config: crm.Config{Extra: map[string]string{"region": "na"}}}
}

func TestRegisteredAndCapabilities(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("hubspot is not registered")
	}
	caps := c.Capabilities()
	if !caps.Lookup || !caps.LogCalls || !caps.Recordings || caps.Ticket {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestLookupBuildsSearchAndParsesTheContact(t *testing.T) {
	c, m, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"results":[{"id":"501","properties":{"firstname":"Ann","lastname":"Smith","company":"ACME"}}]}`))
	})
	got, err := c.Lookup(context.Background(), cc, "0032 11 22 33 44")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "501" || got.Name != "Ann Smith" || got.Company != "ACME" ||
		got.URL != "https://app.hubspot.com/contacts/4711/record/0-1/501" {
		t.Fatalf("contact = %+v", got)
	}
	l := m.last()
	if l.Method != "POST" || l.Path != "/crm/v3/objects/contacts/search" || l.Auth != "Bearer tok-123" || l.Type != "application/json" {
		t.Fatalf("request = %+v", l)
	}
	var body struct {
		FilterGroups []struct {
			Filters []struct{ PropertyName, Operator, Value string }
		}
		Limit int
	}
	if err := json.Unmarshal([]byte(l.Body), &body); err != nil {
		t.Fatal(err)
	}
	var props []string
	for _, g := range body.FilterGroups {
		f := g.Filters[0]
		props = append(props, f.PropertyName+" "+f.Operator+" "+f.Value)
	}
	want := []string{"phone EQ +3211223344", "mobilephone EQ +3211223344", "hs_searchable_calculated_phone_number CONTAINS_TOKEN *211223344"}
	if strings.Join(props, "|") != strings.Join(want, "|") || body.Limit != 1 {
		t.Fatalf("filters = %v limit %d", props, body.Limit)
	}
}

func TestLookupNoMatchAndExtensions(t *testing.T) {
	c, m, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(`{"results":[]}`)) })
	if got, err := c.Lookup(context.Background(), cc, "+3211223344"); got != nil || err != nil {
		t.Fatalf("no match = %+v, %v", got, err)
	}
	n := len(m.calls)
	if got, err := c.Lookup(context.Background(), cc, "201"); got != nil || err != nil || len(m.calls) != n {
		t.Fatal("an extension number reached HubSpot")
	}
}

func TestLogCallProperties(t *testing.T) {
	c, m, cc := setup(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.URL.Path {
		case "/calling/v1/dispositions":
			_, _ = w.Write([]byte(`[{"id":"guid-connected","label":"Connected","deleted":false},{"id":"guid-na","label":"No answer","deleted":false}]`))
		default:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"9001"}`))
		}
	})
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ref, err := c.LogCall(context.Background(), cc, crm.CallLog{
		CallID: "call-1", Direction: "inbound", Number: "+3211223344", From: "+3211223344", To: "201", StartedAt: start,
		Duration: 65 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "77", Contact: &crm.Contact{ID: "501", Name: "Ann Smith"},
		RecordingURL: "https://hello.example/r/abc.wav",
	})
	if err != nil || ref != "9001" {
		t.Fatalf("ref = %q, %v", ref, err)
	}
	l := m.last()
	if l.Method != "POST" || l.Path != "/crm/v3/objects/calls" {
		t.Fatalf("request = %+v", l)
	}
	var body struct {
		Properties   map[string]string
		Associations []struct {
			To    map[string]string
			Types []map[string]any
		}
	}
	if err := json.Unmarshal([]byte(l.Body), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"hs_timestamp": "2026-11-10T08:00:00.000Z", "hs_call_duration": "65000", "hs_call_from_number": "+3211223344",
		"hs_call_to_number": "201", "hs_call_direction": "INBOUND", "hs_call_status": "COMPLETED", "hs_call_source": "INTEGRATIONS_PLATFORM",
		"hubspot_owner_id": "77", "hs_call_recording_url": "https://hello.example/r/abc.wav", "hs_call_disposition": "guid-connected",
		"hs_call_title": "Call from Ann Smith",
	}
	for k, v := range want {
		if body.Properties[k] != v {
			t.Errorf("%s = %q, want %q", k, body.Properties[k], v)
		}
	}
	if len(body.Associations) != 1 || body.Associations[0].To["id"] != "501" ||
		body.Associations[0].Types[0]["associationTypeId"] != float64(194) || body.Associations[0].Types[0]["associationCategory"] != "HUBSPOT_DEFINED" {
		t.Fatalf("associations = %+v", body.Associations)
	}
}

func TestStatusMappingAndOutboundMissed(t *testing.T) {
	for o, want := range map[crm.Outcome]string{crm.OutcomeAnswered: "COMPLETED", crm.OutcomeNoAnswer: "NO_ANSWER", crm.OutcomeBusy: "BUSY",
		crm.OutcomeCancelled: "CANCELED", crm.OutcomeFailed: "FAILED"} {
		if got := status(o); got != want {
			t.Errorf("status(%s) = %s, want %s", o, got, want)
		}
	}
	c, m, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(`{"id":"1"}`)) })
	if _, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211223344", Missed: true, Outcome: crm.OutcomeNoAnswer}); err != nil {
		t.Fatal(err)
	}
	var body struct{ Properties, Associations map[string]any }
	_ = json.Unmarshal([]byte(m.last().Body), &body)
	if body.Properties["hs_call_status"] != "NO_ANSWER" || body.Properties["hs_call_title"] != "Missed call from +3211223344" || body.Associations != nil {
		t.Fatalf("body = %s", m.last().Body)
	}
	if _, set := body.Properties["hubspot_owner_id"]; set {
		t.Fatal("an unowned call sent an owner id")
	}
}

func TestRecordingURLMustBeHTTPSAudio(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://h.example/r/abc.wav": true, "https://h.example/r/abc.MP3": true, "https://h.example/r/abc.wav?x=1": true,
		"http://h.example/r/abc.wav": false, "https://h.example/r/abc": false, "https://h.example/r/abc.ogg": false, "": false, "javascript:alert(1)": false,
	} {
		if got := validRecordingURL(raw); got != ok {
			t.Errorf("validRecordingURL(%q) = %v", raw, got)
		}
	}
	c, m, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(`{"id":"1"}`)) })
	_, _ = c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", RecordingURL: "http://h.example/r/abc.wav"})
	if strings.Contains(m.last().Body, "hs_call_recording_url") {
		t.Fatalf("an insecure recording URL was sent: %s", m.last().Body)
	}
}

func TestUpdateCallLogPatchesTheEngagementWithoutRewritingNotes(t *testing.T) {
	c, m, cc := setup(t, nil)
	err := c.UpdateCallLog(context.Background(), cc, "eng/1", crm.CallLog{Direction: "outbound", Duration: 3 * time.Second, Outcome: crm.OutcomeAnswered,
		From: "201", To: "+3211223344", RecordingURL: "https://h.example/r/a.mp3", OwnerID: "5"})
	if err != nil {
		t.Fatal(err)
	}
	l := m.last()
	if l.Method != "PATCH" || l.Path != "/crm/v3/objects/calls/eng/1" && l.Path != "/crm/v3/objects/calls/eng%2F1" {
		t.Fatalf("request = %+v", l)
	}
	var body struct{ Properties map[string]string }
	_ = json.Unmarshal([]byte(l.Body), &body)
	for _, k := range []string{"hs_call_title", "hs_call_body", "hs_call_source"} {
		if _, ok := body.Properties[k]; ok {
			t.Errorf("an update rewrote %s", k)
		}
	}
	if body.Properties["hs_call_duration"] != "3000" || body.Properties["hs_call_recording_url"] != "https://h.example/r/a.mp3" || body.Properties["hs_call_direction"] != "OUTBOUND" {
		t.Fatalf("properties = %v", body.Properties)
	}
}

func TestResolveUserMatchesEmailExactly(t *testing.T) {
	c, m, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"results":[{"id":"1","email":"anna.smith@example.com"},{"id":"2","email":"ANNA@example.com"}]}`))
	})
	id, err := c.ResolveUser(context.Background(), cc, "anna@example.com")
	if err != nil || id != "2" {
		t.Fatalf("owner = %q, %v (display names and partial matches must not count)", id, err)
	}
	q, _ := url.ParseQuery(m.last().Query)
	if q.Get("email") != "anna@example.com" {
		t.Fatalf("query = %v", q)
	}
	if id, _ := c.ResolveUser(context.Background(), cc, "nobody@example.com"); id != "" {
		t.Fatalf("owner = %q for an unknown email", id)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		hdr    string
		retry  bool
		after  time.Duration
		auth   bool
	}{
		{401, "", false, 0, true}, {429, "7", true, 7 * time.Second, false}, {500, "", true, 0, false}, {503, "", true, 0, false},
		{408, "", true, 0, false}, {400, "", false, 0, false}, {403, "", false, 0, false}, {404, "", false, 0, false},
	}
	for _, tc := range cases {
		c, _, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			if tc.hdr != "" {
				w.Header().Set("Retry-After", tc.hdr)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"status":"error","message":"Bearer tok-123 \n went wrong"}`))
		})
		_, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound"})
		if tc.auth {
			if !errors.Is(err, crm.ErrAuth) {
				t.Errorf("%d: err = %v, want ErrAuth", tc.status, err)
			}
			continue
		}
		var ae *crm.APIError
		if !errors.As(err, &ae) || ae.Status != tc.status || ae.Retry != tc.retry || ae.After != tc.after {
			t.Errorf("%d: err = %#v", tc.status, err)
		}
	}
}

func TestNetworkErrorIsRetryableAndHidesTheURL(t *testing.T) {
	c, _, cc := setup(t, nil)
	c.APIBase = "http://127.0.0.1:1"
	_, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound"})
	var ae *crm.APIError
	if !errors.As(err, &ae) || !ae.Retry || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err = %v", err)
	}
}

func TestOAuthExchangeAndRefresh(t *testing.T) {
	c, m, _ := setup(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case r.URL.Path == "/oauth/v1/token" && strings.Contains(body, "grant_type=authorization_code"):
			_, _ = w.Write([]byte(`{"access_token":"A1","refresh_token":"R1","expires_in":1800}`))
		case r.URL.Path == "/oauth/v1/token" && strings.Contains(body, "refresh_token=BAD"):
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"status":"BAD_REFRESH_TOKEN","message":"invalid"}`))
		case r.URL.Path == "/oauth/v1/token":
			_, _ = w.Write([]byte(`{"access_token":"A2","refresh_token":"R1","expires_in":1800}`))
		case strings.HasPrefix(r.URL.Path, "/oauth/v1/access-tokens/"):
			_, _ = w.Write([]byte(`{"hub_id":4711,"scopes":["crm.objects.contacts.read","oauth"]}`))
		}
	})
	cfg := crm.Config{ClientID: "cid", ClientSecret: "csecret", Extra: map[string]string{"region": "na"}}
	tk, err := c.Exchange(context.Background(), cfg, "https://hello.example/cb", "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Access != "A1" || tk.Refresh != "R1" || tk.Account != "4711" || len(tk.Scopes) != 2 || time.Until(tk.ExpiresAt) < 25*time.Minute {
		t.Fatalf("tokens = %+v", tk)
	}
	form, _ := url.ParseQuery(m.calls[0].Body)
	if form.Get("client_id") != "cid" || form.Get("client_secret") != "csecret" || form.Get("redirect_uri") != "https://hello.example/cb" || form.Get("code") != "the-code" {
		t.Fatalf("exchange form = %v", form)
	}
	if m.calls[0].Type != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", m.calls[0].Type)
	}
	tk, err = c.Refresh(context.Background(), cfg, "R1")
	if err != nil || tk.Access != "A2" {
		t.Fatalf("refresh = %+v, %v", tk, err)
	}
	_, err = c.Refresh(context.Background(), cfg, "BAD")
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("a rejected refresh token must be permanent: %v", err)
	}
}

func TestAuthURLPerRegionAndConfigValidation(t *testing.T) {
	c := New(http.DefaultClient)
	u, _ := url.Parse(c.AuthURL(crm.Config{ClientID: "cid", Extra: map[string]string{"region": "eu"}}, "https://hello.example/cb", "st"))
	if u.Host != "app-eu1.hubspot.com" || u.Path != "/oauth/authorize" || u.Query().Get("state") != "st" || u.Query().Get("client_id") != "cid" ||
		u.Query().Get("redirect_uri") != "https://hello.example/cb" || !strings.Contains(u.Query().Get("scope"), "crm.objects.owners.read") {
		t.Fatalf("auth url = %s", u)
	}
	if u, _ := url.Parse(c.AuthURL(crm.Config{ClientID: "c"}, "r", "s")); u.Host != "app.hubspot.com" {
		t.Fatalf("default region host = %s", u.Host)
	}
	cfg, err := c.ValidateConfig(map[string]string{"clientId": "x"})
	if err != nil || cfg["region"] != "na" {
		t.Fatalf("default region = %v, %v", cfg, err)
	}
	if _, err := c.ValidateConfig(map[string]string{"region": "mars"}); err == nil {
		t.Fatal("bad region accepted")
	}
	if _, err := c.ValidateConfig(map[string]string{"apiBase": "http://169.254.169.254"}); err == nil {
		t.Fatal("an arbitrary setting (a base URL to point the client at) was accepted")
	}
}

func TestPingAndRevoke(t *testing.T) {
	c, m, cc := setup(t, nil)
	if err := c.Ping(context.Background(), cc); err != nil || m.last().Path != "/crm/v3/owners" {
		t.Fatalf("ping = %v, %+v", err, m.last())
	}
	if err := c.RevokeToken(context.Background(), crm.Config{}, "R/1"); err != nil || m.last().Method != "DELETE" {
		t.Fatalf("revoke = %v, %+v", err, m.last())
	}
}
