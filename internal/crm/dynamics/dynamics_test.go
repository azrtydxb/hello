package dynamics

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

const (
	tenant = "11111111-2222-3333-4444-555555555555"
	org    = "https://contoso.crm4.dynamics.com"
	uid    = "aaaaaaaa-0000-0000-0000-000000000001"
	cid    = "bbbbbbbb-0000-0000-0000-000000000002"
	act    = "cccccccc-0000-0000-0000-000000000003"
)

type call struct{ Method, Path, Query, Auth, Body, Prefer string }

type dv struct {
	mu    sync.Mutex
	calls []call
}

func (d *dv) bodies(method, path string) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []map[string]any
	for _, c := range d.calls {
		if c.Method == method && c.Path == path {
			var m map[string]any
			_ = json.Unmarshal([]byte(c.Body), &m)
			out = append(out, m)
		}
	}
	return out
}

func newDV(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body string)) (*Connector, *dv) {
	t.Helper()
	d := &dv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.calls = append(d.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b), r.Header.Get("Prefer")})
		d.mu.Unlock()
		if r.Header.Get("OData-Version") == "" && !strings.HasSuffix(r.URL.Path, "/token") {
			t.Errorf("%s %s without OData-Version", r.Method, r.URL.Path)
		}
		h(w, r, string(b))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.LoginURL = srv.URL
	c.Base = func(string) string { return srv.URL }
	return c, d
}

func conn() crm.Conn {
	return crm.Conn{AccessToken: "tok", Config: crm.Config{Kind: Kind, Extra: map[string]string{"orgUrl": org, "tenantId": tenant}}}
}

func TestValidateConfig(t *testing.T) {
	c := New(nil)
	got, err := c.ValidateConfig(map[string]string{"orgUrl": "https://Contoso.crm4.dynamics.com/", "tenantId": tenant, "clientId": "x"})
	if err != nil || got["orgUrl"] != org {
		t.Fatalf("config = %v, %v", got, err)
	}
	for _, in := range []map[string]string{
		{"tenantId": tenant},
		{"orgUrl": "http://contoso.crm4.dynamics.com", "tenantId": tenant},
		{"orgUrl": "https://contoso.crm4.dynamics.com.evil.example", "tenantId": tenant},
		{"orgUrl": "https://evil.example/.dynamics.com", "tenantId": tenant},
		{"orgUrl": "https://127.0.0.1", "tenantId": tenant},
		{"orgUrl": org, "tenantId": "common"},
		{"orgUrl": org, "tenantId": tenant, "other": "1"},
	} {
		if _, err := c.ValidateConfig(in); err == nil {
			t.Errorf("accepted %v", in)
		}
	}
}

func TestSetupChannelProvider(t *testing.T) {
	items := New(nil).Setup(crm.Config{Extra: map[string]string{"orgUrl": org}}, "https://hello.example")
	m := map[string]string{}
	for _, i := range items {
		m[i.Name] = i.Value
	}
	if m["channelUrl"] != "https://hello.example/integrations/dynamics/cif?org=https%3A%2F%2Fcontoso.crm4.dynamics.com" || m["trustedDomain"] != "https://hello.example" || m["apiVersion"] != "2.0" || m["outbound"] != "Yes" {
		t.Fatalf("setup = %v", m)
	}
}

func TestAuthorizeClientCredentials(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, r *http.Request, body string) {
		f, _ := url.ParseQuery(body)
		if r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" || f.Get("grant_type") != "client_credentials" ||
			f.Get("scope") != org+"/.default" || f.Get("client_id") != "app" || f.Get("client_secret") != "sec" {
			t.Errorf("token request = %s %s", r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3599,"token_type":"Bearer"}`))
	})
	tk, err := c.Authenticate(context.Background(), crm.Config{ClientID: "app", ClientSecret: "sec", Extra: map[string]string{"orgUrl": org, "tenantId": tenant}})
	if err != nil || tk.Access != "at" || tk.Refresh != "" || tk.Account != "contoso.crm4.dynamics.com" {
		t.Fatalf("tokens = %+v, %v", tk, err)
	}
	if dd := time.Until(tk.ExpiresAt); dd < 59*time.Minute || dd > time.Hour {
		t.Fatalf("expiry in %v", dd)
	}
	_ = d

	c2, _ := newDV(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided.\r\nTrace ID: x\r\nTimestamp: y"}`))
	})
	_, err = c2.Authenticate(context.Background(), crm.Config{ClientID: "app", ClientSecret: "bad", Extra: map[string]string{"orgUrl": org, "tenantId": tenant}})
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "AADSTS7000215") || strings.Contains(ae.Msg, "Trace") {
		t.Fatalf("err = %v: a refused secret is permanent and the trace ids are dropped", err)
	}
}

