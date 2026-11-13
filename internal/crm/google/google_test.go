package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

func newServer(t *testing.T, h http.HandlerFunc) *Connector {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.AuthBase, c.TokenURL, c.RevokeURL = srv.URL+"/auth", srv.URL+"/token", srv.URL+"/revoke"
	c.UserInfoURL, c.PeopleBase, c.CalendarBase = srv.URL+"/userinfo", srv.URL+"/people", srv.URL+"/calendar"
	return c
}

func cfg(features string) crm.Config {
	return crm.Config{Kind: Kind, ClientID: "cid", ClientSecret: "sec", Extra: map[string]string{"features": features}}
}

// TestScopes fails if the sign-in asks for more than read-only contacts and
// free/busy, or for calendar access when only contacts are on.
func TestScopes(t *testing.T) {
	c := New(nil)
	for features, want := range map[string][]string{
		"contacts":          {"openid", "email", ScopeContacts},
		"calendar":          {"openid", "email", ScopeFreeBusy},
		"contacts,calendar": {"openid", "email", ScopeFreeBusy, ScopeContacts},
	} {
		u, _ := url.Parse(c.AuthURL(cfg(features), "https://hello.example/cb", "st"))
		q := u.Query()
		if got := strings.Split(q.Get("scope"), " "); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: scope = %v", features, got)
		}
		if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("state") != "st" {
			t.Errorf("query = %v", q)
		}
	}
	for _, s := range ScopesFor(cfg("contacts,calendar")) {
		if strings.Contains(s, "readonly") == false && strings.Contains(s, "auth/") && !strings.HasSuffix(s, "freebusy") {
			t.Errorf("a scope beyond read-only: %s", s)
		}
	}
	if _, err := c.ValidateConfig(map[string]string{"features": "mail"}); err == nil {
		t.Fatal("an unknown feature was accepted")
	}
	if got, err := c.ValidateConfig(map[string]string{"clientId": "x"}); err != nil || got["features"] != "contacts" {
		t.Fatalf("defaults = %v %v", got, err)
	}
}

func TestExchangeRefreshRevoke(t *testing.T) {
	var revoked string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == "dead" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			if r.PostForm.Get("client_secret") != "sec" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3599,"scope":"openid email https://www.googleapis.com/auth/contacts.readonly"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer AT" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"email":"anna@example.com"}`))
		case "/revoke":
			b, _ := io.ReadAll(r.Body)
			revoked = string(b)
		}
	})
	tk, err := c.Exchange(context.Background(), cfg("contacts"), "https://hello.example/cb", "code")
	if err != nil || tk.Account != "anna@example.com" || tk.Refresh != "RT" || len(tk.Scopes) != 3 {
		t.Fatalf("tokens = %+v, %v", tk, err)
	}
	_, err = c.Refresh(context.Background(), cfg("contacts"), "dead")
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry || !strings.Contains(ae.Msg, "invalid_grant") {
		t.Fatalf("dead refresh = %v", err)
	}
	if err := c.RevokeToken(context.Background(), cfg("contacts"), "RT"); err != nil || revoked != "token=RT" {
		t.Fatalf("revoke: %v %q", err, revoked)
	}
}

