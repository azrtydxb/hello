package directory

// The Store contract: the same assertions against Mem (always) and PG (with
// HELLO_TEST_DATABASE_URL, on a migrated throwaway database). The engine,
// SCIM and API tests run on Mem, so a PG change that Mem does not mirror (or
// the reverse) would pass them and still change what production does. The
// contract pins the behaviour both share: which calls report ErrNotFound,
// ErrInvalid and ErrHasUsers, the sealed credential round trip, the run
// queue (oldest first, orphans closed), the change log and its filter, the
// SCIM run of the hour and the push-disable count, linking and releasing
// users, and the per-connector lock.
//
// Known differences the contract leaves out on purpose (Mem is a test double,
// not a second implementation): UpdateRun on an unknown id is ErrNotFound in
// Mem and a no-op in PG; ListConnectors, ListRuns and ListChanges return nil
// for nothing in Mem and an empty slice in PG; a limit of 0 means no limit in
// Mem and the default in PG; Apply's disable of a disabled user is a no-op in
// PG and disables again in Mem; Mem does not record the enable or disable
// audit row of SaveConnector, nor the users' extensions and ring groups of a
// released connector.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// contractRig is one Store under test.
type contractRig struct {
	store Store
}

func memContractRig(*testing.T) contractRig { return contractRig{store: NewMem()} }

func pgContractRig(t *testing.T) contractRig {
	p, _ := scratchPG(t)
	return contractRig{store: p}
}

func TestStoreContractMem(t *testing.T) { runStoreContract(t, memContractRig) }

func TestStoreContractPG(t *testing.T) { runStoreContract(t, pgContractRig) }

func runStoreContract(t *testing.T, newRig func(*testing.T) contractRig) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, s Store)
	}{
		{"Connectors", contractConnectors},
		{"Runs", contractRuns},
		{"Changes", contractChanges},
		{"Push", contractPush},
		{"Users", contractUsers},
		{"Lock", contractLock},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newRig(t).store) })
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// must returns v, or panics with err (which fails the test with its stack):
// Go cannot pass t beside a call's two results.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func newConnector(t *testing.T, s Store, name string, secret *string) Connector {
	t.Helper()
	return must(s.SaveConnector(context.Background(), "admin", Connector{Kind: KindLDAP, Name: name, Mode: "authoritative", ScheduleMinutes: 60,
		Config: json.RawMessage(`{}`)}, secret, "fp-"+name))
}