func TestLookupContactsThenLeads(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.URL.Path {
		case "/api/data/v9.2/contacts":
			_, _ = w.Write([]byte(`{"value":[]}`))
		case "/api/data/v9.2/leads":
			_, _ = w.Write([]byte(`{"value":[{"leadid":"` + cid + `","fullname":"Lee Lead","companyname":"LeadCo","telephone1":"+32 11 22 33 44","mobilephone":null}]}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	})
	ct, err := c.Lookup(context.Background(), conn(), "+32 11 22 33 44")
	if err != nil || ct == nil || ct.ID != cid || ct.Name != "Lee Lead" || ct.Company != "LeadCo" || !strings.Contains(ct.URL, "etn=lead") {
		t.Fatalf("contact = %+v, %v", ct, err)
	}
	f, _ := url.QueryUnescape(strings.Split(strings.Split(d.calls[0].Query, "$filter=")[1], "&")[0])
	for _, want := range []string{"contains(telephone1,'11223344')", "contains(mobilephone,'11223344')", "contains(telephone2,'11223344')",
		"contains(telephone1,'11 22 33 44')", "contains(mobilephone,'11-223-344')"} {
		if !strings.Contains(f, want) {
			t.Errorf("the filter lacks %q: %s", want, f)
		}
	}
	if d.calls[0].Auth != "Bearer tok" {
		t.Fatalf("auth = %q", d.calls[0].Auth)
	}
}

// TestLookupFindsNumbersStoredWithSeparators: Dataverse keeps a phone number
// as typed. It fails if a number stored with spaces, dashes, dots, in national
// form or in a 3-4 split is not found, if a different number that shares only
// a short run of digits is, or if the filter grows beyond what a URL carries.
func TestLookupFindsNumbersStoredWithSeparators(t *testing.T) {
	stored := ""
	c, d := newDV(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"value":[{"contactid":"` + cid + `","fullname":"Ann","telephone1":"` + stored + `"}]}`))
	})
	for _, s := range []string{"+32 11 22 33 44", "011 22 33 44", "011/22.33.44", "0032 11-22-33-44", "+32 (0)11 22 33 44", "011223344", "+32 11 223 344", "+32 1122 3344"} {
		stored = s
		ct, err := c.Lookup(context.Background(), conn(), "+3211223344")
		if err != nil || ct == nil {
			t.Errorf("a contact stored as %q was not found (%v)", s, err)
		}
	}
	// US style, 3-3-4 and 3-4.
	for _, s := range []string{"(555) 123-4567", "555.123.4567", "+1 555 123 4567"} {
		stored = s
		if ct, _ := c.Lookup(context.Background(), conn(), "+15551234567"); ct == nil {
			t.Errorf("a contact stored as %q was not found", s)
		}
	}
	// Candidates the filter returns for a shared fragment are not the number.
	for _, s := range []string{"+32 11 22 33 45", "099 22 33 44", "", "44", "+32 4 11 22 33 44 55"} {
		stored = s
		if ct, _ := c.Lookup(context.Background(), conn(), "+3211223344"); ct != nil {
			t.Errorf("a contact stored as %q matched +3211223344", s)
		}
	}
	for _, k := range d.calls {
		if len(k.Query) > 6000 {
			t.Fatalf("the query is %d bytes", len(k.Query))
		}
	}
}

