package salesforce

import (
	"context"
	"encoding/json"
	"encoding/xml"
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
	Method, Path, Query, Auth string
	Body                      string
}

type sf struct {
	mu    sync.Mutex
	calls []call
	srv   *httptest.Server
	h     func(w http.ResponseWriter, r *http.Request, body string)
}

func newSF(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body string)) (*Connector, *sf) {
	t.Helper()
	s := &sf{h: h}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls = append(s.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		s.mu.Unlock()
		s.h(w, r, string(b))
	}))
	t.Cleanup(s.srv.Close)
	c := New(s.srv.Client())
	c.BaseURL = func(string) string { return s.srv.URL }
	return c, s
}

func conn() crm.Conn {
	return crm.Conn{AccessToken: "tok", ExternalAcct: "acme.my.salesforce.com",
		Config: crm.Config{Kind: Kind, Extra: map[string]string{"domain": "acme.my.salesforce.com", "apiVersion": "62.0", "serverLogging": "true"}}}
}

func TestValidateConfig(t *testing.T) {
	c := New(nil)
	got, err := c.ValidateConfig(map[string]string{"domain": "ACME.my.salesforce.com"})
	if err != nil || got["domain"] != "acme.my.salesforce.com" || got["apiVersion"] != "62.0" || got["serverLogging"] != "false" {
		t.Fatalf("defaults = %v, %v", got, err)
	}
	bad := []map[string]string{
		{"domain": "https://acme.my.salesforce.com"},
		{"domain": "evil.example.com"},
		{"domain": "acme.my.salesforce.com.evil.example"},
		{"domain": "127.0.0.1"},
		{"apiVersion": "v62"},
		{"serverLogging": "yes"},
		{"serverLogging": "true", "clientId": "x"},
		{"serverLogging": "true", "domain": "a.my.salesforce.com"},
		{"nope": "1"},
	}
	for _, in := range bad {
		if _, err := c.ValidateConfig(in); err == nil {
			t.Errorf("accepted %v", in)
		}
	}
	if _, err := c.ValidateConfig(map[string]string{"serverLogging": "true", "clientId": "x", "domain": "a.my.salesforce.com"}); err != nil {
		t.Fatalf("complete server-side settings: %v", err)
	}
	if c.ServerSide(map[string]string{"serverLogging": "false"}) || !c.ServerSide(map[string]string{"serverLogging": "true"}) {
		t.Fatal("ServerSide does not follow serverLogging")
	}
}

func TestSetupCallCenterDefinition(t *testing.T) {
	c := New(nil)
	items := c.Setup(crm.Config{Extra: map[string]string{"serverLogging": "false"}}, "https://hello.example")
	var def, adapter string
	for _, it := range items {
		switch it.Name {
		case "callCenterXml":
			def = it.Value
			if it.Kind != "file" || it.Filename != "HelloSoftphone.xml" {
				t.Fatalf("definition item = %+v", it)
			}
		case "adapterUrl":
			adapter = it.Value
		}
	}
	if adapter != "https://hello.example/integrations/salesforce/softphone?missed=1" {
		t.Fatalf("adapter = %q (without server logging the softphone logs missed calls)", adapter)
	}
	var cc struct {
		Items []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"section>item"`
	}
	if err := xml.Unmarshal([]byte(def), &cc); err != nil {
		t.Fatalf("the definition is not XML: %v\n%s", err, def)
	}
	got := map[string]string{}
	for _, i := range cc.Items {
		got[i.Name] = i.Value
	}
	if got["reqAdapterUrl"] != adapter || got["reqUseApi"] != "true" || got["reqInternalName"] == "" || got["reqDisplayName"] == "" {
		t.Fatalf("items = %v", got)
	}
	on := c.Setup(crm.Config{Extra: map[string]string{"serverLogging": "true"}}, "https://hello.example")
	for _, it := range on {
		if it.Name == "adapterUrl" && strings.Contains(it.Value, "?") {
			t.Fatalf("adapter with server logging = %q", it.Value)
		}
	}
	if x := CallCenterXML(`https://h.example/a?b=1&c=<2>`); strings.Contains(x, "&c=<") {
		t.Fatalf("the URL is not escaped: %s", x)
	}
}

