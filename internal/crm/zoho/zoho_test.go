package zoho

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

type seen struct{ Method, Path, Query, Auth, Body string }

func setup(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) (*Connector, *[]seen, crm.Conn) {
	t.Helper()
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		if h != nil {
			h(w, r, len(calls))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.Accounts = func(string) string { return srv.URL }
	c.API = func(string) string { return srv.URL }
	return c, &calls, crm.Conn{AccessToken: "tok", Config: crm.Config{Extra: map[string]string{"region": "eu"}}}
}

func TestValidateConfigAndRegions(t *testing.T) {
	c := New(http.DefaultClient)
	out, err := c.ValidateConfig(map[string]string{"clientId": "x"})
	if err != nil || out["region"] != "us" {
		t.Fatalf("default = %v, %v", out, err)
	}
	for _, r := range Regions() {
		if _, err := c.ValidateConfig(map[string]string{"region": r}); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	if _, err := c.ValidateConfig(map[string]string{"region": "evil.example/"}); err == nil {
		t.Fatal("a free-form region was accepted")
	}
	if _, err := c.ValidateConfig(map[string]string{"url": "x"}); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if got := New(nil).Accounts("au"); got != "https://accounts.zoho.com.au" {
		t.Fatalf("au accounts = %s", got)
	}
	if got := New(nil).API("eu"); got != "https://www.zohoapis.eu" {
		t.Fatalf("eu api = %s", got)
	}
}

func TestAuthURLIsOfflineWithTheScopes(t *testing.T) {
	c := New(http.DefaultClient)
	u := c.AuthURL(crm.Config{ClientID: "cid", Extra: map[string]string{"region": "in"}}, "https://h/cb", "st")
	for _, want := range []string{"https://accounts.zoho.in/oauth/v2/auth?", "access_type=offline", "prompt=consent", "ZohoCRM.modules.calls.CREATE", "state=st"} {
		if !strings.Contains(u, want) {
			t.Errorf("%q missing from %s", want, u)
		}
	}
}

func TestExchangeNeedsARefreshTokenAndNamesTheUser(t *testing.T) {
	c, calls, _ := setup(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if strings.HasPrefix(r.URL.Path, "/oauth/v2/token") {
			_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"api_domain":"https://www.zohoapis.eu"}`))
			return
		}
		_, _ = w.Write([]byte(`{"users":[{"email":"admin@acme.example"}]}`))
	})
	cfg := crm.Config{ClientID: "cid", ClientSecret: "sec", Extra: map[string]string{"region": "eu"}}
	tk, err := c.Exchange(context.Background(), cfg, "https://h/cb", "code")
	if err != nil || tk.Refresh != "r" || tk.Account != "admin@acme.example" {
		t.Fatalf("tokens = %+v, %v", tk, err)
	}
	if !strings.Contains((*calls)[0].Body, "client_secret=sec") || (*calls)[1].Auth != "Zoho-oauthtoken a" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestAnErrorMemberOn200IsAPermanentRefusal(t *testing.T) {
	c, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"error":"invalid_code"}`))
	})
	_, err := c.Refresh(context.Background(), crm.Config{}, "r")
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("err = %v", err)
	}
	c, _, _ = setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"error":"Access Denied"}`))
	})
	if _, err = c.Refresh(context.Background(), crm.Config{}, "r"); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("rate limit err = %v", err)
	}
}

func TestRefreshKeepsTheStoredRefreshToken(t *testing.T) {
	c, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	})
	tk, err := c.Refresh(context.Background(), crm.Config{}, "r")
	if err != nil || tk.Access != "new" || tk.Refresh != "" {
		t.Fatalf("tokens = %+v, %v", tk, err)
	}
}

func TestLookupFindsAContactThenALeadAndChecksPhones(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		switch {
		case strings.Contains(r.URL.Path, "/Contacts/"):
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.RawQuery, "%2B32"):
			_, _ = w.Write([]byte(`{"data":[{"id":"L9","First_Name":"Bob","Last_Name":"Lead","Company":"Lead Co","Phone":"+32 99 99 99 99"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"L1","First_Name":"Lea","Last_Name":"Lead","Company":"Lead Co","Phone":"+32 11 22 33 44"}]}`))
		}
	})
	got, err := c.Lookup(context.Background(), cc, "0032 11 22 33 44")
	if err != nil || got == nil || got.ID != "Leads:L1" || got.Name != "Lea Lead" || got.URL != "https://crm.zoho.eu/crm/tab/Leads/L1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if (*calls)[0].Path != "/crm/v6/Contacts/search" || (*calls)[0].Auth != "Zoho-oauthtoken tok" {
		t.Fatalf("first call = %+v", (*calls)[0])
	}
}

func TestLookupContactCarriesTheAccount(t *testing.T) {
	c, _, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"data":[{"id":"C1","First_Name":"Ann","Last_Name":"Smith","Account_Name":{"name":"ACME","id":"A1"},"Mobile":"+3211223344"}]}`))
	})
	got, err := c.Lookup(context.Background(), cc, "+3211223344")
	if err != nil || got == nil || got.ID != "Contacts:C1" || got.Company != "ACME" || got.CompanyID != "A1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err = c.Lookup(context.Background(), cc, "201"); got != nil || err != nil {
		t.Fatalf("an extension was looked up: %+v, %v", got, err)
	}
}

