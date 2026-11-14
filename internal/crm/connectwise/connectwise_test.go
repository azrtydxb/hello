package connectwise

import (
	"context"
	"encoding/base64"
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
	cfg := crm.Config{Kind: Kind, ClientID: "guid-1", ClientSecret: "priv", Extra: map[string]string{
		"clientId": "guid-1", "host": "api-na.myconnectwise.net", "companyId": "acme", "publicKey": "pub", "codebase": "v4_6_release", "activityType": "Call"}}
	return c, srv, crm.Conn{Config: cfg, AccessToken: "priv", ExternalAcct: "acme"}
}

func call() crm.CallLog {
	return crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+15551234567", From: "+15551234567", To: "+15559876543",
		StartedAt: time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC), Duration: 90 * time.Second, Outcome: crm.OutcomeAnswered, OwnerID: "21"}
}

func TestRegisteredAndValidated(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if _, direct := c.(crm.Direct); !direct || c.Capabilities().Ticket {
		t.Fatal("connectwise is direct and logs activities")
	}
	good := map[string]string{"clientId": "g", "host": "API-NA.myconnectwise.net", "companyId": "acme", "publicKey": "pub"}
	got, err := New(nil).ValidateConfig(good)
	if err != nil || got["host"] != "api-na.myconnectwise.net" || got["codebase"] != "v4_6_release" || got["activityType"] != "Call" {
		t.Fatalf("defaults = %v %v", got, err)
	}
	for name, mut := range map[string]func(map[string]string){
		"host with scheme": func(m map[string]string) { m["host"] = "https://x.com" },
		"company with +":   func(m map[string]string) { m["companyId"] = "a+b" },
		"no public key":    func(m map[string]string) { delete(m, "publicKey") },
		"no client id":     func(m map[string]string) { delete(m, "clientId") },
		"codebase slash":   func(m map[string]string) { m["codebase"] = "../x" },
		"quote in type":    func(m map[string]string) { m["activityType"] = `Ca"ll` },
		"unknown":          func(m map[string]string) { m["x"] = "1" },
	} {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		mut(m)
		if _, err := New(nil).ValidateConfig(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthenticateSendsKeysAndClientID(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /system/info", 200, `{"version":"v2026.1"}`)
	tok, err := c.Authenticate(context.Background(), cc.Config)
	if err != nil || tok.Access != "priv" || tok.Account != "acme" {
		t.Fatalf("tokens = %+v %v", tok, err)
	}
	h := srv.Calls("")[0].Header
	if h.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("acme+pub:priv")) || h.Get("clientId") != "guid-1" {
		t.Fatalf("headers = %v", h)
	}
	srv.JSON("GET /system/info", 401, `{"message":"nope"}`)
	var ae *crm.APIError
	if _, err := c.Authenticate(context.Background(), cc.Config); !errors.As(err, &ae) || ae.Retry || strings.Contains(ae.Msg, "priv") {
		t.Fatalf("refused = %v", err)
	}
}

func TestLookup(t *testing.T) {
	c, srv, cc := setup(t)
	srv.Handle("GET /company/contacts", func(w http.ResponseWriter, r *http.Request, _ string) {
		q, _ := url.ParseQuery(r.URL.RawQuery)
		if q.Get("childConditions") != `communicationItems/value like "*4567*"` {
			t.Errorf("childConditions = %q", q.Get("childConditions"))
		}
		_, _ = w.Write([]byte(`[
			{"id":1,"firstName":"Wrong","lastName":"One","company":{"id":10,"name":"X"},"communicationItems":[{"value":"(555) 999-4567"}]},
			{"id":2,"firstName":"Ann","lastName":"Smith","company":{"id":11,"name":"ACME"},"communicationItems":[{"value":"ann@x.test"},{"value":"(555) 123-4567"}]}]`))
	})
	got, err := c.Lookup(context.Background(), cc, "+1 555 123 4567")
	if err != nil || got == nil || got.ID != "2:11" || got.Name != "Ann Smith" || got.Company != "ACME" {
		t.Fatalf("contact = %+v %v", got, err)
	}
	srv.JSON("GET /company/contacts", 200, `[]`)
	if got, err := c.Lookup(context.Background(), cc, "+15551234567"); got != nil || err != nil {
		t.Fatalf("miss = %+v %v", got, err)
	}
}

func TestResolveUserRefusesQuotes(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /system/members", 200, `[{"id":3,"primaryEmail":"Ann@acme.test"}]`)
	if id, err := c.ResolveUser(context.Background(), cc, "ann@acme.test"); err != nil || id != "3" {
		t.Fatalf("id = %q %v", id, err)
	}
	n := len(srv.Calls(""))
	if id, err := c.ResolveUser(context.Background(), cc, `x" or id>0 or "`); id != "" || err != nil || len(srv.Calls("")) != n {
		t.Fatal("a quote reached the conditions")
	}
}

func TestLogCallCreatesTheActivity(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("GET /sales/activities/types", 200, `[{"id":6,"name":"Call"}]`)
	srv.JSON("POST /sales/activities", 201, `{"id":9100}`)
	l := call()
	l.Contact = &crm.Contact{ID: "2:11", Name: "Ann"}
	for i := 0; i < 2; i++ {
		if ref, err := c.LogCall(context.Background(), cc, l); err != nil || ref != "9100" {
			t.Fatalf("ref = %q %v", ref, err)
		}
	}
	if n := len(srv.Calls("GET /sales/activities/types")); n != 1 {
		t.Fatalf("type looked up %d times", n)
	}
	b := srv.Calls("POST /sales/activities")[0].Body
	for _, want := range []string{`"type":{"id":6}`, `"company":{"id":11}`, `"contact":{"id":2}`, `"assignTo":{"id":21}`, `"dateEnd":"2026-11-12T14:01:30Z"`} {
		if !strings.Contains(b, want) {
			t.Errorf("body lacks %s: %s", want, b)
		}
	}
}

func TestLogCallSkipsAndFails(t *testing.T) {
	c, srv, cc := setup(t)
	if _, err := c.LogCall(context.Background(), cc, call()); !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("no contact = %v", err)
	}
	srv.JSON("GET /sales/activities/types", 200, `[]`)
	l := call()
	l.Contact = &crm.Contact{ID: "2:11"}
	var ae *crm.APIError
	if _, err := c.LogCall(context.Background(), cc, l); !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "activityType") {
		t.Fatalf("missing type = %v", err)
	}
}

func TestUpdateIsAJSONPatch(t *testing.T) {
	c, srv, cc := setup(t)
	srv.JSON("PATCH /sales/activities/9100", 200, `{}`)
	l := call()
	l.RecordingURL = "https://h.example/r/abc.wav"
	if err := c.UpdateCallLog(context.Background(), cc, "9100", l); err != nil {
		t.Fatal(err)
	}
	p := srv.Calls("PATCH /sales/activities/9100")[0]
	if !strings.HasPrefix(p.Body, `[{"op":"replace","path":"/notes"`) || !strings.Contains(p.Body, "abc.wav") {
		t.Fatalf("patch = %s", p.Body)
	}
}
