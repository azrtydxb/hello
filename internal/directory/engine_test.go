package directory

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeDriver struct {
	res *Result
	err error
	// reads counts Fetch calls.
	reads int
}

func (f *fakeDriver) Validate(cfg json.RawMessage, _ ValidateOptions) (json.RawMessage, error) {
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	if strings.Contains(string(cfg), "bad") {
		return nil, fieldErr("config.url", "bad")
	}
	return cfg, nil
}
func (f *fakeDriver) Identity(cfg json.RawMessage) string { return string(cfg) }
func (f *fakeDriver) DefaultMappings() Mappings           { return testMappings() }
func (f *fakeDriver) Fetch(context.Context, json.RawMessage, string, FetchOptions) (*Result, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}
func (f *fakeDriver) Test(context.Context, json.RawMessage, string, FetchOptions) (*TestResult, error) {
	return &TestResult{OK: true, Sample: []SampleRow{{ExternalID: "g1", Raw: map[string][]string{"mail": {"A@X.org"}}}}}, f.err
}

func testMappings() Mappings {
	return Mappings{
		Username:    AttrMap{Sources: []string{"userPrincipalName"}},
		Email:       AttrMap{Sources: []string{"mail"}},
		DisplayName: AttrMap{Sources: []string{"displayName"}},
		Extension:   AttrMap{Sources: []string{"ipPhone", "telephoneNumber"}, Transforms: []Transform{{Kind: TransformDigits}, {Kind: TransformLast, N: 4}}},
		Active:      ActiveMap{Attribute: "userAccountControl", Mode: ActiveUAC},
	}
}

func su(id, upn, mail, name, ext string, groups ...string) SourceUser {
	a := map[string][]string{"userprincipalname": {upn}, "mail": {mail}, "displayname": {name}}
	if ext != "" {
		a["ipphone"] = []string{ext}
	}
	return SourceUser{ExternalID: id, Attrs: a, Groups: groups}
}

