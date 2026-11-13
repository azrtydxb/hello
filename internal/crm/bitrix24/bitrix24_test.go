package bitrix24

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

type seen struct {
	Path string
	Body map[string]any
}

func setup(t *testing.T, answers map[string]string) (*Connector, *[]seen, crm.Conn) {
	t.Helper()
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		calls = append(calls, seen{r.URL.Path, m})
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/1/code123/"), ".json")
		a, ok := answers[strings.ToLower(method)]
		if !ok {
			a = `{"result":true}`
		}
		if strings.HasPrefix(a, "#") {
			w.WriteHeader(503)
			a = a[1:]
		}
		if strings.HasPrefix(a, "!") {
			w.WriteHeader(400)
			a = a[1:]
		}
		_, _ = w.Write([]byte(a))
	}))
	t.Cleanup(srv.Close)
	return New(srv.Client()), &calls, crm.Conn{AccessToken: srv.URL + "/rest/1/code123/"}
}

func TestParseWebhook(t *testing.T) {
	for _, ok := range []string{"https://p.bitrix24.com/rest/1/abc123/", "https://p.bitrix24.eu/rest/12/abc123", "http://10.0.0.5/rest/1/x1/", "https://crm.example.com/b24/rest/3/tok/"} {
		if _, err := parseWebhook(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://p/rest/1/a/", "https://p.bitrix24.com/", "https://p.bitrix24.com/rest/x/abc/", "https://u:p@p.bitrix24.com/rest/1/a/", "https://p.bitrix24.com/rest/1/a/?x=1"} {
		_, err := parseWebhook(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("the error repeats the address: %v", err)
		}
	}
	if p := portal("https://crm.example.com/b24/rest/3/tok/"); p != "https://crm.example.com/b24" {
		t.Fatalf("portal = %s", p)
	}
}

func TestAuthenticate(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{"profile": `{"result":{"ID":"1"}}`})
	tk, err := c.Authenticate(context.Background(), crm.Config{ClientSecret: cc.AccessToken})
	if err != nil || tk.Account == "" || (*calls)[0].Path != "/rest/1/code123/profile.json" {
		t.Fatalf("tk=%+v err=%v", tk, err)
	}
	c, _, cc = setup(t, map[string]string{"profile": `!{"error":"INVALID_CREDENTIALS","error_description":"Invalid request credentials"}`})
	_, err = c.Authenticate(context.Background(), crm.Config{ClientSecret: cc.AccessToken})
	if err == nil || strings.Contains(err.Error(), "code123") {
		t.Fatalf("err = %v", err)
	}
	if _, err = c.Authenticate(context.Background(), crm.Config{ClientSecret: "nonsense"}); err == nil {
		t.Fatal("a malformed webhook was accepted")
	}
}

func TestErrorsNeverCarryTheWebhook(t *testing.T) {
	c := New(&http.Client{Timeout: time.Second})
	err := c.Ping(context.Background(), crm.Conn{AccessToken: "http://127.0.0.1:1/rest/1/code123/"})
	if err == nil || strings.Contains(err.Error(), "code123") {
		t.Fatalf("err = %v", err)
	}
}

func TestLimitIsRetriedAndScopeIsPermanent(t *testing.T) {
	c, _, cc := setup(t, map[string]string{"profile": `#{"error":"QUERY_LIMIT_EXCEEDED"}`})
	var ae *crm.APIError
	if err := c.Ping(context.Background(), cc); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("limit = %v", err)
	}
	c, _, cc = setup(t, map[string]string{"profile": `!{"error":"insufficient_scope","error_description":"The request requires higher privileges"}`})
	if err := c.Ping(context.Background(), cc); !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("scope = %v", err)
	}
}

func TestLookupFindsAContactAndItsCompany(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{
		"crm.duplicate.findbycomm": `{"result":{"CONTACT":[41],"LEAD":[7]}}`,
		"crm.contact.get":          `{"result":{"NAME":"Ann","LAST_NAME":"Smith","COMPANY_ID":"5"}}`,
		"crm.company.get":          `{"result":{"TITLE":"ACME"}}`})
	got, err := c.Lookup(context.Background(), cc, "0032 11 22 33 44")
	if err != nil || got == nil || got.ID != "CONTACT:41" || got.Name != "Ann Smith" || got.Company != "ACME" || got.CompanyID != "5" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if v := (*calls)[0].Body["values"].([]any)[0]; v != "+3211223344" || (*calls)[0].Body["type"] != "PHONE" {
		t.Fatalf("search = %v", (*calls)[0].Body)
	}
	if !strings.HasSuffix(got.URL, "/crm/contact/details/41/") {
		t.Fatalf("url = %s", got.URL)
	}
}

