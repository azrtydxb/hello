package api

// Tests for recording links and the call and recording lookup (spec
// integrations I-43 to I-45): who may mint and revoke, the lookup's scope and
// extension restrictions, link expiry, revocation and use limits, and the
// public /r/ route. They run through Handler over the in-memory account store
// (internal/store covers the SQL in test/integration).

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

type fakeLink struct {
	store.RecordingLink
	hash string
}

// linkStore adds recordings, recording links and the lookup to the account
// fake, and four API tokens: recordings+calls limited to extension 102,
// recordings+calls unlimited, calls only, and read only.
type linkStore struct {
	*acctFake
	lmu   sync.Mutex
	calls []store.CallMatch
	recs  []store.Recording
	links map[int64]*fakeLink
	next  int64
}

func (s *linkStore) TokenActor(ctx context.Context, h []byte) (auth.Actor, error) {
	tok := func(scopes auth.Scopes, ext []string) (auth.Actor, error) {
		return auth.Actor{UserID: uCarol, Username: "carol", Role: auth.RoleAdmin, Kind: auth.KindPersonalToken, TokenID: 9,
			Scopes: scopes, ExtensionScope: ext}, nil
	}
	switch hex.EncodeToString(h) {
	case hex.EncodeToString(auth.HashToken("tok-rec-102")):
		return tok(auth.Scopes{auth.ScopeRecordings, auth.ScopeCalls}, []string{"102"})
	case hex.EncodeToString(auth.HashToken("tok-rec-all")):
		return tok(auth.Scopes{auth.ScopeRecordings, auth.ScopeCalls}, nil)
	case hex.EncodeToString(auth.HashToken("tok-calls")):
		return tok(auth.Scopes{auth.ScopeCalls}, nil)
	case hex.EncodeToString(auth.HashToken("tok-read")):
		return tok(auth.Scopes{auth.ScopeRead}, nil)
	}
	return s.acctFake.TokenActor(ctx, h)
}

func (s *linkStore) inRange(m store.CallMatch, f store.LookupFilter) bool {
	if f.Restrict && !involves(f.Allowed, []string{m.Source, m.Destination, m.OriginalDestination}) {
		return false
	}
	if f.Extension != "" && !slices.Contains([]string{m.Source, m.Destination, m.OriginalDestination}, f.Extension) {
		return false
	}
	return f.CallID == "" || f.CallID == m.CallID
}

func (s *linkStore) LookupCalls(_ context.Context, f store.LookupFilter, _ int64, _ int) ([]store.CallMatch, string, error) {
	out := []store.CallMatch{}
	for _, m := range s.calls {
		if s.inRange(m, f) && (!f.Recorded || m.RecordingID != nil) {
			out = append(out, m)
		}
	}
	return out, "", nil
}

func (s *linkStore) LookupRecordings(_ context.Context, f store.LookupFilter, _ int64, _ int) ([]store.Recording, string, error) {
	out := []store.Recording{}
	for _, r := range s.recs {
		m := store.CallMatch{CallID: r.CorrelationID, Source: r.Source, Destination: r.Destination}
		if r.CDRID == nil && f.Restrict {
			continue
		}
		if r.CDRID == nil && (f.Extension != "" || f.CallID != "") {
			continue
		}
		if r.CDRID == nil || s.inRange(m, f) {
			out = append(out, r)
		}
	}
	return out, "", nil
}

func (s *linkStore) GetRecording(_ context.Context, id int64) (store.Recording, error) {
	for _, r := range s.recs {
		if r.ID == id {
			return r, nil
		}
	}
	return store.Recording{}, store.ErrNotFound
}

func (s *linkStore) RecordingParties(ctx context.Context, id int64) ([]string, error) {
	r, err := s.GetRecording(ctx, id)
	if err != nil || r.CDRID == nil {
		return nil, err
	}
	return []string{r.Source, r.Destination}, nil
}

func (s *linkStore) CreateRecordingLink(_ context.Context, actor string, rec int64, hash []byte, exp time.Time, maxUses int) (store.RecordingLink, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	s.next++
	l := &fakeLink{hash: hex.EncodeToString(hash), RecordingLink: store.RecordingLink{ID: s.next, RecordingID: rec, ExpiresAt: exp, MaxUses: maxUses, CreatedBy: actor}}
	s.links[l.ID] = l
	s.mu.Lock()
	s.audit = append(s.audit, actor+" create recording_link")
	s.mu.Unlock()
	return l.RecordingLink, nil
}

