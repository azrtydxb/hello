package api

// Tests for the directory routes. They run through Handler over the in-memory
// account fake and the engine on directory.Mem, so they need no services; the
// documented status of every route is produced here (the conformance suite
// checks it).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/store"
)

// dirDriver validates like the LDAP driver and reads a fixed source.
type dirDriver struct {
	directory.LDAP
	res *directory.Result
}

func (d *dirDriver) Fetch(context.Context, json.RawMessage, string, directory.FetchOptions) (*directory.Result, error) {
	return d.res, nil
}

func (d *dirDriver) Test(context.Context, json.RawMessage, string, directory.FetchOptions) (*directory.TestResult, error) {
	return &directory.TestResult{OK: true, MatchedUsers: 1, Groups: []directory.SourceGroup{{ID: "g1", Name: "sales"}},
		Sample: []directory.SampleRow{{ExternalID: "x1", Raw: map[string][]string{"mail": {"ann@corp.org"}}}}}, nil
}

type dirEnv struct {
	h   http.Handler
	f   *acctFake
	mem *directory.Mem
	drv *dirDriver
	eng *directory.Engine
}

const dirCfg = `{"url":"ldaps://dc.corp.test","bindDn":"cn=svc,dc=corp","baseDn":"dc=corp"}`

func newDirEnv(t *testing.T) *dirEnv {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	e := &dirEnv{f: f, mem: directory.NewMem(), drv: &dirDriver{res: &directory.Result{}}}
	e.eng = &directory.Engine{Store: e.mem, Drivers: map[string]directory.Driver{directory.KindLDAP: e.drv}, Replica: "t"}
	e.h = Handler(Config{Store: whStore{f}, Directory: e.eng, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	return e
}

func (e *dirEnv) call(who, bearer, method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *dirEnv) admin(method, path string, body any) *httptest.ResponseRecorder {
	return e.call("carol", "", method, path, body)
}

func (e *dirEnv) connector(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"kind": "ldap", "name": "AD", "bindPassword": "s3cret-bind-pw", "enabled": true, "config": json.RawMessage(dirCfg)}
	for k, v := range extra {
		body[k] = v
	}
	rec := e.admin("POST", "/api/v1/directory/connectors", body)
	return wantJSON(t, rec, http.StatusCreated, "create connector")
}

func (e *dirEnv) run(t *testing.T, id float64, dry bool) map[string]any {
	t.Helper()
	rec := e.admin("POST", "/api/v1/directory/connectors/"+strconv.Itoa(int(id))+"/sync", map[string]any{"dryRun": dry})
	run := wantJSON(t, rec, http.StatusAccepted, "sync")
	if run["status"] != "queued" {
		t.Fatalf("run = %v", run)
	}
	e.eng.TickOnce(context.Background())
	rec = e.admin("GET", "/api/v1/directory/runs/"+strconv.Itoa(int(run["id"].(float64))), nil)
	return wantJSON(t, rec, http.StatusOK, "get run")
}

func srcUser(id, upn, mail, name string) directory.SourceUser {
	return directory.SourceUser{ExternalID: id, Attrs: map[string][]string{"userprincipalname": {upn}, "mail": {mail}, "displayname": {name}}}
}

func TestDirectoryConnectorLifecycle(t *testing.T) {
	e := newDirEnv(t)
	c := e.connector(t, nil)
	if c["hasCredential"] != true || c["enabled"] != true || c["mode"] != "authoritative" || c["status"] != "new" {
		t.Fatalf("connector = %v", c)
	}
	// The bind password never comes back, in any response.
	for _, p := range []string{"/api/v1/directory/connectors", "/api/v1/directory/connectors/1"} {
		if body := e.admin("GET", p, nil).Body.String(); strings.Contains(body, "s3cret") || strings.Contains(strings.ToLower(body), "bindpassword") {
			t.Fatalf("%s leaks the password: %s", p, body)
		}
	}
	cfg := c["config"].(map[string]any)
	if cfg["userFilter"] != directory.DefaultUserFilter || cfg["pageSize"] != float64(500) || cfg["scope"] != "subtree" {
		t.Fatalf("config defaults = %v", cfg)
	}
	maps := c["mappings"].(map[string]any)
	if maps["extension"].(map[string]any)["sources"].([]any)[0] != "ipPhone" {
		t.Fatalf("mappings = %v", maps)
	}
	list := wantJSON(t, e.admin("GET", "/api/v1/directory/connectors", nil), http.StatusOK, "list")
	if len(list["items"].([]any)) != 1 {
		t.Fatalf("list = %v", list)
	}
	// Patch: a rename, then a host change that needs the password again.
	wantJSON(t, e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{"name": "Corp AD", "scheduleMinutes": 15}), http.StatusOK, "rename")
	rec := e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{"config": json.RawMessage(`{"url":"ldaps://evil.example","bindDn":"cn=svc,dc=corp","baseDn":"dc=corp"}`)})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bindPassword") {
		t.Fatalf("host change without the password: %d %s", rec.Code, rec.Body)
	}
	wantJSON(t, e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{
		"config": json.RawMessage(`{"url":"ldaps://evil.example","bindDn":"cn=svc,dc=corp","baseDn":"dc=corp"}`), "bindPassword": "new"}), http.StatusOK, "host change with the password")
	if rec := e.admin("PATCH", "/api/v1/directory/connectors/1", map[string]any{"kind": "ldap"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("kind change: %d", rec.Code)
	}
	if rec := e.admin("PATCH", "/api/v1/directory/connectors/99", map[string]any{"name": "x"}); rec.Code != http.StatusNotFound {
		t.Fatalf("patch unknown: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/connectors/99", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/connectors/x", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", rec.Code)
	}
	// Validation: filter, plaintext, auto-provision, missing name, unknown field, empty password.
	for name, body := range map[string]map[string]any{
		"bad filter": {"kind": "ldap", "name": "X1", "config": json.RawMessage(`{"url":"ldaps://dc","bindDn":"b","baseDn":"d","userFilter":"(&(a=b)"}`)},
		"auto":       {"kind": "ldap", "name": "X2", "config": json.RawMessage(dirCfg), "autoProvision": map[string]any{"enabled": true}},
		"no name":    {"kind": "ldap"},
		"unknown":    {"kind": "ldap", "name": "X3", "nope": 1},
		"enable":     {"kind": "ldap", "name": "X4", "enabled": true, "config": json.RawMessage(dirCfg)},
		"empty pw":   {"kind": "ldap", "name": "X5", "bindPassword": "", "config": json.RawMessage(dirCfg)},
		"kind":       {"kind": "scim", "name": "X6"},
		"dup name":   {"kind": "ldap", "name": "corp ad", "config": json.RawMessage(dirCfg)},
	} {
		if name == "dup name" {
			// The Mem store reports a duplicate as ErrInvalid, which is a 500 for
			// the handler; PostgreSQL maps it to a field error, so skip here.
			continue
		}
		if rec := e.admin("POST", "/api/v1/directory/connectors", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec = e.admin("POST", "/api/v1/directory/connectors", map[string]any{"kind": "ldap", "name": "Plain",
		"config": json.RawMessage(`{"url":"ldap://dc.corp.test","startTls":false,"bindDn":"b","baseDn":"d"}`)})
	if rec.Code != http.StatusBadRequest || acctErrCode(rec) != "tls_required" {
		t.Fatalf("plaintext: %d %s", rec.Code, rec.Body)
	}
	// Delete.
	e.mem.AddUser(directory.LocalUser{Username: "linked", Enabled: true, ConnectorID: 1, ExternalID: "x"})
	rec = e.admin("DELETE", "/api/v1/directory/connectors/1", nil)
	if rec.Code != http.StatusConflict || acctErrCode(rec) != "connector_in_use" {
		t.Fatalf("delete with users: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("DELETE", "/api/v1/directory/connectors/1?release=maybe", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("release=maybe: %d", rec.Code)
	}
	if rec := e.admin("DELETE", "/api/v1/directory/connectors/1?release=true", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete released: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("DELETE", "/api/v1/directory/connectors/1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d", rec.Code)
	}
}

func TestDirectoryCredentialNeedsSecretsScope(t *testing.T) {
	e := newDirEnv(t)
	// An admin credential without the secrets scope may manage connectors but not set the password.
	body := map[string]any{"kind": "ldap", "name": "AD", "bindPassword": "pw", "config": json.RawMessage(dirCfg)}
	rec := e.call("", "tok-admin-only", "POST", "/api/v1/directory/connectors", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create with a password: %d %s", rec.Code, rec.Body)
	}
	delete(body, "bindPassword")
	if rec := e.call("", "tok-admin-only", "POST", "/api/v1/directory/connectors", body); rec.Code != http.StatusCreated {
		t.Fatalf("create without: %d %s", rec.Code, rec.Body)
	}
	if rec := e.call("", "tok-admin-only", "PATCH", "/api/v1/directory/connectors/1", map[string]any{"bindPassword": "pw"}); rec.Code != http.StatusForbidden {
		t.Fatalf("patch with a password: %d", rec.Code)
	}
	if rec := e.call("", "tok-read", "GET", "/api/v1/directory/connectors", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a read token lists connectors: %d", rec.Code)
	}
	// Operators and viewers are refused outright.
	if rec := e.call("dave", "", "GET", "/api/v1/directory/connectors", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", rec.Code)
	}
	if rec := e.call("alice", "", "GET", "/api/v1/directory/runs/1", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("user role: %d", rec.Code)
	}
}

func TestDirectoryTest(t *testing.T) {
	e := newDirEnv(t)
	e.connector(t, nil)
	m := wantJSON(t, e.admin("POST", "/api/v1/directory/connectors/1/test", nil), http.StatusOK, "test")
	if m["ok"] != true || m["matchedUsers"] != float64(1) {
		t.Fatalf("test = %v", m)
	}
	sample := m["sample"].([]any)[0].(map[string]any)
	if sample["mapped"].(map[string]any)["email"] != "ann@corp.org" {
		t.Fatalf("sample = %v", sample)
	}
	// A preview of unsaved mappings.
	m = wantJSON(t, e.admin("POST", "/api/v1/directory/connectors/1/test", map[string]any{"mappings": map[string]any{
		"email": map[string]any{"sources": []string{"mail"}, "transforms": []any{map[string]any{"kind": "last", "n": 3}}}}}), http.StatusOK, "test with mappings")
	if m["sample"].([]any)[0].(map[string]any)["mapped"].(map[string]any)["email"] != "org" {
		t.Fatalf("test = %v", m)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/1/test", map[string]any{"mappings": map[string]any{"email": map[string]any{"transforms": []any{map[string]any{"kind": "x"}}}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mappings: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/1/test", `{"unknown":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/9/test", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connector: %d", rec.Code)
	}
	if !strings.Contains(strings.Join(e.mem.Audits, "|"), "user:carol test directory_connector") {
		t.Fatalf("audits = %v", e.mem.Audits)
	}
}

func TestDirectoryRunPreviewApplyConfirm(t *testing.T) {
	e := newDirEnv(t)
	e.connector(t, nil)
	e.mem.AddUser(directory.LocalUser{Username: "old", Email: "old@corp.org", Enabled: true, Role: "operator"})
	e.drv.res = &directory.Result{Users: []directory.SourceUser{srcUser("a", "ann@corp", "ann@corp.org", "Ann"), srcUser("o", "old@corp", "old@corp.org", "Old")}}

	pre := e.run(t, 1, true)
	if pre["status"] != "preview" || pre["kind"] != "preview" {
		t.Fatalf("preview = %v", pre)
	}
	counts := pre["counts"].(map[string]any)
	if counts["create"] != float64(1) || counts["adopt"] != float64(1) || counts["users"] != float64(2) {
		t.Fatalf("counts = %v", counts)
	}
	changes := wantJSON(t, e.admin("GET", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/changes?action=create", nil), http.StatusOK, "changes")
	items := changes["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["username"] != "ann@corp" || changes["nextAfter"] != nil {
		t.Fatalf("changes = %v", changes)
	}
	paged := wantJSON(t, e.admin("GET", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/changes?limit=1", nil), http.StatusOK, "page")
	if paged["nextAfter"] == nil {
		t.Fatalf("no next page: %v", paged)
	}
	if len(e.mem.Users) != 1 {
		t.Fatal("a preview created users")
	}
	runs := wantJSON(t, e.admin("GET", "/api/v1/directory/connectors/1/runs", nil), http.StatusOK, "runs")
	if len(runs["items"].([]any)) != 1 {
		t.Fatalf("runs = %v", runs)
	}

	// Apply, confirming the adoption.
	rec := e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/apply", map[string]any{"adopt": []string{"o"}})
	done := wantJSON(t, rec, http.StatusOK, "apply")
	if done["status"] != "succeeded" || done["kind"] != "apply" {
		t.Fatalf("applied = %v", done)
	}
	if len(e.mem.Users) != 2 {
		t.Fatalf("users = %d", len(e.mem.Users))
	}
	for _, u := range e.mem.Users {
		if u.ConnectorID != 1 {
			t.Fatalf("user %s not linked", u.Username)
		}
	}
	// Applying twice is refused.
	rec = e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/apply", nil)
	if rec.Code != http.StatusConflict || acctErrCode(rec) != "not_applicable" {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body)
	}
	// Confirming a run that was not halted is refused too.
	if rec := e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/confirm", nil); rec.Code != http.StatusConflict {
		t.Fatalf("confirm of an applied run: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/runs/99/apply", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("apply unknown: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/runs/99/confirm", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("confirm unknown: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/runs/1/apply", `{"bogus":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("apply with a bad body: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/runs/1/confirm", `{"bogus":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("confirm with a bad body: %d", rec.Code)
	}

	// The source empties: the run halts for confirmation, then a person confirms.
	e.drv.res = &directory.Result{}
	halted := e.run(t, 1, false)
	if halted["status"] != "needs_confirmation" || halted["errorCode"] != "empty_result" {
		t.Fatalf("halted = %v", halted)
	}
	rec = e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(halted["id"].(float64)))+"/confirm", nil)
	if c := wantJSON(t, rec, http.StatusOK, "confirm"); c["status"] != "succeeded" {
		t.Fatalf("confirmed = %v", c)
	}

	// A preview that went stale answers 409 with the fresh one.
	e.drv.res = &directory.Result{Users: []directory.SourceUser{srcUser("a", "ann@corp", "ann@corp.org", "Ann"), srcUser("n", "new@corp", "", "New")}}
	pre = e.run(t, 1, true)
	e.drv.res.Users = append(e.drv.res.Users, srcUser("m", "more@corp", "", "More"))
	rec = e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(pre["id"].(float64)))+"/apply", nil)
	stale := wantJSON(t, rec, http.StatusConflict, "stale")
	if stale["error"].(map[string]any)["code"] != "stale_preview" || stale["run"].(map[string]any)["status"] != "preview" {
		t.Fatalf("stale = %v", stale)
	}
	// Another replica running the connector makes an apply busy.
	fresh := stale["run"].(map[string]any)
	release := e.mem.Hold("hello:dirsync:1")
	rec = e.admin("POST", "/api/v1/directory/runs/"+strconv.Itoa(int(fresh["id"].(float64)))+"/apply", nil)
	release()
	if rec.Code != http.StatusConflict || acctErrCode(rec) != "busy" {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
}

func TestDirectoryRunRejections(t *testing.T) {
	e := newDirEnv(t)
	e.connector(t, map[string]any{"enabled": false, "bindPassword": "pw"})
	if rec := e.admin("POST", "/api/v1/directory/connectors/1/sync", map[string]any{"dryRun": true}); rec.Code != http.StatusConflict || acctErrCode(rec) != "connector_disabled" {
		t.Fatalf("sync of a disabled connector: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/1/sync", `{"dryRun":"yes"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/7/sync", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connector: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/connectors/7/runs", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("runs of unknown: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/connectors/1/runs?limit=0", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/runs/5", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run: %d", rec.Code)
	}
	if rec := e.admin("GET", "/api/v1/directory/runs/5/changes", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("changes of an unknown run: %d", rec.Code)
	}
	run, _ := e.mem.CreateRun(context.Background(), directory.Run{ConnectorID: 1, Kind: "preview", Status: "preview", Counts: map[string]int{}})
	for _, q := range []string{"action=bogus", "after=x", "after=-1", "limit=0", "limit=5000"} {
		if rec := e.admin("GET", "/api/v1/directory/runs/"+strconv.FormatInt(run.ID, 10)+"/changes?"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("changes?%s: %d", q, rec.Code)
		}
	}
	// Applying on a disabled connector.
	if rec := e.admin("POST", "/api/v1/directory/runs/"+strconv.FormatInt(run.ID, 10)+"/apply", nil); rec.Code != http.StatusConflict || acctErrCode(rec) != "connector_disabled" {
		t.Fatalf("apply on a disabled connector: %d %s", rec.Code, rec.Body)
	}
}

func TestDirectoryUnconfigured(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	h := Handler(Config{Store: whStore{f}, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	r := httptest.NewRequest("GET", "/api/v1/directory/connectors", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "carol" + cookieSuffix})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUserManagedByDirectory(t *testing.T) {
	e := newDirEnv(t)
	e.f.users[uAlice].ManagedBy = &store.ManagedBy{ConnectorID: 1, Connector: "Corp AD", Fields: []string{"email", "displayName", "role", "extension", "enabled"}}
	e.f.users[uBob].ManagedBy = &store.ManagedBy{ConnectorID: 1, Connector: "Corp AD", Fields: []string{"email"}}
	patch := func(id int, body map[string]any) *httptest.ResponseRecorder {
		return e.admin("PATCH", "/api/v1/users/"+strconv.Itoa(id), body)
	}
	for name, body := range map[string]map[string]any{
		"email": {"email": "x@y.org"}, "display name": {"displayName": "X"}, "role": {"role": "viewer"},
		"extension": {"extensionIds": []int64{}}, "legacy extension": {"extensionId": nil}, "enable": {"enabled": true}, "password": {"password": strongPassword},
	} {
		rec := patch(uAlice, body)
		if rec.Code != http.StatusConflict || acctErrCode(rec) != "managed_by_directory" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "Corp AD") {
			t.Errorf("%s: the message does not name the connector: %s", name, rec.Body)
		}
	}
	// What the connector does not own stays editable, and a directory user's
	// password is never settable even when no field names it.
	wantJSON(t, patch(uAlice, map[string]any{"voicemailEmail": map[string]any{"notify": false}}), http.StatusOK, "voicemail prefs")
	wantJSON(t, patch(uBob, map[string]any{"role": "viewer"}), http.StatusOK, "unmanaged role")
	if rec := patch(uBob, map[string]any{"password": strongPassword}); rec.Code != http.StatusConflict {
		t.Errorf("password of a directory user: %d", rec.Code)
	}
	// Disabling is the safe direction and stays possible.
	wantJSON(t, patch(uAlice, map[string]any{"enabled": false}), http.StatusOK, "disable")
	// A local user is untouched by all this.
	wantJSON(t, patch(uDave, map[string]any{"email": "d@y.org"}), http.StatusOK, "local user")
	// And the user JSON carries the ownership.
	u := wantJSON(t, e.admin("GET", "/api/v1/users/"+strconv.Itoa(uAlice), nil), http.StatusOK, "get")
	mb := u["managedBy"].(map[string]any)
	if mb["connector"] != "Corp AD" || len(mb["fields"].([]any)) != 5 {
		t.Fatalf("managedBy = %v", mb)
	}
	if v := wantJSON(t, e.admin("GET", "/api/v1/users/"+strconv.Itoa(uDave), nil), http.StatusOK, "get local"); v["managedBy"] != nil {
		t.Fatalf("local user managedBy = %v", v["managedBy"])
	}
}