func TestLookupLeadAndNone(t *testing.T) {
	c, _, cc := setup(t, map[string]string{
		"crm.duplicate.findbycomm": `{"result":{"LEAD":["7"]}}`,
		"crm.lead.get":             `{"result":{"TITLE":"Lead 7","NAME":"","LAST_NAME":"","COMPANY_TITLE":"Lead Co"}}`})
	got, err := c.Lookup(context.Background(), cc, "+3211223344")
	if err != nil || got == nil || got.ID != "LEAD:7" || got.Name != "Lead 7" || got.Company != "Lead Co" {
		t.Fatalf("got %+v, %v", got, err)
	}
	c, calls, cc := setup(t, map[string]string{"crm.duplicate.findbycomm": `{"result":[]}`})
	if got, err = c.Lookup(context.Background(), cc, "+3211223344"); got != nil || err != nil || len(*calls) != 2 {
		t.Fatalf("none = %+v %v calls=%d", got, err, len(*calls))
	}
}

func TestResolveUser(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{"user.get": `{"result":[{"ID":"3","EMAIL":"x@y.com"},{"ID":"8","EMAIL":"Anna@Example.com"}]}`})
	id, err := c.ResolveUser(context.Background(), cc, "anna@example.com")
	if err != nil || id != "8" || (*calls)[0].Body["FILTER"].(map[string]any)["EMAIL"] != "anna@example.com" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestLogCallRegistersFinishesAndAttaches(t *testing.T) {
	c, calls, cc := setup(t, map[string]string{"telephony.externalcall.register": `{"result":{"CALL_ID":"externalCall.abc"}}`})
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ref, err := c.LogCall(context.Background(), cc, crm.CallLog{CallID: "c1", Direction: "inbound", Number: "+3211223344", Extension: "201", OwnerID: "8",
		StartedAt: start, Duration: 61 * time.Second, Outcome: crm.OutcomeAnswered, Contact: &crm.Contact{ID: "CONTACT:41"}, RecordingURL: "https://h/r/tok.wav"})
	if err != nil || ref != "externalCall.abc" || len(*calls) != 3 {
		t.Fatalf("ref=%q err=%v calls=%d", ref, err, len(*calls))
	}
	reg, fin, att := (*calls)[0].Body, (*calls)[1].Body, (*calls)[2].Body
	if reg["TYPE"] != float64(2) || reg["USER_ID"] != "8" || reg["CRM_ENTITY_TYPE"] != "CONTACT" || reg["CRM_ENTITY_ID"] != "41" || reg["SHOW"] != float64(0) ||
		reg["CRM_CREATE"] != float64(0) || reg["USER_PHONE_INNER"] != "201" || reg["CALL_START_DATE"] != "2026-11-10T08:00:00Z" {
		t.Fatalf("register = %v", reg)
	}
	if fin["CALL_ID"] != "externalCall.abc" || fin["DURATION"] != float64(61) || fin["STATUS_CODE"] != "200" {
		t.Fatalf("finish = %v", fin)
	}
	if att["RECORD_URL"] != "https://h/r/tok.wav" || att["FILENAME"] != "tok.wav" || (*calls)[2].Path != "/rest/1/code123/telephony.externalCall.attachRecord.json" {
		t.Fatalf("attach = %v", att)
	}
}

func TestRetryResumesWithoutRegisteringTwice(t *testing.T) {
	fail := true
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "register"):
			_, _ = w.Write([]byte(`{"result":{"CALL_ID":"x1"}}`))
		case strings.Contains(r.URL.Path, "finish") && fail:
			w.WriteHeader(503)
		default:
			_, _ = w.Write([]byte(`{"result":true}`))
		}
	}))
	defer srv.Close()
	c := New(srv.Client())
	cc := crm.Conn{AccessToken: srv.URL + "/rest/1/code123/"}
	l := crm.CallLog{CallID: "c1", Number: "+3211223344", OwnerID: "8", Outcome: crm.OutcomeNoAnswer, Missed: true}
	if _, err := c.LogCall(context.Background(), cc, l); err == nil {
		t.Fatal("the failed finish was not reported")
	}
	fail = false
	ref, err := c.LogCall(context.Background(), cc, l)
	if err != nil || ref != "x1" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	n := 0
	for _, p := range calls {
		if strings.Contains(p, "register") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("registered %d times: %v", n, calls)
	}
}

func TestMissedAndBusyStatusCodesAndUpdate(t *testing.T) {
	if statusCode(crm.OutcomeNoAnswer) != "304" || statusCode(crm.OutcomeBusy) != "486" || statusCode(crm.OutcomeFailed) != "503" {
		t.Fatal("status codes")
	}
	c, calls, cc := setup(t, nil)
	if err := c.UpdateCallLog(context.Background(), cc, "x1", crm.CallLog{}); err != nil || len(*calls) != 0 {
		t.Fatal("an update with no link sent a request")
	}
	if err := c.UpdateCallLog(context.Background(), cc, "x1", crm.CallLog{RecordingURL: "https://h/r/t.wav"}); err != nil || len(*calls) != 1 {
		t.Fatalf("update err=%v calls=%d", err, len(*calls))
	}
	caps := c.Capabilities()
	if !caps.RecordingCopy || caps.RecordingUpload {
		t.Fatalf("caps = %+v", caps)
	}
}
