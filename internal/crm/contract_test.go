package crm

// The Repo contract: the same assertions against Mem (always) and PG (with
// HELLO_TEST_DATABASE_URL, on a migrated throwaway database). Handler and
// worker tests run on Mem, so a PG change that Mem does not mirror (or the
// reverse) would pass them and still change what production does. The
// contract pins the behaviour both share: which calls report ErrNotFound and
// ErrConflict, secrets and tokens round-tripping, the single-use OAuth state,
// the call log's state machine and the engagement handshake.
//
// Known differences the contract leaves out on purpose (Mem is a test double,
// not a second implementation): Mem's SaveOrgConnection and
// SaveUserConnection overwrite the granted scopes with an empty list where PG
// keeps the stored ones; Finish and SetCalendarState on an unknown id are
// ErrNotFound in Mem and a no-op in PG; Mem's ClaimDue orders by id, PG's by
// next_attempt_at.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"
)

// contractRig is one Repo under test and how to add a Hello user to it.
type contractRig struct {
	repo    Repo
	addUser func(t *testing.T, username string) int64
}

func memContractRig(*testing.T) contractRig {
	m := NewMem()
	var next int64
	return contractRig{repo: m, addUser: func(_ *testing.T, username string) int64 {
		next++
		m.UsersList = append(m.UsersList, UserRef{ID: next, Username: username})
		return next
	}}
}

func pgContractRig(t *testing.T) contractRig {
	p, db := scratchPG(t)
	return contractRig{repo: p, addUser: func(t *testing.T, username string) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(context.Background(),
			`INSERT INTO users (username, password_hash, role) VALUES ($1, 'x', 'user') RETURNING id`, username).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}}
}

func TestRepoContractMem(t *testing.T) { runRepoContract(t, memContractRig) }

func TestRepoContractPG(t *testing.T) { runRepoContract(t, pgContractRig) }

func runRepoContract(t *testing.T, newRig func(*testing.T) contractRig) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, r contractRig)
	}{
		{"Connectors", contractConnectors},
		{"OrgConnection", contractOrgConnection},
		{"UserConnections", contractUserConnections},
		{"OAuthState", contractOAuthState},
		{"CallLog", contractCallLog},
		{"Mappings", contractMappings},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newRig(t)) })
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v; want %v", what, err, want)
	}
}

