package directory

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// pushDriver is a push kind (like SCIM): no credential, no schedule.
type pushDriver struct{ fakeDriver }

func (pushDriver) Push() bool { return true }

func newPushRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, m: NewMem(), d: &fakeDriver{res: &Result{}}, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	r.e = &Engine{Store: r.m, Drivers: map[string]Driver{KindSCIM: &pushDriver{}}, Replica: "r1", Now: func() time.Time { return r.now }}
	c, err := r.e.Create(context.Background(), "admin", ConnectorInput{Kind: KindSCIM, Name: strPtr("Entra"), Enabled: boolPtr(true),
		Options: &Options{LoginMethod: LoginOIDC, OIDCProvider: KindEntra}})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	return r
}

func (r *rig) push(users []SourceUser, o PushOptions) (PushResult, error) {
	r.t.Helper()
	c, err := r.e.Get(context.Background(), r.c.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.e.Push(context.Background(), c, users, o)
}

func TestPushConnectorSettings(t *testing.T) {
	r := newPushRig(t)
	if r.c.ScheduleMinutes != 0 || !r.c.Enabled || r.c.HasCredential {
		t.Fatalf("connector = %+v", r.c)
	}
	ctx := context.Background()
	if _, err := r.e.Update(ctx, "admin", r.c.ID, ConnectorInput{ScheduleMinutes: intPtr(60)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a schedule on a push connector: %v", err)
	}
	pw := "x"
	if _, err := r.e.Update(ctx, "admin", r.c.ID, ConnectorInput{Secret: &pw}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a password on a push connector: %v", err)
	}
	// A test and a run need no credential.
	res, err := r.e.Test(ctx, "admin", r.c.ID, nil)
	if err != nil || !res.OK {
		t.Fatalf("test = %+v, %v", res, err)
	}
}

func TestPushCreatesUpdatesAndRecords(t *testing.T) {
	r := newPushRig(t)
	ext := r.m.AddExt("2001", false)
	res, err := r.push([]SourceUser{su("s1", "ann@corp.org", "ann@corp.org", "Ann", "2001")}, PushOptions{Strict: true})
	if err != nil || res.Refused != nil || len(res.Changes) != 1 || res.Changes[0].Action != ActionCreate {
		t.Fatalf("push = %+v, %v", res, err)
	}
	var id int64
	for _, u := range r.m.Users {
		if u.ExternalID == "s1" {
			id = u.ID
			if u.OwnedExtension != ext || u.ConnectorID != r.c.ID {
				t.Fatalf("user = %+v", u)
			}
		}
	}
	if id == 0 {
		t.Fatal("no user created")
	}
	// A rename is an update, recorded on the same run of the hour.
	res, err = r.push([]SourceUser{su("s1", "ann@corp.org", "ann@corp.org", "Ann Smith", "2001")}, PushOptions{Strict: true})
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Action != ActionUpdate {
		t.Fatalf("update = %+v, %v", res, err)
	}
	runs, _ := r.m.ListRuns(context.Background(), r.c.ID, 10)
	if len(runs) != 1 || runs[0].Kind != RunSCIM || runs[0].Counts[ActionCreate] != 1 || runs[0].Counts[ActionUpdate] != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	// The next hour starts a new run.
	r.now = r.now.Add(time.Hour)
	if _, err := r.push([]SourceUser{su("s1", "ann@corp.org", "ann@corp.org", "Ann S.", "2001")}, PushOptions{Strict: true}); err != nil {
		t.Fatal(err)
	}
	if runs, _ = r.m.ListRuns(context.Background(), r.c.ID, 10); len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	// A push says nothing about the users it does not name: no absence.
	if _, err := r.push([]SourceUser{su("s2", "bob@corp.org", "bob@corp.org", "Bob", "")}, PushOptions{Strict: true}); err != nil {
		t.Fatal(err)
	}
	if u := r.m.Users[id]; !u.Enabled || u.AbsentRuns != 0 {
		t.Fatalf("ann after a push of bob = %+v", u)
	}
}

func TestPushStrictRefusals(t *testing.T) {
	r := newPushRig(t)
	local := r.m.AddUser(LocalUser{Username: "carl", Email: "carl@corp.org", Role: "user", Enabled: true})
	r.m.AddUser(LocalUser{Username: "root", Email: "root@corp.org", Role: "admin", Enabled: true})
	r.m.AddExt("2002", false)
	r.m.AddUser(LocalUser{Username: "dora", Role: "user", Enabled: true, ExtensionIDs: []int64{r.m.Exts["2002"].ID}})
	cases := []struct {
		name   string
		u      SourceUser
		reason string
	}{
		{"local user, no adoption", su("s1", "carl", "carl@corp.org", "Carl", ""), ReasonAdoptNotConfirmed},
		{"local admin", su("s2", "root", "root@corp.org", "Root", ""), ReasonLocalAdmin},
		{"extension of another user", su("s3", "eve", "eve@corp.org", "Eve", "2002"), ReasonExtensionLinked},
		{"missing extension", su("s4", "fay", "fay@corp.org", "Fay", "2999"), ReasonExtensionMissing},
	}
	for _, tc := range cases {
		res, err := r.push([]SourceUser{tc.u}, PushOptions{Strict: true})
		if err != nil || res.Refused == nil || res.Refused.Reason != tc.reason {
			t.Errorf("%s: %+v, %v", tc.name, res.Refused, err)
		}
	}
	if len(r.m.Users) != 3 {
		t.Fatalf("a refused push changed users: %d", len(r.m.Users))
	}
	// With adoption allowed, the local user is taken over.
	res, err := r.push([]SourceUser{su("s1", "carl", "carl@corp.org", "Carl", "")}, PushOptions{Strict: true, Adopt: true})
	if err != nil || res.Refused != nil || res.Changes[0].Action != ActionAdopt {
		t.Fatalf("adopt = %+v, %v", res, err)
	}
	if u := r.m.Users[local]; u.ConnectorID != r.c.ID || u.ExternalID != "s1" {
		t.Fatalf("adopted = %+v", u)
	}
	// An admin is never adopted, even then.
	if res, _ := r.push([]SourceUser{su("s2", "root", "root@corp.org", "Root", "")}, PushOptions{Strict: true, Adopt: true}); res.Refused == nil {
		t.Fatal("a local admin was adopted")
	}
}

func TestPushDeleteAndGuard(t *testing.T) {
	r := newPushRig(t)
	ctx := context.Background()
	var users []SourceUser
	for i := 0; i < 10; i++ {
		n := strconv.Itoa(i)
		users = append(users, su("s"+n, "u"+n, "u"+n+"@corp.org", "U"+n, ""))
	}
	if _, err := r.push(users, PushOptions{}); err != nil {
		t.Fatal(err)
	}
	// active false disables at once; a delete too, whatever the attributes.
	off := su("s0", "u0", "u0@corp.org", "U0", "")
	off.Attrs["useraccountcontrol"] = []string{"514"}
	res, err := r.push([]SourceUser{off}, PushOptions{Strict: true})
	if err != nil || res.Changes[0].Action != ActionDisable {
		t.Fatalf("inactive = %+v, %v", res, err)
	}
	for i := 1; i <= 4; i++ {
		res, err := r.push([]SourceUser{users[i]}, PushOptions{Strict: true, Delete: true})
		if err != nil || len(res.Changes) != 1 || res.Changes[0].Action != ActionDisable {
			t.Fatalf("delete %d = %+v, %v", i, res, err)
		}
	}
	st, err := r.e.Guard(ctx, r.c)
	if err != nil || st.Disables != 5 || st.Limit != 5 || st.Linked != 5 {
		t.Fatalf("guard = %+v, %v", st, err)
	}
	// The sixth disable within the hour takes it over the limit (20 percent
	// of the linked users, at least 5): the connector pauses, nothing is
	// applied, and a run waits for confirmation.
	if _, err := r.push([]SourceUser{users[5]}, PushOptions{Strict: true, Delete: true}); !errors.Is(err, ErrPaused) {
		t.Fatalf("over the guard: %v", err)
	}
	c, _ := r.e.Get(ctx, r.c.ID)
	if c.PausedAt == nil {
		t.Fatal("not paused")
	}
	for _, u := range r.m.Users {
		if u.ExternalID == "s5" && !u.Enabled {
			t.Fatal("the halted disable was applied")
		}
	}
	// Every write is refused while paused, a create too.
	if _, err := r.push([]SourceUser{su("s99", "new", "new@corp.org", "New", "")}, PushOptions{Strict: true}); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused create: %v", err)
	}
	runs, _ := r.m.ListRuns(ctx, r.c.ID, 10)
	var halted Run
	for _, x := range runs {
		if x.Status == StatusNeedsConfirmation {
			halted = x
		}
	}
	if halted.ID == 0 || halted.Kind != RunSCIM || halted.Counts[ActionDisable] != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	// Apply is not how a SCIM run is confirmed.
	if _, err := r.e.Apply(ctx, "admin", halted.ID, Decisions{}); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("apply of a scim run: %v", err)
	}
	r.now = r.now.Add(time.Minute)
	run, err := r.e.Confirm(ctx, "admin", halted.ID, Decisions{})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("confirm = %+v, %v", run, err)
	}
	if _, err := r.e.Confirm(ctx, "admin", halted.ID, Decisions{}); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("second confirm: %v", err)
	}
	// Resumed, and the window restarted: the IdP's retry goes through.
	r.now = r.now.Add(time.Minute)
	res, err = r.push([]SourceUser{users[5]}, PushOptions{Strict: true, Delete: true})
	if err != nil || res.Changes[0].Action != ActionDisable {
		t.Fatalf("after confirm = %+v, %v", res, err)
	}
	found := false
	for _, a := range r.m.Audits {
		found = found || a == "admin scim-resume directory_connector "+strconv.FormatInt(r.c.ID, 10)
	}
	if !found {
		t.Fatalf("audits = %v", r.m.Audits)
	}
}

func TestPushDisabledConnector(t *testing.T) {
	r := newPushRig(t)
	if _, err := r.e.Update(context.Background(), "admin", r.c.ID, ConnectorInput{Enabled: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.push([]SourceUser{su("s1", "a", "a@corp.org", "A", "")}, PushOptions{Strict: true}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("push to a disabled connector: %v", err)
	}
}