func TestResolveUser(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"users":[{"id":"1","email":"x@y.com"},{"id":"2","email":"ANNA@example.com"}]}`))
	})
	id, err := c.ResolveUser(context.Background(), cc, "anna@example.com")
	if err != nil || id != "2" || !strings.Contains((*calls)[0].Query, "email=anna%40example.com") {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestLogCallCreatesACallRecord(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"data":[{"code":"SUCCESS","status":"success","details":{"id":"555"}}]}`))
	})
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ref, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", StartedAt: start, Duration: 75*time.Minute + 5*time.Second,
		OwnerID: "77", Outcome: crm.OutcomeAnswered, Contact: &crm.Contact{ID: "Contacts:C1", Name: "Ann"}, RecordingURL: "https://h/r/t.wav"})
	if err != nil || ref != "555" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	var b struct{ Data []map[string]any }
	_ = json.Unmarshal([]byte((*calls)[0].Body), &b)
	r := b.Data[0]
	if (*calls)[0].Path != "/crm/v6/Calls" || r["Call_Type"] != "Inbound" || r["Call_Duration"] != "75:05" || r["Call_Start_Time"] != "2026-11-10T08:00:00+00:00" ||
		r["Subject"] != "Call from Ann" || r["Owner"].(map[string]any)["id"] != "77" || r["Who_Id"].(map[string]any)["id"] != "C1" ||
		!strings.Contains(r["Description"].(string), "Recording: https://h/r/t.wav") {
		t.Fatalf("record = %v", r)
	}
	// Missed.
	_, _ = c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", Missed: true, StartedAt: start})
	b.Data = nil
	_ = json.Unmarshal([]byte((*calls)[1].Body), &b)
	if b.Data[0]["Call_Type"] != "Missed" || b.Data[0]["Owner"] != nil || b.Data[0]["Who_Id"] != nil {
		t.Fatalf("missed = %v", b.Data[0])
	}
}

func TestPerRecordFailureIsPermanent(t *testing.T) {
	c, _, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"data":[{"code":"INVALID_DATA","status":"error","message":"invalid data"}]}`))
	})
	_, err := c.LogCall(context.Background(), cc, crm.CallLog{})
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry || ae.Msg != "invalid data" {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateRewritesTheDescriptionOnlyWithALink(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		_, _ = w.Write([]byte(`{"data":[{"code":"SUCCESS","status":"success","details":{"id":"555"}}]}`))
	})
	if err := c.UpdateCallLog(context.Background(), cc, "555", crm.CallLog{}); err != nil || len(*calls) != 0 {
		t.Fatal("an update with no link sent a request")
	}
	if err := c.UpdateCallLog(context.Background(), cc, "555", crm.CallLog{RecordingURL: "https://h/r/t.wav"}); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].Method != http.MethodPut || (*calls)[0].Path != "/crm/v6/Calls/555" || !strings.Contains((*calls)[0].Body, "t.wav") {
		t.Fatalf("update = %+v", (*calls)[0])
	}
}

func TestUnauthorizedIsErrAuth(t *testing.T) {
	c, _, cc := setup(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"code":"INVALID_TOKEN"}`))
	})
	if err := c.Ping(context.Background(), cc); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}
