package pipedrive

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
	Method, Path, Query, Auth, Type, Body string
}

func setup(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) (*Connector, *[]seen, crm.Conn) {
	t.Helper()
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
		if h != nil {
			h(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.OAuthBase = srv.URL
	c.APIBase = func(string) (string, error) { return srv.URL, nil }
	return c, &calls, crm.Conn{AccessToken: "tok", ExternalAcct: "https://acme.pipedrive.com"}
}

func TestRegisteredAndCapabilities(t *testing.T) {
	c, ok := crm.New(Kind, http.DefaultClient)
	if !ok {
		t.Fatal("not registered")
	}
	if caps := c.Capabilities(); !caps.Lookup || !caps.LogCalls || !caps.RecordingUpload || caps.RecordingCopy {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestOAuthUsesBasicAuthAndKeepsTheAPIDomain(t *testing.T) {
	c, calls, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"scope":"base contacts:read","api_domain":"https://acme.pipedrive.com"}`))
	})
	cfg := crm.Config{ClientID: "cid", ClientSecret: "sec"}
	u := c.AuthURL(cfg, "https://h/cb", "st")
	if !strings.Contains(u, "/oauth/authorize?") || !strings.Contains(u, "state=st") || strings.Contains(u, "sec") {
		t.Fatalf("auth url = %s", u)
	}
	tk, err := c.Exchange(context.Background(), cfg, "https://h/cb", "code1")
	if err != nil || tk.Access != "a" || tk.Refresh != "r" || tk.Account != "https://acme.pipedrive.com" || len(tk.Scopes) != 2 {
		t.Fatalf("tokens = %+v, %v", tk, err)
	}
	got := (*calls)[0]
	if !strings.HasPrefix(got.Auth, "Basic ") || !strings.Contains(got.Body, "grant_type=authorization_code") || strings.Contains(got.Body, "sec") {
		t.Fatalf("token call = %+v", got)
	}
}

func TestRefreshRejectionIsPermanent(t *testing.T) {
	c, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
	})
	_, err := c.Refresh(context.Background(), crm.Config{}, "r")
	var ae *crm.APIError
	if !errors.As(err, &ae) || ae.Retry {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIBaseRefusesOtherHosts(t *testing.T) {
	for _, bad := range []string{"", "http://acme.pipedrive.com", "https://evil.example", "https://pipedrive.com.evil.example", "https://acme.pipedrive.com:8443", "https://x@evil.example"} {
		if _, err := apiBase(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if b, err := apiBase("https://acme.pipedrive.com"); err != nil || b != "https://acme.pipedrive.com" {
		t.Fatalf("good = %q, %v", b, err)
	}
}

func TestLookupChecksTheNumberAndFallsBackToTheTail(t *testing.T) {
	n := 0
	c, calls, cc := setup(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = w.Write([]byte(`{"data":{"items":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"items":[
			{"item":{"id":1,"name":"Wrong","phones":["+32 99 99 99 99"]}},
			{"item":{"id":2,"name":"Ann Smith","phones":["011 22 33 44"],"organization":{"id":9,"name":"ACME"}}}]}}`))
	})
	got, err := c.Lookup(context.Background(), cc, "+32 11 22 33 44")
	// 011 22 33 44 has only 8 digits against 11: not the same, so no match.
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	n = 1
	(*calls) = nil
	c2, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"items":[{"item":{"id":2,"name":"Ann Smith","phones":["+32 11 22 33 44"],"organization":{"id":9,"name":"ACME"}}}]}}`))
	})
	got, err = c2.Lookup(context.Background(), cc, "0032 11 22 33 44")
	if err != nil || got == nil || got.ID != "2" || got.Name != "Ann Smith" || got.Company != "ACME" || got.CompanyID != "9" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestResolveUserMatchesTheEmailExactly(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":5,"email":"other@x.com"},{"id":6,"email":"Anna@Example.com"}]}`))
	})
	id, err := c.ResolveUser(context.Background(), cc, "anna@example.com")
	if err != nil || id != "6" || !strings.Contains((*calls)[0].Query, "search_by_email=1") || (*calls)[0].Auth != "Bearer tok" {
		t.Fatalf("id=%q err=%v call=%+v", id, err, (*calls)[0])
	}
}