// jsonEqual compares JSON documents by value: jsonb does not keep the
// spelling of what was written.
func jsonEqual(got []byte, want string) bool {
	var a, b any
	return json.Unmarshal(got, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && reflect.DeepEqual(a, b)
}

func contractConnector(t *testing.T, r Repo, name, secret string) ConnectorRow {
	t.Helper()
	c, err := r.CreateConnector(context.Background(), ConnectorInput{Kind: "fake", Name: name, Enabled: true,
		Config: map[string]string{"clientId": "cid", "region": "eu"}, Recordings: true, LinkTTLHours: 48, LogMissed: false, FallbackOwner: "owner-1"}, secret)
	if err != nil {
		t.Fatalf("CreateConnector(%s): %v", name, err)
	}
	return c
}

// contractConnectors fails if a connector's settings or secret do not round
// trip, a name is not unique on create and on rename, or a missing connector
// is not ErrNotFound for get, update, delete and its secret.
func contractConnectors(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	a := contractConnector(t, r, "HubSpot", "s3cret")
	if a.ID == 0 || a.Kind != "fake" || a.Name != "HubSpot" || !a.Enabled || !a.HasSecret || !a.Recordings || a.LinkTTLHours != 48 ||
		a.LogMissed || a.FallbackOwner != "owner-1" || !maps.Equal(a.Config, map[string]string{"clientId": "cid", "region": "eu"}) {
		t.Fatalf("created = %+v", a)
	}
	if got, err := r.GetConnector(ctx, a.ID); err != nil || got.Name != a.Name || !maps.Equal(got.Config, a.Config) || !got.HasSecret {
		t.Fatalf("GetConnector = %+v, %v", got, err)
	}
	if s, err := r.ClientSecret(ctx, a.ID); err != nil || s != "s3cret" {
		t.Fatalf("ClientSecret = %q, %v", s, err)
	}
	// LinkTTLHours is valid, so only the name can conflict.
	_, err := r.CreateConnector(ctx, ConnectorInput{Kind: "fake", Name: "HubSpot", LinkTTLHours: 1}, "")
	wantErr(t, "duplicate name", err, ErrConflict)

	b := contractConnector(t, r, "Zoho", "")
	if b.HasSecret {
		t.Fatalf("connector without a secret: HasSecret")
	}
	if s, err := r.ClientSecret(ctx, b.ID); err != nil || s != "" {
		t.Fatalf("ClientSecret of a connector without one = %q, %v", s, err)
	}
	_, err = r.UpdateConnector(ctx, b.ID, ConnectorInput{Name: "HubSpot", LinkTTLHours: 1}, nil)
	wantErr(t, "rename to a taken name", err, ErrConflict)

	// A nil secret keeps the stored one; an empty one clears it.
	in := ConnectorInput{Kind: "fake", Name: "HubSpot EU", Enabled: false, Config: nil, Recordings: false, LinkTTLHours: 1, LogMissed: true}
	u, err := r.UpdateConnector(ctx, a.ID, in, nil)
	if err != nil || u.Name != "HubSpot EU" || u.Enabled || u.LinkTTLHours != 1 || !u.LogMissed || len(u.Config) != 0 || u.Config == nil || !u.HasSecret {
		t.Fatalf("UpdateConnector = %+v, %v", u, err)
	}
	if s, _ := r.ClientSecret(ctx, a.ID); s != "s3cret" {
		t.Fatalf("a nil secret changed the stored one to %q", s)
	}
	empty := ""
	if u, err = r.UpdateConnector(ctx, a.ID, in, &empty); err != nil || u.HasSecret {
		t.Fatalf("clearing the secret: %+v, %v", u, err)
	}
	if s, err := r.ClientSecret(ctx, a.ID); err != nil || s != "" {
		t.Fatalf("ClientSecret after clearing = %q, %v", s, err)
	}

	list, err := r.ListConnectors(ctx)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("ListConnectors = %+v, %v", list, err)
	}

	const missing = 987654
	_, err = r.GetConnector(ctx, missing)
	wantErr(t, "GetConnector of a missing id", err, ErrNotFound)
	_, err = r.UpdateConnector(ctx, missing, in, nil)
	wantErr(t, "UpdateConnector of a missing id", err, ErrNotFound)
	_, err = r.ClientSecret(ctx, missing)
	wantErr(t, "ClientSecret of a missing id", err, ErrNotFound)
	wantErr(t, "DeleteConnector of a missing id", r.DeleteConnector(ctx, missing), ErrNotFound)
	if err := r.DeleteConnector(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.GetConnector(ctx, b.ID)
	wantErr(t, "GetConnector after delete", err, ErrNotFound)
}

// contractOrgConnection fails if the org tokens do not round trip, a refresh
// without a refresh token or an account loses the stored ones, the status is
// not settable, or a missing connection is not ErrNotFound.
func contractOrgConnection(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	c := contractConnector(t, r, "Pipedrive", "")
	_, _, err := r.OrgConnection(ctx, c.ID)
	wantErr(t, "OrgConnection before connecting", err, ErrNotFound)
	wantErr(t, "DeleteOrgConnection before connecting", r.DeleteOrgConnection(ctx, c.ID), ErrNotFound)

	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	row, err := r.SaveOrgConnection(ctx, c.ID, Tokens{Access: "A1", Refresh: "R1", ExpiresAt: exp, Account: "acct@x", Scopes: []string{"a", "b"}})
	if err != nil || row.ConnectorID != c.ID || row.Scope != "org" || row.UserID != 0 || row.Status != StatusConnected ||
		row.ExternalAccount != "acct@x" || row.GrantedScopes != "a b" || !row.ExpiresAt.Equal(exp) {
		t.Fatalf("SaveOrgConnection = %+v, %v", row, err)
	}
	if _, tk, err := r.OrgConnection(ctx, c.ID); err != nil || tk.Access != "A1" || tk.Refresh != "R1" || tk.Account != "acct@x" || !tk.ExpiresAt.Equal(exp) {
		t.Fatalf("OrgConnection tokens = %+v, %v", tk, err)
	}
	if _, err := r.SaveOrgConnection(ctx, c.ID, Tokens{Access: "A2"}); err != nil {
		t.Fatal(err)
	}
	row2, tk, err := r.OrgConnection(ctx, c.ID)
	if err != nil || tk.Access != "A2" || tk.Refresh != "R1" || row2.ExternalAccount != "acct@x" || row2.ID != row.ID || !row2.ExpiresAt.IsZero() {
		t.Fatalf("after a refresh without a refresh token: %+v %+v, %v", row2, tk, err)
	}
	if err := r.SetConnectionStatus(ctx, row.ID, StatusNeedsReauth, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	if row2, _, _ = r.OrgConnection(ctx, c.ID); row2.Status != StatusNeedsReauth || row2.LastError != "invalid_grant" {
		t.Fatalf("after SetConnectionStatus: %+v", row2)
	}
	// A new grant reconnects and clears the error.
	if row2, err = r.SaveOrgConnection(ctx, c.ID, Tokens{Access: "A3"}); err != nil || row2.Status != StatusConnected || row2.LastError != "" {
		t.Fatalf("reconnect: %+v, %v", row2, err)
	}
	if err := r.DeleteOrgConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.OrgConnection(ctx, c.ID)
	wantErr(t, "OrgConnection after delete", err, ErrNotFound)
}

// contractUserConnections fails if user connections mix between users or
// with the org connection, user 0 is accepted, or the calendar settings do
// not behave as the calendar poller expects.
func contractUserConnections(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	c := contractConnector(t, r, "Outlook", "")
	anna, bob := rig.addUser(t, "anna"), rig.addUser(t, "bob")
	_, _, err := r.UserConnection(ctx, c.ID, 0)
	wantErr(t, "UserConnection of user 0", err, ErrNotFound)
	_, err = r.SaveUserConnection(ctx, c.ID, 0, Tokens{Access: "x"})
	wantErr(t, "SaveUserConnection of user 0", err, ErrNotFound)
	_, _, err = r.UserConnection(ctx, c.ID, anna)
	wantErr(t, "UserConnection before connecting", err, ErrNotFound)
	wantErr(t, "DeleteUserConnection before connecting", r.DeleteUserConnection(ctx, c.ID, anna), ErrNotFound)

	ra, err := r.SaveUserConnection(ctx, c.ID, anna, Tokens{Access: "ANNA", Refresh: "ANNA-R", Account: "anna@x"})
	if err != nil || ra.Scope != "user" || ra.UserID != anna || ra.Status != StatusConnected {
		t.Fatalf("SaveUserConnection(anna) = %+v, %v", ra, err)
	}
	if _, err := r.SaveUserConnection(ctx, c.ID, bob, Tokens{Access: "BOB"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveOrgConnection(ctx, c.ID, Tokens{Access: "ORG"}); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[int64]string{anna: "ANNA", bob: "BOB"} {
		if _, tk, err := r.UserConnection(ctx, c.ID, user); err != nil || tk.Access != want {
			t.Fatalf("user %d tokens = %+v, %v", user, tk, err)
		}
	}
	if _, tk, _ := r.OrgConnection(ctx, c.ID); tk.Access != "ORG" {
		t.Fatalf("org tokens = %+v", tk)
	}
	if rows, err := r.UserConnections(ctx, c.ID); err != nil || len(rows) != 2 {
		t.Fatalf("UserConnections = %+v, %v", rows, err)
	}
	if rows, err := r.UserConnectionsOf(ctx, bob); err != nil || len(rows) != 1 || rows[0].UserID != bob || rows[0].ConnectorID != c.ID {
		t.Fatalf("UserConnectionsOf(bob) = %+v, %v", rows, err)
	}

	wantErr(t, "SetCalendarSettings of a missing connection", r.SetCalendarSettings(ctx, 987654, true), ErrNotFound)
	if err := r.SetCalendarSettings(ctx, ra.ID, true); err != nil {
		t.Fatal(err)
	}
	busy, checked := time.Now().Add(30*time.Minute).Truncate(time.Second), time.Now().Truncate(time.Second)
	if err := r.SetCalendarState(ctx, ra.ID, true, busy, checked, "slow"); err != nil {
		t.Fatal(err)
	}
	row, _, _ := r.UserConnection(ctx, c.ID, anna)
	if !row.CalendarDND || !row.CalendarActive || !row.CalendarBusyUntil.Equal(busy) || !row.CalendarCheckedAt.Equal(checked) || row.CalendarError != "slow" {
		t.Fatalf("calendar state = %+v", row)
	}
	// Turning DND off clears what the poller set, but not when it last looked.
	if err := r.SetCalendarSettings(ctx, ra.ID, false); err != nil {
		t.Fatal(err)
	}
	row, _, _ = r.UserConnection(ctx, c.ID, anna)
	if row.CalendarDND || row.CalendarActive || !row.CalendarBusyUntil.IsZero() || row.CalendarError != "" || !row.CalendarCheckedAt.Equal(checked) {
		t.Fatalf("calendar state after off = %+v", row)
	}
	if err := r.DeleteUserConnection(ctx, c.ID, anna); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.UserConnection(ctx, c.ID, anna)
	wantErr(t, "UserConnection after delete", err, ErrNotFound)
	if _, _, err := r.OrgConnection(ctx, c.ID); err != nil {
		t.Fatalf("the org connection went with a user's: %v", err)
	}
}

// contractOAuthState fails if a state is usable twice, after it expired, or
// unknown, or if it does not carry its connector and actor.
func contractOAuthState(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	c := contractConnector(t, r, "Salesforce", "")
	s, err := r.NewState(ctx, c.ID, "admin", time.Minute)
	if err != nil || s == "" {
		t.Fatalf("NewState = %q, %v", s, err)
	}
	if id, actor, err := r.TakeState(ctx, s); err != nil || id != c.ID || actor != "admin" {
		t.Fatalf("TakeState = %d %q, %v", id, actor, err)
	}
	_, _, err = r.TakeState(ctx, s)
	wantErr(t, "a state taken twice", err, ErrNotFound)
	_, _, err = r.TakeState(ctx, "never-issued")
	wantErr(t, "an unknown state", err, ErrNotFound)
	old, err := r.NewState(ctx, c.ID, "admin", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.TakeState(ctx, old)
	wantErr(t, "an expired state", err, ErrNotFound)
	if err := r.PurgeStates(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// contractCallLog fails if the hook's upsert, the worker's claim and settle,
// the engagement handshake, the dead-letter retry or the log listing behave
// differently in the two implementations.
func contractCallLog(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	c := contractConnector(t, r, "HaloPSA", "")
	if got, err := r.ActiveConnectors(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("ActiveConnectors before connecting = %+v, %v", got, err)
	}
	if _, err := r.SaveOrgConnection(ctx, c.ID, Tokens{Access: "a", Refresh: "r"}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ActiveConnectors(ctx, nil); err != nil || len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("ActiveConnectors = %+v, %v", got, err)
	}

	// The engagement arrives before the call: a waiting row that adopts it.
	if ok, err := r.AttachEngagement(ctx, c.ID, "call-1", "ENG-1"); err != nil || !ok {
		t.Fatalf("AttachEngagement on a new call = %v, %v", ok, err)
	}
	if ok, err := r.AttachEngagement(ctx, c.ID, "call-1", "ENG-2"); err != nil || ok {
		t.Fatalf("a second engagement replaced the first: %v, %v", ok, err)
	}
	if got, err := r.ClaimDue(ctx, 10, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("a waiting row was claimed: %+v, %v", got, err)
	}
	job := LogJob{CallID: "call-1", Direction: "inbound", EventType: "call.ended", Payload: []byte(`{"from":"100"}`), OwnerExtension: "100"}
	if err := r.UpsertCall(ctx, nil, c.ID, job); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertCall(ctx, nil, c.ID, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := r.ClaimDue(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue = %+v, %v", claimed, err)
	}
	l := claimed[0]
	if l.CallID != "call-1" || l.Status != LogPending || l.Direction != "inbound" || l.ExternalID != "ENG-1" || l.OwnerExtension != "100" ||
		l.EventType != "call.ended" || !jsonEqual(l.Payload, `{"from":"100"}`) || l.RecordingID != 0 || l.LinkExpiresAt != nil {
		t.Fatalf("claimed job = %+v (payload %s)", l, l.Payload)
	}
	if again, err := r.ClaimDue(ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("a leased job was claimed again: %+v, %v", again, err)
	}
	wantErr(t, "RetryLog of a pending job", r.RetryLog(ctx, l.ID), ErrConflict)

	state := "step-1"
	if err := r.Finish(ctx, l.ID, LogResult{Status: LogDead, Attempts: 5, NextAttemptAt: time.Now(), LastError: "boom", State: &state}); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetLog(ctx, l.ID)
	if err != nil || got.Status != LogDead || got.Attempts != 5 || got.LastError != "boom" || got.State != "step-1" || got.ExternalID != "ENG-1" {
		t.Fatalf("after Finish: %+v, %v", got, err)
	}
	if err := r.RetryLog(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.GetLog(ctx, l.ID); got.Status != LogPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("after RetryLog: %+v", got)
	}

	// A skipped call, a waiting recording, then the listing.
	if err := r.Skip(ctx, nil, c.ID, LogJob{CallID: "call-2", Direction: "outbound"}, "no owner"); err != nil {
		t.Fatal(err)
	}
	if err := r.AttachRecording(ctx, nil, c.ID, "call-3", 77); err != nil {
		t.Fatal(err)
	}
	all, err := r.ListLogs(ctx, c.ID, "", 10, 0)
	if err != nil || len(all) != 3 || all[0].CallID != "call-3" || all[1].CallID != "call-2" || all[2].CallID != "call-1" {
		t.Fatalf("ListLogs = %+v, %v", all, err)
	}
	if all[0].Status != LogWaiting || all[0].RecordingID != 77 || all[1].Status != LogSkipped || all[1].LastError != "no owner" {
		t.Fatalf("listed rows = %+v", all[:2])
	}
	if page, err := r.ListLogs(ctx, c.ID, LogSkipped, 10, 0); err != nil || len(page) != 1 || page[0].CallID != "call-2" {
		t.Fatalf("ListLogs(skipped) = %+v, %v", page, err)
	}
	if page, err := r.ListLogs(ctx, c.ID, "", 1, all[0].ID); err != nil || len(page) != 1 || page[0].CallID != "call-2" {
		t.Fatalf("ListLogs(before) = %+v, %v", page, err)
	}
	_, err = r.GetLog(ctx, 987654)
	wantErr(t, "GetLog of a missing id", err, ErrNotFound)
	wantErr(t, "RetryLog of a missing id", r.RetryLog(ctx, 987654), ErrNotFound)
	if ok, err := r.CallInvolves(ctx, "call-1", nil); err != nil || ok {
		t.Fatalf("CallInvolves with no extensions = %v, %v", ok, err)
	}
	if ok, err := r.CallInvolves(ctx, "call-404", []string{"100", "200"}); err != nil || ok {
		t.Fatalf("CallInvolves of an unknown call = %v, %v", ok, err)
	}
}

// contractMappings fails if an automatic mapping replaces a manual one, or a
// mapping is not listed, updated or removed.
func contractMappings(t *testing.T, rig contractRig) {
	r, ctx := rig.repo, context.Background()
	c := contractConnector(t, r, "Dynamics", "")
	var users []int64
	for i := range 3 {
		users = append(users, rig.addUser(t, fmt.Sprintf("user%d", i)))
	}
	if err := r.SetMapping(ctx, c.ID, users[1], "crm-b", "email"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMapping(ctx, c.ID, users[0], "crm-a", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMapping(ctx, c.ID, users[0], "crm-auto", "email"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMapping(ctx, c.ID, users[1], "crm-b2", "email"); err != nil {
		t.Fatal(err)
	}
	got, err := r.Mappings(ctx, c.ID)
	want := []Mapping{{users[0], "crm-a", "manual"}, {users[1], "crm-b2", "email"}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Mappings = %+v, %v; want %+v", got, err, want)
	}
	if err := r.DeleteMapping(ctx, c.ID, users[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Mappings(ctx, c.ID); len(got) != 1 || got[0].UserID != users[1] {
		t.Fatalf("Mappings after delete = %+v", got)
	}
	if err := r.DeleteMapping(ctx, c.ID, users[2]); err != nil {
		t.Fatalf("deleting a mapping that does not exist: %v", err)
	}
}