func contractConnectors(t *testing.T, s Store) {
	ctx := context.Background()
	_, err := s.GetConnector(ctx, 999999)
	wantErr(t, "GetConnector unknown", err, ErrNotFound)
	pw := "bind-secret"
	a := newConnector(t, s, "AD", &pw)
	if a.ID == 0 || a.Name != "AD" {
		t.Fatalf("saved %+v", a)
	}
	got := must(s.GetConnector(ctx, a.ID))
	if !got.HasCredential || got.CredentialFP != "fp-AD" || got.Kind != KindLDAP {
		t.Fatalf("got %+v", got)
	}
	if sec := must(s.Secret(ctx, a.ID)); sec != pw {
		t.Fatalf("secret = %q", sec)
	}
	_, err = s.SaveConnector(ctx, "admin", Connector{Kind: KindLDAP, Name: "AD", Mode: "authoritative", ScheduleMinutes: 60, Config: json.RawMessage(`{}`)}, nil, "")
	wantErr(t, "duplicate name", err, ErrInvalid)
	b := newConnector(t, s, "Google", nil)
	_, err = s.Secret(ctx, b.ID)
	wantErr(t, "Secret without a credential", err, ErrNotFound)
	list := must(s.ListConnectors(ctx))
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list = %+v", list)
	}

	for what, err := range map[string]error{
		"SaveSyncState": s.SaveSyncState(ctx, 999999, SyncState{}),
		"SetPaused":     s.SetPaused(ctx, 999999, true, time.Now()),
		"Delete":        s.DeleteConnector(ctx, "admin", 999999, false),
	} {
		wantErr(t, what+" unknown", err, ErrNotFound)
	}
	_, err = s.RecordOutcome(ctx, 999999, true, "", "", time.Now())
	wantErr(t, "RecordOutcome unknown", err, ErrNotFound)

	now := time.Now().UTC().Truncate(time.Second)
	for i, want := range []int{1, 2} {
		if n := must(s.RecordOutcome(ctx, a.ID, false, "bind", "refused", now)); n != want {
			t.Fatalf("failure %d: consecutive = %d", i, n)
		}
	}
	got = must(s.GetConnector(ctx, a.ID))
	if got.Status != "failing" || got.LastErrorCode != "bind" || got.ConsecutiveFailure != 2 || got.LastSuccessAt != nil {
		t.Fatalf("after failures %+v", got)
	}
	if n := must(s.RecordOutcome(ctx, a.ID, true, "x", "y", now)); n != 0 {
		t.Fatalf("after success consecutive = %d", n)
	}
	got = must(s.GetConnector(ctx, a.ID))
	if got.Status != "ok" || got.LastErrorCode != "" || got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(now) {
		t.Fatalf("after success %+v", got)
	}

	if err := s.SaveSyncState(ctx, a.ID, SyncState{Fingerprint: "f1", LastFullAt: &now}); err != nil {
		t.Fatal(err)
	}
	got = must(s.GetConnector(ctx, a.ID))
	if got.Sync.Fingerprint != "f1" || got.LastFullRunAt == nil || !got.LastFullRunAt.Equal(now) {
		t.Fatalf("sync state %+v", got)
	}

	if err := s.SetPaused(ctx, a.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if got = must(s.GetConnector(ctx, a.ID)); got.PausedAt == nil || !got.PausedAt.Equal(now) {
		t.Fatalf("paused %+v", got.PausedAt)
	}
	later := now.Add(time.Minute)
	if err := s.SetPaused(ctx, a.ID, false, later); err != nil {
		t.Fatal(err)
	}
	if got = must(s.GetConnector(ctx, a.ID)); got.PausedAt != nil || got.GuardSince == nil || !got.GuardSince.Equal(later) {
		t.Fatalf("resumed paused=%v guard=%v", got.PausedAt, got.GuardSince)
	}

	if err := s.DeleteConnector(ctx, "admin", b.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetConnector(ctx, b.ID)
	wantErr(t, "GetConnector deleted", err, ErrNotFound)
}

func contractRuns(t *testing.T, s Store) {
	ctx := context.Background()
	c := newConnector(t, s, "AD", nil)
	_, err := s.GetRun(ctx, 999999)
	wantErr(t, "GetRun unknown", err, ErrNotFound)
	if ok := must(s.HasApplied(ctx, c.ID)); ok {
		t.Fatal("HasApplied before any run")
	}
	if _, found, err := s.LastScheduled(ctx, c.ID); err != nil || found {
		t.Fatalf("LastScheduled before any run: %v %v", found, err)
	}
	r1 := must(s.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunApply, Status: StatusQueued, RequestedBy: "admin"}))
	r2 := must(s.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunApply, Status: StatusQueued, RequestedBy: "admin"}))
	if r1.ID == 0 || r2.ID <= r1.ID {
		t.Fatalf("run ids %d %d", r1.ID, r2.ID)
	}
	for _, want := range []int64{r1.ID, r2.ID} {
		got := must(s.ClaimQueued(ctx, c.ID, "replica-a"))
		if got == nil || got.ID != want || got.Status != StatusRunning || got.Replica != "replica-a" || got.StartedAt == nil {
			t.Fatalf("claimed %+v, want run %d", got, want)
		}
	}
	if got := must(s.ClaimQueued(ctx, c.ID, "replica-a")); got != nil {
		t.Fatalf("claimed %+v from an empty queue", got)
	}
	if n := must(s.CloseOrphans(ctx, c.ID)); n != 2 {
		t.Fatalf("closed %d orphans", n)
	}
	got := must(s.GetRun(ctx, r1.ID))
	if got.Status != StatusFailed || got.ErrorCode != CodeReplicaLost || got.FinishedAt == nil {
		t.Fatalf("orphan %+v", got)
	}
	done := must(s.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunApply, Status: StatusSucceeded, Counts: map[string]int{"create": 3}}))
	if got := must(s.GetRun(ctx, done.ID)); got.Counts["create"] != 3 {
		t.Fatalf("counts %+v", got.Counts)
	}
	if ok := must(s.HasApplied(ctx, c.ID)); !ok {
		t.Fatal("HasApplied after a succeeded apply")
	}
	must(s.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunScheduled, Status: StatusQueued}))
	if _, found, err := s.LastScheduled(ctx, c.ID); err != nil || !found {
		t.Fatalf("LastScheduled after a scheduled run: %v %v", found, err)
	}
	runs := must(s.ListRuns(ctx, c.ID, 10))
	if len(runs) != 4 || runs[0].ID < runs[1].ID {
		t.Fatalf("runs %+v", runs)
	}
}

func contractChanges(t *testing.T, s Store) {
	ctx := context.Background()
	c := newConnector(t, s, "AD", nil)
	run := must(s.CreateRun(ctx, Run{ConnectorID: c.ID, Kind: RunPreview, Status: StatusSucceeded}))
	if err := s.ReplaceChanges(ctx, run.ID, []Change{
		{Action: ActionCreate, Username: "a", ExternalID: "1", FieldDiffs: map[string]Diff{"email": {After: "a@x"}}},
		{Action: ActionDisable, Username: "b", ExternalID: "2"},
		{Action: ActionCreate, Username: "c", ExternalID: "3"},
	}); err != nil {
		t.Fatal(err)
	}
	creates := must(s.ListChanges(ctx, run.ID, ChangeFilter{Action: ActionCreate}))
	if len(creates) != 2 || creates[0].Username != "a" || creates[1].Username != "c" || creates[0].FieldDiffs["email"].After != "a@x" {
		t.Fatalf("creates %+v", creates)
	}
	after := must(s.ListChanges(ctx, run.ID, ChangeFilter{After: creates[0].ID}))
	if len(after) != 2 || after[0].Username != "b" {
		t.Fatalf("after %+v", after)
	}
	if err := s.ReplaceChanges(ctx, run.ID, []Change{{Action: ActionUpdate, Username: "d"}}); err != nil {
		t.Fatal(err)
	}
	all := must(s.ListChanges(ctx, run.ID, ChangeFilter{Limit: 10}))
	if len(all) != 1 || all[0].Username != "d" || all[0].RunID != run.ID {
		t.Fatalf("replaced %+v", all)
	}
}

