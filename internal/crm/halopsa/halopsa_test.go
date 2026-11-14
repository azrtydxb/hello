package halopsa

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
	cfg := crm.Config{Kind: Kind, ClientID: "cid", ClientSecret: "s3cr3t", Extra: map[string]string{"clientId": "cid", "host": "acme.halopsa.com", "tenant": "acme"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "access-1", ExternalAcct: "acme.halopsa.com"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+32475123456", From: "+32475123456", To: "+3211223344",
		StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 30 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "12"}
}

func TestRegisteredAndValidated(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, direct := c.(crm.Direct); !direct || !c.Capabilities().Ticket {
		t.Fatal("halo is a direct ticket connector")
	}
	h := New(nil)
	if got, err := h.ValidateConfig(map[string]string{"clientId": "x", "host": "Acme.HaloPSA.com", "ticketTypeId": "3"}); err != nil || got["host"] != "acme.halopsa.com" {
		t.Fatalf("config = %v %v", got, err)
	}
	for _, bad := range []map[string]string{
		{"host": "https://acme.halopsa.com"}, {"host": "10.0.0.1"}, {"host": "acme.halopsa.com/x"}, {"host": ""},
		{"host": "acme.halopsa.com", "ticketTypeId": "abc"}, {"host": "acme.halopsa.com", "tenant": "a b"}, {"host": "acme.halopsa.com", "z": "1"},
	} {
		if _, err := h.ValidateConfig(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestAuthenticateRequestsAClientCredentialsToken(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("POST /auth/token", func(w http.ResponseWriter, r *http.Request, body string) {
		f, _ := url.ParseQuery(body)
		if f.Get("grant_type") != "client_credentials" || f.Get("client_id") != "cid" || f.Get("client_secret") != "s3cr3t" || r.URL.Query().Get("tenant") != "acme" {
			t.Errorf("request = %q %q", body, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"access_token":"access-9","token_type":"Bearer","expires_in":3600}`))
	})
	before := time.Now()
	tok, err := c.Authenticate(context.Background(), cc.Config)
	if err != nil || tok.Access != "access-9" || tok.Account != "acme.halopsa.com" || tok.ExpiresAt.Before(before.Add(59*time.Minute)) {
		t.Fatalf("tokens = %+v %v", tok, err)
	}
	srv.JSON("POST /auth/token", 400, `{"error":"invalid_client"}`)
	var ae *crm.APIError
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || ae.Retry || strings.Contains(ae.Msg, "s3cr3t") {
		t.Fatalf("refused = %v", err)
	}
	srv.JSON("POST /auth/token", 503, `{}`)
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("outage must be retryable: %v", err)
	}
}

func TestLookupAcceptsBothListShapesAndChecksTheNumber(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/users", 200, `{"record_count":2,"users":[{"id":1,"name":"Wrong","client_id":2,"mobilenumber":"0475 99 99 99"},{"id":5,"name":"Ann Smith","client_id":9,"client_name":"ACME","mobilenumber":"0475 12 34 56"}]}`)
	got, err := c.Lookup(context.Background(), cc, "+32475123456")
	if err != nil || got == nil || got.ID != "5:9" || got.Name != "Ann Smith" || got.Company != "ACME" {
		t.Fatalf("contact = %+v %v", got, err)
	}
	if q := srv.Calls("GET /api/users")[0].Query; !strings.Contains(q, "search=3456") {
		t.Fatalf("query = %q", q)
	}
	if h := srv.Calls("")[0].Header.Get("Authorization"); h != "Bearer access-1" {
		t.Fatalf("authorization = %q", h)
	}
	srv.JSON("GET /api/users", 200, `[{"id":1,"name":"Nobody","client_id":2,"phonenumber":"0499"}]`)
	if got, err := c.Lookup(context.Background(), cc, "+32475123456"); got != nil || err != nil {
		t.Fatalf("miss = %+v %v", got, err)
	}
}

func TestResolveUser(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/agent", 200, `[{"id":3,"email":"x@acme.test"},{"id":4,"email":"Ann@Acme.test"}]`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@acme.test"); err != nil || id != "4" {
		t.Fatalf("id = %q %v", id, err)
	}
}

func TestLogCallForAUserTheFallbackClientOrSkip(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /api/tickets", 201, `[{"id":501}]`)
	l := call()
	l.Contact = &crm.Contact{ID: "5:9", Name: "Ann"}
	if ref, err := c.LogCall(context.Background(), cc, l); err != nil || ref != "501" {
		t.Fatalf("ref = %q %v", ref, err)
	}
	b := srv.Calls("POST /api/tickets")[0].Body
	if !strings.HasPrefix(b, "[{") || !strings.Contains(b, `"user_id":5`) || !strings.Contains(b, `"client_id":9`) || !strings.Contains(b, `"agent_id":12`) ||
		!strings.Contains(b, `"summary":"Call from Ann"`) || strings.Contains(b, "tickettype_id") {
		t.Fatalf("ticket = %s", b)
	}
	// Unknown caller: no client, so skipped.
	if _, err := c.LogCall(context.Background(), cc, call()); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("no fallback = %v", err)
	}
	cc.Config.Extra["fallbackClientId"], cc.Config.Extra["ticketTypeId"] = "77", "4"
	srv.JSON("POST /api/tickets", 201, `{"id":502}`)
	if ref, err := c.LogCall(context.Background(), cc, call()); err != nil || ref != "502" {
		t.Fatalf("fallback = %q %v", ref, err)
	}
	if b := srv.Calls("POST /api/tickets")[1].Body; !strings.Contains(b, `"client_id":77`) || !strings.Contains(b, `"tickettype_id":4`) || strings.Contains(b, "user_id") {
		t.Fatalf("fallback ticket = %s", b)
	}
}

func TestUpdateAddsAHiddenActionOnce(t *testing.T) {
	c, srv, cc := setup(t)
	actions := `{"actions":[{"note":"hi"}]}`
	srv.Handle("GET /api/actions", func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(actions)) })
	srv.JSON("POST /api/actions", 201, `[]`)
	l := call()
	l.RecordingURL = "https://h.example/r/abc.wav"
	if err := c.UpdateCallLog(context.Background(), cc, "501", l); err != nil {
		t.Fatal(err)
	}
	w := srv.Calls("POST /api/actions")
	if len(w) != 1 || !strings.Contains(w[0].Body, `"hiddenfromuser":true`) || !strings.Contains(w[0].Body, `"ticket_id":501`) {
		t.Fatalf("actions = %+v", w)
	}
	actions = `{"actions":[{"note_html":"Recording: <a href=\"https://h.example/r/abc.wav\">"}]}`
	if err := c.UpdateCallLog(context.Background(), cc, "501", l); err != nil || len(srv.Calls("POST /api/actions")) != 1 {
		t.Fatal("wrote the link twice")
	}
}