func (s *linkStore) GetRecordingLink(_ context.Context, id int64) (store.RecordingLink, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if l, ok := s.links[id]; ok {
		return l.RecordingLink, nil
	}
	return store.RecordingLink{}, store.ErrNotFound
}

func (s *linkStore) RevokeRecordingLink(_ context.Context, actor string, id int64) error {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	l, ok := s.links[id]
	if !ok {
		return store.ErrNotFound
	}
	if l.RevokedAt == nil {
		now := time.Now()
		l.RevokedAt = &now
	}
	s.mu.Lock()
	s.audit = append(s.audit, actor+" revoke recording_link")
	s.mu.Unlock()
	return nil
}

// UseRecordingLink mirrors the SQL: a live link counts a use.
func (s *linkStore) UseRecordingLink(_ context.Context, hash []byte) (string, int64, store.LinkResult, error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	for _, l := range s.links {
		if l.hash != hex.EncodeToString(hash) {
			continue
		}
		switch {
		case l.RevokedAt != nil:
			return "", 0, store.LinkRevoked, nil
		case !l.ExpiresAt.After(time.Now()):
			return "", 0, store.LinkExpired, nil
		case l.MaxUses > 0 && l.Uses >= l.MaxUses:
			return "", 0, store.LinkUsedUp, nil
		}
		l.Uses++
		return "rec/obj.wav", l.ID, store.LinkOK, nil
	}
	return "", 0, store.LinkUnknown, nil
}

type linkEnv struct {
	st  *linkStore
	h   http.Handler
	log *bytes.Buffer
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	i64 := func(v int64) *int64 { return &v }
	st := &linkStore{acctFake: f, links: map[int64]*fakeLink{}}
	st.calls = []store.CallMatch{
		{ID: 1, CallID: "c1", Source: "101", Destination: "+3299", Direction: "outbound", RecordingID: i64(1)},
		{ID: 2, CallID: "c2", Source: "102", Destination: "+3298", Direction: "outbound", RecordingID: i64(2)},
		{ID: 3, CallID: "c3", Source: "+3297", Destination: "102", OriginalDestination: "+3200", Direction: "inbound"},
	}
	st.recs = []store.Recording{
		{ID: 1, CorrelationID: "c1", InitiatedBy: "dtmf", CDRID: i64(1), Source: "101", Destination: "+3299"},
		{ID: 2, CorrelationID: "c2", InitiatedBy: "dtmf", CDRID: i64(2), Source: "102", Destination: "+3298"},
		{ID: 3, CorrelationID: "c4", InitiatedBy: "dtmf"}, // its CDR is not written yet
	}
	logs := &bytes.Buffer{}
	h := Handler(Config{Store: st, Objects: &audioFake{}, Log: slog.New(slog.NewTextHandler(logs, nil)), SessionTTL: time.Hour,
		LinkMetrics: NewLinkMetrics(prometheus.NewRegistry(), func() float64 { return 0 })})
	return &linkEnv{st: st, h: h, log: logs}
}

// do sends a request as who (a session user) or with a bearer token.
func (e *linkEnv) do(who, bearer, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host = "pbx.example.com"
	if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

type mintResp struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	MaxUses   int       `json:"maxUses"`
}