// TestLookup fails if the pages of the address book are not all read once, if
// a number is not found in either of its stored forms, or if two contacts
// sharing a number name nobody.
func TestLookup(t *testing.T) {
	var reads atomic.Int32
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer AT" {
			w.WriteHeader(401)
			return
		}
		reads.Add(1)
		q := r.URL.Query()
		if q.Get("personFields") != "names,phoneNumbers,organizations" {
			t.Errorf("personFields = %q", q.Get("personFields"))
		}
		if q.Get("pageToken") == "p2" {
			_, _ = w.Write([]byte(`{"connections":[{"resourceName":"people/c3","names":[{"displayName":"Cy"}],"phoneNumbers":[{"value":"09 111 22 33"}]},
				{"resourceName":"people/c4","names":[{"displayName":"Dee"}],"phoneNumbers":[{"value":"+32 9 111 22 33","canonicalForm":"+3291112233"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"connections":[
			{"resourceName":"people/c1","names":[{"displayName":"Anna Plumber"}],"organizations":[{"name":"Pipes NV"}],"phoneNumbers":[{"value":"0475 11 22 33","canonicalForm":"+32475112233"}]},
			{"resourceName":"people/c2","phoneNumbers":[{"value":"050 98 76 54"}]}],"nextPageToken":"p2"}`))
	})
	cc := crm.Conn{AccessToken: "AT", ExternalAcct: "anna@example.com"}
	for in, want := range map[string]string{"+32475112233": "Anna Plumber", "0475112233": "Anna Plumber", "+3250987654": "", "+3291112233": "", "+4912345678": ""} {
		got, err := c.Lookup(context.Background(), cc, in)
		if err != nil {
			t.Fatal(err)
		}
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != want {
			t.Errorf("Lookup(%q) = %q, want %q", in, name, want)
		}
	}
	if reads.Load() != 2 {
		t.Fatalf("%d reads, want two pages once", reads.Load())
	}
	if got, _ := c.Lookup(context.Background(), cc, "+32475112233"); got.Company != "Pipes NV" || got.ID != "people/c1" {
		t.Fatalf("contact = %+v", got)
	}
}

func freeBusy(calendars string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TimeMin, TimeMax string
			Items            []map[string]string
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if r.URL.Path != "/calendar/freeBusy" || r.Method != "POST" || len(req.Items) != 1 || req.Items[0]["id"] != "primary" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"calendars":{"primary":` + calendars + `}}`))
	}
}

// TestBusy fails if a free calendar is busy, if a block that does not cover
// the moment makes the user busy, if blocks that follow each other are not
// chained into one end, or if a calendar error is read as free.
func TestBusy(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		busy       bool
		until      string
	}{
		{"free", `{"busy":[]}`, false, ""},
		{"later block", `{"busy":[{"start":"2026-11-12T11:00:00Z","end":"2026-11-12T12:00:00Z"}]}`, false, ""},
		{"now", `{"busy":[{"start":"2026-11-12T09:30:00Z","end":"2026-11-12T10:30:00Z"}]}`, true, "2026-11-12T10:30:00Z"},
		{"back to back", `{"busy":[{"start":"2026-11-12T09:30:00Z","end":"2026-11-12T10:30:00Z"},{"start":"2026-11-12T10:30:00Z","end":"2026-11-12T11:00:00Z"},{"start":"2026-11-12T12:00:00Z","end":"2026-11-12T13:00:00Z"}]}`, true, "2026-11-12T11:00:00Z"},
		{"all day", `{"busy":[{"start":"2026-11-12T00:00:00Z","end":"2026-11-13T00:00:00Z"}]}`, true, "2026-11-13T00:00:00Z"},
	} {
		c := newServer(t, freeBusy(tc.body))
		b, err := c.Busy(context.Background(), crm.Conn{AccessToken: "AT"}, at)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if b.Busy != tc.busy || (tc.busy && b.Until.Format(time.RFC3339) != tc.until) {
			t.Errorf("%s: %+v, want busy=%v until=%s", tc.name, b, tc.busy, tc.until)
		}
	}
	c := newServer(t, freeBusy(`{"errors":[{"domain":"global","reason":"notFound"}],"busy":[]}`))
	if b, err := c.Busy(context.Background(), crm.Conn{AccessToken: "AT"}, at); err == nil || b.Busy {
		t.Fatalf("a calendar error was read as free: %+v %v", b, err)
	}
	c = newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	if _, err := c.Busy(context.Background(), crm.Conn{AccessToken: "x"}, at); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("401 = %v", err)
	}
	if k := c.Capabilities(); k.LogCalls || !k.UserScope || !k.Calendar {
		t.Fatalf("capabilities = %+v", k)
	}
}