func TestAuthorizeClientCredentials(t *testing.T) {
	c, s := newSF(t, func(w http.ResponseWriter, r *http.Request, body string) {
		f, _ := url.ParseQuery(body)
		if r.URL.Path != "/services/oauth2/token" || f.Get("grant_type") != "client_credentials" || f.Get("client_id") != "cid" || f.Get("client_secret") != "sec" {
			t.Errorf("token request = %s %s", r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"access_token":"at","instance_url":"https://acme.my.salesforce.com","scope":"api","token_type":"Bearer"}`))
	})
	tk, err := c.Authenticate(context.Background(), crm.Config{ClientID: "cid", ClientSecret: "sec", Extra: map[string]string{"domain": "acme.my.salesforce.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Access != "at" || tk.Account != "acme.my.salesforce.com" || tk.Refresh != "" || len(tk.Scopes) != 1 {
		t.Fatalf("tokens = %+v", tk)
	}
	if d := time.Until(tk.ExpiresAt); d <= 0 || d > sessionLifetime {
		t.Fatalf("expiry in %v: the token response has no expiry, so a short one is assumed", d)
	}
	_ = s

	// An instance URL outside Salesforce is not trusted.
	c2, _ := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"access_token":"at","instance_url":"https://evil.example"}`))
	})
	tk, _ = c2.Authenticate(context.Background(), crm.Config{ClientID: "cid", ClientSecret: "sec", Extra: map[string]string{"domain": "acme.my.salesforce.com"}})
	if tk.Account != "acme.my.salesforce.com" {
		t.Fatalf("account = %q", tk.Account)
	}

	// A refused app is a permanent failure with Salesforce's reason.
	c3, _ := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"invalid client credentials"}`))
	})
	_, err = c3.Authenticate(context.Background(), crm.Config{ClientID: "cid", ClientSecret: "bad", Extra: map[string]string{"domain": "acme.my.salesforce.com"}})
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "invalid_client") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Authenticate(context.Background(), crm.Config{Extra: map[string]string{}}); err == nil {
		t.Fatal("authorized without a domain")
	}
}

func TestLookupPrefersContactOverLead(t *testing.T) {
	c, s := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"searchRecords":[
			{"attributes":{"type":"Lead"},"Id":"00Q000000000001AAA","Name":"Lee Lead","Company":"LeadCo"},
			{"attributes":{"type":"Contact"},"Id":"003000000000001AAA","Name":"Ann Contact","Account":{"Name":"Acme"}}]}`))
	})
	ct, err := c.Lookup(context.Background(), conn(), "0032 11 22 33 44")
	if err != nil || ct == nil {
		t.Fatalf("lookup = %v, %v", ct, err)
	}
	if ct.ID != "003000000000001AAA" || ct.Name != "Ann Contact" || ct.Company != "Acme" ||
		ct.URL != "https://acme.my.salesforce.com/lightning/r/Contact/003000000000001AAA/view" {
		t.Fatalf("contact = %+v", ct)
	}
	q := s.calls[0]
	if q.Path != "/services/data/v62.0/search/" || q.Auth != "Bearer tok" {
		t.Fatalf("request = %+v", q)
	}
	sosl, _ := url.QueryUnescape(strings.TrimPrefix(q.Query, "q="))
	if !strings.Contains(sosl, "FIND {*") || !strings.Contains(sosl, "IN PHONE FIELDS") || !strings.Contains(sosl, "IsConverted = false") {
		t.Fatalf("SOSL = %q", sosl)
	}
	if strings.Count(sosl, "{") != 1 || strings.ContainsAny(sosl[strings.Index(sosl, "{")+1:strings.Index(sosl, "}")], "'\"\\") {
		t.Fatalf("the search term is not digits only: %q", sosl)
	}
	// An extension is never sent.
	n := len(s.calls)
	if ct, _ := c.Lookup(context.Background(), conn(), "201"); ct != nil || len(s.calls) != n {
		t.Fatal("an extension was looked up")
	}
	// Nothing found.
	c2, _ := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(`{"searchRecords":[]}`)) })
	if ct, err := c2.Lookup(context.Background(), conn(), "+3211223344"); ct != nil || err != nil {
		t.Fatalf("no match = %v, %v", ct, err)
	}
}

