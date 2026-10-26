package api

// Tests for the call-event routes: the stream, its resume and tickets, the
// history and the ownership rules. They run through Handler over the
// in-memory user store and events.Memory, so they need no services.

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/events"
)

// evStore adds two API tokens to the account fake: one scoped to events and
// extension 102, one that only reads.
type evStore struct{ *acctFake }

func (s evStore) TokenActor(ctx context.Context, h []byte) (auth.Actor, error) {
	switch hex.EncodeToString(h) {
	case hex.EncodeToString(auth.HashToken("tok-events-102")):
		return auth.Actor{UserID: uCarol, Username: "carol", Role: auth.RoleAdmin, Kind: auth.KindPersonalToken,
			Scopes: auth.Scopes{auth.ScopeEvents}, ExtensionScope: []string{"102"}}, nil
	case hex.EncodeToString(auth.HashToken("tok-read")):
		return auth.Actor{UserID: uCarol, Username: "carol", Role: auth.RoleAdmin, Kind: auth.KindPersonalToken,
			Scopes: auth.Scopes{auth.ScopeRead}}, nil
	}
	return s.acctFake.TokenActor(ctx, h)
}

type evEnv struct {
	f   *acctFake
	mem *events.Memory
	srv *httptest.Server
}

func newEvEnv(t *testing.T) *evEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	mem := events.NewMemory()
	h := Handler(Config{Store: evStore{f}, Events: mem, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &evEnv{f: f, mem: mem, srv: srv}
}

func callEvent(typ, ext string) events.Event {
	return events.New(typ, events.Data{CallID: "call-" + ext, Extension: ext, From: events.Party{Number: "100"}, To: events.Party{Number: ext}}, "", time.Now())
}

// req builds a request as who (a session cookie) or with a bearer token.
func (e *evEnv) req(t *testing.T, ctx context.Context, method, path, who, bearer string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// stream is an open event stream.
type sseStream struct {
	t      *testing.T
	resp   *http.Response
	frames chan sseFrame
}

type sseFrame struct {
	id, event string
	data      events.Event
	comment   string
}

func (e *evEnv) open(t *testing.T, path, who, bearer string, hdr map[string]string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := e.req(t, ctx, "GET", path, who, bearer)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	s := &sseStream{t: t, resp: resp, frames: make(chan sseFrame, 64)}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	go func() {
		defer close(s.frames)
		sc := bufio.NewScanner(resp.Body)
		var f sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f.id != "" || f.event != "" || f.comment != "" {
					s.frames <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, ":"):
				f.comment = strings.TrimSpace(line[1:])
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(line[6:]), &f.data)
			}
		}
	}()
	// The stream is open once its first comment arrives.
	if f := s.next(); f.comment != "stream open" {
		t.Fatalf("first sseFrame = %+v, want the stream-open comment", f)
	}
	return s
}

func (s *sseStream) next() sseFrame {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			s.t.Fatal("the stream ended")
		}
		return f
	case <-time.After(10 * time.Second):
		s.t.Fatal("no sseFrame within 10s")
		return sseFrame{}
	}
}

// nextEvent returns the next event sseFrame, skipping comments.
func (s *sseStream) nextEvent() sseFrame {
	s.t.Helper()
	for {
		if f := s.next(); f.event != "" {
			return f
		}
	}
}

func (s *sseStream) quiet(d time.Duration) {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if ok && f.event != "" {
			s.t.Fatalf("unexpected event %s for %s", f.event, f.data.Data.Extension)
		}
	case <-time.After(d):
	}
}

func (s *sseStream) types() []string {
	var out []string
	for {
		select {
		case f := <-s.frames:
			if f.event != "" {
				out = append(out, f.event+"/"+f.data.Data.Extension)
			}
		case <-time.After(150 * time.Millisecond):
			return out
		}
	}
}

