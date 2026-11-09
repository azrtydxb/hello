package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// hookFake is the webhook part of the store, in memory, over the account
// fake's sessions.
type hookFake struct {
	*acctFake
	hmu     sync.Mutex
	hooks   map[int64]*store.DoorWebhook
	secrets map[int64]string
	next    int64
	touched []int64
}

func newHookFake(t *testing.T) *hookFake {
	af, _, _, _ := newAcctFake(t)
	return &hookFake{acctFake: af, hooks: map[int64]*store.DoorWebhook{}, secrets: map[int64]string{}, next: 1}
}

func (f *hookFake) ListDoorWebhooks(context.Context) ([]store.DoorWebhook, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	out := []store.DoorWebhook{}
	for id := int64(1); id < f.next; id++ {
		if h, ok := f.hooks[id]; ok {
			out = append(out, *h)
		}
	}
	return out, nil
}

func (f *hookFake) GetDoorWebhook(_ context.Context, id int64) (store.DoorWebhook, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	if h, ok := f.hooks[id]; ok {
		return *h, nil
	}
	return store.DoorWebhook{}, store.ErrNotFound
}

func (f *hookFake) CreateDoorWebhook(_ context.Context, _ string, in store.DoorWebhookInput) (store.DoorWebhook, string, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	if in.Name == "bad name" {
		return store.DoorWebhook{}, "", &store.ValidationError{Fields: []routing.FieldError{{Path: "name", Message: "no good"}}}
	}
	for _, h := range f.hooks {
		if h.Name == in.Name {
			return store.DoorWebhook{}, "", store.ErrConflict
		}
	}
	id := f.next
	f.next++
	f.hooks[id] = &store.DoorWebhook{ID: id, Name: in.Name, Action: in.Action, RateLimit: in.RateLimit, Enabled: in.Enabled}
	f.secrets[id] = fmt.Sprintf("%064x", id)
	return *f.hooks[id], f.secrets[id], nil
}

func (f *hookFake) UpdateDoorWebhook(_ context.Context, _ string, id int64, in store.DoorWebhookInput) (store.DoorWebhook, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	h, ok := f.hooks[id]
	if !ok {
		return store.DoorWebhook{}, store.ErrNotFound
	}
	h.Name, h.Action, h.RateLimit, h.Enabled = in.Name, in.Action, in.RateLimit, in.Enabled
	return *h, nil
}

func (f *hookFake) DeleteDoorWebhook(_ context.Context, _ string, id int64) error {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	if _, ok := f.hooks[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.hooks, id)
	return nil
}

func (f *hookFake) RotateDoorWebhookSecret(_ context.Context, _ string, id int64) (string, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	if _, ok := f.hooks[id]; !ok {
		return "", store.ErrNotFound
	}
	f.secrets[id] = fmt.Sprintf("%064x", id+1000)
	return f.secrets[id], nil
}

func (f *hookFake) DoorWebhookForTrigger(_ context.Context, id int64) (store.DoorTriggerHook, bool, error) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	h, ok := f.hooks[id]
	if !ok {
		return store.DoorTriggerHook{}, false, nil
	}
	return store.DoorTriggerHook{ID: id, Name: h.Name, Action: h.Action, Enabled: h.Enabled, RateLimit: h.RateLimit,
		Secret: f.secrets[id], RingGroup: "800", PagingCode: "*60", Announcement: "chime"}, true, nil
}

func (f *hookFake) NoteDoorWebhookTriggered(_ context.Context, id int64) error {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	f.touched = append(f.touched, id)
	return nil
}

// memTriggers is the rate window, the duplicate window and the queue, in
// memory.
type memTriggers struct {
	mu      sync.Mutex
	counts  map[int64]int64
	claimed map[int64]bool
	queue   []livestate.DoorTrigger
}

func (m *memTriggers) DoorHookRate(_ context.Context, id int64, _ time.Duration) (int64, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[id]++
	return m.counts[id], 42*time.Second + 300*time.Millisecond, nil
}

