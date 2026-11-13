package crm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// pfake is a per-user connector (Outlook, Google): contacts and calendar come
// from the user's access token.
type pfake struct {
	mu       sync.Mutex
	contacts map[string]map[string]string // token -> number -> name
	busy     map[string]Busy              // token -> answer
	busyErr  map[string]error
	asked    []string
}

func (f *pfake) Kind() string { return "pfake" }
func (f *pfake) Capabilities() Capabilities {
	return Capabilities{Lookup: true, UserScope: true, Calendar: true}
}
func (f *pfake) Lookup(_ context.Context, c Conn, number string) (*Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, c.AccessToken+":"+number)
	if n, ok := f.contacts[c.AccessToken][number]; ok {
		return &Contact{ID: "c", Name: n}, nil
	}
	return nil, nil
}
func (f *pfake) LogCall(context.Context, Conn, CallLog) (string, error)     { return "", errors.New("no") }
func (f *pfake) UpdateCallLog(context.Context, Conn, string, CallLog) error { return errors.New("no") }
func (f *pfake) ResolveUser(context.Context, Conn, string) (string, error)  { return "", nil }
func (f *pfake) AuthURL(cfg Config, _, state string) string {
	return "https://idp.example/auth?scope=" + strings.Join(strings.Split(cfg.Extra["features"], ","), "+") + "&state=" + state
}
func (f *pfake) Exchange(_ context.Context, _ Config, _, code string) (Tokens, error) {
	if code == "bad" {
		return Tokens{}, &APIError{Status: 400, Msg: "bad"}
	}
	return Tokens{Access: code, Refresh: "r-" + code, ExpiresAt: time.Now().Add(time.Hour), Account: code + "@example.com",
		Scopes: []string{"Contacts.Read", "Calendars.Read"}}, nil
}
func (f *pfake) Refresh(_ context.Context, _ Config, refresh string) (Tokens, error) {
	return Tokens{Access: "new-" + refresh, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (f *pfake) Busy(_ context.Context, c Conn, _ time.Time) (Busy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.busyErr[c.AccessToken]; err != nil {
		return Busy{}, err
	}
	return f.busy[c.AccessToken], nil
}

// dndLog records what the calendar did to DND and models extensions.dnd and
// dnd_source for one user: manual wins.
type dndLog struct {
	mu     sync.Mutex
	dnd    bool
	source string
	calls  []string
}

func (d *dndLog) SetCalendarDND(_ context.Context, _ int64, on bool) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case on && !d.dnd:
		d.dnd, d.source = true, "calendar"
		d.calls = append(d.calls, "on")
		return 1, nil
	case on:
		d.calls = append(d.calls, "on-skipped")
		return 0, nil
	case d.dnd && d.source == "calendar":
		d.dnd, d.source = false, "manual"
		d.calls = append(d.calls, "off")
		return 1, nil
	}
	d.calls = append(d.calls, "off-skipped")
	return 0, nil
}

type prig struct {
	*rig
	pf   *pfake
	conn ConnectorRow
	dnd  *dndLog
	cw   *CalendarWorker
}

