package slack

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/secret"
)

type rig struct {
	svc *Service
	w   *Worker
	st  *Mem
	srv *httptest.Server

	mu         sync.Mutex
	reqs       []*http.Request
	bodies     []string
	answer     func(r *http.Request) (int, string)
	retryAfter string
	clockMu    sync.Mutex
	now        time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{st: NewMem(), now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		answer: func(*http.Request) (int, string) { return 200, "ok" }}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		r.mu.Lock()
		r.reqs, r.bodies = append(r.reqs, q), append(r.bodies, string(b))
		fn, ra := r.answer, r.retryAfter
		r.mu.Unlock()
		code, body := fn(q)
		if ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(r.srv.Close)
	r.svc = &Service{Store: r.st, Box: box, PublicURL: "https://pbx.example.com", SkipHostCheck: true, Client: r.srv.Client(),
		PostURL: r.srv.URL + "/api/chat.postMessage", Now: func() time.Time { r.clockMu.Lock(); defer r.clockMu.Unlock(); return r.now }}
	r.w = &Worker{Svc: r.svc, Pace: time.Millisecond, Rand: func() float64 { return 0.5 }}
	return r
}

func (r *rig) advance(d time.Duration) { r.clockMu.Lock(); r.now = r.now.Add(d); r.clockMu.Unlock() }

func (r *rig) hook(t *testing.T, e events.Entry) {
	t.Helper()
	if err := r.svc.Hook()(context.Background(), nil, e); err != nil {
		t.Fatal(err)
	}
}

func missed(id, ext string) events.Entry {
	return events.Entry{Event: events.Event{APIVersion: "1", ID: id, Type: events.CallMissed, CreatedAt: time.Date(2026, 11, 12, 9, 59, 0, 0, time.UTC),
		Data: events.Data{CallID: "c1", Direction: "inbound", Extension: ext, Reason: "no_answer",
			From: events.Party{Number: "+3250123456", Name: "Acme Ltd"}, To: events.Party{Number: ext, Extension: ext, Name: "Reception"}}}}
}

func voicemail(id, ext string) events.Entry {
	return events.Entry{Event: events.Event{APIVersion: "1", ID: id, Type: events.VoicemailReceived, CreatedAt: time.Now(),
		Data: events.Data{CallID: "c2", Direction: "inbound", Extension: ext, DurationMs: 83000,
			From: events.Party{Number: "+3250123456"}, To: events.Party{Number: ext, Extension: ext}}}}
}

