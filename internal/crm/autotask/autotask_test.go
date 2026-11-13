package autotask

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

func setup(t *testing.T) (*Connector, *kittest.Server, crm.Conn) {
	t.Helper()
	srv := kittest.New(t)
	c := New(srv.Client)
	c.Base, c.Discovery = srv.URL, srv.URL
	cfg := crm.Config{Kind: Kind, ClientID: "api@acme.test", ClientSecret: "s3cr3t", Extra: map[string]string{"clientId": "api@acme.test", "integrationCode": "CODE"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "s3cr3t", ExternalAcct: "webservices5.autotask.net"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "outbound", Number: "+15551234567", From: "+15559876543", To: "+15551234567",
		StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 60 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "30"}
}

func TestRegisteredAndValidated(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, direct := c.(crm.Direct); !direct || c.Capabilities().Ticket {
		t.Fatal("autotask is direct and logs notes")
	}
	a := New(nil)
	if got, err := a.ValidateConfig(map[string]string{"clientId": "api@acme.test", "integrationCode": "C", "host": "Webservices5.Autotask.net"}); err != nil || got["host"] != "webservices5.autotask.net" {
		t.Fatalf("config = %v %v", got, err)
	}
	for _, bad := range []map[string]string{
		{"clientId": "api", "integrationCode": "C"}, {"clientId": "a@b.test"}, {"clientId": "a@b.test", "integrationCode": "C", "host": "evil.example.com"},
		{"clientId": "a@b.test", "integrationCode": "C", "actionType": "x"}, {"clientId": "a@b.test", "integrationCode": "C", "z": "1"},
	} {
		if _, err := a.ValidateConfig(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestAuthenticateDiscoversTheZone(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /atservicesrest/v1.0/zoneInformation", 200, `{"url":"https://webservices5.autotask.net/atservicesrest/"}`)
	srv.JSON("GET /Contacts/entityInformation", 200, `{}`)
	tok, err := c.Authenticate(context.Background(), cc.Config)
	if err != nil || tok.Access != "s3cr3t" || tok.Account != "webservices5.autotask.net" {
		t.Fatalf("tokens = %+v %v", tok, err)
	}
	h := srv.Calls("GET /Contacts/entityInformation")[0].Header
	if h.Get("UserName") != "api@acme.test" || h.Get("Secret") != "s3cr3t" || h.Get("ApiIntegrationCode") != "CODE" {
		t.Fatalf("headers = %v", h)
	}
	srv.JSON("GET /atservicesrest/v1.0/zoneInformation", 200, `{"url":"https://evil.example.com/atservicesrest/"}`)
	var ae *crm.APIError
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("foreign zone = %v", err)
	}
	cc.Config.Extra["host"] = "webservices5.autotask.net"
	srv.JSON("GET /Contacts/entityInformation", 401, `{"errors":["Unauthorized"]}`)
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || ae.Status != 401 || ae.Retry || strings.Contains(ae.Msg, "s3cr3t") {
		t.Fatalf("refused = %v", err)
	}
}

func TestLookup(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("POST /Contacts/query", func(w http.ResponseWriter, _ *http.Request, body string) {
		for _, want := range []string{`"field":"phone"`, `"field":"mobilePhone"`, `"field":"alternatePhone"`, `"value":"4567"`, `"field":"isActive"`} {
			if !strings.Contains(body, want) {
				t.Errorf("filter lacks %s: %s", want, body)
			}
		}
		_, _ = w.Write([]byte(`{"items":[
			{"id":1,"firstName":"Wrong","lastName":"One","companyID":5,"phone":"555-999-4567"},
			{"id":2,"firstName":"Ann","lastName":"Smith","companyID":6,"mobilePhone":"(555) 123-4567"}]}`))
	})
	srv.JSON("GET /Companies/6", 200, `{"item":{"companyName":"ACME"}}`)
	got, err := c.Lookup(context.Background(), cc, "+15551234567")
	if err != nil || got == nil || got.ID != "2:6" || got.Name != "Ann Smith" || got.Company != "ACME" {
		t.Fatalf("contact = %+v %v", got, err)
	}
}

func TestResolveUser(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("POST /Resources/query", 200, `{"items":[{"id":30,"email":"Ann@Acme.test"}]}`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@acme.test"); err != nil || id != "30" {
		t.Fatalf("id = %q %v", id, err)
	}
}

func TestLogCallFindsTheCallActionType(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /CompanyNotes/entityInformation/fields", 200, `{"fields":[{"name":"actionType","picklistValues":[{"value":"3","label":"Email"},{"value":"7","label":"Call"}]}]}`)
	srv.JSON("POST /CompanyNotes", 201, `{"itemId":4400}`)
	l := call()
	l.Contact = &crm.Contact{ID: "2:6", Name: "Ann"}
	for i := 0; i < 2; i++ {
		if ref, err := c.LogCall(context.Background(), cc, l); err != nil || ref != "4400" {
			t.Fatalf("ref = %q %v", ref, err)
		}
	}
	if len(srv.Calls("GET /CompanyNotes/entityInformation/fields")) != 1 {
		t.Fatal("pick list read twice")
	}
	b := srv.Calls("POST /CompanyNotes")[0].Body
	for _, want := range []string{`"companyID":6`, `"contactID":2`, `"assignedResourceID":30`, `"actionType":7`, `"endDateTime":"2026-11-12T14:01:00Z"`, "Call to Ann"} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %s: %s", want, b)
		}
	}
	cc.Config.Extra["actionType"] = "9"
	if _, err := c.LogCall(context.Background(), cc, l); err != nil || !strings.Contains(srv.Calls("POST /CompanyNotes")[2].Body, `"actionType":9`) {
		t.Fatalf("configured: %v", err)
	}
}

func TestLogCallSkipsAndFails(t *testing.T) {
	c, srv, cc := setup(t)
	if _, err := c.LogCall(context.Background(), cc, call()); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("no contact = %v", err)
	}
	srv.JSON("GET /CompanyNotes/entityInformation/fields", 200, `{"fields":[{"name":"actionType","picklistValues":[{"value":"3","label":"Email"}]}]}`)
	l := call()
	l.Contact = &crm.Contact{ID: "2:6"}
	var ae *crm.APIError
	if _, err := c.LogCall(context.Background(), cc, l); !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "actionType") {
		t.Fatalf("no action type = %v", err)
	}
}

func TestUpdatePatchesTheNote(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("PATCH /CompanyNotes", 200, `{"itemId":4400}`)
	l := call()
	l.RecordingURL = "https://h.example/r/abc.wav"
	if err := c.UpdateCallLog(context.Background(), cc, "4400", l); err != nil {
		t.Fatal(err)
	}
	if b := srv.Calls("PATCH /CompanyNotes")[0].Body; !strings.Contains(b, `"id":4400`) || !strings.Contains(b, "abc.wav") {
		t.Fatalf("patch = %s", b)
	}
	var ae *crm.APIError
	if err := c.UpdateCallLog(context.Background(), cc, "nope", l); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("bad ref = %v", err)
	}
}