func newPRig(t *testing.T) *prig {
	t.Helper()
	r := newRig(t)
	pf := &pfake{contacts: map[string]map[string]string{}, busy: map[string]Busy{}, busyErr: map[string]error{}}
	prov := func(kind string) (Connector, bool) {
		if kind == "pfake" {
			return pf, true
		}
		return r.f, kind == "fake"
	}
	r.tokens.Providers = prov
	r.look.Tokens = r.tokens
	r.look.Invalidate()
	c, err := r.repo.CreateConnector(context.Background(), ConnectorInput{Kind: "pfake", Name: "Outlook", Enabled: true,
		Config: map[string]string{"clientId": "cid", "features": "contacts,calendar"}, LinkTTLHours: 720}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	r.repo.UsersList = []UserRef{{ID: 7, Username: "anna", Email: "anna@example.com", Extension: "201"},
		{ID: 8, Username: "bob", Email: "bob@example.com", Extension: "202"}, {ID: 9, Username: "cy", Email: "cy@example.com", Extension: "203"}}
	d := &dndLog{}
	r.svc.DND = d
	cw := &CalendarWorker{Repo: r.repo, Tokens: r.tokens, DND: d, Now: func() time.Time { return r.now }}
	return &prig{rig: r, pf: pf, conn: c, dnd: d, cw: cw}
}

func (p *prig) signIn(t *testing.T, user int64, code string) {
	t.Helper()
	u := userActor(user, "user:test")
	state, err := p.repo.NewState(context.Background(), p.conn.ID, u, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, personal, err := p.svc.Callback(context.Background(), state, code); err != nil || !personal {
		t.Fatalf("callback: personal=%v err=%v", personal, err)
	}
}

func (p *prig) connectionOf(t *testing.T, user int64) ConnectionRow {
	t.Helper()
	c, _, err := p.repo.UserConnection(context.Background(), p.conn.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPersonalConnectionFlow fails if a user's sign-in is stored anywhere but
// under that user, if the organisation-level sign-in works on a per-user
// connector (or the reverse), or if a disconnect leaves tokens behind.
func TestPersonalConnectionFlow(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	if _, err := p.svc.Connect(ctx, "admin", p.conn.ID); !errors.Is(err, ErrUserScope) {
		t.Fatalf("org connect of a per-user connector: %v", err)
	}
	if _, err := p.svc.ConnectUser(ctx, "anna", 7, p.rig.conn.ID); !errors.Is(err, ErrNotUserScope) {
		t.Fatalf("personal connect of an org connector: %v", err)
	}
	u, err := p.svc.ConnectUser(ctx, "user:anna", 7, p.conn.ID)
	if err != nil || !strings.Contains(u, "scope=contacts+calendar") {
		t.Fatalf("authorize url = %q, %v", u, err)
	}
	// An organisation state cannot finish a personal connector's sign-in.
	orgState, _ := p.repo.NewState(ctx, p.conn.ID, "user:admin", time.Minute)
	if _, _, err := p.svc.Callback(ctx, orgState, "good"); !errors.Is(err, ErrBadState) {
		t.Fatalf("org state on a per-user connector: %v", err)
	}
	p.signIn(t, 7, "anna-tok")
	p.signIn(t, 8, "bob-tok")
	if _, _, err := p.repo.OrgConnection(ctx, p.conn.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a personal sign-in created an organisation connection")
	}
	_, ta, _ := p.repo.UserConnection(ctx, p.conn.ID, 7)
	_, tb, _ := p.repo.UserConnection(ctx, p.conn.ID, 8)
	if ta.Access != "anna-tok" || tb.Access != "bob-tok" {
		t.Fatalf("tokens crossed: %q %q", ta.Access, tb.Access)
	}
	list, err := p.svc.UserConnectors(ctx, 7)
	if err != nil || len(list) != 1 || list[0].Connection == nil || list[0].Connection.ExternalAccount != "anna-tok@example.com" ||
		len(list[0].Features) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if l, _ := p.svc.UserConnectors(ctx, 9); l[0].Connection != nil {
		t.Fatal("cy sees a connection that is not hers")
	}
	if err := p.svc.DisconnectUser(ctx, "user:anna", 7, p.conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.repo.UserConnection(ctx, p.conn.ID, 7); !errors.Is(err, ErrNotFound) {
		t.Fatal("the connection survived the disconnect")
	}
	if _, _, err := p.repo.UserConnection(ctx, p.conn.ID, 8); err != nil {
		t.Fatal("bob lost his connection")
	}
	if err := p.svc.DisconnectUser(ctx, "user:anna", 7, p.conn.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second disconnect: %v", err)
	}
}

// TestCallerNameFromCalleeContacts fails if a caller is named from anyone's
// contacts but the called user's, if the organisation's lookup asks a
// per-user connector, or if the answer is not cached per user.
func TestCallerNameFromCalleeContacts(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	p.signIn(t, 7, "anna-tok")
	p.signIn(t, 8, "bob-tok")
	p.pf.contacts["anna-tok"] = map[string]string{"+3250123456": "Anna's plumber"}
	p.pf.contacts["bob-tok"] = map[string]string{"+3250123456": "Bob's ex"}
	// Both users know the number under different names: the callee decides.
	if n, ok := p.look.NameFor(ctx, "201", "+3250123456", time.Second); !ok || n != "Anna's plumber" {
		t.Fatalf("name for 201 = %q %v", n, ok)
	}
	if n, ok := p.look.NameFor(ctx, "202", "+3250123456", time.Second); !ok || n != "Bob's ex" {
		t.Fatalf("name for 202 = %q %v", n, ok)
	}
	// cy has no connection: nothing, and nothing is asked of any account.
	before := len(p.pf.asked)
	if n, ok := p.look.NameFor(ctx, "203", "+3250123456", time.Second); ok {
		t.Fatalf("cy got %q from someone else's contacts", n)
	}
	if len(p.pf.asked) != before {
		t.Fatalf("an account was queried for a user who is not connected: %v", p.pf.asked[before:])
	}
	// An extension no user owns, and a short number, ask nothing.
	if _, ok := p.look.NameFor(ctx, "299", "+3250123456", time.Second); ok {
		t.Fatal("an unowned extension was named")
	}
	if _, ok := p.look.NameFor(ctx, "201", "12345", time.Second); ok {
		t.Fatal("a short number was looked up")
	}
	// The second call from the same number is answered from the cache.
	before = len(p.pf.asked)
	if n, _ := p.look.NameFor(ctx, "201", "+3250123456", time.Second); n != "Anna's plumber" || len(p.pf.asked) != before {
		t.Fatalf("not cached: %q, %d new queries", n, len(p.pf.asked)-before)
	}
	// The organisation-level lookup never reaches a per-user connector.
	if c := p.look.Find(ctx, "+3250123456", time.Second); c != nil {
		t.Fatalf("org lookup found %+v in a personal address book", c)
	}
}

// TestCalendarDND fails if a busy calendar does not set DND once, if its end
// does not clear it, if a DND the user set by hand is overridden or cleared,
// or if a user who did not opt in is polled.
func TestCalendarDND(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	p.signIn(t, 7, "anna-tok")
	cid := p.connectionOf(t, 7).ID
	p.cw.Once(ctx)
	if len(p.pf.asked)+len(p.dnd.calls) != 0 || p.pf.busy["anna-tok"].Busy {
		t.Fatal("a user who did not opt in was polled")
	}
	if _, err := p.svc.SetCalendarDND(ctx, "user:anna", 7, p.conn.ID, true); err != nil {
		t.Fatal(err)
	}
	// Free: nothing happens.
	p.cw.Once(ctx)
	if len(p.dnd.calls) != 0 {
		t.Fatalf("calls = %v", p.dnd.calls)
	}
	// A meeting starts: DND on, once, however often it is polled.
	end := p.now.Add(time.Hour)
	p.pf.busy["anna-tok"] = Busy{Busy: true, Until: end}
	p.cw.Once(ctx)
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if got := strings.Join(p.dnd.calls, ","); got != "on" {
		t.Fatalf("calls = %s, want one on", got)
	}
	c := p.connectionOf(t, 7)
	if !c.CalendarActive || !c.CalendarBusyUntil.Equal(end) || c.CalendarError != "" {
		t.Fatalf("state = %+v", c)
	}
	// The meeting ends: DND off.
	p.pf.busy["anna-tok"] = Busy{}
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if got := strings.Join(p.dnd.calls, ","); got != "on,off" || p.dnd.dnd {
		t.Fatalf("calls = %s dnd=%v", got, p.dnd.dnd)
	}
	if c := p.connectionOf(t, 7); c.CalendarActive {
		t.Fatalf("still active: %+v", c)
	}

	// The user turned DND on by hand before the meeting: the calendar takes
	// nothing and clears nothing.
	p.dnd.dnd, p.dnd.source, p.dnd.calls = true, "manual", nil
	p.pf.busy["anna-tok"] = Busy{Busy: true, Until: p.now.Add(time.Hour)}
	p.cw.Once(ctx)
	p.pf.busy["anna-tok"] = Busy{}
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if !p.dnd.dnd || p.dnd.source != "manual" {
		t.Fatalf("the manual DND was changed: %+v", p.dnd)
	}
	if c := p.connectionOf(t, 7); c.CalendarActive {
		t.Fatalf("active over a manual DND: %+v", c)
	}
	p.dnd.dnd, p.dnd.source = false, "manual"

	// Turning the setting off mid-meeting clears the calendar's DND at once.
	p.pf.busy["anna-tok"] = Busy{Busy: true, Until: p.now.Add(time.Hour)}
	p.cw.Once(ctx)
	if !p.dnd.dnd || p.dnd.source != "calendar" {
		t.Fatalf("dnd = %+v", p.dnd)
	}
	if _, err := p.svc.SetCalendarDND(ctx, "user:anna", 7, p.conn.ID, false); err != nil {
		t.Fatal(err)
	}
	if p.dnd.dnd {
		t.Fatal("the calendar DND stayed on after the user turned the setting off")
	}
	_ = cid
}

// TestCalendarDNDTwoCalendars fails if the end of one calendar's meeting
// clears DND while the user's other calendar is still busy.
func TestCalendarDNDTwoCalendars(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	c2, err := p.repo.CreateConnector(ctx, ConnectorInput{Kind: "pfake", Name: "Outlook 2", Enabled: true,
		Config: map[string]string{"clientId": "c2", "features": "calendar"}, LinkTTLHours: 720}, "s2")
	if err != nil {
		t.Fatal(err)
	}
	p.signIn(t, 7, "t1")
	state, _ := p.repo.NewState(ctx, c2.ID, userActor(7, "x"), time.Minute)
	if _, _, err := p.svc.Callback(ctx, state, "t2"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{p.conn.ID, c2.ID} {
		c, _, _ := p.repo.UserConnection(ctx, id, 7)
		if err := p.repo.SetCalendarSettings(ctx, c.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	p.pf.busy["t1"] = Busy{Busy: true, Until: p.now.Add(time.Hour)}
	p.pf.busy["t2"] = Busy{Busy: true, Until: p.now.Add(2 * time.Hour)}
	p.cw.Once(ctx)
	p.pf.busy["t1"] = Busy{}
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if !p.dnd.dnd {
		t.Fatalf("DND cleared while a calendar is still busy: %v", p.dnd.calls)
	}
	p.pf.busy["t2"] = Busy{}
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if p.dnd.dnd {
		t.Fatalf("DND stayed on after both calendars were free: %v", p.dnd.calls)
	}
}

// TestCalendarDNDUnreadable fails if a calendar that cannot be read drops DND
// mid-meeting, holds it forever, or hides that the user must sign in again.
func TestCalendarDNDUnreadable(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	p.signIn(t, 7, "anna-tok")
	if _, err := p.svc.SetCalendarDND(ctx, "user:anna", 7, p.conn.ID, true); err != nil {
		t.Fatal(err)
	}
	end := p.now.Add(30 * time.Minute)
	p.pf.busy["anna-tok"] = Busy{Busy: true, Until: end}
	p.cw.Once(ctx)
	// A blip inside the meeting keeps the DND.
	p.pf.busyErr["anna-tok"] = &APIError{Status: 503, Retry: true, Msg: "down"}
	p.now = p.now.Add(2 * time.Minute)
	p.cw.Once(ctx)
	if !p.dnd.dnd {
		t.Fatal("a blip dropped DND mid-meeting")
	}
	if c := p.connectionOf(t, 7); c.CalendarError == "" || !c.CalendarActive {
		t.Fatalf("state = %+v", c)
	}
	// Long past the end plus the grace, still unreadable: cleared.
	p.now = end.Add(calendarGrace + time.Minute)
	p.cw.Once(ctx)
	if p.dnd.dnd {
		t.Fatal("an unreadable calendar held DND past the meeting")
	}
	// A refused refresh token marks the connection and says so.
	p.pf.busyErr["anna-tok"] = nil
	p.pf.busy["anna-tok"] = Busy{}
	if err := p.repo.SetConnectionStatus(ctx, p.connectionOf(t, 7).ID, StatusNeedsReauth, "revoked"); err != nil {
		t.Fatal(err)
	}
	p.cw.Once(ctx)
	if c := p.connectionOf(t, 7); !strings.Contains(c.CalendarError, "sign in") {
		t.Fatalf("state = %+v", c)
	}
}

// TestCalendarDNDNeedsCalendarAccess fails if the setting can be turned on
// for a connection without calendar access or a connector without the
// feature.
func TestCalendarDNDNeedsCalendarAccess(t *testing.T) {
	p := newPRig(t)
	ctx := context.Background()
	if _, err := p.svc.SetCalendarDND(ctx, "u", 7, p.conn.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not connected: %v", err)
	}
	p.signIn(t, 7, "anna-tok")
	if _, err := p.repo.UpdateConnector(ctx, p.conn.ID, ConnectorInput{Kind: "pfake", Name: "Outlook", Enabled: true,
		Config: map[string]string{"clientId": "cid", "features": "contacts"}, LinkTTLHours: 720}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.SetCalendarDND(ctx, "u", 7, p.conn.ID, true); !errors.Is(err, ErrNoCalendar) {
		t.Fatalf("a contacts-only connector took calendar DND: %v", err)
	}
	// Turning it off is always allowed.
	if _, err := p.svc.SetCalendarDND(ctx, "u", 7, p.conn.ID, false); err != nil {
		t.Fatal(err)
	}
}

// TestContactIndex fails if numbers are matched on anything but their digits,
// if two contacts sharing a number name nobody, or if short numbers match.
func TestContactIndex(t *testing.T) {
	x := NewContactIndex()
	x.Add(Contact{ID: "1", Name: "Anna"}, "+32 50 12 34 56", "0032 475 11 22 33")
	x.Add(Contact{ID: "2", Name: "Bob"}, "050/98 76 54")
	x.Add(Contact{ID: "3", Name: "Cy"}, "+32 9 111 22 33")
	x.Add(Contact{ID: "4", Name: "Dee"}, "09 111 22 33")
	x.Add(Contact{ID: "5", Name: "Eve"}, "112", "n/a")
	for _, tc := range []struct{ in, want string }{
		{"+3250123456", "Anna"}, {"050123456", "Anna"}, {"+32475112233", "Anna"}, {"0475 11 22 33", "Anna"},
		{"+3250987654", "Bob"}, {"112", ""}, {"+31612345678", ""}, {"", ""},
		{"+3291112233", ""}, // Cy and Dee share it: ambiguous
	} {
		got := ""
		if c := x.Find(tc.in); c != nil {
			got = c.Name
		}
		if got != tc.want {
			t.Errorf("Find(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIndexCache fails if concurrent lookups load an address book twice, if a
// failed load is cached, or if an index outlives its TTL.
func TestIndexCache(t *testing.T) {
	now := time.Now()
	c := &IndexCache{TTL: time.Minute, Now: func() time.Time { return now }}
	loads := 0
	load := func() (*ContactIndex, error) { loads++; return NewContactIndex(), nil }
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Get("a", load) }()
	}
	wg.Wait()
	if loads != 1 {
		t.Fatalf("%d loads for concurrent callers", loads)
	}
	now = now.Add(2 * time.Minute)
	_, _ = c.Get("a", load)
	if loads != 2 {
		t.Fatalf("loads = %d after the TTL", loads)
	}
	bad := errors.New("down")
	if _, err := c.Get("b", func() (*ContactIndex, error) { return nil, bad }); !errors.Is(err, bad) {
		t.Fatal(err)
	}
	if _, err := c.Get("b", load); err != nil || loads != 3 {
		t.Fatalf("a failed load was cached: %v %d", err, loads)
	}
}

func TestFeatures(t *testing.T) {
	for in, want := range map[string]string{"": "contacts", "calendar": "calendar", " calendar , contacts,calendar": "calendar,contacts"} {
		if got := strings.Join(Features(map[string]string{"features": in}), ","); got != want {
			t.Errorf("Features(%q) = %q, want %q", in, got, want)
		}
	}
	if id, a := parseActor(userActor(12, "user:anna")); id != 12 || a != "user:anna" {
		t.Fatalf("parseActor = %d %q", id, a)
	}
	if id, a := parseActor("user:admin"); id != 0 || a != "user:admin" {
		t.Fatalf("an admin actor parsed as a personal one: %d %q", id, a)
	}
}