func (r *rig) newWebhook(t *testing.T, mod func(*Input)) Destination {
	t.Helper()
	in := Input{Name: "Phone", Mode: ModeWebhook, Enabled: true, Secret: r.srv.URL + "/services/T000/B000/xxxx"}
	if mod != nil {
		mod(&in)
	}
	d, err := r.svc.Create(context.Background(), "admin", in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestPostMissedCallAndVoicemail fails if a missed call or a voicemail does
// not reach the incoming webhook once, as a Slack message with the caller's
// number and name, or if a repeated event posts twice.
func TestPostMissedCallAndVoicemail(t *testing.T) {
	r := newRig(t)
	r.newWebhook(t, nil)
	r.hook(t, missed("evt_1", "200"))
	r.hook(t, missed("evt_1", "200"))
	r.hook(t, voicemail("evt_2", "200"))
	if n := r.w.Process(t.Context()); n != 2 {
		t.Fatalf("processed %d, want 2", n)
	}
	if len(r.bodies) != 2 {
		t.Fatalf("%d requests, want 2", len(r.bodies))
	}
	var m struct {
		Text    string           `json:"text"`
		Blocks  []map[string]any `json:"blocks"`
		Channel string           `json:"channel"`
	}
	if err := json.Unmarshal([]byte(r.bodies[0]), &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "Missed call from Acme Ltd (+3250123456) to Reception (200)") || m.Channel != "" || len(m.Blocks) != 2 {
		t.Fatalf("message = %+v", m)
	}
	if !strings.Contains(r.bodies[1], "New voicemail for") || !strings.Contains(r.bodies[1], "1 min 23 s") ||
		!strings.Contains(r.bodies[1], "https://pbx.example.com/voicemail") {
		t.Fatalf("voicemail message = %s", r.bodies[1])
	}
	if r.reqs[0].Header.Get("Authorization") != "" {
		t.Fatal("an incoming webhook request carries an Authorization header")
	}
	posts, _ := r.svc.Posts(t.Context(), PostQuery{})
	for _, p := range posts {
		if p.Status != StatusSucceeded {
			t.Fatalf("post %+v", p)
		}
	}
}

// TestBotMode fails if a bot destination does not call chat.postMessage with
// its token in the Authorization header and the channel in the body, or
// treats {"ok":false} as success.
func TestBotMode(t *testing.T) {
	r := newRig(t)
	d := r.newWebhook(t, func(in *Input) { in.Mode, in.Channel, in.Secret = ModeBot, "#phone", "xoxb-1234-secret" })
	r.answer = func(*http.Request) (int, string) { return 200, `{"ok":true}` }
	r.hook(t, missed("evt_1", "200"))
	r.w.Process(t.Context())
	if got := r.reqs[0].Header.Get("Authorization"); got != "Bearer xoxb-1234-secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if !strings.Contains(r.bodies[0], `"channel":"#phone"`) {
		t.Fatalf("body = %s", r.bodies[0])
	}
	// A channel the bot is not in: the post is dead, the destination stays on.
	r.answer = func(*http.Request) (int, string) { return 200, `{"ok":false,"error":"not_in_channel"}` }
	r.hook(t, missed("evt_2", "200"))
	r.w.Process(t.Context())
	got, _ := r.st.Get(t.Context(), d.ID)
	if !got.Enabled || got.Dead != 1 || got.LastError != "slack: not_in_channel" {
		t.Fatalf("destination = %+v", got)
	}
	// A revoked token switches the destination off.
	r.answer = func(*http.Request) (int, string) { return 200, `{"ok":false,"error":"token_revoked"}` }
	r.hook(t, missed("evt_3", "200"))
	r.w.Process(t.Context())
	got, _ = r.st.Get(t.Context(), d.ID)
	if got.Enabled || got.DisabledReason != ReasonAuth {
		t.Fatalf("destination = %+v, want disabled for auth", got)
	}
	// A new token re-enables it.
	tok := "xoxb-new-token"
	if got, err := r.svc.Update(t.Context(), "admin", d.ID, Patch{Secret: &tok}); err != nil || !got.Enabled || got.DisabledReason != "" {
		t.Fatalf("after a new token: %+v, %v", got, err)
	}
}

// TestRetryAndDead fails if a 429 or 5xx is not retried with the webhook
// schedule (honouring Retry-After), or if the series is not dead after the
// last retry and a manual retry does not make it due again.
func TestRetryAndDead(t *testing.T) {
	r := newRig(t)
	d := r.newWebhook(t, nil)
	r.answer = func(*http.Request) (int, string) { return 503, "" }
	r.hook(t, missed("evt_1", "200"))
	for i := 0; i < 8; i++ {
		if n := r.w.Process(t.Context()); n != 1 {
			t.Fatalf("attempt %d: processed %d, want 1", i+1, n)
		}
		r.advance(13 * time.Hour)
	}
	posts, _ := r.svc.Posts(t.Context(), PostQuery{DestinationID: d.ID})
	if len(posts) != 1 || posts[0].Status != StatusDead || posts[0].Attempts != 8 || !strings.Contains(posts[0].LastError, "retries exhausted") {
		t.Fatalf("posts = %+v", posts)
	}
	r.answer = func(*http.Request) (int, string) { return 200, "ok" }
	if _, err := r.svc.Retry(t.Context(), "admin", posts[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := r.w.Process(t.Context()); n != 1 {
		t.Fatalf("after retry processed %d", n)
	}
	posts, _ = r.svc.Posts(t.Context(), PostQuery{})
	if posts[0].Status != StatusSucceeded {
		t.Fatalf("post = %+v", posts[0])
	}
	if _, err := r.svc.Retry(t.Context(), "admin", posts[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry of a succeeded post: %v", err)
	}
}

// TestRetryAfter fails if a 429 with Retry-After is retried sooner than asked.
func TestRetryAfter(t *testing.T) {
	r := newRig(t)
	r.newWebhook(t, nil)
	r.answer = func(*http.Request) (int, string) { return 429, "" }
	r.retryAfter = "1800"
	r.hook(t, missed("evt_1", "200"))
	r.w.Process(t.Context())
	r.advance(20 * time.Minute)
	if n := r.w.Process(t.Context()); n != 0 {
		t.Fatal("retried before the Retry-After")
	}
	r.advance(11 * time.Minute)
	if n := r.w.Process(t.Context()); n != 1 {
		t.Fatalf("processed %d after the Retry-After, want 1", n)
	}
}

// TestGoneWebhookDisables fails if a 404 from an incoming webhook (the app
// was removed) keeps the destination on.
func TestGoneWebhookDisables(t *testing.T) {
	r := newRig(t)
	d := r.newWebhook(t, nil)
	r.answer = func(*http.Request) (int, string) { return 404, "no_service" }
	r.hook(t, missed("evt_1", "200"))
	r.w.Process(t.Context())
	got, _ := r.st.Get(t.Context(), d.ID)
	if got.Enabled || got.DisabledReason != ReasonGone {
		t.Fatalf("destination = %+v", got)
	}
}

// TestFilters fails if an extension limit or an event choice is ignored, or a
// disabled destination is queued.
func TestFilters(t *testing.T) {
	r := newRig(t)
	r.newWebhook(t, func(in *Input) {
		in.Name = "only 300"
		in.Extensions = []string{"300"}
		in.Events = []string{events.CallMissed}
	})
	off := r.newWebhook(t, func(in *Input) { in.Name = "off"; in.Enabled = false })
	r.hook(t, missed("evt_1", "200"))
	r.hook(t, voicemail("evt_2", "300"))
	posts, _ := r.svc.Posts(t.Context(), PostQuery{})
	if len(posts) != 0 {
		t.Fatalf("posts = %+v, want none", posts)
	}
	r.hook(t, missed("evt_3", "300"))
	posts, _ = r.svc.Posts(t.Context(), PostQuery{})
	if len(posts) != 1 || posts[0].DestinationID == off.ID {
		t.Fatalf("posts = %+v", posts)
	}
}

// TestInjection fails if a caller name can make Slack ping a channel, build
// a link or break the formatting of the message.
func TestInjection(t *testing.T) {
	e := missed("evt_1", "200")
	e.Data.From.Name = "<!channel> *evil* <https://evil.example|click> & <@U123>\x00"
	text, blocks, ok := Message(e.Event, "")
	if !ok {
		t.Fatal("not postable")
	}
	b, _ := json.Marshal(blocks)
	for _, s := range []string{text, string(b)} {
		if strings.Contains(s, "<!channel>") || strings.Contains(s, "<https://evil") || strings.Contains(s, "<@U123>") || strings.Contains(s, "*evil*") {
			t.Fatalf("unescaped name in %s", s)
		}
	}
	if sec, _ := blocks[0]["text"].(map[string]any); !strings.Contains(sec["text"].(string), "&lt;!channel&gt;") {
		t.Fatalf("blocks = %s", b)
	}
	e.Data.From.Name = strings.Repeat("x", 500)
	if text, _, _ = Message(e.Event, ""); len(text) > 400 {
		t.Fatalf("an unbounded name reached the message: %d bytes", len(text))
	}
	if _, _, ok := Message(events.Event{Type: events.CallRinging}, ""); ok {
		t.Fatal("call.ringing is postable")
	}
}

// TestValidation fails if a bad destination is accepted or the secret is
// returned in a view.
func TestValidation(t *testing.T) {
	r := newRig(t)
	r.svc.SkipHostCheck = false
	bad := []Input{
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "http://hooks.slack.com/services/x"},
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "https://evil.example/services/x"},
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com.evil.example/services/x"},
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/other"},
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/services/x?next=1"},
		{Name: "a", Mode: ModeBot, Enabled: true, Secret: "xoxb-1", Channel: ""},
		{Name: "a", Mode: ModeBot, Enabled: true, Secret: "not-a-token", Channel: "#c"},
		{Name: "a", Mode: ModeBot, Enabled: true, Secret: "xoxb-1", Channel: "#c", Events: []string{events.CallRinging}},
		{Name: " ", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/services/x"},
		{Name: "a", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/services/x", Extensions: []string{"abc"}},
		{Name: "a", Mode: "other", Enabled: true, Secret: "x"},
		{Name: "a", Mode: ModeWebhook, Enabled: true},
	}
	for i, in := range bad {
		if _, err := r.svc.Create(t.Context(), "admin", in); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
	d, err := r.svc.Create(t.Context(), "admin", Input{Name: "ok", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/services/T0/B0/secretvalue"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "secretvalue") || strings.Contains(string(b), "hooks.slack.com") || !d.HasSecret {
		t.Fatalf("view leaks the secret: %s", b)
	}
	if len(d.Events) != 2 {
		t.Fatalf("default events = %v", d.Events)
	}
	if _, err := r.svc.Create(t.Context(), "admin", Input{Name: "ok", Mode: ModeWebhook, Enabled: true, Secret: "https://hooks.slack.com/services/T0/B0/other"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
}

// TestTest fails if the test ping is not rate limited or reports Slack's
// answer.
func TestTest(t *testing.T) {
	r := newRig(t)
	d := r.newWebhook(t, nil)
	res, err := r.svc.Test(t.Context(), d.ID)
	if err != nil || !res.Delivered {
		t.Fatalf("test = %+v, %v", res, err)
	}
	r.answer = func(*http.Request) (int, string) { return 404, "no_service" }
	res, _ = r.svc.Test(t.Context(), d.ID)
	if res.Delivered || res.Status != 404 {
		t.Fatalf("test = %+v", res)
	}
	for i := 0; i < 4; i++ {
		_, _ = r.svc.Test(t.Context(), d.ID)
	}
	if _, err := r.svc.Test(t.Context(), d.ID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("7th test: %v", err)
	}
	if got, _ := r.st.Get(t.Context(), d.ID); got.ConsecutiveFailures != 0 {
		t.Fatalf("a test counted against the health: %+v", got)
	}
}

// TestClassify fails if an answer is classified wrongly.
func TestClassify(t *testing.T) {
	cases := []struct {
		mode string
		r    result
		want Outcome
	}{
		{ModeWebhook, result{Status: 200, Body: []byte("ok")}, OutcomeSuccess},
		{ModeWebhook, result{Status: 400, Body: []byte("invalid_payload")}, OutcomeDead},
		{ModeWebhook, result{Status: 429}, OutcomeRetry},
		{ModeWebhook, result{Status: 502}, OutcomeRetry},
		{ModeWebhook, result{Status: 410}, OutcomeDisable},
		{ModeWebhook, result{Err: errors.New("dial")}, OutcomeRetry},
		{ModeBot, result{Status: 200, Body: []byte(`{"ok":true}`)}, OutcomeSuccess},
		{ModeBot, result{Status: 200, Body: []byte(`{"ok":false,"error":"ratelimited"}`)}, OutcomeRetry},
		{ModeBot, result{Status: 200, Body: []byte(`{"ok":false,"error":"invalid_auth"}`)}, OutcomeDisable},
		{ModeBot, result{Status: 200, Body: []byte(`{"ok":false,"error":"channel_not_found"}`)}, OutcomeDead},
		{ModeBot, result{Status: 200, Body: []byte(`<html>`)}, OutcomeRetry},
		{ModeBot, result{Status: 404}, OutcomeDead},
		{ModeBot, result{Status: 401}, OutcomeDisable},
	}
	for i, c := range cases {
		if got, _, _ := Classify(c.mode, c.r); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
	// An error code with odd characters is never copied into the log.
	if _, msg, _ := Classify(ModeBot, result{Status: 200, Body: []byte(`{"ok":false,"error":"x<script>"}`)}); msg != "slack_error" {
		t.Fatalf("message = %q", msg)
	}
}
