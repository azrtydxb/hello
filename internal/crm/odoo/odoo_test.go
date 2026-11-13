package odoo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/crm"
)

type rpcCall struct {
	Service, Method string
	Args            []any
}

// server answers JSON-RPC calls by "model.method" (or "service.method").
func setup(t *testing.T, answers map[string]string) (*Connector, *[]rpcCall, crm.Conn) {
	t.Helper()
	var calls []rpcCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Params struct {
				Service, Method string
				Args            []any
			}
		}
		_ = json.Unmarshal(b, &req)
		if r.URL.Path != "/jsonrpc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		calls = append(calls, rpcCall{req.Params.Service, req.Params.Method, req.Params.Args})
		key := req.Params.Service + "." + req.Params.Method
		if req.Params.Service == "object" && len(req.Params.Args) > 4 {
			key = req.Params.Args[3].(string) + "." + req.Params.Args[4].(string)
		}
		a, ok := answers[key]
		if !ok {
			a = `{"result":null}`
		}
		_, _ = w.Write([]byte(a))
	}))
	t.Cleanup(srv.Close)
	return New(srv.Client()), &calls, crm.Conn{AccessToken: "apikey", ExternalAcct: "7",
		Config: crm.Config{ClientID: "me@x.com", Extra: map[string]string{"url": srv.URL, "db": "prod"}}}
}

func TestValidateConfig(t *testing.T) {
	c := New(http.DefaultClient)
	out, err := c.ValidateConfig(map[string]string{"url": "https://odoo.example.com/", "db": "prod", "clientId": "a"})
	if err != nil || out["url"] != "https://odoo.example.com" {
		t.Fatalf("out = %v, %v", out, err)
	}
	for _, bad := range []string{"ftp://x", "https://u:p@x.example", "https://x.example/?a=1", "odoo.example", "https://x.example/#f"} {
		if _, err := c.ValidateConfig(map[string]string{"url": bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := c.ValidateConfig(map[string]string{"other": "x"}); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestAuthenticate(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{"common.authenticate": `{"result":7}`})
	cfg := cc.Config
	cfg.ClientSecret = "apikey"
	tk, err := c.Authenticate(context.Background(), cfg)
	if err != nil || tk.Account != "7" {
		t.Fatalf("tk = %+v, %v", tk, err)
	}
	if a := (*calls)[0].Args; a[0] != "prod" || a[1] != "me@x.com" || a[2] != "apikey" {
		t.Fatalf("args = %v", a)
	}
	c, _, cc2 := setup(t, map[string]string{"common.authenticate": `{"result":false}`})
	cfg2 := cc2.Config
	cfg2.ClientSecret = "apikey"
	if _, err := c.Authenticate(context.Background(), cfg2); err == nil || strings.Contains(err.Error(), "apikey") {
		t.Fatalf("a refused login = %v", err)
	}
	cfg2.Extra = map[string]string{"url": cfg2.Extra["url"]}
	if _, err := c.Authenticate(context.Background(), cfg2); err == nil {
		t.Fatal("a missing database was accepted")
	}
}

func TestFaultsAccessDeniedIsAuthOthersPermanent(t *testing.T) {
	c, _, cc := setup(t, map[string]string{
		"res.users.search_count": `{"error":{"message":"Odoo Server Error","data":{"name":"odoo.exceptions.AccessDenied","message":"Access Denied"}}}`})
	if err := c.Ping(context.Background(), cc); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	c, _, cc = setup(t, map[string]string{
		"res.users.search_count": `{"error":{"message":"x","data":{"name":"odoo.exceptions.UserError","message":"Bad   thing"}}}`})
	var ae *crm.APIError
	if err := c.Ping(context.Background(), cc); !errors.As(err, &ae) || ae.Retry || ae.Msg != "Bad thing" {
		t.Fatalf("err = %v", err)
	}
	cc.ExternalAcct = ""
	if err := c.Ping(context.Background(), cc); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("no uid = %v", err)
	}
}

func TestLookupUsesPhoneMobileSearchThenFallsBack(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{
		"res.partner.search_read": `{"result":[{"id":12,"name":"Ann Smith","phone":"+32 11 22 33 44","parent_id":[3,"ACME"]}]}`})
	got, err := c.Lookup(context.Background(), cc, "+3211223344")
	if err != nil || got == nil || got.ID != "12" || got.Company != "ACME" || got.CompanyID != "3" || !strings.Contains(got.URL, "model=res.partner") {
		t.Fatalf("got %+v, %v", got, err)
	}
	dom := (*calls)[0].Args[5].([]any)[0].([]any)[0].([]any)
	if dom[0] != "phone_mobile_search" || dom[2] != "211223344" {
		t.Fatalf("domain = %v", dom)
	}
	// An Odoo without the field: fall back and filter by the full number.
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = w.Write([]byte(`{"error":{"data":{"name":"builtins.ValueError","message":"Invalid field"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":[{"id":1,"name":"Other","phone":"+33 6 11 22 33 44","parent_id":false},{"id":2,"name":"Ann","phone":"0032 11 22 33 44","parent_id":false}]}`))
	}))
	defer srv.Close()
	c = New(srv.Client())
	cc.Config.Extra["url"] = srv.URL
	got, err = c.Lookup(context.Background(), cc, "+3211223344")
	if err != nil || got == nil || got.ID != "2" || got.Company != "" {
		t.Fatalf("fallback got %+v, %v", got, err)
	}
}

