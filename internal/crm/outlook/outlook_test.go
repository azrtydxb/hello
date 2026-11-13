package outlook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
)

func newServer(t *testing.T, h http.HandlerFunc) (*Connector, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.LoginBase, c.GraphBase = srv.URL+"/login", srv.URL+"/graph"
	return c, srv
}

func cfg(features string) crm.Config {
	return crm.Config{Kind: Kind, ClientID: "cid", ClientSecret: "sec", Extra: map[string]string{"features": features, "tenant": "contoso.com"}}
}

// TestAuthURLAsksOnlyForTheEnabledFeatures fails if the sign-in asks for
// calendar access when only contacts are on (or the reverse), for a write
// scope, or for the wrong tenant.
func TestAuthURLAsksOnlyForTheEnabledFeatures(t *testing.T) {
	c := New(http.DefaultClient)
	for features, want := range map[string]string{
		"contacts":          "offline_access User.Read Contacts.Read",
		"calendar":          "offline_access User.Read Calendars.Read",
		"contacts,calendar": "offline_access User.Read Calendars.Read Contacts.Read",
	} {
		u, _ := url.Parse(c.AuthURL(cfg(features), "https://hello.example/cb", "st"))
		if u.Host != "login.microsoftonline.com" || u.Path != "/contoso.com/oauth2/v2.0/authorize" {
			t.Fatalf("url = %s", u)
		}
		q := u.Query()
		if q.Get("scope") != want || q.Get("state") != "st" || q.Get("client_id") != "cid" || q.Get("response_type") != "code" {
			t.Fatalf("%s: query = %v", features, q)
		}
		if strings.Contains(q.Get("scope"), "ReadWrite") {
			t.Fatal("a write scope")
		}
	}
}