func (m *memTriggers) DoorHookClaim(_ context.Context, id int64, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed[id] {
		return false, nil
	}
	m.claimed[id] = true
	return true, nil
}

func (m *memTriggers) EnqueueDoorTrigger(_ context.Context, t livestate.DoorTrigger) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, t)
	return nil
}

func (m *memTriggers) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts, m.claimed, m.queue = map[int64]int64{}, map[int64]bool{}, nil
}

func (m *memTriggers) queued() []livestate.DoorTrigger {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]livestate.DoorTrigger(nil), m.queue...)
}

// TestDoorWebhook fails if a trigger with a missing, wrong, stale or future
// signature is not 401, if the 401 differs between an existing, a disabled
// and an unknown webhook, if the rate limit does not give 429 with
// Retry-After, if a valid trigger is not queued with its action, groups and
// chime, if a duplicate within the window is queued again, if an oversize or
// non-object body is accepted, or if a secret is returned after its creation
// (spec S-48).
func TestDoorWebhook(t *testing.T) {
	f := newHookFake(t)
	trig := &memTriggers{}
	trig.reset()
	logs := &bytes.Buffer{}
	h := Handler(Config{Store: f, DoorTriggers: trig, Log: slog.New(slog.NewTextHandler(logs, nil)), SessionTTL: time.Hour})

	var id int64
	var sec string
	t.Run("the secret is shown once", func(t *testing.T) {
		for _, who := range []string{"dave", "alice"} {
			if rec := acctCall(h, who, "POST", "/api/v1/door/webhooks", map[string]any{"name": "Gate", "action": "ring_door"}); rec.Code != http.StatusForbidden {
				t.Errorf("%s may create a webhook: %d", who, rec.Code)
			}
		}
		rec := acctCall(h, "carol", "POST", "/api/v1/door/webhooks", map[string]any{"name": "Gate", "action": "ring_door", "rateLimit": 3})
		got := wantJSON(t, rec, http.StatusCreated, "create")
		sec, _ = got["secret"].(string)
		id = int64(got["id"].(float64))
		if len(sec) != 64 {
			t.Fatalf("created = %v, want a 64-character secret", got)
		}
		for _, p := range []struct{ who, method, path string }{
			{"carol", "GET", "/api/v1/door/webhooks"}, {"dave", "GET", "/api/v1/door/webhooks"},
			{"carol", "GET", "/api/v1/door/webhooks/1"}, {"carol", "PUT", "/api/v1/door/webhooks/1"},
		} {
			var body any
			if p.method == "PUT" {
				body = map[string]any{"name": "Gate", "action": "ring_door", "rateLimit": 3}
			}
			if r := acctCall(h, p.who, p.method, p.path, body); r.Code != http.StatusOK || strings.Contains(r.Body.String(), sec) {
				t.Errorf("%s %s = %d: the secret is shown again or the call failed: %s", p.method, p.path, r.Code, r.Body)
			}
		}
		if r := acctCall(h, "carol", "POST", "/api/v1/door/webhooks/1/rotate-secret", nil); r.Code != http.StatusOK ||
			strings.Contains(r.Body.String(), sec) || len(wantJSON(t, r, http.StatusOK, "rotate")["secret"].(string)) != 64 {
			t.Errorf("rotate = %d %s", r.Code, r.Body)
		}
		sec = f.secrets[id] // the rotated one signs from here on
		if strings.Contains(logs.String(), sec) {
			t.Error("a secret reached the log")
		}
	})

	t.Run("management answers", func(t *testing.T) {
		for _, c := range []struct {
			method, path string
			body         any
			want         int
		}{
			{"POST", "/api/v1/door/webhooks", map[string]any{"name": "Gate", "action": "ring_door"}, http.StatusConflict},
			{"POST", "/api/v1/door/webhooks", map[string]any{"action": "ring_door"}, http.StatusBadRequest},
			{"POST", "/api/v1/door/webhooks", map[string]any{"name": "x", "action": "ring_door", "extra": 1}, http.StatusBadRequest},
			{"POST", "/api/v1/door/webhooks", map[string]any{"name": "bad name", "action": "ring_door"}, http.StatusBadRequest},
			{"GET", "/api/v1/door/webhooks/99", nil, http.StatusNotFound},
			{"PUT", "/api/v1/door/webhooks/99", map[string]any{"name": "x", "action": "ring_door"}, http.StatusNotFound},
			{"PUT", "/api/v1/door/webhooks/1", map[string]any{"action": "ring_door"}, http.StatusBadRequest},
			{"POST", "/api/v1/door/webhooks/99/rotate-secret", nil, http.StatusNotFound},
			{"DELETE", "/api/v1/door/webhooks/99", nil, http.StatusNotFound},
		} {
			if r := acctCall(h, "carol", c.method, c.path, c.body); r.Code != c.want {
				t.Errorf("%s %s = %d %s, want %d", c.method, c.path, r.Code, r.Body, c.want)
			}
		}
	})

	post := func(path string, headers map[string]string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	signed := func(key string, at time.Time, body string) map[string]string {
		ts := strconv.FormatInt(at.Unix(), 10)
		return map[string]string{"X-Hello-Timestamp": ts, "X-Hello-Signature": doorSignature(key, ts, []byte(body))}
	}
	path := "/api/v1/door/webhooks/" + strconv.FormatInt(id, 10) + "/trigger"

	t.Run("a bad signature is 401, the same for every id", func(t *testing.T) {
		now := time.Now()
		stale := signed(sec, now.Add(-10*time.Minute), "")
		future := signed(sec, now.Add(10*time.Minute), "")
		wrong := signed(sec+"x", now, "")
		other := signed(sec, now, "x") // signed for another body
		half := map[string]string{"X-Hello-Timestamp": strconv.FormatInt(now.Unix(), 10)}
		junkTS := map[string]string{"X-Hello-Timestamp": "yesterday", "X-Hello-Signature": "00"}
		var first string
		for name, hd := range map[string]map[string]string{"none": nil, "stale": stale, "future": future, "wrong key": wrong,
			"wrong body": other, "no signature": half, "junk timestamp": junkTS} {
			rec := post(path, hd, "")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: %d %s, want 401", name, rec.Code, rec.Body)
			}
			if first == "" {
				first = rec.Body.String()
			}
			if rec.Body.String() != first {
				t.Errorf("%s: the 401 body differs: %s vs %s", name, rec.Body, first)
			}
		}
		// A correctly signed request for an unknown webhook is the same 401.
		unknown := post("/api/v1/door/webhooks/999/trigger", signed(sec, now, ""), "")
		if unknown.Code != http.StatusUnauthorized || unknown.Body.String() != first {
			t.Errorf("unknown id: %d %s, want the same 401 as a known one: %s", unknown.Code, unknown.Body, first)
		}
		if post("/api/v1/door/webhooks/abc/trigger", signed(sec, now, ""), "").Code != http.StatusUnauthorized {
			t.Error("a non-numeric id was not 401")
		}
		if n := len(trig.queued()); n != 0 {
			t.Errorf("%d triggers were queued for unauthenticated requests", n)
		}
	})

	t.Run("a valid trigger is queued once and a duplicate folds in", func(t *testing.T) {
		trig.reset()
		body := `{"label":"Front gate"}`
		rec := post(path, signed(sec, time.Now(), body), body)
		if got := wantJSON(t, rec, http.StatusAccepted, "trigger"); got["status"] != "queued" {
			t.Fatalf("first trigger = %v", got)
		}
		q := trig.queued()
		if len(q) != 1 || q[0].Webhook != id || q[0].Name != "Gate" || q[0].Action != "ring_door" || q[0].RingGroup != "800" ||
			q[0].Label != "Front gate" || q[0].Announcement != "chime" {
			t.Fatalf("queued = %+v", q)
		}
		dup := post(path, signed(sec, time.Now(), ""), "")
		if got := wantJSON(t, dup, http.StatusAccepted, "duplicate"); got["status"] != "coalesced" {
			t.Errorf("duplicate = %v, want coalesced", got)
		}
		if n := len(trig.queued()); n != 1 {
			t.Errorf("%d triggers are queued after a duplicate, want 1", n)
		}
		found := false
		for _, a := range f.audit {
			if strings.Contains(a, "webhook:Gate trigger door_webhook") {
				found = true
			}
			if strings.Contains(a, "Front gate") {
				t.Errorf("audit carries the body: %q", a)
			}
		}
		if !found {
			t.Errorf("no audit row for the trigger: %v", f.audit)
		}
		if len(f.touched) == 0 {
			t.Error("the trigger time was not stamped")
		}
	})

	t.Run("bodies", func(t *testing.T) {
		trig.reset()
		big := `{"label":"` + strings.Repeat("x", 1100) + `"}`
		if rec := post(path, signed(sec, time.Now(), big), big); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("a 1.1 KiB body: %d, want 413", rec.Code)
		}
		for _, bad := range []string{"not json", "[1]", `"s"`} {
			if rec := post(path, signed(sec, time.Now(), bad), bad); rec.Code != http.StatusBadRequest {
				t.Errorf("body %q: %d, want 400", bad, rec.Code)
			}
		}
		label, ok := doorLabel([]byte(`{"label":"` + strings.Repeat("é", 100) + "\\u0007\\n\"}"))
		if !ok || len([]rune(label)) != doorLabelMax || strings.ContainsAny(label, "\x07\n") {
			t.Errorf("label = %q, %v, want %d printable runes", label, ok, doorLabelMax)
		}
	})

	t.Run("the rate limit", func(t *testing.T) {
		trig.reset() // the limit of this webhook is 3 a minute
		var last *httptest.ResponseRecorder
		for i := 1; i <= 4; i++ {
			last = post(path, signed(sec, time.Now(), ""), "")
			if i <= 3 && last.Code != http.StatusAccepted {
				t.Fatalf("trigger %d = %d, want 202", i, last.Code)
			}
		}
		if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") != "43" {
			t.Errorf("trigger over the limit = %d, Retry-After %q, want 429 and 43", last.Code, last.Header().Get("Retry-After"))
		}
		// The limit is not shown to someone without the secret.
		if rec := post(path, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("unsigned over the limit = %d, want 401", rec.Code)
		}
	})

	t.Run("a disabled webhook is 401", func(t *testing.T) {
		if r := acctCall(h, "carol", "PUT", "/api/v1/door/webhooks/"+strconv.FormatInt(id, 10),
			map[string]any{"name": "Gate", "action": "ring_door", "rateLimit": 3, "enabled": false}); r.Code != http.StatusOK {
			t.Fatalf("disable = %d %s", r.Code, r.Body)
		}
		trig.reset()
		if rec := post(path, signed(sec, time.Now(), ""), ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("a disabled webhook: %d, want the same 401", rec.Code)
		}
		if n := len(trig.queued()); n != 0 {
			t.Error("a disabled webhook queued a trigger")
		}
	})

	t.Run("no Valkey is 503 and delete works", func(t *testing.T) {
		h2 := Handler(Config{Store: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), SessionTTL: time.Hour})
		f.hmu.Lock()
		f.hooks[id].Enabled = true
		f.hmu.Unlock()
		req := httptest.NewRequest("POST", path, nil)
		for k, v := range signed(sec, time.Now(), "") {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("without DoorTriggers: %d, want 503", rec.Code)
		}
		if r := acctCall(h, "carol", "DELETE", "/api/v1/door/webhooks/"+strconv.FormatInt(id, 10), nil); r.Code != http.StatusNoContent {
			t.Errorf("delete = %d", r.Code)
		}
		if rec := post(path, signed(sec, time.Now(), ""), ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("a deleted webhook: %d, want 401", rec.Code)
		}
	})
}
