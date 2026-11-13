package directory

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// incRig is the engine over the in-process LDAP server and the in-memory
// store, with a clock the test moves.
type incRig struct {
	t   *testing.T
	f   *fakeLDAP
	m   *Mem
	e   *Engine
	c   Connector
	now time.Time
}

var invA = guidBytes(0xaaaa0001, 2, 3, []byte{4, 5, 6, 7, 8, 9, 10, 11})

func newIncRig(t *testing.T, users int) *incRig {
	t.Helper()
	f := startFakeLDAP(t, true)
	f.addUsers(users)
	for i := range f.Users {
		f.Users[i].Attrs["userPrincipalName"] = strs(fmt.Sprintf("u%02d@corp.test", i))
		f.Users[i].Attrs["displayName"] = strs(fmt.Sprintf("User %02d", i))
		f.Users[i].Attrs["uSNChanged"] = strs(fmt.Sprint(10 + i))
	}
	f.enableAD(100, invA)
	r := &incRig{t: t, f: f, m: NewMem(), now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	r.e = &Engine{Store: r.m, Drivers: map[string]Driver{KindLDAP: ldapDriver}, Replica: "r1", Now: func() time.Time { return r.now },
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, f.addr())
		}}
	pw := "pw"
	c, err := r.e.Create(context.Background(), "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("AD"), Secret: &pw, Enabled: boolPtr(true),
		Config: ldapCfg(f, nil), Options: &Options{AbsenceRuns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	return r
}

// full applies a manual run (a full read) and returns it.
func (r *incRig) full() Run {
	r.t.Helper()
	run, err := r.e.RequestRun(context.Background(), "admin", r.c.ID, false)
	if err != nil {
		r.t.Fatal(err)
	}
	r.e.TickOnce(context.Background())
	run, _ = r.m.GetRun(context.Background(), run.ID)
	if run.Status != StatusSucceeded {
		r.t.Fatalf("manual run: %s %s %s", run.Status, run.ErrorCode, run.ErrorDetail)
	}
	return run
}

// scheduled moves the clock and returns the scheduled run that ticked.
func (r *incRig) scheduled(after time.Duration) Run {
	r.t.Helper()
	r.now = r.now.Add(after)
	before := len(r.m.Runs)
	r.e.TickOnce(context.Background())
	if len(r.m.Runs) != before+1 {
		r.t.Fatalf("expected one scheduled run, got %d new", len(r.m.Runs)-before)
	}
	run, _ := r.m.GetRun(context.Background(), int64(len(r.m.Runs)))
	if run.Kind != RunScheduled || run.Status != StatusSucceeded {
		r.t.Fatalf("scheduled run: %s %s %s %s", run.Kind, run.Status, run.ErrorCode, run.ErrorDetail)
	}
	return run
}

func (r *incRig) lastFilter() string {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.Filters[len(r.f.Filters)-1]
}

func (r *incRig) user(name string) *MemUser {
	for _, u := range r.m.Users {
		if u.Username == name {
			return u
		}
	}
	return nil
}

func (r *incRig) disabled(name string) bool {
	for _, u := range r.m.Users {
		if u.Username == name {
			return !u.Enabled
		}
	}
	r.t.Fatalf("no user %s", name)
	return false
}

func TestFullRunStoresTheHighWaterMark(t *testing.T) {
	r := newIncRig(t, 3)
	run := r.full()
	if run.Incremental || run.FullReason != FullManual {
		t.Fatalf("manual run: incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
	c, _ := r.m.GetConnector(context.Background(), r.c.ID)
	if c.Sync.Mark == nil || c.Sync.Mark.Method != markUSN || c.Sync.Mark.USN != 100 || c.LastFullRunAt == nil {
		t.Fatalf("state = %+v", c.Sync)
	}
	if c.Sync.Mark.Invocation != "aaaa0001-0002-0003-0405-060708090a0b" {
		t.Errorf("invocation = %q", c.Sync.Mark.Invocation)
	}
}

func TestIncrementalPicksUpAChange(t *testing.T) {
	r := newIncRig(t, 3)
	r.full()
	// u01 changes after the mark; the others do not.
	r.f.Users[1].Attrs["displayName"] = strs("Renamed")
	r.f.Users[1].Attrs["uSNChanged"] = strs("101")
	r.f.setUSN(105)

	run := r.scheduled(61 * time.Minute)
	if !run.Incremental || run.FullReason != "" {
		t.Fatalf("incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
	if !strings.Contains(r.lastFilter(), "(uSNChanged>=100)") {
		t.Errorf("filter = %q", r.lastFilter())
	}
	if run.Counts["users"] != 1 {
		t.Errorf("an incremental run read %d users, want 1", run.Counts["users"])
	}
	cs, _ := r.m.ListChanges(context.Background(), run.ID, ChangeFilter{})
	if len(cs) != 1 || cs[0].Action != ActionUpdate || cs[0].Username != "u01@corp.test" {
		t.Fatalf("changes = %+v", cs)
	}
	u := r.user("u01@corp.test")
	if u == nil || u.DisplayName != "Renamed" {
		t.Fatalf("user = %+v", u)
	}
	// The mark moved on.
	c, _ := r.m.GetConnector(context.Background(), r.c.ID)
	if c.Sync.Mark.USN != 105 {
		t.Errorf("mark = %+v", c.Sync.Mark)
	}
	// Nothing changed since: an empty incremental run is normal, not an
	// empty-result halt.
	run = r.scheduled(61 * time.Minute)
	if !run.Incremental || run.Counts["users"] != 0 || run.Status != StatusSucceeded {
		t.Fatalf("idle run: %+v", run)
	}
}

func TestIncrementalNeverDisablesAnAbsentUser(t *testing.T) {
	r := newIncRig(t, 3)
	r.full()
	// u02 vanishes from the directory; absenceRuns is 1, so a full run would
	// disable it at once.
	r.f.Users = r.f.Users[:2]
	r.f.setUSN(110)

	for i := 0; i < 3; i++ {
		run := r.scheduled(61 * time.Minute)
		if !run.Incremental {
			t.Fatalf("run %d was full: %q", i, run.FullReason)
		}
		cs, _ := r.m.ListChanges(context.Background(), run.ID, ChangeFilter{})
		for _, ch := range cs {
			if ch.Action == ActionDisable || ch.Reason == ReasonAbsent || strings.HasPrefix(ch.Reason, ReasonAbsentPending) {
				t.Fatalf("an incremental run acted on an absence: %+v", ch)
			}
		}
		if r.disabled("u02@corp.test") {
			t.Fatalf("run %d disabled an absent user", i)
		}
	}
	for _, u := range r.m.Users {
		if u.AbsentRuns != 0 {
			t.Errorf("%s counted absent by an incremental run", u.Username)
		}
	}
}

func TestFullRunAfter24HoursDisablesTheAbsentUser(t *testing.T) {
	r := newIncRig(t, 3)
	r.full()
	r.f.Users = r.f.Users[:2]
	r.f.setUSN(110)

	run := r.scheduled(2 * time.Hour)
	if !run.Incremental || r.disabled("u02@corp.test") {
		t.Fatal("the run inside 24 hours must stay incremental and not disable")
	}
	run = r.scheduled(23 * time.Hour) // 25 hours after the full run
	if run.Incremental || run.FullReason != FullInterval {
		t.Fatalf("incremental=%v reason=%q, want a full run for %s", run.Incremental, run.FullReason, FullInterval)
	}
	if !r.disabled("u02@corp.test") {
		t.Fatal("the full run did not disable the absent user")
	}
	if strings.Contains(r.lastFilter(), "uSNChanged") {
		t.Errorf("a full run filtered by uSNChanged: %q", r.lastFilter())
	}
	// The 24 hour clock restarted: the next run is incremental again.
	if run = r.scheduled(61 * time.Minute); !run.Incremental {
		t.Fatalf("run after the full run: reason %q", run.FullReason)
	}
}

func TestUSNGoingBackwardsForcesAFullRun(t *testing.T) {
	r := newIncRig(t, 3)
	r.full()
	r.f.Users = r.f.Users[:2]
	// The directory was restored from a backup: its USN is lower than the mark.
	r.f.setUSN(40)

	run := r.scheduled(61 * time.Minute)
	if run.Incremental || run.FullReason != FullUSNRegressed {
		t.Fatalf("incremental=%v reason=%q, want %s", run.Incremental, run.FullReason, FullUSNRegressed)
	}
	if run.Counts["users"] != 2 {
		t.Errorf("a full read returned %d users, want 2", run.Counts["users"])
	}
	if !r.disabled("u02@corp.test") {
		t.Error("the forced full run is the one that detects the absence")
	}
	c, _ := r.m.GetConnector(context.Background(), r.c.ID)
	if c.Sync.Mark.USN != 40 {
		t.Errorf("mark = %+v", c.Sync.Mark)
	}
}

func TestInvocationIDChangeForcesAFullRun(t *testing.T) {
	r := newIncRig(t, 2)
	r.full()
	r.f.enableAD(500, guidBytes(0xbbbb0001, 2, 3, []byte{4, 5, 6, 7, 8, 9, 10, 11}))
	run := r.scheduled(61 * time.Minute)
	if run.Incremental || run.FullReason != FullInvocation {
		t.Fatalf("incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
}

func TestMappingChangeForcesAFullRun(t *testing.T) {
	r := newIncRig(t, 2)
	r.full()
	m := r.c.Mappings
	m.DisplayName = AttrMap{Sources: []string{"cn"}}
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Mappings: &m}); err != nil {
		t.Fatal(err)
	}
	run := r.scheduled(61 * time.Minute)
	if run.Incremental || run.FullReason != FullConfigChanged {
		t.Fatalf("incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
	if run = r.scheduled(61 * time.Minute); !run.Incremental {
		t.Fatalf("the run after the full one: %q", run.FullReason)
	}
}

func TestNoMarkMeansFull(t *testing.T) {
	r := newIncRig(t, 2)
	r.full()
	if err := r.m.SaveSyncState(context.Background(), r.c.ID, SyncState{}); err != nil {
		t.Fatal(err)
	}
	run := r.scheduled(61 * time.Minute)
	if run.Incremental || run.FullReason != FullNoMark {
		t.Fatalf("incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
}

func TestChangedGroupForcesAFullReadButKeepsAbsencesSafe(t *testing.T) {
	r := newIncRig(t, 3)
	gid := "00000001-0002-0003-0405-060708090a0b"
	r.f.Groups = []fakeEntry{{DN: "CN=Sales,OU=Groups,DC=corp", Attrs: map[string][][]byte{
		"objectGUID": {guidBytes(1, 2, 3, []byte{4, 5, 6, 7, 8, 9, 10, 11})}, "cn": strs("sales"), "uSNChanged": strs("20")}}}
	r.f.Users[1].Attrs["memberOf"] = strs("CN=Sales,OU=Groups,DC=corp")
	rules := GroupRules{Roles: []RoleRule{{GroupID: gid, GroupName: "sales", Role: "viewer"}}, RingGroups: []RingGroupRule{}}
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{GroupRules: &rules}); err != nil {
		t.Fatal(err)
	}
	r.full()
	// Membership changed on the group only: the user objects are untouched.
	r.f.Groups[0].Attrs["uSNChanged"] = strs("101")
	r.f.setUSN(105)
	run := r.scheduled(61 * time.Minute)
	if run.Incremental || run.FullReason != FullGroupsChanged {
		t.Fatalf("incremental=%v reason=%q", run.Incremental, run.FullReason)
	}
	if run.Counts["users"] != 3 {
		t.Errorf("users = %d", run.Counts["users"])
	}
}