// TestEventStreamOwnership fails if a stream delivers an event about an
// extension its credential may not see: a user sees only the extensions
// linked to them, a token with an extensionScope only those, and the viewer,
// operator and admin roles everything.
func TestEventStreamOwnership(t *testing.T) {
	e := newEvEnv(t)
	alice := e.open(t, "/api/v1/events/stream", "alice", "", nil)
	bob := e.open(t, "/api/v1/events/stream", "bob", "", nil)
	noext := e.open(t, "/api/v1/events/stream", "noext", "", nil)
	carol := e.open(t, "/api/v1/events/stream", "carol", "", nil)
	dave := e.open(t, "/api/v1/events/stream", "dave", "", nil)
	scoped := e.open(t, "/api/v1/events/stream", "", "tok-events-102", nil)

	e.mem.Append(callEvent(events.CallRinging, "101"))
	e.mem.Append(callEvent(events.CallRinging, "102"))
	e.mem.Append(callEvent(events.CallEnded, "101"))
	e.mem.Append(events.New(events.CallCompleted, events.Data{CallID: "x"}, "", time.Now())) // no extension

	for who, want := range map[string][]string{
		"alice":  {"call.ringing/101", "call.ended/101"},
		"bob":    {"call.ringing/102"},
		"noext":  nil,
		"carol":  {"call.ringing/101", "call.ringing/102", "call.ended/101", "call.completed/"},
		"dave":   {"call.ringing/101", "call.ringing/102", "call.ended/101", "call.completed/"},
		"scoped": {"call.ringing/102"},
	} {
		s := map[string]*sseStream{"alice": alice, "bob": bob, "noext": noext, "carol": carol, "dave": dave, "scoped": scoped}[who]
		if got := s.types(); !slices.Equal(got, want) {
			t.Errorf("%s received %v, want %v", who, got, want)
		}
	}
}

// TestEventStreamFrames fails if a sseFrame lacks its cursor, type or JSON
// envelope, or an event type or extension filter lets another event through.
func TestEventStreamFrames(t *testing.T) {
	e := newEvEnv(t)
	s := e.open(t, "/api/v1/events/stream?types=call.ended,call.missed&extension=102", "carol", "", nil)
	e.mem.Append(callEvent(events.CallRinging, "102"))
	e.mem.Append(callEvent(events.CallEnded, "101"))
	want := e.mem.Append(callEvent(events.CallEnded, "102"))
	e.mem.Append(callEvent(events.CallMissed, "102"))
	f := s.nextEvent()
	if f.id != want.Cursor || f.event != events.CallEnded || f.data.ID != want.ID || f.data.APIVersion != "1" || f.data.Data.CallID != "call-102" {
		t.Fatalf("sseFrame = %+v, want cursor %s", f, want.Cursor)
	}
	if f := s.nextEvent(); f.event != events.CallMissed {
		t.Fatalf("second sseFrame = %+v", f)
	}
}

// TestEventStreamResume fails if a reconnect with Last-Event-ID repeats an
// event, skips one, or shows a user an event of an extension that is not
// theirs while replaying.
func TestEventStreamResume(t *testing.T) {
	e := newEvEnv(t)
	first := e.mem.Append(callEvent(events.CallRinging, "101"))
	e.mem.Append(callEvent(events.CallRinging, "102"))
	e.mem.Append(callEvent(events.CallEnded, "101"))

	carol := e.open(t, "/api/v1/events/stream", "carol", "", map[string]string{"Last-Event-ID": first.Cursor})
	if got := carol.nextEvent(); got.event != events.CallRinging || got.data.Data.Extension != "102" {
		t.Fatalf("first replayed = %+v", got)
	}
	if got := carol.nextEvent(); got.event != events.CallEnded {
		t.Fatalf("second replayed = %+v", got)
	}
	live := e.mem.Append(callEvent(events.CallAnswered, "101"))
	if got := carol.nextEvent(); got.id != live.Cursor {
		t.Fatalf("live event after the replay = %+v", got)
	}

	alice := e.open(t, "/api/v1/events/stream?lastEventId="+first.Cursor, "alice", "", nil)
	if got := alice.types(); !slices.Equal(got, []string{"call.ended/101", "call.answered/101"}) {
		t.Fatalf("alice resumed with %v", got)
	}
}

// TestEventStreamKeepalive fails if an idle stream sends nothing.
func TestEventStreamKeepalive(t *testing.T) {
	old := sseKeepalive
	sseKeepalive = 30 * time.Millisecond
	t.Cleanup(func() { sseKeepalive = old })
	e := newEvEnv(t)
	s := e.open(t, "/api/v1/events/stream", "carol", "", nil)
	if f := s.next(); f.comment != "keepalive" {
		t.Fatalf("idle sseFrame = %+v", f)
	}
}