func TestLogCallBody(t *testing.T) {
	c, calls, cc := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"abc-123"}}`))
	})
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ref, err := c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", From: "+3211", To: "201",
		StartedAt: start, EndedAt: start.Add(65 * time.Second), Duration: 60 * time.Second, Outcome: crm.OutcomeAnswered,
		OwnerID: "17", Contact: &crm.Contact{ID: "2", Name: "Ann", CompanyID: "9"}})
	if err != nil || ref != "abc-123" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	var b map[string]any
	_ = json.Unmarshal([]byte((*calls)[0].Body), &b)
	if b["outcome"] != "connected" || b["duration"] != "60" || b["start_time"] != "2026-11-10 08:00:00" || b["end_time"] != "2026-11-10 08:01:05" ||
		b["user_id"] != float64(17) || b["person_id"] != float64(2) || b["org_id"] != float64(9) || b["subject"] != "Call from Ann" || b["to_phone_number"] != "201" {
		t.Fatalf("body = %v", b)
	}
	if (*calls)[0].Path != "/api/v1/callLogs" {
		t.Fatalf("path = %s", (*calls)[0].Path)
	}
	// A missed call is no_answer, with no owner or person when unknown.
	_, _ = c.LogCall(context.Background(), cc, crm.CallLog{Direction: "inbound", Number: "+3211", Missed: true, Outcome: crm.OutcomeNoAnswer, StartedAt: start})
	b = map[string]any{}
	_ = json.Unmarshal([]byte((*calls)[1].Body), &b)
	if b["outcome"] != "no_answer" || b["person_id"] != nil || b["user_id"] != nil {
		t.Fatalf("missed body = %v", b)
	}
}

func TestRecordingUploadIsMultipartAndBounded(t *testing.T) {
	c, calls, cc := setup(t, nil)
	rec := &crm.RecordingFile{Body: io.NopCloser(strings.NewReader("RIFFdata")), Size: 8, Name: "recording-5.wav", ContentType: "audio/wav"}
	if err := c.UpdateCallLog(context.Background(), cc, "abc-123", crm.CallLog{Recording: rec}); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0]
	if got.Path != "/api/v1/callLogs/abc-123/recordings" || !strings.HasPrefix(got.Type, "multipart/form-data") ||
		!strings.Contains(got.Body, `name="file"; filename="recording-5.wav"`) || !strings.Contains(got.Body, "RIFFdata") {
		t.Fatalf("upload = %+v", got)
	}
	old := MaxUploadBytes
	MaxUploadBytes = 4
	defer func() { MaxUploadBytes = old }()
	rec = &crm.RecordingFile{Body: io.NopCloser(strings.NewReader("RIFFdata")), Size: 8}
	var se *crm.SkipError
	if err := c.UpdateCallLog(context.Background(), cc, "abc-123", crm.CallLog{Recording: rec}); !errors.As(err, &se) || len(*calls) != 1 {
		t.Fatalf("oversize = %v, calls %d", err, len(*calls))
	}
	// Size unknown (0) is still bounded by reading.
	rec = &crm.RecordingFile{Body: io.NopCloser(strings.NewReader("RIFFdata")), Size: 0}
	if err := c.UpdateCallLog(context.Background(), cc, "abc-123", crm.CallLog{Recording: rec}); !errors.As(err, &se) {
		t.Fatalf("unknown size = %v", err)
	}
	// No recording: nothing is sent (a call log cannot be edited).
	if err := c.UpdateCallLog(context.Background(), cc, "abc-123", crm.CallLog{}); err != nil || len(*calls) != 1 {
		t.Fatal("an update with no recording sent a request")
	}
}

func TestUnauthorizedIsErrAuth(t *testing.T) {
	c, _, cc := setup(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	if err := c.Ping(context.Background(), cc); !errors.Is(err, crm.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}