func TestValidateConfig(t *testing.T) {
	c := New(nil)
	got, err := c.ValidateConfig(map[string]string{"clientId": "x"})
	if err != nil || got["tenant"] != "organizations" || got["features"] != "contacts" {
		t.Fatalf("defaults = %v %v", got, err)
	}
	for _, bad := range []map[string]string{{"tenant": "evil.example/../x"}, {"features": "mail"}, {"region": "eu"}, {"tenant": "a b"}} {
		if _, err := c.ValidateConfig(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	for _, ok := range []string{"common", "organizations", "contoso.onmicrosoft.com", "72f988bf-86f1-41af-91ab-2d7cd011db47"} {
		if _, err := c.ValidateConfig(map[string]string{"tenant": ok}); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

// TestExchangeAndRefresh fails if the token request does not carry the
// client secret and the code in the body, or the account is not read.
func TestExchangeAndRefresh(t *testing.T) {
	var form url.Values
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/contoso.com/oauth2/v2.0/token":
			_ = r.ParseForm()
			form = r.PostForm
			if r.URL.RawQuery != "" {
				t.Errorf("the token request carries a query: %s", r.URL.RawQuery)
			}
			if form.Get("grant_type") == "refresh_token" && form.Get("refresh_token") == "dead" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS70008: expired\r\nTrace ID: x"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600,"scope":"Contacts.Read Calendars.Read User.Read"}`))
		case "/graph/me":
			if r.Header.Get("Authorization") != "Bearer AT" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"id":"1","mail":"","userPrincipalName":"anna@contoso.com"}`))
		}
	})
	tk, err := c.Exchange(context.Background(), cfg("contacts,calendar"), "https://hello.example/cb", "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Access != "AT" || tk.Refresh != "RT" || tk.Account != "anna@contoso.com" || len(tk.Scopes) != 3 || time.Until(tk.ExpiresAt) < 59*time.Minute {
		t.Fatalf("tokens = %+v", tk)
	}
	if form.Get("code") != "the-code" || form.Get("client_secret") != "sec" || form.Get("redirect_uri") != "https://hello.example/cb" {
		t.Fatalf("form = %v", form)
	}
	// A dead refresh token is a final failure (the framework then asks the
	// user to sign in again), with a message free of the provider's trace.
	_, err = c.Refresh(context.Background(), cfg("contacts"), "dead")
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry || ae.Status != 400 || strings.Contains(ae.Msg, "Trace") && strings.Contains(ae.Msg, "\n") {
		t.Fatalf("refresh error = %v", err)
	}
}

const page1 = `{"value":[
 {"id":"a","displayName":"Anna Plumber","companyName":"Pipes NV","mobilePhone":"+32 475 11 22 33","businessPhones":["+32 50 12 34 56"],"homePhones":[]},
 {"id":"b","displayName":"Bob","companyName":"","mobilePhone":null,"businessPhones":[],"homePhones":["050 98 76 54"]}],
 "@odata.nextLink":"%s/graph/me/contacts?$skip=2"}`
const page2 = `{"value":[
 {"id":"c","displayName":"Cy","mobilePhone":"09 111 22 33","businessPhones":[],"homePhones":[]},
 {"id":"d","displayName":"Dee","mobilePhone":"+32 9 111 22 33","businessPhones":[],"homePhones":[]}]}`

// TestLookup fails if a contact on the second page is not found, if the
// address book is read again within the TTL, if two contacts with one number
// name nobody, or if a nextLink to another host is followed.
func TestLookup(t *testing.T) {
	var reads atomic.Int32
	var srvURL string
	c, srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer AT" {
			w.WriteHeader(401)
			return
		}
		reads.Add(1)
		if r.URL.Query().Get("$skip") == "2" {
			_, _ = w.Write([]byte(page2))
			return
		}
		_, _ = w.Write([]byte(strings.Replace(page1, "%s", srvURL, 1)))
	})
	srvURL = srv.URL
	cc := crm.Conn{AccessToken: "AT", ExternalAcct: "anna@contoso.com"}
	for in, want := range map[string]string{"+32475112233": "Anna Plumber", "050123456": "Anna Plumber", "+3250987654": "Bob", "+3291112233": "", "+31600000000": ""} {
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
		t.Fatalf("%d reads of the address book, want 2 pages once", reads.Load())
	}
	if got, _ := c.Lookup(context.Background(), cc, "+32475112233"); got == nil || got.Company != "Pipes NV" {
		t.Fatalf("contact = %+v", got)
	}
	// Another account has its own index, and a refused token is ErrAuth.
	if _, err := c.Lookup(context.Background(), crm.Conn{AccessToken: "BAD", ExternalAcct: "bob@contoso.com"}, "+32475112233"); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("a refused token: %v", err)
	}
}

func TestLookupDoesNotFollowAnotherHost(t *testing.T) {
	var evil atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { evil.Add(1) }))
	defer bad.Close()
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"` + bad.URL + `/steal"}`))
	})
	if _, err := c.Lookup(context.Background(), crm.Conn{AccessToken: "AT", ExternalAcct: "x"}, "+32475112233"); err != nil {
		t.Fatal(err)
	}
	if evil.Load() != 0 {
		t.Fatal("the access token was sent to a host the nextLink named")
	}
}

func event(showAs string, allDay, cancelled bool, response, end string) string {
	b, _ := json.Marshal(map[string]any{"showAs": showAs, "isAllDay": allDay, "isCancelled": cancelled,
		"start": map[string]string{"dateTime": "2026-11-12T09:00:00.0000000"}, "end": map[string]string{"dateTime": end},
		"responseStatus": map[string]string{"response": response}})
	return string(b)
}