func TestPhoneVariantsAndMatch(t *testing.T) {
	v := phoneVariants("3211223344")
	for _, want := range []string{"11223344", "11 22 33 44", "11.223.344", "1122-3344", "22/33/44", "122-3344"} {
		found := false
		for _, x := range v {
			found = found || x == want
		}
		if want != "" && !found {
			t.Errorf("no variant %q in %v", want, v)
		}
	}
	if len(v) > 30 {
		t.Errorf("%d variants", len(v))
	}
	if got := phoneVariants("123456"); len(got) < 1 || got[0] != "123456" {
		t.Errorf("short number variants = %v", got)
	}
	if !phoneMatches("011 22 33 44", "3211223344") || phoneMatches("011 22 33 55", "3211223344") || phoneMatches("", "3211223344") {
		t.Error("phoneMatches")
	}
	if !phoneMatches("123 456", "123456") || phoneMatches("23 456", "123456") {
		t.Error("a short number must match in full")
	}
}

func TestResolveUserEscapes(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"value":[{"systemuserid":"` + uid + `","internalemailaddress":"Anna@example.com"}]}`))
	})
	id, err := c.ResolveUser(context.Background(), conn(), "anna@example.com")
	if err != nil || id != uid {
		t.Fatalf("user = %q, %v", id, err)
	}
	_, _ = c.ResolveUser(context.Background(), conn(), "x' or 1 eq 1 or 'a")
	f, _ := url.QueryUnescape(d.calls[1].Query)
	if !strings.Contains(f, "internalemailaddress eq 'x'' or 1 eq 1 or ''a'") {
		t.Fatalf("filter = %q", f)
	}
}

func TestLogCallCreatesAndClosesAPhoneCall(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"activityid":"` + act + `"}`))
			return
		}
		w.WriteHeader(204)
	})
	l := crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+3211223344", From: "+3211223344", To: "201",
		StartedAt: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 11, 12, 8, 1, 5, 0, time.UTC),
		Duration: 61 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: uid,
		Contact: &crm.Contact{ID: cid, Name: "Ann", URL: org + "/main.aspx?etn=contact&id=" + cid}, RecordingURL: "https://hello.example/r/t.wav"}
	ref, err := c.LogCall(context.Background(), conn(), l)
	if err != nil || ref != act {
		t.Fatalf("ref = %q, %v", ref, err)
	}
	if d.calls[0].Prefer != "return=representation" {
		t.Errorf("Prefer = %q", d.calls[0].Prefer)
	}
	b := d.bodies("POST", "/api/data/v9.2/phonecalls")[0]
	for k, v := range map[string]any{"subject": "Call from Ann", "phonenumber": "+3211223344", "directioncode": false, "actualdurationminutes": float64(2),
		"ownerid@odata.bind": "/systemusers(" + uid + ")", "regardingobjectid_contact_phonecall@odata.bind": "/contacts(" + cid + ")", "actualstart": "2026-11-12T08:00:00Z"} {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
	if desc, _ := b["description"].(string); !strings.Contains(desc, "Recording: https://hello.example/r/t.wav") {
		t.Errorf("description = %q", desc)
	}
	parties, _ := b["phonecall_activity_parties"].([]any)
	if len(parties) != 2 {
		t.Fatalf("parties = %v", b["phonecall_activity_parties"])
	}
	from, to := parties[0].(map[string]any), parties[1].(map[string]any)
	// Inbound: the contact is the sender (1), the user the recipient (2).
	if from["participationtypemask"] != float64(1) || from["partyid_contact@odata.bind"] == nil {
		t.Errorf("parties = %v", parties)
	}
	if to["participationtypemask"] != float64(2) {
		t.Errorf("recipient = %v", to)
	}
	st := d.bodies("PATCH", "/api/data/v9.2/phonecalls("+act+")")
	if len(st) != 1 || st[0]["statecode"] != float64(1) || st[0]["statuscode"] != float64(4) {
		t.Fatalf("close = %v: an inbound call closes as Received", st)
	}

	// An outbound missed call to a lead closes as Made and says nobody answered.
	l = crm.CallLog{CallID: "c2", Direction: "outbound", Number: "+3211", Missed: true, Outcome: crm.OutcomeNoAnswer,
		Contact: &crm.Contact{ID: cid, URL: org + "/main.aspx?etn=lead&id=" + cid}}
	if _, err := c.LogCall(context.Background(), conn(), l); err != nil {
		t.Fatal(err)
	}
	b = d.bodies("POST", "/api/data/v9.2/phonecalls")[1]
	if b["directioncode"] != true || b["regardingobjectid_lead_phonecall@odata.bind"] == nil || b["actualdurationminutes"] != float64(0) {
		t.Errorf("missed = %v", b)
	}
	st = d.bodies("PATCH", "/api/data/v9.2/phonecalls("+act+")")
	if st[1]["statuscode"] != float64(2) {
		t.Errorf("outbound close = %v", st[1])
	}
}