func TestResolveUser(t *testing.T) {
	c, _, cc := setup(t, map[string]string{
		"res.users.search_read": `{"result":[{"id":4,"login":"admin","email":false},{"id":9,"login":"anna","email":"Anna@Example.com"}]}`})
	id, err := c.ResolveUser(context.Background(), cc, "anna@example.com")
	if err != nil || id != "9" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if id, _ = c.ResolveUser(context.Background(), cc, "none@example.com"); id != "" {
		t.Fatalf("matched %q", id)
	}
}

func TestLogCallPostsANoteOnThePartner(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{
		"res.users.read":           `{"result":[{"id":9,"name":"Anna <b>","partner_id":[88,"Anna"]}]}`,
		"res.partner.message_post": `{"result":4711}`})
	ref, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", From: "+3211", To: "201", OwnerID: "9",
		Outcome: crm.OutcomeAnswered, Contact: &crm.Contact{ID: "12", Name: "Ann"}, RecordingURL: "https://h/r/t.wav"})
	if err != nil || ref != "4711" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	post := (*calls)[1].Args
	if post[3] != "res.partner" || post[4] != "message_post" || post[5].([]any)[0].([]any)[0] != float64(12) {
		t.Fatalf("post = %v", post)
	}
	kw := post[6].(map[string]any)
	body := kw["body"].(string)
	if kw["subtype_xmlid"] != "mail.mt_note" || kw["message_type"] != "comment" || strings.Contains(body, "<b>") && !strings.Contains(body, "&lt;b&gt;") ||
		!strings.Contains(body, "Call from Ann") || !strings.Contains(body, `<a href="https://h/r/t.wav">`) {
		t.Fatalf("kwargs = %v", kw)
	}
	// An unknown caller: the note goes on the owner's own partner.
	_, err = c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", OwnerID: "9", Missed: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[3].Args[5].([]any)[0].([]any)[0]; got != float64(88) {
		t.Fatalf("target = %v", got)
	}
}

func TestLogCallWithNowhereToPostIsSkipped(t *testing.T) {
	c, _, cc := setup(t, map[string]string{"res.users.read": `{"result":[]}`})
	_, err := c.LogCall(context.Background(), cc, crm.CallLog{OwnerID: "9"})
	if !errors.Is(err, crm.ErrNoTarget) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateWritesTheMessageBody(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{"res.users.read": `{"result":[{"name":"Anna","partner_id":[88,"Anna"]}]}`, "mail.message.write": `{"result":true}`})
	if err := c.UpdateCallLog(context.Background(), cc, "4711", crm.CallLog{}); err != nil || len(*calls) != 0 {
		t.Fatal("an update with no link sent a request")
	}
	if err := c.UpdateCallLog(context.Background(), cc, "4711", crm.CallLog{OwnerID: "9", RecordingURL: "https://h/r/t.wav"}); err != nil {
		t.Fatal(err)
	}
	w := (*calls)[len(*calls)-1].Args
	if w[3] != "mail.message" || w[4] != "write" || w[5].([]any)[0].([]any)[0] != float64(4711) {
		t.Fatalf("write = %v", w)
	}
}
