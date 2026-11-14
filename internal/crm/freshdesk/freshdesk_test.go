package freshdesk

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
	cfg := crm.Config{Kind: Kind, ClientSecret: "s3cr3t", Extra: map[string]string{"subdomain": "acme", "answeredStatus": "open"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "s3cr3t", ExternalAcct: "acme"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+32475123456", From: "+32475123456", To: "+3211223344",
		StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 30 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "88"}
}

func TestRegisteredAndValidated(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, direct := c.(crm.Direct); !direct || !c.Capabilities().Ticket {
		t.Fatal("freshdesk is a direct ticket connector")
	}
	fd := New(nil)
	if got, err := fd.ValidateConfig(map[string]string{"subdomain": "Acme", "clientId": ""}); err != nil || got["subdomain"] != "acme" || got["answeredStatus"] != "open" {
		t.Fatalf("config = %v %v", got, err)
	}
	for _, bad := range []map[string]string{{}, {"subdomain": "a.b"}, {"subdomain": "acme", "clientId": "x"}, {"subdomain": "acme", "answeredStatus": "closed"}, {"subdomain": "acme", "q": "1"}} {
		if _, err := fd.ValidateConfig(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestAuthenticateUsesTheKeyAsBasicUser(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/agents/me", 200, `{"id":1}`)
	tok, err := c.Authenticate(context.Background(), cc.Config)
	if err != nil || tok.Access != "s3cr3t" || tok.Account != "acme" {
		t.Fatalf("tokens = %+v %v", tok, err)
	}
	if got := srv.Calls("")[0].Header.Get("Authorization"); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("s3cr3t:X")) {
		t.Fatalf("authorization = %q", got)
	}
	srv.JSON("GET /api/v2/agents/me", 401, `{"code":"invalid_credentials"}`)
	var ae *crm.APIError
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || ae.Retry || strings.Contains(ae.Msg, "s3cr3t") {
		t.Fatalf("refused = %v", err)
	}
}

func TestLookupTriesTheFormatsAndChecksTheNumber(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("GET /api/v2/contacts", func(w http.ResponseWriter, r *http.Request, _ string) {
		q := r.URL.Query()
		switch {
		case q.Get("mobile") == "32475123456":
			_, _ = w.Write([]byte(`[{"id":7,"name":"Wrong","mobile":"32475999999"},{"id":8,"name":"Ann Smith","mobile":"32475123456","company_id":4}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	srv.JSON("GET /api/v2/companies/4", 200, `{"name":"ACME"}`)
	got, err := c.Lookup(context.Background(), cc, "0032 475 12 34 56")
	if err != nil || got == nil || got.ID != "8" || got.Name != "Ann Smith" || got.Company != "ACME" || got.URL != "https://acme.freshdesk.com/a/contacts/8" {
		t.Fatalf("contact = %+v %v", got, err)
	}
	srv.Handle("GET /api/v2/contacts", func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(`[]`)) })
	if got, err := c.Lookup(context.Background(), cc, "+32475123456"); got != nil || err != nil {
		t.Fatalf("miss = %+v %v", got, err)
	}
}

func TestResolveUserMatchesTheAgentEmail(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /api/v2/agents", 200, `[{"id":1,"contact":{"email":"other@acme.test"}},{"id":2,"contact":{"email":"Ann@acme.test"}}]`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@acme.test"); err != nil || id != "2" {
		t.Fatalf("id = %q %v", id, err)
	}
	if id, _ := c.ResolveUser(context.Background(), cc, "zed@acme.test"); id != "" {
		t.Fatal("matched a stranger")
	}
}

func TestLogCallWithAndWithoutAContact(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /api/v2/tickets", 201, `{"id":99}`)
	l := call()
	l.Contact = &crm.Contact{ID: "8", Name: "Ann"}
	if ref, err := c.LogCall(context.Background(), cc, l); err != nil || ref != "99" {
		t.Fatalf("ref = %q %v", ref, err)
	}
	var tk map[string]any
	if err := json.Unmarshal([]byte(srv.Calls("POST /api/v2/tickets")[0].Body), &tk); err != nil {
		t.Fatal(err)
	}
	if tk["requester_id"] != float64(8) || tk["source"] != float64(3) || tk["status"] != float64(2) || tk["priority"] != float64(1) ||
		tk["responder_id"] != float64(88) || tk["subject"] != "Call from Ann" || tk["phone"] != nil {
		t.Fatalf("ticket = %v", tk)
	}
	if d, _ := tk["description"].(string); !strings.Contains(d, "<br>") {
		t.Fatalf("description is not html: %q", d)
	}
	// No contact: the phone number makes Freshdesk create one; it needs a name.
	if _, err := c.LogCall(context.Background(), cc, call()); err != nil {
		t.Fatal(err)
	}
	b := srv.Calls("POST /api/v2/tickets")[1].Body
	if !strings.Contains(b, `"phone":"+32475123456"`) || !strings.Contains(b, `"name":"+32475123456"`) || strings.Contains(b, "requester_id") {
		t.Fatalf("anonymous ticket = %s", b)
	}
	// Answered status setting, and a missed call stays open.
	cc.Config.Extra["answeredStatus"] = "resolved"
	if _, err := c.LogCall(context.Background(), cc, call()); err != nil {
		t.Fatal(err)
	}
	m := call()
	m.Missed, m.Outcome = true, crm.OutcomeNoAnswer
	if _, err := c.LogCall(context.Background(), cc, m); err != nil {
		t.Fatal(err)
	}
	all := srv.Calls("POST /api/v2/tickets")
	if !strings.Contains(all[2].Body, `"status":4`) || !strings.Contains(all[3].Body, `"status":2`) || !strings.Contains(all[3].Body, "missed_call") {
		t.Fatalf("statuses: %s | %s", all[2].Body, all[3].Body)
	}
}

func TestUpdateAddsAPrivateNoteOnce(t *testing.T) {
	c, srv, cc := setup(t)
	conv := `[{"body":"x","body_text":"x"}]`
	srv.Handle("GET /api/v2/tickets/99/conversations", func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(conv)) })
	srv.JSON("POST /api/v2/tickets/99/notes", 201, `{}`)
	l := call()
	if err := c.UpdateCallLog(context.Background(), cc, "99", l); err != nil || len(srv.Calls("")) != 0 {
		t.Fatalf("no link must do nothing: %v", err)
	}
	l.RecordingURL = "https://h.example/r/abc.wav"
	if err := c.UpdateCallLog(context.Background(), cc, "99", l); err != nil {
		t.Fatal(err)
	}
	if n := srv.Calls("POST /api/v2/tickets/99/notes"); len(n) != 1 || !strings.Contains(n[0].Body, `"private":true`) || !strings.Contains(n[0].Body, "abc.wav") {
		t.Fatalf("notes = %+v", n)
	}
	conv = `[{"body":"<a href=\"https://h.example/r/abc.wav\">","body_text":"Recording: https://h.example/r/abc.wav"}]`
	if err := c.UpdateCallLog(context.Background(), cc, "99", l); err != nil || len(srv.Calls("POST /api/v2/tickets/99/notes")) != 1 {
		t.Fatal("wrote the same link twice")
	}
}
