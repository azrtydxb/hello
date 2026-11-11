package crm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// fake is a scriptable connector.
type fake struct {
	mu       sync.Mutex
	caps     Capabilities
	contact  *Contact
	lookupFn func(ctx context.Context) (*Contact, error)
	logs     []CallLog
	updates  []CallLog
	refs     []string
	logErr   []error // consumed one per LogCall
	updErr   error
	owners   map[string]string
	ownerHit atomic.Int32
	lookups  atomic.Int32
	tokens   []string // access tokens LogCall saw
	authFail map[string]bool
	refresh  func(refresh string) (Tokens, error)
	nextRef  int
}

func newFake() *fake {
	return &fake{caps: Capabilities{Lookup: true, LogCalls: true, Recordings: true}, owners: map[string]string{}}
}

func (f *fake) Kind() string               { return "fake" }
func (f *fake) Capabilities() Capabilities { return f.caps }
func (f *fake) Lookup(ctx context.Context, _ Conn, _ string) (*Contact, error) {
	f.lookups.Add(1)
	if f.lookupFn != nil {
		return f.lookupFn(ctx)
	}
	return f.contact, nil
}

func (f *fake) LogCall(_ context.Context, c Conn, l CallLog) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, c.AccessToken)
	if f.authFail[c.AccessToken] {
		return "", ErrAuth
	}
	if len(f.logErr) > 0 {
		e := f.logErr[0]
		f.logErr = f.logErr[1:]
		if e != nil {
			return "", e
		}
	}
	f.nextRef++
	f.logs = append(f.logs, l)
	ref := "ref-" + string(rune('0'+f.nextRef))
	f.refs = append(f.refs, ref)
	return ref, nil
}

func (f *fake) UpdateCallLog(_ context.Context, _ Conn, ref string, l CallLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updErr != nil {
		return f.updErr
	}
	f.updates = append(f.updates, l)
	return nil
}

func (f *fake) ResolveUser(_ context.Context, _ Conn, email string) (string, error) {
	f.ownerHit.Add(1)
	return f.owners[email], nil
}

func (f *fake) AuthURL(cfg Config, redirect, state string) string {
	return "https://crm.example/auth?client=" + cfg.ClientID + "&state=" + state + "&redirect=" + redirect
}

func (f *fake) Exchange(_ context.Context, _ Config, _, code string) (Tokens, error) {
	if code != "good" {
		return Tokens{}, &APIError{Status: 400, Msg: "bad code"}
	}
	return Tokens{Access: "acc1", Refresh: "ref1", ExpiresAt: time.Now().Add(time.Hour), Account: "99", Scopes: []string{"a", "b"}}, nil
}