// TestEventStreamFollowsExtensionLinks fails if a user's open stream keeps
// the extensions they had when it opened after an administrator links
// another.
func TestEventStreamFollowsExtensionLinks(t *testing.T) {
	old := eventsAllowedRefresh
	eventsAllowedRefresh = 20 * time.Millisecond
	t.Cleanup(func() { eventsAllowedRefresh = old })
	e := newEvEnv(t)
	alice := e.open(t, "/api/v1/events/stream", "alice", "", nil)
	e.mem.Append(callEvent(events.CallRinging, "102"))
	alice.quiet(100 * time.Millisecond)
	e.f.mu.Lock()
	e.f.links[uAlice] = append(e.f.links[uAlice], extBob)
	e.f.mu.Unlock()
	time.Sleep(100 * time.Millisecond) // a refresh tick
	e.mem.Append(callEvent(events.CallEnded, "102"))
	if f := alice.nextEvent(); f.event != events.CallEnded || f.data.Data.Extension != "102" {
		t.Fatalf("after the link: %+v", f)
	}
}

// TestEventStreamRejects fails if a bad filter or cursor opens a stream, a
// credential without the events scope gets one, or the sixth stream of one
// credential is allowed.
func TestEventStreamRejects(t *testing.T) {
	e := newEvEnv(t)
	for _, path := range []string{
		"/api/v1/events/stream?types=call.nope", "/api/v1/events/stream?types=*", "/api/v1/events/stream?extension=abc",
		"/api/v1/events/stream?lastEventId=zz",
	} {
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", path, "carol", ""))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, resp.StatusCode)
		}
	}
	resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events/stream", "", "tok-read"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only token = %d, want 403", resp.StatusCode)
	}
	resp, err = http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events/stream", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credentials = %d, want 401", resp.StatusCode)
	}

	var open []*sseStream
	for range events.MaxStreamsPerCredential {
		open = append(open, e.open(t, "/api/v1/events/stream", "carol", "", nil))
	}
	over, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events/stream", "carol", ""))
	if err != nil {
		t.Fatal(err)
	}
	_ = over.Body.Close()
	if over.StatusCode != http.StatusTooManyRequests || over.Header.Get("Retry-After") == "" {
		t.Fatalf("sixth stream = %d (Retry-After %q), want 429 with Retry-After", over.StatusCode, over.Header.Get("Retry-After"))
	}
	// Another credential is not affected, and a closed stream frees its slot.
	e.open(t, "/api/v1/events/stream", "dave", "", nil)
	_ = open[0].resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events/stream", "carol", ""))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed stream never freed its slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEventTickets fails if a ticket does not open one stream as the caller
// it was minted for, can be used twice, or opens anything else; or if the
// ticket carries the minting user's limits.
func TestEventTickets(t *testing.T) {
	e := newEvEnv(t)
	mint := func(who string) string {
		t.Helper()
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "POST", "/api/v1/events/tickets", who, ""))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("mint = %d %s", resp.StatusCode, b)
		}
		var out struct {
			Ticket    string
			ExpiresAt time.Time
		}
		if err := json.Unmarshal(b, &out); err != nil || !strings.HasPrefix(out.Ticket, "hello_tkt_") || time.Until(out.ExpiresAt) > 31*time.Second || time.Until(out.ExpiresAt) < 20*time.Second {
			t.Fatalf("ticket = %s (%v)", b, err)
		}
		return out.Ticket
	}
	aliceTicket := mint("alice")
	s := e.open(t, "/api/v1/events/stream?ticket="+url.QueryEscape(aliceTicket), "", "", nil)
	e.mem.Append(callEvent(events.CallRinging, "101"))
	e.mem.Append(callEvent(events.CallRinging, "102"))
	if got := s.types(); !slices.Equal(got, []string{"call.ringing/101"}) {
		t.Fatalf("alice's ticket stream received %v, want only her extension", got)
	}
	for name, path := range map[string]string{
		"reused":  "/api/v1/events/stream?ticket=" + url.QueryEscape(aliceTicket),
		"unknown": "/api/v1/events/stream?ticket=hello_tkt_00",
		"garbage": "/api/v1/events/stream?ticket=abc",
	} {
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", path, "", ""))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s ticket = %d, want 401", name, resp.StatusCode)
		}
	}
	// A ticket opens the stream only: the history route does not take it.
	resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events?ticket="+url.QueryEscape(mint("carol")), "", ""))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a ticket on the history = %d, want 401", resp.StatusCode)
	}
}

