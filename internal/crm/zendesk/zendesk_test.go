package zendesk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
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
	cfg := crm.Config{Kind: Kind, ClientID: "agent@acme.test", ClientSecret: "s3cr3t",
		Extra: map[string]string{"clientId": "agent@acme.test", "subdomain": "acme", "createContacts": "true", "answeredStatus": "open"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "s3cr3t", ExternalAcct: "acme"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+32475123456", From: "+32475123456", To: "+3211223344",
		Extension: "201", StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 65 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "77"}
}

func TestRegisteredAsDirectTicketConnector(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, direct := c.(crm.Direct); !direct {
		t.Fatal("zendesk signs in with a token, not OAuth")
	}
	if caps := c.Capabilities(); !caps.Ticket || !caps.Lookup || !caps.LogCalls || !caps.Recordings || caps.ClickToCall {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestValidateConfig(t *testing.T) {
	c := New(http.DefaultClient)
	got, err := c.ValidateConfig(map[string]string{"clientId": "a@b.test", "subdomain": "ACME"})
	if err != nil || got["subdomain"] != "acme" || got["createContacts"] != "true" || got["answeredStatus"] != "open" {
		t.Fatalf("defaults = %v %v", got, err)
	}
	for name, cfg := range map[string]map[string]string{
		"no subdomain":  {"clientId": "a@b.test"},
		"dotted":        {"clientId": "a@b.test", "subdomain": "a.evil.test"},
		"no email":      {"clientId": "agent", "subdomain": "acme"},
		"unknown key":   {"clientId": "a@b.test", "subdomain": "acme", "x": "1"},
		"bad status":    {"clientId": "a@b.test", "subdomain": "acme", "answeredStatus": "closed"},
		"bad createCon": {"clientId": "a@b.test", "subdomain": "acme", "createContacts": "yes"},
	} {
		if _, err := c.ValidateConfig(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthenticateSendsEmailSlashToken(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/users/me.json", 200, `{"user":{"id":1}}`)
	tok, err := c.Authenticate(context.Background(), cc.Config)
	if err != nil || tok.Access != "s3cr3t" || tok.Account != "acme" || !tok.ExpiresAt.IsZero() {
		t.Fatalf("tokens = %+v %v", tok, err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("agent@acme.test/token:s3cr3t"))
	if got := srv.Calls("GET /api/v2/users/me.json")[0].Header.Get("Authorization"); got != want {
		t.Fatalf("authorization = %q", got)
	}
}

func TestAuthenticateRefusedIsPermanent(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/users/me.json", 401, `{"error":"Couldn't authenticate you"}`)
	_, err := c.Authenticate(context.Background(), cc.Config)
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Retry || strings.Contains(ae.Msg, "s3cr3t") {
		t.Fatalf("err = %v", err)
	}
}

func TestLookupFindsTheUserAndHisOrganization(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("GET /api/v2/search.json", func(w http.ResponseWriter, r *http.Request, _ string) {
		if q := r.URL.Query().Get("query"); q != "type:user phone:+32475123456" {
			t.Errorf("query = %q", q)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":5,"name":"Other","phone":"+32999999999"},{"id":9,"name":"Ann Smith","phone":"+32475123456","organization_id":3}]}`))
	})
	srv.JSON("GET /api/v2/organizations/3.json", 200, `{"organization":{"name":"ACME"}}`)
	got, err := c.Lookup(context.Background(), cc, "0032 475 12 34 56")
	if err != nil || got == nil || got.ID != "9" || got.Name != "Ann Smith" || got.Company != "ACME" || got.URL != "https://acme.zendesk.com/agent/users/9" {
		t.Fatalf("contact = %+v %v", got, err)
	}
	srv.JSON("GET /api/v2/search.json", 200, `{"results":[]}`)
	if got, err := c.Lookup(context.Background(), cc, "+32475123456"); got != nil || err != nil {
		t.Fatalf("miss = %+v %v", got, err)
	}
	// A number Zendesk cannot hold in E.164 is not looked up at all.
	before := len(srv.Calls(""))
	if got, _ := c.Lookup(context.Background(), cc, "0475123456"); got != nil || len(srv.Calls("")) != before {
		t.Fatal("national number queried")
	}
}

func TestResolveUserNeedsAnAgentWithThatEmail(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/users/search.json", 200, `{"users":[{"id":1,"email":"x@acme.test","role":"agent"},{"id":2,"email":"Ann@Acme.test","role":"end-user"},{"id":3,"email":"ann@acme.test","role":"admin"}]}`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@acme.test"); err != nil || id != "3" {
		t.Fatalf("id = %q %v", id, err)
	}
	if id, _ := c.ResolveUser(context.Background(), cc, "nobody@acme.test"); id != "" {
		t.Fatalf("id = %q", id)
	}
}

func TestLogCallCreatesAnInternalTicket(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /api/v2/tickets.json", 201, `{"ticket":{"id":4321}}`)
	l := call()
	l.Contact = &crm.Contact{ID: "9", Name: "Ann"}
	ref, err := c.LogCall(context.Background(), cc, l)
	if err != nil || ref != "4321" {
		t.Fatalf("ref = %q %v", ref, err)
	}
	var body struct {
		Ticket struct {
			Subject     string
			RequesterID int64 `json:"requester_id"`
			AssigneeID  int64 `json:"assignee_id"`
			Status      string
			Tags        []string
			ExternalID  string `json:"external_id"`
			Comment     struct {
				Body   string
				Public bool
			}
		}
	}
	if err := json.Unmarshal([]byte(srv.Calls("POST /api/v2/tickets.json")[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	tk := body.Ticket
	if tk.Subject != "Call from Ann" || tk.RequesterID != 9 || tk.AssigneeID != 77 || tk.Status != "open" || tk.ExternalID != "hello:c1" ||
		tk.Comment.Public || !strings.Contains(tk.Comment.Body, "talk time 1m 5s") || strings.Join(tk.Tags, ",") != "hello,call_inbound" {
		t.Fatalf("ticket = %+v", tk)
	}
	// A missed call is a new ticket tagged as missed.
	l.Missed, l.Outcome = true, crm.OutcomeNoAnswer
	if _, err := c.LogCall(context.Background(), cc, l); err != nil {
		t.Fatal(err)
	}
	last := srv.Calls("POST /api/v2/tickets.json")[1].Body
	if !strings.Contains(last, `"status":"new"`) || !strings.Contains(last, "missed_call") {
		t.Fatalf("missed = %s", last)
	}
}

func TestLogCallCreatesTheEndUserOrSkips(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /api/v2/users/create_or_update.json", 201, `{"user":{"id":55}}`)
	srv.JSON("POST /api/v2/tickets.json", 201, `{"ticket":{"id":1}}`)
	if _, err := c.LogCall(context.Background(), cc, call()); err != nil {
		t.Fatal(err)
	}
	if b := srv.Calls("POST /api/v2/users/create_or_update.json")[0].Body; !strings.Contains(b, `"phone":"+32475123456"`) || !strings.Contains(b, `"role":"end-user"`) {
		t.Fatalf("user = %s", b)
	}
	if b := srv.Calls("POST /api/v2/tickets.json")[0].Body; !strings.Contains(b, `"requester_id":55`) {
		t.Fatalf("ticket = %s", b)
	}
	// Creating users switched off.
	cc.Config.Extra["createContacts"] = "false"
	if _, err := c.LogCall(context.Background(), cc, call()); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("off = %v", err)
	}
	// A national number cannot be stored as a Zendesk phone.
	cc.Config.Extra["createContacts"] = "true"
	l := call()
	l.Number = "0475123456"
	if _, err := c.LogCall(context.Background(), cc, l); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("national = %v", err)
	}
}

func TestUpdateAddsTheRecordingOnceAndNeverTwice(t *testing.T) {
	c, srv, cc := setup(t)
	comments := `{"comments":[{"body":"first"}]}`
	srv.Handle("GET /api/v2/tickets/4321/comments.json", func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(comments)) })
	srv.JSON("PUT /api/v2/tickets/4321.json", 200, `{}`)
	l := call()
	if err := c.UpdateCallLog(context.Background(), cc, "4321", l); err != nil || len(srv.Calls("")) != 0 {
		t.Fatalf("no link must be a no-op: %v", err)
	}
	l.RecordingURL = "https://h.example/r/abc.wav"
	if err := c.UpdateCallLog(context.Background(), cc, "4321", l); err != nil {
		t.Fatal(err)
	}
	put := srv.Calls("PUT /api/v2/tickets/4321.json")
	if len(put) != 1 || !strings.Contains(put[0].Body, `"public":false`) || !strings.Contains(put[0].Body, "https://h.example/r/abc.wav") {
		t.Fatalf("put = %+v", put)
	}
	comments = `{"comments":[{"body":"Recording: https://h.example/r/abc.wav"}]}`
	if err := c.UpdateCallLog(context.Background(), cc, "4321", l); err != nil || len(srv.Calls("PUT /api/v2/tickets/4321.json")) != 1 {
		t.Fatalf("repeat wrote again: %v", err)
	}
}

func TestExpiredTokenIsAnAuthError(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/search.json", 401, `{"error":"x"}`)
	if _, err := c.Lookup(context.Background(), cc, "+32475123456"); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}