type rig struct {
	t   *testing.T
	m   *Mem
	d   *fakeDriver
	e   *Engine
	c   Connector
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, m: NewMem(), d: &fakeDriver{res: &Result{}}, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	r.e = &Engine{Store: r.m, Drivers: map[string]Driver{KindLDAP: r.d}, Replica: "r1", Now: func() time.Time { return r.now }}
	pw := "s3cret-bind-pw"
	c, err := r.e.Create(context.Background(), "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("AD"), Secret: &pw, Enabled: boolPtr(true),
		Config: json.RawMessage(`{"url":"ldaps://dc"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	return r
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// preview runs a preview and returns it.
func (r *rig) preview() Run {
	r.t.Helper()
	run, err := r.e.RequestRun(context.Background(), "admin", r.c.ID, true)
	if err != nil {
		r.t.Fatal(err)
	}
	r.e.TickOnce(context.Background())
	run, err = r.m.GetRun(context.Background(), run.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return run
}

func (r *rig) apply() Run {
	r.t.Helper()
	run, err := r.e.RequestRun(context.Background(), "admin", r.c.ID, false)
	if err != nil {
		r.t.Fatal(err)
	}
	r.e.TickOnce(context.Background())
	run, _ = r.m.GetRun(context.Background(), run.ID)
	return run
}

func (r *rig) changes(run Run) map[string]Change {
	r.t.Helper()
	cs, err := r.m.ListChanges(context.Background(), run.ID, ChangeFilter{})
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]Change{}
	for _, c := range cs {
		out[c.ExternalID] = c
	}
	return out
}

func (r *rig) user(name string) *MemUser {
	for _, u := range r.m.Users {
		if u.Username == name {
			return u
		}
	}
	return nil
}

func TestCreateLinkAndUpdate(t *testing.T) {
	r := newRig(t)
	r.m.AddExt("2001", false)
	r.d.res = &Result{Users: []SourceUser{su("g1", "ann@corp", "ann@corp.org", "Ann A", "+41 21 555 2001"), su("g2", "bob@corp", "", "Bob B", "")}}
	run := r.apply()
	if run.Status != StatusSucceeded {
		t.Fatalf("run = %+v", run)
	}
	ann := r.user("ann@corp")
	if ann == nil || ann.ConnectorID != r.c.ID || ann.ExternalID != "g1" || ann.Role != "user" || ann.DisplayName != "Ann A" || ann.OwnedExtension == 0 {
		t.Fatalf("ann = %+v", ann)
	}
	if bob := r.user("bob@corp"); bob == nil || bob.OwnedExtension != 0 {
		t.Fatalf("bob = %+v", bob)
	}
	if !strings.Contains(strings.Join(ann.Managed, ","), "extension") {
		t.Fatalf("managed = %v", ann.Managed)
	}
	// A change in the directory is an update with field diffs.
	r.d.res.Users[0] = su("g1", "ann@corp", "ann@corp.org", "Ann Z", "2001")
	pre := r.preview()
	ch := r.changes(pre)["g1"]
	if ch.Action != ActionUpdate || ch.FieldDiffs["displayName"] != (Diff{"Ann A", "Ann Z"}) {
		t.Fatalf("change = %+v", ch)
	}
	if r.user("ann@corp").DisplayName != "Ann A" {
		t.Fatal("a preview changed a user")
	}
	if _, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{}); err != nil {
		t.Fatal(err)
	}
	if r.user("ann@corp").DisplayName != "Ann Z" {
		t.Fatalf("not applied: %+v", r.user("ann@corp"))
	}
	if got := strings.Join(r.m.Audits, "\n"); !strings.Contains(got, "directory:AD create user") || !strings.Contains(got, "admin sync-apply directory_connector") {
		t.Fatalf("audits:\n%s", got)
	}
}

func TestConflicts(t *testing.T) {
	r := newRig(t)
	r.m.AddExt("2001", false)
	r.m.AddExt("2002", false)
	r.m.AddExt("2003", true)
	r.m.AddUser(LocalUser{Username: "holder", Enabled: true, ExtensionIDs: []int64{r.m.Exts["2002"].ID}})
	r.m.AddUser(LocalUser{Username: "taken", Email: "dup@x.org", Enabled: true, Role: "viewer"})
	r.m.AddUser(LocalUser{Username: "claimed", Enabled: true, ConnectorID: 99, ExternalID: "zz"})
	r.d.res = &Result{Users: []SourceUser{
		su("a-invalid", "u1", "", "N", "12"),              // 2 digits is valid! use a letter value below
		su("b-dup1", "u2", "", "N", "2001"),               // duplicate with next
		su("b-dup2", "u3", "", "N", "2001"),               // duplicate
		su("c-linked", "u4", "", "N", "2002"),             // linked to holder
		su("d-door", "u5", "", "N", "2003"),               // door
		su("e-missing", "u6", "", "N", "2999"),            // no such extension
		su("f-email", "u7", "dup@x.org", "N", ""),         // adopts "taken" by email
		su("g-claimed", "claimed", "", "N", ""),           // claimed by another connector
		su("h-badname", "no spaces allowed", "", "N", ""), // username invalid
		su("i-samemail", "u9", "same@x.org", "N", ""),     // ok
		su("j-samemail", "u10", "SAME@x.org", "N", ""),    // email collision within the run
	}}
	// A value too long to be an extension is invalid; the transforms are off
	// so it reaches the check.
	r.d.res.Users[0].Attrs["ipphone"] = []string{"123456789012345"}
	r.c.Mappings.Extension.Transforms = nil
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Mappings: &r.c.Mappings}); err != nil {
		t.Fatal(err)
	}
	pre := r.preview()
	got := map[string]string{}
	for id, c := range r.changes(pre) {
		got[id] = c.Action + ":" + c.Reason
	}
	want := map[string]string{
		"a-invalid":  "conflict:extension_invalid",
		"b-dup1":     "conflict:duplicate_extension",
		"b-dup2":     "conflict:duplicate_extension",
		"c-linked":   "conflict:extension_linked",
		"d-door":     "conflict:extension_kind",
		"e-missing":  "conflict:extension_missing",
		"f-email":    "adopt:adopt_not_confirmed",
		"g-claimed":  "conflict:claimed_by_other_connector",
		"h-badname":  "conflict:username_invalid",
		"i-samemail": "create:",
		"j-samemail": "conflict:email_in_use",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if pre.Counts["conflicts"] != 9 {
		t.Errorf("conflicts = %d, counts %v", pre.Counts["conflicts"], pre.Counts)
	}
	if !r.changes(pre)["c-linked"].Overridable {
		t.Error("extension_linked is not marked overridable")
	}
	// Applying changes only what is not in conflict, and not the unconfirmed adoption.
	run, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("apply = %+v, %v", run, err)
	}
	if r.user("u9") == nil || r.user("u10") != nil || r.user("u1") != nil || r.user("u4") != nil {
		t.Fatal("conflicted users were created")
	}
	if r.user("taken").ConnectorID != 0 {
		t.Fatal("adoption applied without confirmation")
	}
}

func TestAdoptNeedsConfirmationAndNeverLocalAdmins(t *testing.T) {
	r := newRig(t)
	r.m.AddUser(LocalUser{Username: "old", Email: "old@x.org", Enabled: true, Role: "operator", DisplayName: "Old"})
	r.m.AddUser(LocalUser{Username: "root", Email: "root@x.org", Enabled: true, Role: "admin"})
	r.d.res = &Result{Users: []SourceUser{su("o", "old@corp", "old@x.org", "Old New", ""), su("r", "root@corp", "root@x.org", "Root", "")}}
	pre := r.preview()
	cs := r.changes(pre)
	if cs["o"].Action != ActionAdopt || cs["r"].Action != ActionSkip || cs["r"].Reason != ReasonLocalAdmin {
		t.Fatalf("changes = %+v", cs)
	}
	run, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{Adopt: []string{"o", "r"}})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("apply = %+v %v", run, err)
	}
	old := r.user("old@corp")
	if old == nil || old.ConnectorID != r.c.ID || old.Role != "operator" || old.DisplayName != "Old New" {
		t.Fatalf("old = %+v", old)
	}
	if root := r.user("root"); root == nil || root.ConnectorID != 0 || root.Role != "admin" {
		t.Fatalf("root = %+v", root)
	}
}

func TestDeprovisionAbsenceAndReappear(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	r.apply()
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	// First absence: counted, not disabled.
	first := r.apply()
	b := r.user("b@c")
	if !b.Enabled || b.AbsentRuns != 1 {
		t.Fatalf("after one run: %+v", b.LocalUser)
	}
	if c := r.changes(first)["b"]; c.Action != ActionSkip || !strings.HasPrefix(c.Reason, ReasonAbsentPending) {
		t.Fatalf("row = %+v", c)
	}
	// A preview does not count.
	r.preview()
	if r.user("b@c").AbsentRuns != 1 {
		t.Fatal("a preview counted an absence")
	}
	r.apply()
	b = r.user("b@c")
	if b.Enabled || !b.DisabledByDirectory || b.Devices {
		t.Fatalf("after two runs: %+v devices=%v", b.LocalUser, b.Devices)
	}
	if _, ok := r.m.Users[b.ID]; !ok {
		t.Fatal("user was deleted")
	}
	// It comes back: re-enabled with its devices.
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	r.apply()
	b = r.user("b@c")
	if !b.Enabled || b.AbsentRuns != 0 || !b.Devices {
		t.Fatalf("after return: %+v", b.LocalUser)
	}
}

func TestInactiveDisablesAtOnceButNotUnknownUsers(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.apply()
	off := su("a", "a@c", "", "A", "")
	off.Attrs["useraccountcontrol"] = []string{"514"}
	ghost := su("g", "g@c", "", "G", "")
	ghost.Attrs["useraccountcontrol"] = []string{"514"}
	r.d.res = &Result{Users: []SourceUser{off, ghost}}
	run := r.apply()
	if r.user("a@c").Enabled {
		t.Fatal("inactive user still enabled")
	}
	if r.user("g@c") != nil {
		t.Fatal("an inactive unknown user was created")
	}
	if c := r.changes(run)["a"]; c.Action != ActionDisable || c.Reason != ReasonInactive {
		t.Fatalf("row = %+v", c)
	}
}

func TestMassDeprovisionGuardAndConfirm(t *testing.T) {
	r := newRig(t)
	var users []SourceUser
	for i := 0; i < 20; i++ {
		users = append(users, su("u"+strconv.Itoa(i), "u"+strconv.Itoa(i)+"@c", "", "U", ""))
	}
	r.d.res = &Result{Users: users}
	r.apply()
	// Ten users disappear for the second time: more than 20 percent.
	r.d.res = &Result{Users: users[10:]}
	r.apply()
	halted := r.apply()
	if halted.Status != StatusNeedsConfirmation || halted.ErrorCode != "needs_confirmation" {
		t.Fatalf("run = %+v", halted)
	}
	for _, u := range r.m.Users {
		if !u.Enabled {
			t.Fatalf("%s disabled by a halted run", u.Username)
		}
	}
	run, err := r.e.Confirm(context.Background(), "admin", halted.ID, Decisions{})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("confirm = %+v, %v", run, err)
	}
	n := 0
	for _, u := range r.m.Users {
		if !u.Enabled {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("disabled = %d", n)
	}
	if _, err := r.e.Confirm(context.Background(), "admin", halted.ID, Decisions{}); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("second confirm = %v", err)
	}
}

func TestEmptyResultNeedsConfirmation(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.apply()
	r.d.res = &Result{}
	run := r.apply()
	if run.Status != StatusNeedsConfirmation || run.ErrorCode != CodeEmptyResult {
		t.Fatalf("run = %+v", run)
	}
	if !r.user("a@c").Enabled || r.user("a@c").AbsentRuns != 0 {
		t.Fatal("an empty result changed users")
	}
}

func TestStalePreview(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	pre := r.preview()
	// The source changes after the preview.
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	fresh, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
	if fresh.Status != StatusPreview || fresh.ID == pre.ID || fresh.Counts["create"] != 2 {
		t.Fatalf("fresh = %+v", fresh)
	}
	if old, _ := r.m.GetRun(context.Background(), pre.ID); old.Status != StatusStale {
		t.Fatalf("old = %+v", old)
	}
	if len(r.m.Users) != 0 {
		t.Fatal("a stale preview applied")
	}
	// The fresh one applies.
	if _, err := r.e.Apply(context.Background(), "admin", fresh.ID, Decisions{}); err != nil || len(r.m.Users) != 2 {
		t.Fatalf("apply fresh: %v, users %d", err, len(r.m.Users))
	}
	// Age makes a preview stale too.
	pre = r.preview()
	r.now = r.now.Add(16 * time.Minute)
	if _, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{}); !errors.Is(err, ErrStale) {
		t.Fatalf("old preview: %v", err)
	}
}

func TestApplyRejectsWrongStateAndBusy(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	done := r.apply()
	if _, err := r.e.Apply(context.Background(), "admin", done.ID, Decisions{}); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("apply of an applied run: %v", err)
	}
	pre := r.preview()
	release := r.m.Hold(lockKey(r.c.ID))
	if _, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("apply while another replica runs: %v", err)
	}
	release()
	if _, err := r.e.Apply(context.Background(), "admin", 999, Decisions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
}

func TestOnlyTheLockHolderRuns(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	run, _ := r.e.RequestRun(context.Background(), "admin", r.c.ID, true)
	release := r.m.Hold(lockKey(r.c.ID))
	r.e.TickOnce(context.Background())
	if got, _ := r.m.GetRun(context.Background(), run.ID); got.Status != StatusQueued || r.d.reads != 0 {
		t.Fatalf("a replica without the lock ran: %+v reads %d", got, r.d.reads)
	}
	release()
	r.e.TickOnce(context.Background())
	if got, _ := r.m.GetRun(context.Background(), run.ID); got.Status != StatusPreview {
		t.Fatalf("run = %+v", got)
	}
}

func TestOrphanedRunClosed(t *testing.T) {
	r := newRig(t)
	run, _ := r.m.CreateRun(context.Background(), Run{ConnectorID: r.c.ID, Kind: RunScheduled, Status: StatusRunning, Counts: map[string]int{}})
	r.e.TickOnce(context.Background())
	got, _ := r.m.GetRun(context.Background(), run.ID)
	if got.Status != StatusFailed || got.ErrorCode != CodeReplicaLost {
		t.Fatalf("run = %+v", got)
	}
}

func TestScheduleStartsOnlyAfterAManualApply(t *testing.T) {
	r := newRig(t)
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.e.TickOnce(context.Background())
	if len(r.m.Runs) != 0 || len(r.m.Users) != 0 {
		t.Fatal("an unreviewed connector ran on its schedule")
	}
	r.apply()
	r.now = r.now.Add(10 * time.Minute)
	r.e.TickOnce(context.Background())
	sched := 0
	for _, run := range r.m.Runs {
		if run.Kind == RunScheduled {
			sched++
		}
	}
	if sched != 1 {
		t.Fatalf("scheduled runs = %d", sched)
	}
	// Not again before the interval.
	r.e.TickOnce(context.Background())
	sched = 0
	for _, run := range r.m.Runs {
		if run.Kind == RunScheduled {
			sched++
		}
	}
	if sched != 1 {
		t.Fatalf("scheduled runs after a second tick = %d", sched)
	}
}

func TestFailedRunsAreClassifiedAndScrubbed(t *testing.T) {
	r := newRig(t)
	notified := make(chan string, 4)
	r.e.Notify = func(_ context.Context, _ Connector, code string) { notified <- code }
	r.d.err = errors.New("ldap bind as cn=svc failed with password s3cret-bind-pw: Invalid Credentials")
	for i := 0; i < 3; i++ {
		run := r.apply()
		if run.Status != StatusFailed || run.ErrorCode != CodeInternal || strings.Contains(run.ErrorDetail, "s3cret") {
			t.Fatalf("run = %+v", run)
		}
	}
	select {
	case code := <-notified:
		if code != CodeInternal {
			t.Fatalf("notified code = %s", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the administrators were not told after three failures")
	}
	if len(notified) != 0 {
		t.Fatalf("told more than once: %d", len(notified))
	}
	c, _ := r.m.GetConnector(context.Background(), r.c.ID)
	if c.Status != "failing" || c.ConsecutiveFailure != 3 {
		t.Fatalf("connector = %+v", c)
	}
	r.d.err = &Error{Code: CodeAuthFailed, Detail: "the server refused the bind"}
	if run := r.apply(); run.ErrorCode != CodeAuthFailed {
		t.Fatalf("run = %+v", run)
	}
	r.d.err = nil
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.apply()
	if c, _ := r.m.GetConnector(context.Background(), r.c.ID); c.Status != "ok" || c.ConsecutiveFailure != 0 {
		t.Fatalf("connector = %+v", c)
	}
}

func TestUserCap(t *testing.T) {
	r := newRig(t)
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Options: &Options{UserCap: 2}}); err != nil {
		t.Fatal(err)
	}
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", ""), su("c", "c@c", "", "C", "")}}
	run := r.apply()
	if run.Status != StatusFailed || run.ErrorCode != CodeUserCap || len(r.m.Users) != 0 {
		t.Fatalf("run = %+v", run)
	}
}

func TestRolesRingGroupsAndAdminGuard(t *testing.T) {
	r := newRig(t)
	ext := r.m.AddExt("2001", false)
	r.m.AddRing(7)
	r.m.AddRing(8)
	r.m.Rings[8][ext] = 0 // a manual member
	rules := GroupRules{
		Roles:      []RoleRule{{GroupID: "g-op", Role: "operator"}, {GroupID: "g-admin", Role: "admin"}, {GroupID: "g-view", Role: "viewer"}},
		RingGroups: []RingGroupRule{{GroupID: "g-sales", RingGroupID: 7}, {GroupID: "g-sales", RingGroupID: 8}},
	}
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{GroupRules: &rules}); err != nil {
		t.Fatal(err)
	}
	r.d.res = &Result{Users: []SourceUser{
		su("a", "a@c", "", "A", "2001", "g-view", "g-op", "g-sales"),
		su("b", "b@c", "", "B", "", "g-admin"),
	}}
	r.apply()
	a, b := r.user("a@c"), r.user("b@c")
	if a.Role != "operator" {
		t.Errorf("a role = %q", a.Role)
	}
	if b.Role != "user" {
		t.Errorf("admin from a directory group without the switch: role = %q", b.Role)
	}
	if r.m.Rings[7][ext] != r.c.ID || r.m.Rings[8][ext] != 0 {
		t.Errorf("rings = %v", r.m.Rings)
	}
	// The user leaves the sales group: the synced row goes, the manual one stays.
	r.d.res.Users[0] = su("a", "a@c", "", "A", "2001", "g-op")
	r.apply()
	if _, in7 := r.m.Rings[7][ext]; in7 {
		t.Error("synced ring-group row stayed")
	}
	if _, in8 := r.m.Rings[8][ext]; !in8 {
		t.Error("a manual ring-group member was removed")
	}
	// With the switch on an admin rule applies.
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Options: &Options{AllowAdminFromDirectory: true}}); err != nil {
		t.Fatal(err)
	}
	r.apply()
	if r.user("b@c").Role != "admin" {
		t.Errorf("b role = %q", r.user("b@c").Role)
	}
}

func TestLastAdminIsNeverDisabledOrDemoted(t *testing.T) {
	r := newRig(t)
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{
		Options:    &Options{AllowAdminFromDirectory: true},
		GroupRules: &GroupRules{Roles: []RoleRule{{GroupID: "adm", Role: "admin"}}}}); err != nil {
		t.Fatal(err)
	}
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "", "adm")}}
	r.apply()
	if r.user("a@c").Role != "admin" {
		t.Fatal("not admin")
	}
	off := su("a", "a@c", "", "A", "")
	off.Attrs["useraccountcontrol"] = []string{"2"}
	r.d.res = &Result{Users: []SourceUser{off}}
	run := r.apply()
	if !r.user("a@c").Enabled {
		t.Fatal("the last enabled admin was disabled")
	}
	if c := r.changes(run)["a"]; c.Action != ActionConflict || c.Reason != ReasonLastAdmin {
		t.Fatalf("row = %+v", c)
	}
}

func TestOverrideMovesAnExtension(t *testing.T) {
	r := newRig(t)
	ext := r.m.AddExt("2002", false)
	r.m.AddUser(LocalUser{Username: "holder", Enabled: true, ExtensionIDs: []int64{ext}})
	r.d.res = &Result{Users: []SourceUser{su("c", "c@c", "", "C", "2002")}}
	pre := r.preview()
	if c := r.changes(pre)["c"]; c.Action != ActionConflict || !c.Overridable {
		t.Fatalf("row = %+v", c)
	}
	run, err := r.e.Apply(context.Background(), "admin", pre.ID, Decisions{Override: []string{"c"}})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("apply = %+v %v", run, err)
	}
	if u := r.user("c@c"); u == nil || u.OwnedExtension != ext {
		t.Fatalf("c = %+v", u)
	}
	if h := r.user("holder"); len(h.ExtensionIDs) != 0 {
		t.Fatalf("holder = %+v", h)
	}
}

func TestOneFailingChangeDoesNotStopTheRun(t *testing.T) {
	r := newRig(t)
	r.m.ApplyErr["a"] = errors.New("pq: duplicate key value violates unique constraint \"users_username_key\" (password=s3cret-bind-pw)")
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", ""), su("b", "b@c", "", "B", "")}}
	run := r.apply()
	if run.Status != StatusSucceeded || run.Counts["error"] != 1 || r.user("b@c") == nil {
		t.Fatalf("run = %+v", run)
	}
	c := r.changes(run)["a"]
	if c.Action != ActionError || c.Reason != "apply_failed" {
		t.Fatalf("row = %+v", c)
	}
	b, _ := json.Marshal(r.changes(run))
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "pq:") {
		t.Fatalf("rows leak: %s", b)
	}
}

func TestModes(t *testing.T) {
	t.Run("link_only never creates", func(t *testing.T) {
		r := newRig(t)
		r.m.AddUser(LocalUser{Username: "x", Email: "x@c", Enabled: true})
		if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Mode: strPtr(ModeLinkOnly)}); err != nil {
			t.Fatal(err)
		}
		r.d.res = &Result{Users: []SourceUser{su("n", "new@c", "new@c", "N", ""), su("x", "x@c", "x@c", "X", "")}}
		pre := r.preview()
		cs := r.changes(pre)
		if cs["n"].Reason != ReasonLinkOnlyNoMatch || cs["x"].Action != ActionAdopt {
			t.Fatalf("rows = %+v", cs)
		}
	})
	t.Run("create_only seeds once and owns nothing", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Mode: strPtr(ModeCreateOnly)}); err != nil {
			t.Fatal(err)
		}
		r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "First", "")}}
		r.apply()
		a := r.user("a@c")
		if len(a.Managed) != 0 || a.DisplayName != "First" {
			t.Fatalf("a = %+v", a)
		}
		r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "Second", "")}}
		r.apply()
		if r.user("a@c").DisplayName != "First" {
			t.Fatal("create_only overwrote a field")
		}
	})
}

func TestConnectorValidation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	cases := []struct {
		name string
		in   ConnectorInput
		path string
	}{
		{"name", ConnectorInput{Name: strPtr(" ")}, "name"},
		{"mode", ConnectorInput{Mode: strPtr("x")}, "mode"},
		{"schedule", ConnectorInput{ScheduleMinutes: intPtr(3)}, "scheduleMinutes"},
		{"auto-provision", ConnectorInput{AutoProvision: json.RawMessage(`{"enabled":true}`)}, "autoProvision.enabled"},
		{"empty secret", ConnectorInput{Secret: strPtr("")}, "bindPassword"},
		{"identity change needs the secret", ConnectorInput{Config: json.RawMessage(`{"url":"ldaps://evil"}`)}, "bindPassword"},
		{"driver config", ConnectorInput{Config: json.RawMessage(`{"url":"bad"}`)}, "config.url"},
		{"options", ConnectorInput{Options: &Options{AbsenceRuns: 11}}, "options.absenceRuns"},
		{"regex", ConnectorInput{Mappings: &Mappings{Extension: AttrMap{Sources: []string{"a"}, Transforms: []Transform{{Kind: TransformRegex, Pattern: "(("}}}}}, "mappings.extension.transforms[0].pattern"},
		{"role", ConnectorInput{GroupRules: &GroupRules{Roles: []RoleRule{{GroupID: "g", Role: "root"}}}}, "groupRules.roles[0].role"},
	}
	for _, tc := range cases {
		_, err := r.e.Update(ctx, "admin", r.c.ID, tc.in)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		found := false
		for _, f := range ve.Fields {
			found = found || f.Path == tc.path
		}
		if !found {
			t.Errorf("%s: fields = %+v, want %s", tc.name, ve.Fields, tc.path)
		}
	}
	// The same host with the secret again is fine, and replaces it.
	if _, err := r.e.Update(ctx, "admin", r.c.ID, ConnectorInput{Config: json.RawMessage(`{"url":"ldaps://other"}`), Secret: strPtr("new")}); err != nil {
		t.Fatal(err)
	}
	if r.m.Secrets[r.c.ID] != "new" {
		t.Fatal("secret not replaced")
	}
	// Enabling without a secret fails.
	c, err := r.e.Create(ctx, "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("Two")})
	if err != nil || c.Enabled {
		t.Fatalf("create = %+v, %v", c, err)
	}
	if _, err := r.e.Update(ctx, "admin", c.ID, ConnectorInput{Enabled: boolPtr(true)}); err == nil {
		t.Fatal("enabled without a secret")
	}
	if _, err := r.e.Create(ctx, "admin", ConnectorInput{Kind: "nope", Name: strPtr("X")}); err == nil {
		t.Fatal("unknown kind")
	}
}

func intPtr(i int) *int { return &i }

func TestDeleteConnector(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.d.res = &Result{Users: []SourceUser{su("a", "a@c", "", "A", "")}}
	r.apply()
	if err := r.e.Delete(ctx, "admin", r.c.ID, false); !errors.Is(err, ErrHasUsers) {
		t.Fatalf("delete with users = %v", err)
	}
	if err := r.e.Delete(ctx, "admin", r.c.ID, true); err != nil {
		t.Fatal(err)
	}
	if u := r.user("a@c"); u == nil || u.ConnectorID != 0 {
		t.Fatalf("user = %+v", u)
	}
}

func TestTestAppliesMappingsToTheSample(t *testing.T) {
	r := newRig(t)
	res, err := r.e.Test(context.Background(), "admin", r.c.ID, &Mappings{Email: AttrMap{Sources: []string{"mail"}, Transforms: []Transform{{Kind: TransformLast, N: 5}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sample) != 1 || res.Sample[0].Mapped.Email != "A@X.org"[2:] {
		t.Fatalf("sample = %+v", res.Sample)
	}
	if !strings.Contains(strings.Join(r.m.Audits, "|"), "admin test directory_connector") {
		t.Fatal("test not audited")
	}
}
