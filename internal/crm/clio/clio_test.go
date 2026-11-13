package clio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/kit/kittest"
)

func setup(t *testing.T) (*Connector, *kittest.Server, crm.Conn) {
	t.Helper()
	srv := kittest.New(t)
	c := New(srv.Client)
	c.Base = srv.URL
	cfg := crm.Config{Kind: Kind, ClientID: "key", ClientSecret: "s3cr3t", Extra: map[string]string{"clientId": "key", "region": "eu"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "acc"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+32475123456", From: "+32475123456", To: "+3211223344",
		StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 30 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "55"}
}

func TestRegisteredAsOAuth(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, oa := c.(crm.OAuth); !oa {
		t.Fatal("clio uses OAuth")
	}
	if _, direct := c.(crm.Direct); direct {
		t.Fatal("clio must not be direct")
	}
	if caps := c.Capabilities(); caps.Ticket || !caps.LogCalls || !caps.Lookup || !caps.Recordings {
		t.Fatalf("caps = %+v", caps)
	}
	if got, err := New(nil).ValidateConfig(map[string]string{"clientId": "k"}); err != nil || got["region"] != "us" {
		t.Fatalf("default region = %v %v", got, err)
	}
	if _, err := New(nil).ValidateConfig(map[string]string{"clientId": "k", "region": "mars"}); err == nil {
		t.Fatal("accepted an unknown region")
	}
}

func TestAuthURLPerRegion(t *testing.T) {
	c := New(nil)
	u, err := url.Parse(c.AuthURL(crm.Config{ClientID: "key", Extra: map[string]string{"region": "eu"}}, "https://hello.test/cb", "st"))
	q := u.Query()
	if err != nil || u.Host != "eu.app.clio.com" || u.Path != "/oauth/authorize" || q.Get("state") != "st" || q.Get("client_id") != "key" || q.Get("response_type") != "code" {
		t.Fatalf("url = %v %v", u, err)
	}
	if u, _ := url.Parse(c.AuthURL(crm.Config{Extra: map[string]string{}}, "x", "y")); u.Host != "app.clio.com" {
		t.Fatalf("default host = %s", u.Host)
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request, body string) {
		f, _ := url.ParseQuery(body)
		switch f.Get("grant_type") {
		case "authorization_code":
			if f.Get("code") != "good" || f.Get("client_secret") != "s3cr3t" {
				t.Errorf("exchange = %q", body)
			}
			_, _ = w.Write([]byte(`{"access_token":"a1","refresh_token":"r1","expires_in":604800}`))
		case "refresh_token":
			if f.Get("refresh_token") == "dead" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"a2","expires_in":604800}`))
		}
	})
	srv.JSON("GET /api/v4/users/who_am_i.json", 200, `{"data":{"name":"Ann Smith"}}`)
	tok, err := c.Exchange(context.Background(), cc.Config, "https://hello.test/cb", "good")
	if err != nil || tok.Access != "a1" || tok.Refresh != "r1" || tok.Account != "Ann Smith" {
		t.Fatalf("exchange = %+v %v", tok, err)
	}
	if tok, err := c.Refresh(context.Background(), cc.Config, "r1"); err != nil || tok.Access != "a2" || tok.Refresh != "" {
		t.Fatalf("refresh = %+v %v", tok, err)
	}
	var ae *crm.APIError
	if _, err := c.Refresh(context.Background(), cc.Config, "dead"); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("a rejected refresh token must be permanent: %v", err)
	}
}

func TestLookupMatchesByNumberAndKeepsTheKind(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v4/contacts.json", 200, `{"data":[
		{"id":1,"name":"Wrong","type":"Person","phone_numbers":[{"number":"0475 99 99 99"}]},
		{"id":2,"name":"Ann Smith","type":"Person","company":{"name":"Firm"},"phone_numbers":[{"number":"+32 475 12 34 56"}]}]}`)
	got, err := c.Lookup(context.Background(), cc, "0032475123456")
	if err != nil || got == nil || got.ID != "Person:2" || got.Company != "Firm" {
		t.Fatalf("contact = %+v %v", got, err)
	}
	if q := srv.Calls("GET /api/v4/contacts.json")[0].Query; !strings.Contains(q, "query=3456") {
		t.Fatalf("query = %q", q)
	}
	srv.JSON("GET /api/v4/contacts.json", 200, `{"data":[]}`)
	if got, err := c.Lookup(context.Background(), cc, "+32475123456"); got != nil || err != nil {
		t.Fatalf("miss = %+v %v", got, err)
	}
}

func TestResolveUser(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v4/users.json", 200, `{"data":[{"id":1,"email":"other@firm.test"},{"id":2,"email":"Ann@Firm.test"}]}`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@firm.test"); err != nil || id != "2" {
		t.Fatalf("id = %q %v", id, err)
	}
}

func TestLogCallDirection(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /api/v4/communications.json", 201, `{"data":{"id":8001}}`)
	l := call()
	l.Contact = &crm.Contact{ID: "Person:2", Name: "Ann"}
	if ref, err := c.LogCall(context.Background(), cc, l); err != nil || ref != "8001" {
		t.Fatalf("ref = %q %v", ref, err)
	}
	b := srv.Calls("POST /api/v4/communications.json")[0].Body
	for _, want := range []string{`"type":"PhoneCommunication"`, `"senders":[{"id":2,"type":"Person"}]`, `"receivers":[{"id":55,"type":"User"}]`, `"date":"2026-11-12"`} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %s: %s", want, b)
		}
	}
	l.Direction = "outbound"
	if _, err := c.LogCall(context.Background(), cc, l); err != nil {
		t.Fatal(err)
	}
	b = srv.Calls("POST /api/v4/communications.json")[1].Body
	if !strings.Contains(b, `"senders":[{"id":55,"type":"User"}]`) || !strings.Contains(b, `"receivers":[{"id":2,"type":"Person"}]`) {
		t.Fatalf("outbound = %s", b)
	}
}

func TestLogCallNeedsAContactAndAUser(t *testing.T) {
	c, _, cc := setup(t)
	if _, err := c.LogCall(context.Background(), cc, call()); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("no contact = %v", err)
	}
	l := call()
	l.Contact, l.OwnerID = &crm.Contact{ID: "Person:2"}, ""
	if _, err := c.LogCall(context.Background(), cc, l); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("no user = %v", err)
	}
	l.Contact, l.OwnerID = &crm.Contact{ID: "garbage"}, "5"
	var ae *crm.APIError
	if _, err := c.LogCall(context.Background(), cc, l); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("bad id = %v", err)
	}
}

func TestUpdateIsIdempotent(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("PATCH /api/v4/communications/8001.json", 200, `{}`)
	l := call()
	l.RecordingURL = "https://h.example/r/abc.wav"
	for i := 0; i < 2; i++ {
		if err := c.UpdateCallLog(context.Background(), cc, "8001", l); err != nil {
			t.Fatal(err)
		}
	}
	p := srv.Calls("PATCH /api/v4/communications/8001.json")
	if len(p) != 2 || p[0].Body != p[1].Body || !strings.Contains(p[0].Body, "abc.wav") {
		t.Fatalf("patches = %+v", p)
	}
}