func TestResolveUserEscapesAndRefusesAmbiguity(t *testing.T) {
	var recs string
	c, s := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"records":` + recs + `}`))
	})
	recs = `[{"Id":"005A","Email":"anna@example.com"}]`
	id, err := c.ResolveUser(context.Background(), conn(), "anna@example.com")
	if err != nil || id != "005A" {
		t.Fatalf("user = %q, %v", id, err)
	}
	recs = `[{"Id":"005A","Email":"anna@example.com"},{"Id":"005B","Email":"Anna@example.com"}]`
	if id, _ := c.ResolveUser(context.Background(), conn(), "anna@example.com"); id != "" {
		t.Fatalf("an ambiguous address resolved to %q", id)
	}
	recs = `[{"Id":"005C","Email":"someone@else.com"}]`
	if id, _ := c.ResolveUser(context.Background(), conn(), "anna@example.com"); id != "" {
		t.Fatalf("a different address resolved to %q", id)
	}
	_, _ = c.ResolveUser(context.Background(), conn(), `x' OR Email != '`)
	q, _ := url.QueryUnescape(strings.TrimPrefix(s.calls[len(s.calls)-1].Query, "q="))
	if !strings.Contains(q, `Email = 'x\' OR Email != \''`) {
		t.Fatalf("SOQL injection: %q", q)
	}
}

func TestLogCallCreatesACompletedCallTask(t *testing.T) {
	c, s := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"00T000000000001AAA","success":true}`))
	})
	l := crm.CallLog{CallID: "c-1", Direction: "inbound", Number: "+3211223344", From: "+3211223344", To: "201",
		StartedAt: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC), Duration: 65 * time.Second, Outcome: crm.OutcomeAnswered,
		OwnerID: "005A", Contact: &crm.Contact{ID: "003000000000001AAA", Name: "Ann"}, RecordingURL: "https://hello.example/r/t.wav"}
	ref, err := c.LogCall(context.Background(), conn(), l)
	if err != nil || ref != "00T000000000001AAA" {
		t.Fatalf("ref = %q, %v", ref, err)
	}
	r := s.calls[0]
	if r.Method != "POST" || r.Path != "/services/data/v62.0/sobjects/Task" {
		t.Fatalf("request = %+v", r)
	}
	var b map[string]any
	_ = json.Unmarshal([]byte(r.Body), &b)
	want := map[string]any{"Subject": "Call from Ann", "Status": "Completed", "TaskSubtype": "Call", "CallType": "Inbound",
		"CallDurationInSeconds": float64(65), "CallDisposition": "Connected", "CallObject": "c-1", "ActivityDate": "2026-11-12",
		"OwnerId": "005A", "WhoId": "003000000000001AAA"}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
	if d, _ := b["Description"].(string); !strings.Contains(d, "Recording: https://hello.example/r/t.wav") {
		t.Errorf("description = %q", d)
	}

	// Missed, outbound, an account (not a contact) match.
	l = crm.CallLog{CallID: "c-2", Direction: "outbound", Number: "+3211", Missed: true, Outcome: crm.OutcomeNoAnswer,
		Contact: &crm.Contact{ID: "001000000000001AAA", Name: "Acme"}}
	_, _ = c.LogCall(context.Background(), conn(), l)
	b = map[string]any{}
	_ = json.Unmarshal([]byte(s.calls[1].Body), &b)
	if b["CallType"] != "Outbound" || b["CallDisposition"] != "No answer" || b["Subject"] != "Missed call from Acme" {
		t.Errorf("missed call = %v", b)
	}
	if _, has := b["WhoId"]; has {
		t.Error("a record that is neither a contact nor a lead became the WhoId")
	}
	if _, has := b["OwnerId"]; has {
		t.Error("an empty owner was sent")
	}
}

func TestUpdateKeepsWhatAPersonWrote(t *testing.T) {
	c, s := newSF(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"Description":"Asked for a quote.\nRecording: https://hello.example/r/old.wav\nCall me back","WhoId":null}`))
			return
		}
		w.WriteHeader(204)
	})
	l := crm.CallLog{Duration: 90 * time.Second, Outcome: crm.OutcomeAnswered, RecordingURL: "https://hello.example/r/new.wav",
		Contact: &crm.Contact{ID: "00Q000000000001AAA"}}
	if err := c.UpdateCallLog(context.Background(), conn(), "00T000000000001AAA", l); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 || s.calls[1].Method != "PATCH" || s.calls[1].Path != "/services/data/v62.0/sobjects/Task/00T000000000001AAA" {
		t.Fatalf("calls = %+v", s.calls)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(s.calls[1].Body), &p)
	if p["Description"] != "Asked for a quote.\nRecording: https://hello.example/r/new.wav\nCall me back" {
		t.Errorf("description = %q", p["Description"])
	}
	if p["CallDurationInSeconds"] != float64(90) || p["WhoId"] != "00Q000000000001AAA" {
		t.Errorf("patch = %v", p)
	}
	// Without a link the description is not touched.
	l.RecordingURL = ""
	_ = c.UpdateCallLog(context.Background(), conn(), "00T000000000001AAA", l)
	var p2 map[string]any
	_ = json.Unmarshal([]byte(s.calls[3].Body), &p2)
	if _, has := p2["Description"]; has {
		t.Error("the description was rewritten without a link")
	}
	if err := c.UpdateCallLog(context.Background(), conn(), "../x", l); err == nil {
		t.Error("a path was accepted as a task id")
	}
}

func TestErrors(t *testing.T) {
	status, body := 401, `[{"message":"Session expired","errorCode":"INVALID_SESSION_ID"}]`
	c, _ := newSF(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	if err := c.Ping(context.Background(), conn()); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("401 = %v, want ErrAuth", err)
	}
	status, body = 403, `[{"message":"TotalRequests Limit exceeded.","errorCode":"REQUEST_LIMIT_EXCEEDED"}]`
	var ae *crm.APIError
	if err := c.Ping(context.Background(), conn()); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("API limit = %v, want a retry", err)
	}
	status, body = 403, `[{"message":"no access","errorCode":"INSUFFICIENT_ACCESS"}]`
	if err := c.Ping(context.Background(), conn()); !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "INSUFFICIENT_ACCESS") {
		t.Fatalf("403 = %v, want a permanent failure", err)
	}
	status, body = 503, ``
	if err := c.Ping(context.Background(), conn()); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("503 = %v", err)
	}
	if !c.ValidRef("00T000000000001AAA") || c.ValidRef("x/../y") || c.ValidRef("") {
		t.Fatal("ValidRef")
	}
}