// TestEventHistory fails if the history ignores the caller's extensions, a
// filter, the order or the cursor, or reports a next page that is not there.
func TestEventHistory(t *testing.T) {
	e := newEvEnv(t)
	var cur []string
	for _, x := range []struct{ typ, ext string }{
		{events.CallRinging, "101"}, {events.CallRinging, "102"}, {events.CallEnded, "101"}, {events.CallEnded, "102"}, {events.CallCompleted, "101"},
	} {
		cur = append(cur, e.mem.Append(callEvent(x.typ, x.ext)).Cursor)
	}
	get := func(who, bearer, query string) (int, struct {
		Items      []events.Entry
		NextCursor string
	}) {
		t.Helper()
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), "GET", "/api/v1/events"+query, who, bearer))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Items      []events.Entry
			NextCursor string
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	list := func(es []events.Entry) []string {
		var out []string
		for _, x := range es {
			out = append(out, x.Type+"/"+x.Data.Extension)
		}
		return out
	}
	code, page := get("carol", "", "")
	if code != 200 || len(page.Items) != 5 || page.NextCursor != "" || page.Items[0].Cursor != cur[0] {
		t.Fatalf("admin history = %d %+v", code, page)
	}
	if _, page := get("alice", "", ""); !slices.Equal(list(page.Items), []string{"call.ringing/101", "call.ended/101", "call.completed/101"}) {
		t.Fatalf("alice's history = %v", list(page.Items))
	}
	if _, page := get("", "tok-events-102", ""); !slices.Equal(list(page.Items), []string{"call.ringing/102", "call.ended/102"}) {
		t.Fatalf("scoped token's history = %v", list(page.Items))
	}
	if _, page := get("noext", "", ""); len(page.Items) != 0 {
		t.Fatalf("a user without extensions sees %v", list(page.Items))
	}
	_, page = get("carol", "", "?limit=2")
	if len(page.Items) != 2 || page.NextCursor != cur[1] {
		t.Fatalf("first page = %v next %q, want 2 items next %s", list(page.Items), page.NextCursor, cur[1])
	}
	if _, next := get("carol", "", "?limit=2&after="+page.NextCursor); !slices.Equal(list(next.Items), []string{"call.ended/101", "call.ended/102"}) {
		t.Fatalf("second page = %v", list(next.Items))
	}
	if _, page := get("carol", "", "?order=desc&limit=2"); !slices.Equal(list(page.Items), []string{"call.completed/101", "call.ended/102"}) || page.NextCursor != cur[3] {
		t.Fatalf("newest = %v next %q", list(page.Items), page.NextCursor)
	}
	if _, page := get("carol", "", "?order=desc&before="+cur[2]+"&limit=10"); !slices.Equal(list(page.Items), []string{"call.ringing/102", "call.ringing/101"}) {
		t.Fatalf("before = %v", list(page.Items))
	}
	if _, page := get("carol", "", "?types=call.completed,call.missed&extension=101"); !slices.Equal(list(page.Items), []string{"call.completed/101"}) {
		t.Fatalf("filtered = %v", list(page.Items))
	}
	for _, q := range []string{"?limit=0", "?limit=1001", "?limit=x", "?order=sideways", "?after=zz", "?before=1-x", "?types=nope", "?extension=1"} {
		if code, _ := get("carol", "", q); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	if code, _ := get("", "tok-read", ""); code != http.StatusForbidden {
		t.Errorf("history with a read-only token = %d, want 403", code)
	}
}

// TestEventsUnavailable fails if a node without the event pipeline answers
// anything but 503 on the three routes.
func TestEventsUnavailable(t *testing.T) {
	e := newEvEnv(t)
	e.mem.Down = true
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/events/stream"}, {"POST", "/api/v1/events/tickets"}, {"GET", "/api/v1/events/stream?ticket=hello_tkt_00"},
	} {
		resp, err := http.DefaultClient.Do(e.req(t, context.Background(), rt.method, rt.path, "carol", ""))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", rt.method, rt.path, resp.StatusCode)
		}
	}
	// A server with no Events at all, too.
	f, _, _, _ := newAcctFake(t)
	srv := httptest.NewServer(Handler(Config{Store: f, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour}))
	defer srv.Close()
	for _, path := range []string{"/api/v1/events", "/api/v1/events/stream"} {
		r, _ := http.NewRequest("GET", srv.URL+path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "carol" + cookieSuffix})
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s without events = %d, want 503", path, resp.StatusCode)
		}
	}
}
