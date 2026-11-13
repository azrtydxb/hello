package api

// Tests for the Slack notification routes (spec integrations P17). They run
// through Handler over the in-memory user store and slack.Mem.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/slack"
)

type slackEnv struct {
	st  *slack.Mem
	svc *slack.Service
	srv *httptest.Server
	hit *int
	url string
}

func newSlackEnv(t *testing.T) *slackEnv {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(fake.Close)
	st := slack.NewMem()
	svc := &slack.Service{Store: st, Box: box, SkipHostCheck: true, Client: fake.Client()}
	f, _, _, _ := newAcctFake(t)
	srv := httptest.NewServer(Handler(Config{Store: evStore{f}, Slack: svc, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour}))
	t.Cleanup(srv.Close)
	return &slackEnv{st: st, svc: svc, srv: srv, hit: &hits, url: fake.URL}
}

func (e *slackEnv) do(t *testing.T, who, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m, string(b)
}

func (e *slackEnv) want(t *testing.T, code int, who, method, path string, body any) map[string]any {
	t.Helper()
	got, m, raw := e.do(t, who, method, path, body)
	if got != code {
		t.Fatalf("%s %s as %s = %d %s, want %d", method, path, who, got, raw, code)
	}
	return m
}

// TestSlackDestinations: create, read, update, test, list posts, retry and
// delete a destination. It fails if the webhook URL or token appears in any
// response, if a non-administrator reaches the routes, if the mode can
// change, or if a bad destination is accepted.
func TestSlackDestinations(t *testing.T) {
	e := newSlackEnv(t)
	url := e.url + "/services/T0/B0/SECRETPART"
	d := e.want(t, 201, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "Phone", "mode": "webhook", "secret": url})
	id := toS(d["id"])
	if d["hasSecret"] != true || len(d["events"].([]any)) != 2 || d["enabled"] != true {
		t.Fatalf("created = %v", d)
	}
	e.want(t, 409, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "Phone", "mode": "webhook", "secret": url})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "x", "mode": "webhook", "secret": "http://hooks.slack.com/services/x"})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "x"})
	e.want(t, 400, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "x", "mode": "bot", "secret": "xoxb-1"})
	e.want(t, 403, "dave", "GET", "/api/v1/integrations/slack", nil)
	e.want(t, 403, "alice", "POST", "/api/v1/integrations/slack", map[string]any{"name": "y"})
	e.want(t, 403, "svc", "GET", "/api/v1/integrations/slack", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/slack/999", nil)
	if list := e.want(t, 200, "carol", "GET", "/api/v1/integrations/slack", nil); len(list["items"].([]any)) != 1 {
		t.Fatalf("list = %v", list)
	}

	if u := e.want(t, 200, "carol", "PATCH", "/api/v1/integrations/slack/"+id, map[string]any{"events": []string{"call.missed"}, "extensions": []string{"101"}}); len(u["events"].([]any)) != 1 {
		t.Fatalf("updated = %v", u)
	}
	e.want(t, 400, "carol", "PATCH", "/api/v1/integrations/slack/"+id, map[string]any{"events": []string{"call.ringing"}})
	e.want(t, 400, "carol", "PATCH", "/api/v1/integrations/slack/"+id, map[string]any{"mode": "bot"})
	e.want(t, 404, "carol", "PATCH", "/api/v1/integrations/slack/999", map[string]any{"name": "z"})
	other := e.want(t, 201, "carol", "POST", "/api/v1/integrations/slack", map[string]any{"name": "Other", "mode": "bot", "channel": "#phone", "secret": "xoxb-1-token"})
	e.want(t, 409, "carol", "PATCH", "/api/v1/integrations/slack/"+toS(other["id"]), map[string]any{"name": "Phone"})

	if r := e.want(t, 200, "carol", "POST", "/api/v1/integrations/slack/"+id+"/test", nil); r["delivered"] != true || *e.hit != 1 {
		t.Fatalf("test = %v", r)
	}
	e.want(t, 404, "carol", "POST", "/api/v1/integrations/slack/999/test", nil)
	for i := 0; i < 5; i++ {
		e.do(t, "carol", "POST", "/api/v1/integrations/slack/"+id+"/test", nil)
	}
	e.want(t, 429, "carol", "POST", "/api/v1/integrations/slack/"+id+"/test", nil)

	// A dead post can be retried; others cannot.
	ev := events.Entry{Event: events.Event{ID: "evt_1", Type: events.CallMissed, CreatedAt: time.Now(),
		Data: events.Data{CallID: "c", Extension: "101", From: events.Party{Number: "+3250123456"}, To: events.Party{Extension: "101"}}}}
	if err := e.st.Enqueue(context.Background(), nil, ev); err != nil {
		t.Fatal(err)
	}
	claims, _ := e.st.Claim(context.Background(), time.Now(), 10, time.Minute)
	var claim slack.Claim
	for _, c := range claims {
		if toS(float64(c.DestinationID)) == id {
			claim = c
		}
	}
	if claim.ID == 0 {
		t.Fatalf("no post for the destination: %d claims", len(claims))
	}
	posts := e.want(t, 200, "carol", "GET", "/api/v1/integrations/slack/"+id+"/posts", nil)
	pid := toS(posts["items"].([]any)[0].(map[string]any)["id"])
	_ = e.st.Finish(context.Background(), slack.Finish{PostID: claim.ID + 0, Now: time.Now(), Outcome: slack.OutcomeRetry, NextAttemptAt: time.Now().Add(time.Hour)})
	e.want(t, 409, "carol", "POST", "/api/v1/integrations/slack/posts/"+pid+"/retry", nil)
	_ = e.st.Finish(context.Background(), slack.Finish{PostID: claim.ID, Now: time.Now(), Outcome: slack.OutcomeDead, Error: "slack: channel_not_found"})
	if p := e.want(t, 200, "carol", "GET", "/api/v1/integrations/slack/"+id+"/posts?status=dead&limit=5", nil); len(p["items"].([]any)) != 1 {
		t.Fatalf("dead posts = %v", p)
	}
	e.want(t, 200, "carol", "POST", "/api/v1/integrations/slack/posts/"+pid+"/retry", nil)
	e.want(t, 404, "carol", "POST", "/api/v1/integrations/slack/posts/999/retry", nil)
	e.want(t, 404, "carol", "GET", "/api/v1/integrations/slack/999/posts", nil)
	e.want(t, 400, "carol", "GET", "/api/v1/integrations/slack/"+id+"/posts?status=nope", nil)
	e.want(t, 400, "carol", "GET", "/api/v1/integrations/slack/"+id+"/posts?limit=0", nil)

	for _, p := range []string{"/api/v1/integrations/slack", "/api/v1/integrations/slack/" + id, "/api/v1/integrations/slack/" + id + "/posts"} {
		_, _, raw := e.do(t, "carol", "GET", p, nil)
		for _, s := range []string{"SECRETPART", e.url, "xoxb-1-token"} {
			if strings.Contains(raw, s) {
				t.Fatalf("%s leaks %q: %s", p, s, raw)
			}
		}
	}
	e.want(t, 204, "carol", "DELETE", "/api/v1/integrations/slack/"+id, nil)
	e.want(t, 404, "carol", "DELETE", "/api/v1/integrations/slack/"+id, nil)
}

// TestSlackUnavailable fails if the routes work without a Slack service.
func TestSlackUnavailable(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	srv := httptest.NewServer(Handler(Config{Store: evStore{f}, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/integrations/slack", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "carol" + cookieSuffix})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