func (e *linkEnv) mint(t *testing.T, who, bearer, path string, body any) mintResp {
	t.Helper()
	rec := e.do(who, bearer, "POST", path, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s as %s%s = %d %s", path, who, bearer, rec.Code, rec.Body)
	}
	var m mintResp
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// linkPath is the path of a minted URL.
func linkPath(t *testing.T, url string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(url, "http://pbx.example.com")
	if !ok || !strings.HasPrefix(rest, "/r/") || !strings.HasSuffix(rest, ".wav") {
		t.Fatalf("link %q is not http://pbx.example.com/r/<token>.wav", url)
	}
	return rest
}

// TestRecordingLinkMintOwnership fails if a credential can mint a link for a
// call it was not part of, or one that does not need a scope is admitted.
func TestRecordingLinkMintOwnership(t *testing.T) {
	e := newLinkEnv(t)
	for _, c := range []struct {
		who, bearer string
		rec         string
		want        int
	}{
		{"alice", "", "1", 201}, {"alice", "", "2", 403}, {"alice", "", "3", 403}, // a recording without a CDR is nobody's
		{"bob", "", "1", 403}, {"bob", "", "2", 201},
		{"noext", "", "1", 403},
		{"dave", "", "1", 403}, // a viewer is limited to the extensions linked to them
		{"carol", "", "1", 201}, {"carol", "", "3", 201},
		{"", "tok-rec-102", "1", 403}, {"", "tok-rec-102", "2", 201}, {"", "tok-rec-102", "3", 403},
		{"", "tok-rec-all", "1", 201}, {"", "tok-rec-all", "3", 201},
		{"", "tok-calls", "1", 403}, {"", "tok-read", "1", 403}, // missing scope
		{"alice", "", "99", 404}, {"alice", "", "0", 404},
	} {
		if rec := e.do(c.who, c.bearer, "POST", "/api/v1/recordings/"+c.rec+"/links", nil); rec.Code != c.want {
			t.Errorf("%s%s mints recording %s = %d %s, want %d", c.who, c.bearer, c.rec, rec.Code, rec.Body, c.want)
		}
	}
	if rec := e.do("", "", "POST", "/api/v1/recordings/1/links", nil); rec.Code != 401 {
		t.Errorf("anonymous mint = %d, want 401", rec.Code)
	}
}

// TestRecordingLinkPlayback fails if a minted link does not play without a
// session, lacks its headers, ignores Range, or is audited without the
// minting credential.
func TestRecordingLinkPlayback(t *testing.T) {
	e := newLinkEnv(t)
	m := e.mint(t, "alice", "", "/api/v1/recordings/1/links", nil)
	if d := time.Until(m.ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("default expiry in %v, want about 30 days", d)
	}
	path := linkPath(t, m.URL)
	if got := len(strings.TrimSuffix(strings.TrimPrefix(path, "/r/"), ".wav")); got != 43 {
		t.Errorf("token has %d characters, want 43 (256 bits)", got)
	}
	rec := e.do("", "", "GET", path, nil)
	if rec.Code != 200 || rec.Body.Len() != 100 {
		t.Fatalf("GET link = %d (%d bytes)", rec.Code, rec.Body.Len())
	}
	for k, want := range map[string]string{"Cache-Control": "private, no-store", "Referrer-Policy": "no-referrer", "X-Robots-Tag": "noindex", "Content-Type": "audio/wav"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if rec := e.do("", "", "GET", path, nil, "Range", "bytes=10-19"); rec.Code != 206 || rec.Body.Len() != 10 {
		t.Errorf("Range request = %d (%d bytes), want 206 with 10", rec.Code, rec.Body.Len())
	}
	if !slices.Contains(e.st.audit, "user:alice create recording_link") {
		t.Errorf("audit %v lacks the link creation", e.st.audit)
	}
	for _, a := range e.st.audit {
		if strings.Contains(a, strings.TrimSuffix(strings.TrimPrefix(path, "/r/"), ".wav")) {
			t.Errorf("audit row %q holds the token", a)
		}
	}
	if rec := e.do("", "tok-rec-all", "POST", "/api/v1/recordings/1/links", nil); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	if !slices.Contains(e.st.audit, "token:9 create recording_link") {
		t.Errorf("audit %v lacks the token as actor", e.st.audit)
	}
}

// TestRecordingLinkLifetime fails if an expired, revoked or used-up link still
// plays, or the refusals differ from each other.
func TestRecordingLinkLifetime(t *testing.T) {
	e := newLinkEnv(t)
	expired := e.mint(t, "carol", "", "/api/v1/recordings/1/links", map[string]any{"ttlHours": 1})
	revoked := e.mint(t, "carol", "", "/api/v1/recordings/1/links", nil)
	once := e.mint(t, "carol", "", "/api/v1/recordings/1/links", map[string]any{"maxUses": 1})
	if d := time.Until(expired.ExpiresAt); d > time.Hour+time.Minute || d < 59*time.Minute {
		t.Fatalf("a 1 hour link expires in %v", d)
	}
	for _, m := range []mintResp{expired, revoked, once} {
		if rec := e.do("", "", "GET", linkPath(t, m.URL), nil); rec.Code != 200 {
			t.Fatalf("a fresh link = %d", rec.Code)
		}
	}
	e.st.links[expired.ID].ExpiresAt = time.Now().Add(-time.Second) // an hour passes
	if rec := e.do("carol", "", "DELETE", "/api/v1/recording-links/"+itoa(revoked.ID), nil); rec.Code != 204 {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	var bodies []string
	for name, m := range map[string]mintResp{"expired": expired, "revoked": revoked, "used up": once} {
		rec := e.do("", "", "GET", linkPath(t, m.URL), nil)
		if rec.Code != 404 {
			t.Errorf("%s link = %d, want 404", name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("refusals differ and reveal why: %q", bodies)
	}
	// An unknown token and a malformed one are 404 too.
	if rec := e.do("", "", "GET", "/r/"+strings.Repeat("A", 43)+".wav", nil); rec.Code != 404 {
		t.Errorf("unknown token = %d", rec.Code)
	}
	for _, p := range []string{"/r/short.wav", "/r/" + strings.Repeat("A", 43), "/r/" + strings.Repeat("A", 43) + ".mp3"} {
		if rec := e.do("", "", "GET", p, nil); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestRecordingLinkRevoke fails if a user can revoke a link for a call it was
// not part of.
func TestRecordingLinkRevoke(t *testing.T) {
	e := newLinkEnv(t)
	m := e.mint(t, "alice", "", "/api/v1/recordings/1/links", nil)
	path := "/api/v1/recording-links/" + itoa(m.ID)
	if rec := e.do("bob", "", "DELETE", path, nil); rec.Code != 403 {
		t.Errorf("bob revokes alice's link = %d, want 403", rec.Code)
	}
	if rec := e.do("", "tok-rec-102", "DELETE", path, nil); rec.Code != 403 {
		t.Errorf("a token limited to 102 revokes a link of 101's call = %d, want 403", rec.Code)
	}
	if rec := e.do("", "tok-calls", "DELETE", path, nil); rec.Code != 403 {
		t.Errorf("a token without the recordings scope revokes = %d, want 403", rec.Code)
	}
	if rec := e.do("", "", "GET", linkPath(t, m.URL), nil); rec.Code != 200 {
		t.Fatalf("link must still work: %d", rec.Code)
	}
	if rec := e.do("alice", "", "DELETE", path, nil); rec.Code != 204 {
		t.Errorf("alice revokes = %d, want 204", rec.Code)
	}
	if rec := e.do("alice", "", "DELETE", path, nil); rec.Code != 204 {
		t.Errorf("revoking again = %d, want 204", rec.Code)
	}
	if rec := e.do("", "", "GET", linkPath(t, m.URL), nil); rec.Code != 404 {
		t.Errorf("a revoked link = %d, want 404", rec.Code)
	}
	if rec := e.do("alice", "", "DELETE", "/api/v1/recording-links/999", nil); rec.Code != 404 {
		t.Errorf("revoking a missing link = %d, want 404", rec.Code)
	}
	if !slices.Contains(e.st.audit, "user:alice revoke recording_link") {
		t.Errorf("audit %v lacks the revocation", e.st.audit)
	}
}

// TestRecordingLinkRequestValidation fails if a bad lifetime or use limit is
// accepted.
func TestRecordingLinkRequestValidation(t *testing.T) {
	e := newLinkEnv(t)
	for _, body := range []any{
		map[string]any{"ttlHours": 0}, map[string]any{"ttlHours": 8761}, map[string]any{"ttlHours": -5},
		map[string]any{"maxUses": -1}, map[string]any{"maxUses": 2000000}, map[string]any{"ttl": 5}, map[string]any{"ttlHours": "x"},
	} {
		if rec := e.do("carol", "", "POST", "/api/v1/recordings/1/links", body); rec.Code != 400 {
			t.Errorf("body %v = %d, want 400", body, rec.Code)
		}
	}
	if m := e.mint(t, "carol", "", "/api/v1/recordings/1/links", map[string]any{"ttlHours": 8760, "maxUses": 3}); m.MaxUses != 3 {
		t.Errorf("maxUses = %d", m.MaxUses)
	}
}

// TestRecordingLinkRateLimit fails if one token can be requested more than 30
// times a minute from one address, or the limit leaks across addresses.
func TestRecordingLinkRateLimit(t *testing.T) {
	e := newLinkEnv(t)
	path := linkPath(t, e.mint(t, "carol", "", "/api/v1/recordings/1/links", nil).URL)
	for i := 1; i <= 30; i++ {
		if rec := e.do("", "", "GET", path, nil, "X-Real-IP", "198.51.100.7"); rec.Code != 200 {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	rec := e.do("", "", "GET", path, nil, "X-Real-IP", "198.51.100.7")
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("31st request = %d (Retry-After %q), want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := e.do("", "", "GET", path, nil, "X-Real-IP", "198.51.100.8"); rec.Code != 200 {
		t.Errorf("another address = %d, want 200", rec.Code)
	}
}

func callIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("lookup = %d %s", rec.Code, rec.Body)
	}
	var page struct {
		Items []struct {
			CallID        string `json:"callId"`
			CorrelationID string `json:"correlationId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range page.Items {
		ids = append(ids, i.CallID+i.CorrelationID)
	}
	return ids
}

// TestLookupRestrictions fails if the lookup returns a call or recording the
// credential may not see, or names an extension that is not its own.
func TestLookupRestrictions(t *testing.T) {
	e := newLinkEnv(t)
	for _, c := range []struct {
		who, bearer, path string
		want              []string
	}{
		{"alice", "", "/api/v1/lookup/calls", []string{"c1"}},
		{"bob", "", "/api/v1/lookup/calls", []string{"c2", "c3"}},
		{"noext", "", "/api/v1/lookup/calls", []string{}},
		{"carol", "", "/api/v1/lookup/calls", []string{"c1", "c2", "c3"}},
		{"", "tok-calls", "/api/v1/lookup/calls", []string{"c1", "c2", "c3"}},
		{"", "tok-rec-102", "/api/v1/lookup/calls", []string{"c2", "c3"}},
		{"", "tok-rec-102", "/api/v1/lookup/calls?extension=102", []string{"c2", "c3"}},
		{"", "tok-rec-all", "/api/v1/lookup/calls?callId=c2", []string{"c2"}},
		{"", "tok-rec-102", "/api/v1/lookup/calls?callId=c1", []string{}},
		{"", "tok-rec-all", "/api/v1/lookup/calls?recorded=true", []string{"c1", "c2"}},
		{"alice", "", "/api/v1/lookup/recordings", []string{"c1"}},
		{"bob", "", "/api/v1/lookup/recordings", []string{"c2"}},
		{"carol", "", "/api/v1/lookup/recordings", []string{"c1", "c2", "c4"}},
		{"", "tok-rec-all", "/api/v1/lookup/recordings?callId=c1", []string{"c1"}},
		{"", "tok-rec-102", "/api/v1/lookup/recordings", []string{"c2"}},
	} {
		if got := callIDs(t, e.do(c.who, c.bearer, "GET", c.path, nil)); !slices.Equal(got, c.want) {
			t.Errorf("%s%s GET %s = %v, want %v", c.who, c.bearer, c.path, got, c.want)
		}
	}
	for _, c := range []struct{ who, bearer, path string }{
		{"alice", "", "/api/v1/lookup/calls?extension=102"},
		{"alice", "", "/api/v1/lookup/recordings?extension=102"},
		{"", "tok-rec-102", "/api/v1/lookup/calls?extension=101"},
		{"", "tok-rec-102", "/api/v1/lookup/recordings?extension=101"},
	} {
		if rec := e.do(c.who, c.bearer, "GET", c.path, nil); rec.Code != 403 {
			t.Errorf("%s%s GET %s = %d, want 403", c.who, c.bearer, c.path, rec.Code)
		}
	}
}

// TestLookupScopes fails if a token with the wrong scope reaches a lookup.
func TestLookupScopes(t *testing.T) {
	e := newLinkEnv(t)
	for _, c := range []struct {
		bearer, path string
		want         int
	}{
		{"tok-calls", "/api/v1/lookup/calls", 200}, {"tok-calls", "/api/v1/lookup/recordings", 403},
		{"tok-read", "/api/v1/lookup/calls", 403}, {"tok-read", "/api/v1/lookup/recordings", 403},
		{"tok-rec-all", "/api/v1/lookup/recordings", 200},
	} {
		if rec := e.do("", c.bearer, "GET", c.path, nil); rec.Code != c.want {
			t.Errorf("%s GET %s = %d, want %d", c.bearer, c.path, rec.Code, c.want)
		}
	}
	if rec := e.do("", "", "GET", "/api/v1/lookup/calls", nil); rec.Code != 401 {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}
}

// TestLookupValidation fails if a malformed filter is accepted.
func TestLookupValidation(t *testing.T) {
	e := newLinkEnv(t)
	for _, q := range []string{
		"number=abc", "number=" + strings.Repeat("1", 40), "extension=1", "extension=abc", "from=yesterday", "to=2026-13-01T00:00:00Z",
		"from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z", "recorded=maybe", "before=0", "before=x", "limit=0", "limit=201", "callId=" + strings.Repeat("x", 300),
	} {
		for _, p := range []string{"calls", "recordings"} {
			if rec := e.do("carol", "", "GET", "/api/v1/lookup/"+p+"?"+q, nil); rec.Code != 400 {
				t.Errorf("GET lookup/%s?%.40s = %d, want 400", p, q, rec.Code)
			}
		}
	}
	ok := "number=%2B32%20(11)%20111-22&from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&limit=5&before=10&extension=101"
	for _, p := range []string{"calls", "recordings"} {
		if rec := e.do("carol", "", "GET", "/api/v1/lookup/"+p+"?"+ok, nil); rec.Code != 200 {
			t.Errorf("GET lookup/%s with valid filters = %d %s", p, rec.Code, rec.Body)
		}
	}
}