func contractPush(t *testing.T, s Store) {
	ctx := context.Background()
	c := newConnector(t, s, "SCIM", nil)
	now := time.Now()
	if err := s.RecordPush(ctx, c.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.RecordPush(ctx, c.ID, []Change{{Action: ActionDisable, Username: "x"}, {Action: ActionDisable, Username: "y"},
			{Action: ActionUpdate, Username: "z"}}, now); err != nil {
			t.Fatal(err)
		}
	}
	if n := must(s.PushDisables(ctx, c.ID, now.Add(-time.Minute))); n != 4 {
		t.Fatalf("push disables = %d", n)
	}
	if n := must(s.PushDisables(ctx, c.ID, now.Add(time.Minute))); n != 0 {
		t.Fatalf("push disables after the window = %d", n)
	}
	var scim []Run
	for _, r := range must(s.ListRuns(ctx, c.ID, 10)) {
		if r.Kind == RunSCIM {
			scim = append(scim, r)
		}
	}
	if len(scim) != 1 || scim[0].Status != StatusSucceeded || scim[0].Counts[ActionDisable] != 4 || scim[0].Counts[ActionUpdate] != 2 {
		t.Fatalf("scim runs %+v", scim)
	}
	if got := must(s.ListChanges(ctx, scim[0].ID, ChangeFilter{Limit: 100})); len(got) != 6 {
		t.Fatalf("scim changes %d", len(got))
	}
}

func contractUsers(t *testing.T, s Store) {
	ctx := context.Background()
	c := newConnector(t, s, "AD", nil)
	user := func() LocalUser {
		t.Helper()
		st := must(s.LoadState(ctx, c.ID))
		for _, u := range st.Users {
			if u.Username == "u1" {
				return u
			}
		}
		t.Fatal("user u1 not in the state")
		return LocalUser{}
	}
	name, role, email := "u1", "user", "u1@corp"
	if _, err := s.Apply(ctx, "directory:AD", c, Op{Kind: ActionCreate, Username: &name, Role: &role, Email: &email, ExternalID: "g1"}); err != nil {
		t.Fatal(err)
	}
	u := user()
	if u.ConnectorID != c.ID || u.ExternalID != "g1" || !u.Enabled || u.Email != email || u.Role != role {
		t.Fatalf("created %+v", u)
	}
	if _, err := s.Apply(ctx, "directory:AD", c, Op{Kind: ActionDisable, UserID: u.ID, AbsentRuns: 2}); err != nil {
		t.Fatal(err)
	}
	if u = user(); u.Enabled || u.AbsentRuns != 2 {
		t.Fatalf("disabled %+v", u)
	}
	if _, err := s.Apply(ctx, "directory:AD", c, Op{Kind: ActionEnable, UserID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if u = user(); !u.Enabled || u.AbsentRuns != 0 {
		t.Fatalf("enabled %+v", u)
	}
	if got := must(s.GetConnector(ctx, c.ID)); got.LinkedUsers != 1 {
		t.Fatalf("linked users = %d", got.LinkedUsers)
	}
	wantErr(t, "delete with users", s.DeleteConnector(ctx, "admin", c.ID, false), ErrHasUsers)
	if err := s.DeleteConnector(ctx, "admin", c.ID, true); err != nil {
		t.Fatal(err)
	}
	if u = user(); u.ConnectorID != 0 || u.ExternalID != "" || len(u.Managed) != 0 {
		t.Fatalf("released %+v", u)
	}
}

func contractLock(t *testing.T, s Store) {
	ctx := context.Background()
	unlock, ok, err := s.TryLock(ctx, "hello:dirsync:contract")
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if _, again, err := s.TryLock(ctx, "hello:dirsync:contract"); err != nil || again {
		t.Fatalf("second lock while held: %v %v", again, err)
	}
	if other, ok, err := s.TryLock(ctx, "hello:dirsync:other"); err != nil || !ok {
		t.Fatalf("another key: %v %v", ok, err)
	} else {
		other()
	}
	unlock()
	unlock2, ok, err := s.TryLock(ctx, "hello:dirsync:contract")
	if err != nil || !ok {
		t.Fatalf("lock after unlock: %v %v", ok, err)
	}
	unlock2()
}