// TestBusy fails if a free, tentative, declined, cancelled or free all-day
// event marks the user busy, if an out-of-office or busy event does not, or if
// the end is not the latest end of the overlapping events.
func TestBusy(t *testing.T) {
	var events []string
	var query url.Values
	var prefer string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		query, prefer = r.URL.Query(), r.Header.Get("Prefer")
		_, _ = w.Write([]byte(`{"value":[` + strings.Join(events, ",") + `]}`))
	})
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cc := crm.Conn{AccessToken: "AT"}
	for _, tc := range []struct {
		name   string
		events []string
		busy   bool
		until  string
	}{
		{"none", nil, false, ""},
		{"free", []string{event("free", false, false, "accepted", "2026-11-12T10:30:00.0000000")}, false, ""},
		{"tentative", []string{event("tentative", false, false, "tentativelyAccepted", "2026-11-12T10:30:00.0000000")}, false, ""},
		{"declined", []string{event("busy", false, false, "declined", "2026-11-12T10:30:00.0000000")}, false, ""},
		{"cancelled", []string{event("busy", false, true, "accepted", "2026-11-12T10:30:00.0000000")}, false, ""},
		{"free all-day marker", []string{event("free", true, false, "accepted", "2026-11-13T00:00:00.0000000")}, false, ""},
		{"busy all-day is not a vacation", []string{event("busy", true, false, "accepted", "2026-11-13T00:00:00.0000000")}, false, ""},
		{"busy", []string{event("busy", false, false, "accepted", "2026-11-12T10:30:00.0000000")}, true, "2026-11-12T10:30:00Z"},
		{"organizer", []string{event("busy", false, false, "organizer", "2026-11-12T10:30:00.0000000")}, true, "2026-11-12T10:30:00Z"},
		{"out of office all day", []string{event("oof", true, false, "accepted", "2026-11-13T00:00:00.0000000")}, true, "2026-11-13T00:00:00Z"},
		{"overlap takes the latest end", []string{event("busy", false, false, "accepted", "2026-11-12T10:30:00.0000000"),
			event("oof", false, false, "accepted", "2026-11-12T12:00:00.0000000"), event("free", false, false, "accepted", "2026-11-12T18:00:00.0000000")}, true, "2026-11-12T12:00:00Z"},
	} {
		events = tc.events
		b, err := c.Busy(context.Background(), cc, at)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if b.Busy != tc.busy || (tc.busy && b.Until.Format(time.RFC3339) != tc.until) {
			t.Errorf("%s: busy=%v until=%v, want busy=%v until=%s", tc.name, b.Busy, b.Until, tc.busy, tc.until)
		}
	}
	if query.Get("startDateTime") != "2026-11-12T10:00:00" || prefer != `outlook.timezone="UTC"` || !strings.Contains(query.Get("$select"), "showAs") {
		t.Fatalf("query = %v, Prefer = %q", query, prefer)
	}
	if strings.Contains(query.Get("$select"), "subject") || strings.Contains(query.Get("$select"), "attendees") || strings.Contains(query.Get("$select"), "body") {
		t.Fatalf("the calendar read asks for more than availability: %s", query.Get("$select"))
	}
}

func TestErrors(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer limited":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
		case "Bearer down":
			w.WriteHeader(503)
		default:
			w.WriteHeader(401)
		}
	})
	_, err := c.Busy(context.Background(), crm.Conn{AccessToken: "limited"}, time.Now())
	var ae *crm.APIError
	if !errors.As(err, &ae) || !ae.Retry || ae.After != 7*time.Second {
		t.Fatalf("429 = %v", err)
	}
	if _, err := c.Busy(context.Background(), crm.Conn{AccessToken: "down"}, time.Now()); !errors.As(err, &ae) || !ae.Retry {
		t.Fatalf("503 = %v", err)
	}
	if _, err := c.Busy(context.Background(), crm.Conn{AccessToken: "x"}, time.Now()); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("401 = %v", err)
	}
	if c.Capabilities().LogCalls || !c.Capabilities().UserScope || !c.Capabilities().Calendar {
		t.Fatalf("capabilities = %+v", c.Capabilities())
	}
	if _, err := c.LogCall(context.Background(), crm.Conn{}, crm.CallLog{}); err == nil {
		t.Fatal("Outlook logged a call")
	}
}