func (f *fake) Refresh(_ context.Context, _ Config, refresh string) (Tokens, error) {
	if f.refresh != nil {
		return f.refresh(refresh)
	}
	return Tokens{Access: "acc2", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type fakeLinks struct {
	minted  []int64
	revoked []string
	err     error
}

func (l *fakeLinks) Mint(_ context.Context, rec int64, _ string, ttl time.Duration) (string, time.Time, error) {
	if l.err != nil {
		return "", time.Time{}, l.err
	}
	l.minted = append(l.minted, rec)
	return "https://hello.example/r/tok.wav", time.Now().Add(ttl), nil
}

func (l *fakeLinks) RevokeAll(_ context.Context, label string) error {
	l.revoked = append(l.revoked, label)
	return nil
}

type rig struct {
	repo   *Mem
	f      *fake
	links  *fakeLinks
	tokens *TokenManager
	worker *Worker
	hook   *Hook
	look   *Lookup
	svc    *Service
	now    time.Time
	conn   ConnectorRow
	notes  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{repo: NewMem(), f: newFake(), links: &fakeLinks{}, now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	r.repo.Now = func() time.Time { return r.now }
	prov := func(kind string) (Connector, bool) { return r.f, kind == "fake" }
	r.tokens = &TokenManager{Repo: r.repo, Providers: prov, Now: func() time.Time { return r.now },
		Notify: func(_ context.Context, c ConnectorRow, reason string) { r.notes = append(r.notes, c.Name+": "+reason) }}
	r.worker = &Worker{Repo: r.repo, Tokens: r.tokens, Links: r.links, Now: func() time.Time { return r.now }, Rnd: func() float64 { return 0.5 }}
	r.hook = &Hook{Repo: r.repo, Providers: prov}
	r.look = &Lookup{Repo: r.repo, Tokens: r.tokens, Cache: &MemCache{Now: func() time.Time { return r.now }}, Now: func() time.Time { return r.now }}
	r.svc = &Service{Repo: r.repo, Tokens: r.tokens, Lookup: r.look, Links: r.links, PublicURL: "https://hello.example", Now: func() time.Time { return r.now }}
	c, err := r.repo.CreateConnector(context.Background(), ConnectorInput{Kind: "fake", Name: "crm", Enabled: true,
		Config: map[string]string{"clientId": "cid"}, Recordings: true, LinkTTLHours: 720, LogMissed: true}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	r.conn = c
	if _, err := r.repo.SaveOrgConnection(context.Background(), c.ID, Tokens{Access: "acc1", Refresh: "ref1", ExpiresAt: r.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.repo.UsersList = []UserRef{{ID: 7, Username: "anna", Email: "anna@example.com", Extension: "201"}}
	return r
}

func completed(callID, dir, ext string) events.Entry {
	d := events.Data{CallID: callID, Direction: dir, Extension: ext, Disposition: "answered",
		From: events.Party{Number: "+3211223344"}, To: events.Party{Number: "201", Extension: "201"},
		StartedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), AnsweredAt: time.Date(2026, 11, 10, 8, 0, 5, 0, time.UTC),
		EndedAt: time.Date(2026, 11, 10, 8, 1, 5, 0, time.UTC), DurationMs: 65000, CDRID: 5}
	return events.Entry{Cursor: "1-1", Event: events.New(events.CallCompleted, d, "", time.Now())}
}

func (r *rig) feed(t *testing.T, e events.Entry) {
	t.Helper()
	if err := r.hook.Handle(context.Background(), nil, e); err != nil {
		t.Fatal(err)
	}
}

func TestLogsAnAnsweredCallOnceWithOwner(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "owner-9"
	r.f.contact = &Contact{ID: "c1", Name: "Ann"}
	e := completed("call-1", "inbound", "201")
	r.feed(t, e)
	r.feed(t, e) // delivered twice: still one job
	if n := r.worker.Process(context.Background()); n != 1 {
		t.Fatalf("processed %d jobs, want 1", n)
	}
	if len(r.f.logs) != 1 {
		t.Fatalf("logged %d calls, want 1", len(r.f.logs))
	}
	l := r.f.logs[0]
	if l.OwnerID != "owner-9" || l.Contact == nil || l.Contact.ID != "c1" || l.Number != "+3211223344" || l.Outcome != OutcomeAnswered || l.Duration != 60*time.Second {
		t.Fatalf("call = %+v", l)
	}
	row, _ := r.repo.Log(r.conn.ID, "call-1")
	if row.Status != LogLogged || row.ExternalID != "ref-1" {
		t.Fatalf("row = %+v", row)
	}
	// The mapping is remembered: the next call does not ask the CRM again.
	r.feed(t, completed("call-2", "inbound", "201"))
	r.worker.Process(context.Background())
	if r.f.ownerHit.Load() != 1 {
		t.Fatalf("owner lookups = %d, want 1", r.f.ownerHit.Load())
	}
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatalf("a settled job ran again (%d)", n)
	}
}

func TestInternalCallsAndOtherEventsAreNotLogged(t *testing.T) {
	r := newRig(t)
	r.feed(t, completed("c-int", "internal", "201"))
	ring := completed("c-ring", "inbound", "201")
	ring.Type = events.CallRinging
	r.feed(t, ring)
	if _, ok := r.repo.Log(r.conn.ID, "c-int"); ok {
		t.Fatal("an internal call was queued")
	}
	if _, ok := r.repo.Log(r.conn.ID, "c-ring"); ok {
		t.Fatal("a ringing event was queued")
	}
}

func TestUnmappedUserIsSkippedOrUsesFallback(t *testing.T) {
	r := newRig(t)
	r.feed(t, completed("c1", "inbound", "201")) // anna has no CRM user
	r.worker.Process(context.Background())
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogSkipped || len(r.f.logs) != 0 {
		t.Fatalf("row = %+v, logs = %d", row, len(r.f.logs))
	}
	// An administrator sets a fallback owner and retries the skipped call.
	if _, err := r.svc.Update(context.Background(), "admin", r.conn.ID, Settings{FallbackOwner: ptr("owner-0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RetryLog(context.Background(), "admin", row.ID); err != nil {
		t.Fatal(err)
	}
	r.worker.Process(context.Background())
	if len(r.f.logs) != 1 || r.f.logs[0].OwnerID != "owner-0" {
		t.Fatalf("logs = %+v", r.f.logs)
	}
}

func ptr[T any](v T) *T { return &v }

func TestMissedCallIsLoggedAsNoAnswerAndCanBeSwitchedOff(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	d := events.Data{CallID: "m1", Direction: "inbound", Extension: "201", Reason: "no_answer",
		From: events.Party{Number: "+3211223344"}, To: events.Party{Number: "201", Extension: "201"}, DurationMs: 30000}
	r.feed(t, events.Entry{Cursor: "1-2", Event: events.New(events.CallMissed, d, "", time.Now())})
	r.worker.Process(context.Background())
	if len(r.f.logs) != 1 || !r.f.logs[0].Missed || r.f.logs[0].Outcome != OutcomeNoAnswer || r.f.logs[0].Duration != 30*time.Second {
		t.Fatalf("logs = %+v", r.f.logs)
	}
	// The completed event of the same call does not log it a second time.
	c := completed("m1", "inbound", "201")
	c.Data.Disposition = "no_answer"
	r.feed(t, c)
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatalf("the completed event of a missed call queued another job (%d)", n)
	}
	if _, err := r.svc.Update(context.Background(), "a", r.conn.ID, Settings{LogMissed: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	d.CallID = "m2"
	r.feed(t, events.Entry{Cursor: "1-3", Event: events.New(events.CallMissed, d, "", time.Now())})
	if _, ok := r.repo.Log(r.conn.ID, "m2"); ok {
		t.Fatal("a missed call was queued with log_missed off")
	}
}

func TestRetrySchedulePermanentFailureAndDeadLetter(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	r.f.logErr = []error{&APIError{Status: 503, Retry: true, Msg: "down"}}
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogPending || row.Attempts != 1 || !row.NextAttemptAt.Equal(r.now.Add(10*time.Second)) {
		t.Fatalf("after one failure: %+v", row)
	}
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatal("a job ran before its retry time")
	}
	r.now = r.now.Add(11 * time.Second)
	r.worker.Process(context.Background())
	row, _ = r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogLogged {
		t.Fatalf("after retry: %+v", row)
	}

	// A 4xx is final at once and shows in the dead letters.
	r.f.logErr = []error{&APIError{Status: 400, Msg: "bad property"}}
	r.feed(t, completed("c2", "inbound", "201"))
	r.worker.Process(context.Background())
	row, _ = r.repo.Log(r.conn.ID, "c2")
	if row.Status != LogDead || row.LastError == "" {
		t.Fatalf("permanent failure: %+v", row)
	}

	// Eight transient failures end in the dead state, and Retry-After wins
	// over the schedule but is capped at an hour.
	r.f.logErr = nil
	for range MaxAttempts {
		r.f.logErr = append(r.f.logErr, &APIError{Status: 429, Retry: true, After: 5 * time.Hour})
	}
	r.feed(t, completed("c3", "inbound", "201"))
	for i := 0; i < MaxAttempts; i++ {
		r.worker.Process(context.Background())
		r.now = r.now.Add(2 * time.Hour)
	}
	row, _ = r.repo.Log(r.conn.ID, "c3")
	if row.Status != LogDead || row.Attempts != MaxAttempts {
		t.Fatalf("exhausted: %+v", row)
	}
	// Retry from the dead-letter list starts a new series.
	r.f.logErr = nil
	if _, err := r.svc.RetryLog(context.Background(), "a", row.ID); err != nil {
		t.Fatal(err)
	}
	r.worker.Process(context.Background())
	row, _ = r.repo.Log(r.conn.ID, "c3")
	if row.Status != LogLogged {
		t.Fatalf("after manual retry: %+v", row)
	}
	if _, err := r.svc.RetryLog(context.Background(), "a", row.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("retrying a logged job = %v, want ErrConflict", err)
	}
}

func TestRecordingArrivingAfterTheLogPatchesIt(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	if len(r.f.logs) != 1 || r.f.logs[0].RecordingURL != "" {
		t.Fatalf("logs = %+v", r.f.logs)
	}
	rec := events.Entry{Cursor: "1-4", Event: events.New(events.CallRecordingReady, events.Data{CallID: "c1", Direction: "inbound", Extension: "201", RecordingID: 42}, "42", time.Now())}
	r.feed(t, rec)
	r.worker.Process(context.Background())
	if len(r.f.updates) != 1 || r.f.updates[0].RecordingURL == "" {
		t.Fatalf("updates = %+v", r.f.updates)
	}
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.Status != LogLogged || row.LinkExpiresAt == nil || len(r.f.logs) != 1 {
		t.Fatalf("row = %+v, logs = %d", row, len(r.f.logs))
	}
}

func TestRecordingArrivingBeforeTheCompletedEvent(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	rec := events.Entry{Cursor: "1-4", Event: events.New(events.CallRecordingReady, events.Data{CallID: "c1", Direction: "inbound", Extension: "201", RecordingID: 42}, "42", time.Now())}
	r.feed(t, rec)
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatal("a waiting row ran before its call data arrived")
	}
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	if len(r.f.logs) != 1 || r.f.logs[0].RecordingURL == "" {
		t.Fatalf("the first write should carry the link: %+v", r.f.logs)
	}
}

func TestRecordingsOffNeverMintsALink(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	if _, err := r.svc.Update(context.Background(), "a", r.conn.ID, Settings{Recordings: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	rec := events.Entry{Cursor: "1-4", Event: events.New(events.CallRecordingReady, events.Data{CallID: "c1", Direction: "inbound", Extension: "201", RecordingID: 42}, "42", time.Now())}
	r.feed(t, rec)
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	if len(r.links.minted) != 0 || r.f.logs[0].RecordingURL != "" {
		t.Fatalf("a link was minted with recordings off: %v", r.links.minted)
	}
	if len(r.links.revoked) == 0 {
		t.Fatal("switching recordings off should revoke the connector's links")
	}
}

func TestLinkRenewal(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	r.feed(t, events.Entry{Cursor: "1-4", Event: events.New(events.CallRecordingReady, events.Data{CallID: "c1", Direction: "inbound", Extension: "201", RecordingID: 42}, "42", time.Now())})
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	renew := &Renewer{Repo: r.repo, Now: func() time.Time { return r.now }}
	renew.Once(context.Background())
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatal("a link with 30 days left was renewed")
	}
	r.now = r.now.Add(26 * 24 * time.Hour) // 4 days left
	renew.Once(context.Background())
	r.worker.Process(context.Background())
	if len(r.f.updates) != 1 || len(r.links.minted) != 2 {
		t.Fatalf("updates = %d, minted = %v", len(r.f.updates), r.links.minted)
	}
	// A call older than the retention window is left alone.
	r.now = r.now.Add(200 * 24 * time.Hour)
	renew.Once(context.Background())
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatal("a link of an old call was renewed")
	}
}

func TestEngagementHandshakeAvoidsADuplicate(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	adopted, err := r.svc.Engagement(context.Background(), "u", "fake", "c1", "eng-77", nil, false)
	if err != nil || !adopted {
		t.Fatalf("handshake = %v, %v", adopted, err)
	}
	r.feed(t, completed("c1", "outbound", "201"))
	r.worker.Process(context.Background())
	if len(r.f.logs) != 0 || len(r.f.updates) != 1 {
		t.Fatalf("logs = %d, updates = %d: the server created a duplicate", len(r.f.logs), len(r.f.updates))
	}
	row, _ := r.repo.Log(r.conn.ID, "c1")
	if row.ExternalID != "eng-77" || row.Status != LogLogged {
		t.Fatalf("row = %+v", row)
	}
	// A late handshake after the server logged its own keeps the server's.
	r.feed(t, completed("c2", "inbound", "201"))
	r.worker.Process(context.Background())
	adopted, err = r.svc.Engagement(context.Background(), "u", "fake", "c2", "eng-late", nil, false)
	if err != nil || adopted {
		t.Fatalf("late handshake = %v, %v; the server's engagement must win", adopted, err)
	}
}

func TestEngagementOwnershipForTheUserRole(t *testing.T) {
	r := newRig(t)
	r.repo.Events = [][2]string{{"c1", "201"}}
	if _, err := r.svc.Engagement(context.Background(), "u", "fake", "c1", "e", []string{"305"}, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another user's extension = %v, want ErrForbidden", err)
	}
	if _, err := r.svc.Engagement(context.Background(), "u", "fake", "c1", "e", nil, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a user with no extension = %v, want ErrForbidden", err)
	}
	if _, err := r.svc.Engagement(context.Background(), "u", "fake", "c1", "e", []string{"201"}, true); err != nil {
		t.Fatalf("own call: %v", err)
	}
	if _, err := r.svc.Engagement(context.Background(), "u", "nope", "c1", "e", nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such connector = %v, want ErrNotFound", err)
	}
}

func TestRefreshBeforeExpiryAndOnRefusal(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	r.now = r.now.Add(time.Hour - 30*time.Second) // inside the 60 s skew
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(context.Background())
	if r.f.tokens[0] != "acc2" {
		t.Fatalf("the call used %q, want the refreshed token", r.f.tokens[0])
	}
	_, tok, _ := r.repo.OrgConnection(context.Background(), r.conn.ID)
	if tok.Access != "acc2" || tok.Refresh != "ref1" {
		t.Fatalf("stored tokens = %+v (the refresh token must be kept)", tok)
	}

	// The CRM refuses the access token once: refresh and retry, no attempt
	// is spent.
	r.f.authFail = map[string]bool{"acc2": true}
	r.f.refresh = func(string) (Tokens, error) { return Tokens{Access: "acc3", ExpiresAt: r.now.Add(time.Hour)}, nil }
	r.feed(t, completed("c2", "inbound", "201"))
	r.worker.Process(context.Background())
	row, _ := r.repo.Log(r.conn.ID, "c2")
	if row.Status != LogLogged || row.Attempts != 0 {
		t.Fatalf("row = %+v", row)
	}
}

func TestInvalidGrantMarksNeedsReauthOnceAndPausesJobs(t *testing.T) {
	r := newRig(t)
	r.f.owners["anna@example.com"] = "o1"
	r.f.authFail = map[string]bool{"acc1": true}
	r.f.refresh = func(string) (Tokens, error) { return Tokens{}, &APIError{Status: 400, Msg: "invalid_grant"} }
	r.feed(t, completed("c1", "inbound", "201"))
	r.feed(t, completed("c2", "inbound", "201"))
	r.worker.Process(context.Background())
	cr, _, _ := r.repo.OrgConnection(context.Background(), r.conn.ID)
	if cr.Status != StatusNeedsReauth {
		t.Fatalf("status = %q", cr.Status)
	}
	if len(r.notes) != 1 {
		t.Fatalf("notified %d times, want once: %v", len(r.notes), r.notes)
	}
	for _, id := range []string{"c1", "c2"} {
		row, _ := r.repo.Log(r.conn.ID, id)
		if row.Status != LogPending || row.Attempts != 0 {
			t.Fatalf("%s = %+v: a job of a connection that needs re-auth must wait, not burn attempts", id, row)
		}
	}
	if n := r.worker.Process(context.Background()); n != 0 {
		t.Fatalf("%d jobs claimed while the connection needs re-authorisation", n)
	}
	// Connecting again resumes them.
	r.f.authFail = nil
	if _, err := r.repo.SaveOrgConnection(context.Background(), r.conn.ID, Tokens{Access: "acc9", Refresh: "r", ExpiresAt: r.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(10 * time.Minute)
	if n := r.worker.Process(context.Background()); n != 2 {
		t.Fatalf("resumed %d jobs, want 2", n)
	}
}

func TestOAuthFlowThroughTheService(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.svc.Disconnect(ctx, "a", r.conn.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.links.revoked) != 1 || r.links.revoked[0] != LinkLabel(r.conn.ID) {
		t.Fatalf("revoked = %v", r.links.revoked)
	}
	u, err := r.svc.Connect(ctx, "admin", r.conn.ID)
	if err != nil || u == "" {
		t.Fatalf("connect = %q, %v", u, err)
	}
	state := u[len("https://crm.example/auth?client=cid&state="):]
	state = state[:len(state)-len("&redirect=https://hello.example"+CallbackPath)]
	if _, err := r.svc.Callback(ctx, "wrong", "good"); !errors.Is(err, ErrBadState) {
		t.Fatalf("wrong state = %v", err)
	}
	if _, err := r.svc.Callback(ctx, state, "bad"); err == nil {
		t.Fatal("a bad code was accepted")
	}
	if _, err := r.svc.Callback(ctx, state, "good"); !errors.Is(err, ErrBadState) {
		t.Fatalf("a state must be single use, got %v", err)
	}
	u, _ = r.svc.Connect(ctx, "admin", r.conn.ID)
	state = u[len("https://crm.example/auth?client=cid&state="):]
	state = state[:len(state)-len("&redirect=https://hello.example"+CallbackPath)]
	if id, err := r.svc.Callback(ctx, state, "good"); err != nil || id != r.conn.ID {
		t.Fatalf("callback = %d, %v", id, err)
	}
	v, _ := r.svc.Get(ctx, r.conn.ID)
	if v.Connection == nil || v.Connection.Status != StatusConnected || v.Connection.ExternalAccount != "99" || len(v.Connection.GrantedScopes) != 2 {
		t.Fatalf("connection = %+v", v.Connection)
	}
	raw, _ := json.Marshal(v)
	for _, s := range []string{"acc1", "ref1", "secret"} {
		if contains(string(raw), s) {
			t.Fatalf("the connector view leaks %q: %s", s, raw)
		}
	}
	// Without a public URL the flow is off.
	r.svc.PublicURL = ""
	if _, err := r.svc.Connect(ctx, "a", r.conn.ID); !errors.Is(err, ErrNoPublicURL) {
		t.Fatalf("connect without public url = %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestSettingsValidation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	var ve *ValidationError
	if _, err := r.svc.Create(ctx, "a", Settings{Kind: ptr("nope"), Name: ptr("x")}); !errors.As(err, &ve) || ve.Fields["kind"] == "" {
		t.Fatalf("unknown kind = %v", err)
	}
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{Config: map[string]string{"clientId": "other"}}); !errors.As(err, &ve) || ve.Fields["clientSecret"] == "" {
		t.Fatalf("a new client id must require the secret again: %v", err)
	}
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{Config: map[string]string{"clientId": "other"}, ClientSecret: ptr("s2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{LinkTTLHours: ptr(0)}); !errors.As(err, &ve) || ve.Fields["linkTtlHours"] == "" {
		t.Fatalf("ttl 0 = %v", err)
	}
	if _, err := r.svc.Update(ctx, "a", r.conn.ID, Settings{Kind: ptr("other")}); !errors.As(err, &ve) {
		t.Fatalf("kind change = %v", err)
	}
	if _, err := r.svc.Create(ctx, "a", Settings{Kind: ptr("fake"), Name: ptr("crm")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name = %v", err)
	}
}

func TestManualMappingWinsOverEmail(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.f.owners["anna@example.com"] = "from-email"
	if err := r.svc.SetMapping(ctx, "a", r.conn.ID, 7, "manual-1"); err != nil {
		t.Fatal(err)
	}
	r.feed(t, completed("c1", "inbound", "201"))
	r.worker.Process(ctx)
	if r.f.logs[0].OwnerID != "manual-1" || r.f.ownerHit.Load() != 0 {
		t.Fatalf("owner = %q, email lookups = %d", r.f.logs[0].OwnerID, r.f.ownerHit.Load())
	}
	ms, _ := r.svc.Mappings(ctx, r.conn.ID)
	if len(ms) != 1 || ms[0].ExternalUserID != "manual-1" || ms[0].Source != "manual" {
		t.Fatalf("mappings = %+v", ms)
	}
	if err := r.svc.SetMapping(ctx, "a", r.conn.ID, 99, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user = %v", err)
	}
	if err := r.svc.SetMapping(ctx, "a", r.conn.ID, 7, ""); err != nil {
		t.Fatal(err)
	}
	ms, _ = r.svc.Mappings(ctx, r.conn.ID)
	if ms[0].ExternalUserID != "" {
		t.Fatalf("mapping not cleared: %+v", ms)
	}
}

func TestTestEndpointRateLimit(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 6; i++ {
		if _, err := r.svc.Test(context.Background(), "a", r.conn.ID); err != nil {
			t.Fatalf("test %d: %v", i, err)
		}
	}
	if _, err := r.svc.Test(context.Background(), "a", r.conn.ID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("7th test = %v, want ErrRateLimited", err)
	}
	r.now = r.now.Add(time.Minute)
	if _, err := r.svc.Test(context.Background(), "a", r.conn.ID); err != nil {
		t.Fatal(err)
	}
}