func TestLogCallDoesNotRepeatWhenClosingFails(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"activityid":"` + act + `"}`))
			return
		}
		w.WriteHeader(500)
	})
	ref, err := c.LogCall(context.Background(), conn(), crm.CallLog{Direction: "inbound", Number: "+3211223344", Outcome: crm.OutcomeAnswered})
	if err != nil || ref != act {
		t.Fatalf("ref = %q, %v: a created record must be reported", ref, err)
	}
	if len(d.bodies("POST", "/api/data/v9.2/phonecalls")) != 1 {
		t.Fatal("the call was created twice")
	}
}

func TestUpdateReopensPatchesAndCloses(t *testing.T) {
	c, d := newDV(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"statecode":1,"description":"Notes by Ann\nRecording: https://hello.example/r/old.wav","directioncode":true,"_regardingobjectid_value":null}`))
			return
		}
		w.WriteHeader(204)
	})
	l := crm.CallLog{Duration: 3 * time.Minute, RecordingURL: "https://hello.example/r/new.wav", Contact: &crm.Contact{ID: cid, URL: org + "/main.aspx?etn=contact&id=" + cid}}
	if err := c.UpdateCallLog(context.Background(), conn(), act, l); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, k := range d.calls {
		var m map[string]any
		_ = json.Unmarshal([]byte(k.Body), &m)
		switch {
		case k.Method == "GET":
			seq = append(seq, "get")
		case m["statecode"] == float64(0):
			seq = append(seq, "open")
		case m["statecode"] == float64(1):
			seq = append(seq, "close")
			if m["statuscode"] != float64(2) {
				t.Errorf("an outbound call must close as Made: %v", m)
			}
		default:
			seq = append(seq, "patch")
			if m["description"] != "Notes by Ann\nRecording: https://hello.example/r/new.wav" || m["actualdurationminutes"] != float64(3) ||
				m["regardingobjectid_contact_phonecall@odata.bind"] != "/contacts("+cid+")" {
				t.Errorf("patch = %v", m)
			}
		}
	}
	if strings.Join(seq, ",") != "get,open,patch,close" {
		t.Fatalf("sequence = %v", seq)
	}
	if err := c.UpdateCallLog(context.Background(), conn(), "x/../y", l); err == nil {
		t.Fatal("a path was accepted as an activity id")
	}
}

func TestErrorsAndRefs(t *testing.T) {
	status := 401
	c, _ := newDV(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"0x80040220","message":"Principal user is missing privilege"}}`))
	})
	if err := c.Ping(context.Background(), conn()); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("401 = %v", err)
	}
	var ae *crm.APIError
	status = 403
	if err := c.Ping(context.Background(), conn()); !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "privilege") {
		t.Fatalf("403 = %v", err)
	}
	status = 429
	if err := c.Ping(context.Background(), conn()); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("429 = %v", err)
	}
	if !c.ValidRef(act) || c.ValidRef("123") || c.ValidRef("") {
		t.Fatal("ValidRef")
	}
	if minutes(0) != 0 || minutes(time.Second) != 1 || minutes(61*time.Second) != 2 || minutes(2*time.Minute) != 2 {
		t.Fatal("minutes")
	}
}
